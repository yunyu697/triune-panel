// Package hostopt 提供主机网络优化的只读体检，以及可选的参数应用/撤销。
//
// 体检范围（全部只读）：内核 BBR 支持、当前拥塞控制与 qdisc、tc 实测、
// 容器环境、root 权限，以及时钟同步、文件描述符水位、conntrack 水位、
// sysctl 覆盖检查、系统概览（负载/内存/磁盘/IPv6/MTU）。
//
// 产品约束（本包所有代码都必须遵守）：
//   - 默认绝不修改系统：Detect 只做只读探测，连目录都不创建、连文件都不写。
//   - 必须用户显式确认（ApplyConfirm / RevertConfirm）才允许写系统配置，
//     且确认串必须精确匹配，差一个字符都拒绝。
//   - 必须能完整回滚：应用前先把每个键的原值存成备份；生效失败或复检不符
//     都「失败零改动」地整体回滚。
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
)

// ConfigPath 是面板唯一会写的 sysctl 配置文件。
const ConfigPath = "/etc/sysctl.d/99-triune-bbr.conf"

// 确认字面量：UI 必须原样回传，避免误触写系统。
//
// v1.6.0 起参数从「只有 BBR」扩成一张参数表，确认串随之改成 APPLY NET；
// 旧版前端/脚本仍在用的 APPLY BBR 依旧接受，免得升级后旧页面点不动。
const (
	ApplyConfirm        = "APPLY NET"
	RevertConfirm       = "REVERT NET"
	LegacyApplyConfirm  = "APPLY BBR"
	LegacyRevertConfirm = "REVERT BBR"
)

var (
	ErrConfirm     = errors.New("需要显式确认")
	ErrUnsupported = errors.New("当前主机不支持由面板应用网络优化")
)

// Check 是体检报告里的一项。Warn 表示「无法确认 / 不适用」，不算失败。
//
// Fix 只在「这一项失败、且面板确实能安全代劳」时才填：它是 fix.go 里修复动作的
// 标识，前端据此在行内挂一个修复按钮。没有对应动作的项一律留空——挂一个点了
// 不管用的按钮，比不挂更让人困惑。
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Warn   bool   `json:"warn"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
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

	// 以下为新增：可调参数现值、可选项与系统概览。
	// 全部来自只读探测，Detect 仍然不写任何文件。
	Tune     Tune           `json:"tune"`
	Options  TuneOptions    `json:"options"`
	System   SystemOverview `json:"system"`
	Expected *Tune          `json:"expected,omitempty"` // 面板上次写入的期望值

	// Fixes 是可执行的修复动作及其现状（是否可执行 / 是否已修复过）。
	// 体检只报状态时用户还得自己查「该改哪个键」，这里给出能安全代劳的那几项。
	Fixes []FixAction `json:"fixes"`
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
	r.Tune = currentTune()
	r.Options = tuneOptions(r.AvailableCC)
	if !platformUnsupported() {
		r.System = systemOverview(dataDir)
		// 修复动作只读汇总（是否可执行、是否已修复过），Detect 依旧不落盘。
		r.Fixes = listFixActions(dataDir)
	}
	r.Expected = readExpected(dataDir)
	if r.Expected != nil {
		r.Checks = append(r.Checks, verifyCheck(*r.Expected, r.Tune))
	}
	return r
}

// platformUnsupported 判断当前（可替换的）平台是否为非 Linux。
// 抽成函数是为了让 !linux 兜底逻辑能在任意开发机上被单测直接调用。
func platformUnsupported() bool { return goos != "linux" }

// Apply 把参数表写入系统；confirm 必须是 ApplyConfirm（或旧版 APPLY BBR）精确字面量。
//
// tune 不给时用 DefaultTune()——单测与旧调用点因此不用改签名。
func Apply(dataDir, confirm string, tune ...Tune) error {
	// 校验放在最前面：confirm 不对时绝不触碰任何文件，这是「默认绝不修改系统」的第一道闸门。
	if confirm != ApplyConfirm && confirm != LegacyApplyConfirm {
		return ErrConfirm
	}
	if platformUnsupported() {
		return ErrUnsupported
	}
	want := DefaultTune()
	if len(tune) > 0 {
		want = tune[0]
	}

	// 「失败零改动」核心不变量：任何写操作之前重新做一次只读体检，
	// 条件不满足就绝不落盘，并把中文原因带给调用方。
	r := Detect(dataDir)
	if !r.ApplyEnabled {
		return fmt.Errorf("%w: %s", ErrUnsupported, r.Reason)
	}
	if err := validateTune(want, r.AvailableCC); err != nil {
		return err
	}
	// 幂等：已应用且现值就是要的值，直接成功返回，不重复写盘。
	if appliedState(dataDir) && r.Tune.equal(want) {
		return nil
	}
	return applyTune(dataDir, want)
}

// Revert 撤销面板写过的配置并恢复原值；confirm 必须是 RevertConfirm（或旧版）。
func Revert(dataDir, confirm string) error {
	if confirm != RevertConfirm && confirm != LegacyRevertConfirm {
		return ErrConfirm
	}
	if platformUnsupported() {
		return ErrUnsupported
	}
	return revertTune(dataDir)
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

// readExpected 读取面板上次写入的期望值（只读）。
func readExpected(dataDir string) *Tune {
	b, err := readFile(expectedPath(dataDir))
	if err != nil {
		return nil
	}
	var t Tune
	if err := json.Unmarshal(b, &t); err != nil {
		return nil
	}
	return &t
}

func expectedPath(d string) string { return filepath.Join(d, expectedFileName) }
