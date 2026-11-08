// Package hostopt 提供主机网络优化（当前为 TCP BBR）的只读体检与可选应用/撤销。
//
// 产品约束（本包所有代码都必须遵守）：
//   - 默认绝不修改系统：Detect 只做只读探测，连目录都不创建、连文件都不写。
//   - 必须用户显式确认（ApplyConfirm / RevertConfirm）才允许写系统配置，
//     且确认串必须精确匹配，差一个字符都拒绝。
//   - 必须能完整回滚：应用前先把原值存成备份；生效失败则「失败零改动」地整体回滚。
//   - 容器（docker/k8s/lxc/OpenVZ）里改 sysctl 通常无效甚至危险，
//     识别出来后一律禁用应用按钮（ApplyEnabled=false）。
package hostopt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ConfigPath 是面板唯一会写的 sysctl 配置文件。
const ConfigPath = "/etc/sysctl.d/99-triune-bbr.conf"

// 确认字面量：UI 必须原样回传，避免误触写系统。
const (
	ApplyConfirm  = "APPLY BBR"
	RevertConfirm = "REVERT BBR"
)

var (
	ErrConfirm     = errors.New("需要显式确认")
	ErrUnsupported = errors.New("当前主机不支持由面板应用 BBR")
)

// Check 是体检报告里的一项。Warn 表示「无法确认 / 不适用」，不算失败。
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Warn   bool   `json:"warn"`
	Detail string `json:"detail"`
}

// Report 是一次完整体检的结果，直接序列化给前端。
type Report struct {
	Platform     string   `json:"platform"`
	Kernel       string   `json:"kernel"`
	CurrentCC    string   `json:"current_cc"`
	AvailableCC  []string `json:"available_cc"`
	BBRModule    bool     `json:"bbr_module"`
	DefaultQdisc string   `json:"default_qdisc"`
	QdiscDetail  string   `json:"qdisc_detail"`
	InContainer  bool     `json:"in_container"`
	IsRoot       bool     `json:"is_root"`
	Checks       []Check  `json:"checks"`
	Suggested    []string `json:"suggested"`
	ApplyEnabled bool     `json:"apply_enabled"`
	Reason       string   `json:"reason"`
	Applied      bool     `json:"applied"`
	ConfigPath   string   `json:"config_path"`
}

// ---------------------------------------------------------------------------
// 可替换依赖
//
// 把 IO / 进程 / 平台依赖全部收敛成包级变量，单测可整体替换，从而在不碰真实
// /proc、/etc，也不真的执行 sysctl/tc 的前提下覆盖完整 Linux 逻辑。
// 注意：这些是共享状态，并发测试不要用 t.Parallel()，每个用例用 t.Cleanup 还原。
// ---------------------------------------------------------------------------
var (
	readFile  = os.ReadFile
	writeFile = os.WriteFile
	stat      = os.Stat
	mkdirAll  = os.MkdirAll
	remove    = os.Remove
	rename    = os.Rename
	geteuid   = os.Geteuid
	lookPath  = exec.LookPath
	lookupEnv = os.LookupEnv

	// runCmd 与 internal/backup/upload.go:171 的 runRclone 同款：
	// 把 stderr 收进错误信息，避免上层只看到一句 "exit status 1"。
	runCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			if msg := strings.TrimSpace(stderr.String()); msg != "" {
				return out, fmt.Errorf("%w: %s", err, msg)
			}
			return out, err
		}
		return out, nil
	}

	// goos 默认等于 runtime.GOOS；替换它是为了在任意开发机上覆盖另一平台分支
	// （build tag 是编译期决定的，运行时改字符串并不会切换编译进来的文件）。
	goos = runtime.GOOS

	// 路径同样可替换：测试全部指向 t.TempDir()，绝不真的写 /etc/sysctl.d。
	cfgPath   = ConfigPath
	sysctlDir = "/etc/sysctl.d"
	procSys   = "/proc/sys"

	// 其余只读探测路径，测试也一律指向临时目录。
	procVersion  = "/proc/version"
	procRoute    = "/proc/net/route"
	procCgroup   = "/proc/1/cgroup"
	bbrModuleDir = "/sys/module/tcp_bbr"
	procVZDir    = "/proc/vz"
	dockerEnv    = "/.dockerenv"
	containerEnv = "/.containerenv"
)

const (
	origFileName    = "hostopt-bbr.orig.json"
	appliedFileName = "hostopt-bbr.applied"
)

// bbrConfig 是面板写入的配置文件内容：恰好两行，顺序固定。
const bbrConfig = "net.core.default_qdisc=fq\nnet.ipv4.tcp_congestion_control=bbr\n"

func ccPath() string { return filepath.Join(procSys, "net", "ipv4", "tcp_congestion_control") }
func availPath() string {
	return filepath.Join(procSys, "net", "ipv4", "tcp_available_congestion_control")
}
func qdiscPath() string           { return filepath.Join(procSys, "net", "core", "default_qdisc") }
func origPath(d string) string    { return filepath.Join(d, origFileName) }
func appliedPath(d string) string { return filepath.Join(d, appliedFileName) }

// Detect 只做只读体检：不创建目录、不写文件、不执行任何会修改系统的命令。
//
// 生产环境 goos 恒等于 runtime.GOOS，于是走 compiledReport（由 build tag 决定
// 是 Linux 探测还是非 Linux 兜底）；测试替换 goos 后可直接演练另一条分支。
func Detect(dataDir string) Report {
	var r Report
	switch {
	case goos == runtime.GOOS:
		// 严格按编译期平台分派：只有 Linux 二进制才会真正去读 /proc。
		r = compiledReport(dataDir)
	case goos == "linux":
		// 单测覆盖：在非 Linux 开发机上强制演练 Linux 探测（IO 已被桩替换）。
		r = linuxReport(dataDir)
	default:
		r = unsupportedReport(dataDir)
	}
	r.Platform = goos
	r.ConfigPath = cfgPath
	r.Applied = appliedState(dataDir)
	return r
}

// platformUnsupported 判断当前（可替换的）平台是否为非 Linux。
// 抽成函数是为了让 !linux 兜底逻辑能在任意开发机上被单测直接调用。
func platformUnsupported() bool { return goos != "linux" }

// Apply 可选地把 BBR 配置写入系统；confirm 必须是 ApplyConfirm 精确字面量。
func Apply(dataDir, confirm string) error {
	// 校验放在最前面：confirm 不对时绝不触碰任何文件，这是「默认绝不修改系统」的第一道闸门。
	if confirm != ApplyConfirm {
		return ErrConfirm
	}
	// 非 Linux 二进制直接拒绝，连 /proc 都不读。
	if platformUnsupported() {
		return ErrUnsupported
	}
	// 「失败零改动」核心不变量：任何写操作之前重新做一次只读体检，
	// 条件不满足就绝不落盘，并把中文原因带给调用方。
	r := Detect(dataDir)
	if !r.ApplyEnabled {
		return fmt.Errorf("%w: %s", ErrUnsupported, r.Reason)
	}
	return applyBBR(dataDir)
}

// Revert 撤销面板写过的配置并恢复原值；confirm 必须是 RevertConfirm 精确字面量。
func Revert(dataDir, confirm string) error {
	if confirm != RevertConfirm {
		return ErrConfirm
	}
	if platformUnsupported() {
		return ErrUnsupported
	}
	return revertBBR(dataDir)
}

// appliedState 同时要求「状态标记」与「配置文件」都在，避免标记残留导致误判。
// stat 是只读的，Detect 全程不写任何东西。
func appliedState(dataDir string) bool {
	if _, err := stat(appliedPath(dataDir)); err != nil {
		return false
	}
	if _, err := stat(cfgPath); err != nil {
		return false
	}
	return true
}

// bbrOrig 是应用前的原值快照，写到 dataDir 供 Revert 审计与恢复。
// 连「配置文件原本是否存在 / 内容」都记下来，是为了做到完整回滚：
// 若原本就有同名配置，撤销时恢复它，而不是粗暴删除用户的文件。
type bbrOrig struct {
	ConfigExisted bool   `json:"config_existed"`
	ConfigContent []byte `json:"config_content"`
	CurrentCC     string `json:"current_cc"`
	DefaultQdisc  string `json:"default_qdisc"`
}

func applyBBR(dataDir string) error {
	// 幂等：已经应用过就直接成功返回，绝不重复覆盖备份文件。
	// 这里选「返回 nil」而不是报错：重复点应用是用户的正常行为，不该吓人。
	if appliedState(dataDir) {
		return nil
	}

	// 「先备份原值」：读不到内核当前值就干脆不应用，避免恢复时写回空串。
	orig, err := snapshotOrig()
	if err != nil {
		return fmt.Errorf("读取原值失败，未改动系统: %w", err)
	}
	if err := mkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("创建数据目录失败，未改动系统: %w", err)
	}
	// 备份已存在时不覆盖：首次应用留下的才是真正的「原值」，重试不能把它冲掉。
	if _, err := stat(origPath(dataDir)); err != nil {
		blob, mErr := json.MarshalIndent(orig, "", "  ")
		if mErr != nil {
			return fmt.Errorf("序列化备份失败，未改动系统: %w", mErr)
		}
		if err := writeFile(origPath(dataDir), blob, 0o600); err != nil {
			return fmt.Errorf("写备份失败，未改动系统: %w", err)
		}
	}

	// 写配置：临时文件 + rename 原子替换，避免半截配置被 sysctl 读到。
	if err := mkdirAll(sysctlDir, 0o755); err != nil {
		return fmt.Errorf("创建 %s 失败，未改动系统: %w", sysctlDir, err)
	}
	tmp := cfgPath + ".tmp"
	if err := writeFile(tmp, []byte(bbrConfig), 0o644); err != nil {
		_ = remove(tmp)
		return fmt.Errorf("写临时配置失败，未改动系统: %w", err)
	}
	if err := rename(tmp, cfgPath); err != nil {
		_ = remove(tmp)
		return fmt.Errorf("替换配置失败，未改动系统: %w", err)
	}

	// 生效：优先 sysctl -p；sysctl 不存在才退回直接写 /proc/sys。
	// 任一失败都必须整体回滚——「失败零改动」是这个项目的核心不变量：
	// 绝不能留下「配置写了却没生效」或「生效了一半」的中间态。
	if err := activate(); err != nil {
		return rollbackErr("使配置生效失败", err, rollback(dataDir, orig))
	}

	// 状态标记最后写：只有配置与生效都成功，面板才认为自己「已应用」。
	if err := writeFile(appliedPath(dataDir), []byte("applied\n"), 0o600); err != nil {
		return rollbackErr("写状态标记失败", err, rollback(dataDir, orig))
	}
	return nil
}

// rollbackErr 把「原错误」和「回滚是否真的干净」分开说清。
// 回滚本身也可能失败（例如 /proc/sys 变得只读），这时若还宣称「已整体回滚」，
// 用户会以为系统已复原而不再检查——比原错误更危险。
func rollbackErr(stage string, cause, rbErr error) error {
	if rbErr != nil {
		return fmt.Errorf("%s: %w；回滚不完整，请按报告里的原值人工恢复: %v", stage, cause, rbErr)
	}
	return fmt.Errorf("%s，已整体回滚: %w", stage, cause)
}

// snapshotOrig 只做只读采样，返回应用前的系统与配置原值。
func snapshotOrig() (bbrOrig, error) {
	cc, err := readFile(ccPath())
	if err != nil {
		return bbrOrig{}, fmt.Errorf("读取 %s: %w", ccPath(), err)
	}
	qd, err := readFile(qdiscPath())
	if err != nil {
		return bbrOrig{}, fmt.Errorf("读取 %s: %w", qdiscPath(), err)
	}
	o := bbrOrig{
		CurrentCC:    strings.TrimSpace(string(cc)),
		DefaultQdisc: strings.TrimSpace(string(qd)),
	}
	if b, err := readFile(cfgPath); err == nil {
		o.ConfigExisted = true
		o.ConfigContent = b
	}
	return o, nil
}

// activate 让刚写的配置真正生效。
func activate() error {
	if sysctl, err := lookPath("sysctl"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := runCmd(ctx, sysctl, "-p", cfgPath); err != nil {
			return fmt.Errorf("sysctl -p %s: %w", cfgPath, err)
		}
		return nil
	}
	// sysctl 不存在：退回直接写 /proc/sys，不依赖任何外部命令。
	if err := writeFile(ccPath(), []byte("bbr"), 0o644); err != nil {
		return fmt.Errorf("写 %s: %w", ccPath(), err)
	}
	if err := writeFile(qdiscPath(), []byte("fq"), 0o644); err != nil {
		return fmt.Errorf("写 %s: %w", qdiscPath(), err)
	}
	return nil
}

// rollback 是「失败零改动」的落地点：把系统状态恢复到应用之前。
//
// 只回滚系统侧（配置文件 + /proc/sys 运行值 + 状态标记）；dataDir 里的 orig
// 备份按产品要求刻意保留，供审计追溯「面板曾经改过什么、原值是什么」。
func rollback(dataDir string, o bbrOrig) error {
	var errs []string
	if o.ConfigExisted {
		if err := writeFile(cfgPath, o.ConfigContent, 0o644); err != nil {
			errs = append(errs, "恢复原配置: "+err.Error())
		}
	} else if err := remove(cfgPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, "删除配置: "+err.Error())
	}
	// 直接写 /proc/sys 恢复运行值，不依赖 sysctl，避免二次失败。
	if err := writeFile(ccPath(), []byte(o.CurrentCC), 0o644); err != nil {
		errs = append(errs, "恢复拥塞控制: "+err.Error())
	}
	if err := writeFile(qdiscPath(), []byte(o.DefaultQdisc), 0o644); err != nil {
		errs = append(errs, "恢复 qdisc: "+err.Error())
	}
	if err := remove(appliedPath(dataDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, "删除状态标记: "+err.Error())
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func revertBBR(dataDir string) error {
	blob, err := readFile(origPath(dataDir))
	if err != nil {
		// 没有备份就绝不动配置文件：否则可能把用户自己写的 sysctl 配置删掉。
		return fmt.Errorf("缺少备份 %s，拒绝撤销（未删除任何配置）: %w", origPath(dataDir), err)
	}
	var o bbrOrig
	if err := json.Unmarshal(blob, &o); err != nil {
		return fmt.Errorf("备份文件损坏，拒绝撤销: %w", err)
	}

	// 先恢复运行值；失败就停在这里，保留配置与标记，避免留下「已撤销却没恢复」的假象。
	if err := writeFile(ccPath(), []byte(o.CurrentCC), 0o644); err != nil {
		return fmt.Errorf("恢复 net.ipv4.tcp_congestion_control 失败: %w", err)
	}
	if err := writeFile(qdiscPath(), []byte(o.DefaultQdisc), 0o644); err != nil {
		return fmt.Errorf("恢复 net.core.default_qdisc 失败: %w", err)
	}

	// 完整回滚配置文件：原本有就还原内容，原本没有就删除。
	if o.ConfigExisted {
		if err := writeFile(cfgPath, o.ConfigContent, 0o644); err != nil {
			return fmt.Errorf("恢复原配置文件失败: %w", err)
		}
	} else if err := remove(cfgPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("删除配置文件失败: %w", err)
	}
	if err := remove(appliedPath(dataDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("删除状态标记失败: %w", err)
	}
	// orig 备份刻意保留，供审计。
	return nil
}
