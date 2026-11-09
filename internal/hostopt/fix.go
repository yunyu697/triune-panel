// 体检项的「一键修复 / 回退」。
//
// 体检只报状态时用户只能自己去查该改哪个键；这一层把「能由面板安全代劳的」
// 那几项做成可点动作，并且每一项都配一条回退路径。约束与 Apply/Revert 相同：
//
//   - 必须显式确认（FixConfirm / FixRevertConfirm 精确字面量），差一个字符都拒绝。
//   - 先记原值再动手：写在 dataDir 的 hostopt-fixes.json 是回退的唯一依据。
//   - 写完回读复检，不符就「失败零改动」地恢复原状。
//   - 记录写不进去时立刻撤销本次修复——「改过但没法还原」比不改更危险。
package hostopt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// 修复动作的确认串与参数应用分开：两者的风险面不同（一个改网络栈性能，
// 一个改时钟 / 资源上限 / 连接跟踪表），混用会让人分不清自己在确认什么。
const (
	FixConfirm       = "APPLY FIX"
	FixRevertConfirm = "REVERT FIX"

	fixFileName = "hostopt-fixes.json"
)

// 修复项标识：前端原样回传，别改（改了旧页面按钮就失效）。
const (
	fixKeyClock     = "clock"
	fixKeyFD        = "fd"
	fixKeyConntrack = "conntrack"
)

// maxConntrackCap 是面板愿意自动调到的最上限。
// nf_conntrack_max=262144 时内核最多约占 80 MiB 表项内存，对代理节点是合理天花板；
// 再往上就属于「先排查连接泄漏」的范畴，不该由按钮一键决定。
const maxConntrackCap = 262144

var (
	// conntrack 的持久化配置：98 前缀保证排在 99-triune-bbr.conf 之前加载，
	// 但两者键不重叠，顺序只影响可读性。
	conntrackConfPath = "/etc/sysctl.d/98-triune-conntrack.conf"

	// rlimit 走可替换变量：macOS 开发机上也能用桩覆盖完整分支。
	getNofile = getNofileImpl
	setNofile = setNofileImpl

	// nowFn 让测试拿到可预期的记录时间。
	nowFn = time.Now
)

// FixAction 是一项可执行的修复动作，直接序列化给前端。
type FixAction struct {
	Key     string `json:"key"`          // 稳定标识，确认时原样回传
	Check   string `json:"check"`        // 对应的体检项名称
	Label   string `json:"label"`        // 修复按钮文案
	Revert  string `json:"revert_label"` // 回退按钮文案
	Note    string `json:"note"`         // 会做什么
	Ready   bool   `json:"ready"`        // 当前主机能否执行
	Blocked string `json:"blocked,omitempty"`
	Applied bool   `json:"applied"` // 已修复过（记录存在，可回退）
	At      string `json:"at,omitempty"`
	Changes string `json:"changes,omitempty"` // 已修复时：原值 → 新值
}

// fixRecord 是回退依据。Backup 里没有某个路径的键，表示那个文件原本不存在。
type fixRecord struct {
	Key     string            `json:"key"`
	At      string            `json:"at"`
	Orig    map[string]string `json:"orig,omitempty"`
	Files   []string          `json:"files,omitempty"`
	Backup  map[string]string `json:"backup,omitempty"`
	Changes string            `json:"changes,omitempty"`
}

// fixSpec 是一项修复动作的实现。Blocked 返回非空字符串表示当前不可执行，
// 那段话会原样显示给用户——不允许只说「不可用」。
type fixSpec struct {
	Key      string
	Check    string
	Label    string
	Revert   string
	Note     string
	Blocked  func() string
	Apply    func(rec *fixRecord) error
	RevertFn func(rec fixRecord) error
}

func fixSpecs() []fixSpec {
	return []fixSpec{clockFix(), fdFix(), conntrackFix()}
}

func findFix(key string) *fixSpec {
	for _, s := range fixSpecs() {
		if s.Key == key {
			sp := s
			return &sp
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// 三个动作
// ---------------------------------------------------------------------------

func clockFix() fixSpec {
	return fixSpec{
		Key:    fixKeyClock,
		Check:  "时钟同步（NTP）",
		Label:  "启用时间同步",
		Revert: "关回自动对时",
		Note:   "用 timedatectl 打开 NTP 自动对时。对时完成需要几十秒，之后再点「重新检测」即可看到结果。",
		Blocked: func() string {
			if _, err := lookPath("timedatectl"); err != nil {
				return "本机没有 timedatectl（非 systemd 系统），请按体检建议手动启用 chrony 或 timesyncd"
			}
			if geteuid() != 0 {
				return "需要 root 才能改时间同步开关"
			}
			// 开关已经是开的、只是还没完成对时：再点一次「启用」不会改变任何东西。
			if ntp, ok := clockNTPState(); ok && ntpEnabled(ntp) {
				return "自动对时开关已是启用状态（NTP=yes）；未完成对时请检查上游 NTP 是否可达，面板不重复启用"
			}
			return ""
		},
		Apply: applyClockFix,
		RevertFn: func(rec fixRecord) error {
			if err := runTimedatectl("set-ntp", clockBool(rec.Orig["ntp"])); err != nil {
				return fmt.Errorf("恢复时间同步开关失败: %w", err)
			}
			return nil
		},
	}
}

func applyClockFix(rec *fixRecord) error {
	orig, ok := clockNTPState()
	if !ok {
		return fmt.Errorf("无法读取当前时间同步开关（timedatectl show 失败），未做任何改动")
	}
	if ntpEnabled(orig) {
		return fmt.Errorf("自动对时已是启用状态，未做任何改动")
	}
	if err := runTimedatectl("set-ntp", "true"); err != nil {
		return fmt.Errorf("启用自动对时失败: %w", err)
	}
	now, _ := clockNTPState()
	if !ntpEnabled(now) {
		// 失败零改动：把开关拨回原样，而不是留下一个「点了没生效」的状态。
		if rb := runTimedatectl("set-ntp", clockBool(orig)); rb != nil {
			return fmt.Errorf("启用后复检未通过（NTP=%s），且恢复原状失败: %v", valueOr(now, "空"), rb)
		}
		return fmt.Errorf("启用后复检未通过（NTP=%s），已恢复原状", valueOr(now, "空"))
	}
	rec.Orig["ntp"] = orig
	rec.Changes = fmt.Sprintf("自动对时开关 %s → yes", valueOr(orig, "未知"))
	return nil
}

func fdFix() fixSpec {
	return fixSpec{
		Key:    fixKeyFD,
		Check:  "文件描述符上限",
		Label:  fmt.Sprintf("提高到 %d", fdWarnThreshold),
		Revert: "恢复原上限",
		Note: fmt.Sprintf("把面板进程的 RLIMIT_NOFILE soft 上限提到 %d（不超过 hard）；"+
			"面板 fork 的内核进程会继承它。重启面板后会回落，长期生效要把 systemd 单元加 "+
			"LimitNOFILE=%d。", fdWarnThreshold, fdWarnThreshold),
		Blocked: func() string {
			cur, hard, ok := getNofile()
			if !ok {
				return "当前平台不支持在进程内调整 rlimit"
			}
			if cur >= fdWarnThreshold {
				return fmt.Sprintf("soft 上限已是 %d，无需调整", cur)
			}
			if hard < fdWarnThreshold {
				return fmt.Sprintf("hard 上限只有 %d，进程内提不上去：请在 systemd 单元里设 LimitNOFILE=%d 后重启面板",
					hard, fdWarnThreshold)
			}
			return ""
		},
		Apply: applyFDFix,
		RevertFn: func(rec fixRecord) error {
			soft, err := strconv.ParseUint(rec.Orig["soft"], 10, 64)
			if err != nil {
				return fmt.Errorf("修复记录里的原上限不可解析，拒绝回退: %w", err)
			}
			_, hard, ok := getNofile()
			if !ok {
				return fmt.Errorf("当前平台不支持在进程内调整 rlimit")
			}
			// 只回退 soft：hard 从未被改动，用现值最安全（降 hard 是不可逆的）。
			if err := setNofile(soft, hard); err != nil {
				return fmt.Errorf("恢复 soft 上限失败: %w", err)
			}
			if got, _, ok := getNofile(); ok && got != soft {
				return fmt.Errorf("回退后复检未通过（soft=%d，期望 %d）", got, soft)
			}
			return nil
		},
	}
}

func applyFDFix(rec *fixRecord) error {
	cur, hard, ok := getNofile()
	if !ok {
		return fmt.Errorf("当前平台不支持在进程内调整 rlimit，未做任何改动")
	}
	if hard < fdWarnThreshold {
		return fmt.Errorf("hard 上限只有 %d，低于建议值 %d，请在 systemd 单元里设置 LimitNOFILE 后重启面板",
			hard, fdWarnThreshold)
	}
	if cur >= fdWarnThreshold {
		return fmt.Errorf("soft 上限已是 %d，未做任何改动", cur)
	}
	if err := setNofile(fdWarnThreshold, hard); err != nil {
		return fmt.Errorf("调整 rlimit 失败: %w", err)
	}
	got, _, ok := getNofile()
	if !ok || got < fdWarnThreshold {
		if rb := setNofile(cur, hard); rb != nil {
			return fmt.Errorf("调整后复检未通过（soft=%d），且恢复原值失败: %v", got, rb)
		}
		return fmt.Errorf("调整后复检未通过（soft=%d），已恢复原值", got)
	}
	rec.Orig["soft"] = strconv.FormatUint(cur, 10)
	rec.Changes = fmt.Sprintf("面板进程 soft 上限 %d → %d", cur, fdWarnThreshold)
	return nil
}

func conntrackFix() fixSpec {
	return fixSpec{
		Key:    fixKeyConntrack,
		Check:  "conntrack 连接跟踪水位",
		Label:  "调大连接跟踪上限",
		Revert: "恢复原上限",
		Note: fmt.Sprintf("把 nf_conntrack_max 翻倍（最多到 %d），写入 %s 并立即生效；"+
			"回退会恢复原值并删除该文件。", maxConntrackCap, conntrackConfPath),
		Blocked: func() string {
			if geteuid() != 0 {
				return "需要 root 才能调整 nf_conntrack_max"
			}
			max, ok := readUintFile(procCtMax)
			if !ok || max == 0 {
				return "本机未启用 nf_conntrack（没有 NAT / 端口转发时无需调整）"
			}
			if max >= maxConntrackCap {
				return fmt.Sprintf("nf_conntrack_max 已是 %d，达到面板建议上限：请先排查连接泄漏或缩短超时", max)
			}
			if in, _ := detectContainer(); in {
				return "检测到容器环境：容器内改 conntrack 通常无效，且可能影响宿主机，请到宿主机上调整"
			}
			return ""
		},
		Apply:    applyConntrackFix,
		RevertFn: revertConntrackFix,
	}
}

func applyConntrackFix(rec *fixRecord) error {
	max, ok := readUintFile(procCtMax)
	if !ok || max == 0 {
		return fmt.Errorf("读不到 %s，未做任何改动", procCtMax)
	}
	if max >= maxConntrackCap {
		return fmt.Errorf("nf_conntrack_max 已是 %d，未做任何改动", max)
	}
	target := max * 2
	if target > maxConntrackCap {
		target = maxConntrackCap
	}
	if err := mkdirAll(sysctlDir, 0o755); err != nil {
		return fmt.Errorf("创建 %s 失败，未改动系统: %w", sysctlDir, err)
	}
	if b, err := readFile(conntrackConfPath); err == nil {
		rec.Backup[conntrackConfPath] = string(b)
	}
	text := "# 由 Triune Panel 写入（conntrack 水位修复）；要还原请用面板的「回退」\n" +
		"net.netfilter.nf_conntrack_max=" + strconv.FormatUint(target, 10) + "\n"
	tmp := conntrackConfPath + ".tmp"
	if err := writeFile(tmp, []byte(text), 0o644); err != nil {
		_ = remove(tmp)
		return fmt.Errorf("写临时配置失败，未改动系统: %w", err)
	}
	if err := rename(tmp, conntrackConfPath); err != nil {
		_ = remove(tmp)
		return fmt.Errorf("替换配置失败，未改动系统: %w", err)
	}

	if err := activateConntrack(target); err != nil {
		_ = remove(conntrackConfPath)
		rec.Backup = nil
		return fmt.Errorf("使 conntrack 配置生效失败，已删除写入的配置文件: %w", err)
	}
	if got, _ := readUintFile(procCtMax); got != target {
		_ = remove(conntrackConfPath)
		rec.Backup = nil
		if rb := restoreCtMax(max); rb != nil {
			return fmt.Errorf("复检未通过（期望 %d、实测 %d），且恢复原值失败: %v", target, got, rb)
		}
		return fmt.Errorf("复检未通过（期望 %d、实测 %d），已恢复原值", target, got)
	}
	rec.Orig["max"] = strconv.FormatUint(max, 10)
	rec.Files = []string{conntrackConfPath}
	rec.Changes = fmt.Sprintf("nf_conntrack_max %d → %d", max, target)
	return nil
}

func revertConntrackFix(rec fixRecord) error {
	if v, ok := rec.Orig["max"]; ok && v != "" {
		orig, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return fmt.Errorf("修复记录里的原上限不可解析，拒绝回退: %w", err)
		}
		if err := restoreCtMax(orig); err != nil {
			return fmt.Errorf("恢复 nf_conntrack_max=%d 失败: %w", orig, err)
		}
	}
	for _, p := range rec.Files {
		if content, existed := rec.Backup[p]; existed {
			if err := writeFile(p, []byte(content), 0o644); err != nil {
				return fmt.Errorf("恢复原配置文件 %s 失败: %w", p, err)
			}
			continue
		}
		if err := remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("删除配置文件 %s 失败: %w", p, err)
		}
	}
	return nil
}

// activateConntrack 让新上限立即生效：优先 sysctl -p，没有 sysctl 就直写 /proc/sys。
func activateConntrack(target uint64) error {
	if sysctl, err := lookPath("sysctl"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := runCmd(ctx, sysctl, "-p", conntrackConfPath); err != nil {
			return fmt.Errorf("sysctl -p %s: %w", conntrackConfPath, err)
		}
		return nil
	}
	if err := writeFile(procCtMax, []byte(strconv.FormatUint(target, 10)), 0o644); err != nil {
		return fmt.Errorf("写 %s: %w", procCtMax, err)
	}
	return nil
}

func restoreCtMax(v uint64) error {
	if err := writeFile(procCtMax, []byte(strconv.FormatUint(v, 10)), 0o644); err != nil {
		return fmt.Errorf("写 %s: %w", procCtMax, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 记录读写与公开入口
// ---------------------------------------------------------------------------

func fixRecordPath(d string) string { return filepath.Join(d, fixFileName) }

// loadFixRecords 只读；文件不存在或损坏都当成「没有记录」，绝不因为读失败就写盘。
func loadFixRecords(dataDir string) []fixRecord {
	b, err := readFile(fixRecordPath(dataDir))
	if err != nil {
		return nil
	}
	var recs []fixRecord
	if err := json.Unmarshal(b, &recs); err != nil {
		return nil
	}
	return recs
}

func saveFixRecords(dataDir string, recs []fixRecord) error {
	if err := mkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	if len(recs) == 0 {
		if err := remove(fixRecordPath(dataDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	blob, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(fixRecordPath(dataDir), blob, 0o600)
}

func findFixRecord(recs []fixRecord, key string) *fixRecord {
	for i := range recs {
		if recs[i].Key == key {
			r := recs[i]
			return &r
		}
	}
	return nil
}

func withoutFixRecord(recs []fixRecord, key string) []fixRecord {
	out := make([]fixRecord, 0, len(recs))
	for _, r := range recs {
		if r.Key != key {
			out = append(out, r)
		}
	}
	return out
}

// listFixActions 汇总三项动作的现状，供前端渲染按钮与「已修复」区。
func listFixActions(dataDir string) []FixAction {
	recs := loadFixRecords(dataDir)
	out := make([]FixAction, 0, 3)
	for _, s := range fixSpecs() {
		a := FixAction{Key: s.Key, Check: s.Check, Label: s.Label, Revert: s.Revert, Note: s.Note}
		if b := s.Blocked(); b != "" {
			a.Blocked = b
		} else {
			a.Ready = true
		}
		if r := findFixRecord(recs, s.Key); r != nil {
			a.Applied, a.At, a.Changes = true, r.At, r.Changes
		}
		out = append(out, a)
	}
	return out
}

// ApplyFix 执行一项修复。key 必须是注册表里的标识，confirm 必须精确匹配。
func ApplyFix(dataDir, confirm, key string) error {
	if confirm != FixConfirm {
		return ErrConfirm
	}
	if platformUnsupported() {
		return ErrUnsupported
	}
	spec := findFix(key)
	if spec == nil {
		return fmt.Errorf("未知的修复项 %q", key)
	}
	if b := spec.Blocked(); b != "" {
		return fmt.Errorf("%w: %s", ErrUnsupported, b)
	}
	rec := fixRecord{
		Key:    key,
		At:     nowFn().Format("2006-01-02 15:04:05"),
		Orig:   map[string]string{},
		Backup: map[string]string{},
	}
	if err := spec.Apply(&rec); err != nil {
		return err
	}
	recs := append(withoutFixRecord(loadFixRecords(dataDir), key), rec)
	if err := saveFixRecords(dataDir, recs); err != nil {
		// 没有记录就无法回退：宁可撤销这次修复，也不留「改过但说不清改了什么」的状态。
		if rb := spec.RevertFn(rec); rb != nil {
			return fmt.Errorf("记录修复失败，且回退也失败，请手工核对系统状态: %v；%w", rb, err)
		}
		return fmt.Errorf("记录修复失败，已撤销本次改动: %w", err)
	}
	return nil
}

// RevertFix 按记录把某一项恢复到修复前的状态。
func RevertFix(dataDir, confirm, key string) error {
	if confirm != FixRevertConfirm {
		return ErrConfirm
	}
	if platformUnsupported() {
		return ErrUnsupported
	}
	spec := findFix(key)
	if spec == nil {
		return fmt.Errorf("未知的修复项 %q", key)
	}
	recs := loadFixRecords(dataDir)
	rec := findFixRecord(recs, key)
	if rec == nil {
		// 没有记录就绝不动系统：否则可能把用户自己的配置删掉或改回错误的值。
		return fmt.Errorf("没有找到「%s」的修复记录，拒绝回退（未改动任何配置）", key)
	}
	if err := spec.RevertFn(*rec); err != nil {
		return err
	}
	if err := saveFixRecords(dataDir, withoutFixRecord(recs, key)); err != nil {
		return fmt.Errorf("回退已执行，但删除记录失败: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

func clockNTPState() (string, bool) {
	p, err := lookPath("timedatectl")
	if err != nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := runCmd(ctx, p, "show", "-p", "NTP", "--value")
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

func runTimedatectl(args ...string) error {
	p, err := lookPath("timedatectl")
	if err != nil {
		return fmt.Errorf("找不到 timedatectl")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := runCmd(ctx, p, args...); err != nil {
		return err
	}
	return nil
}

// clockBool 把 timedatectl 的开关值转成 set-ntp 接受的布尔字面量。
func clockBool(v string) string {
	if ntpEnabled(v) {
		return "true"
	}
	return "false"
}

// ntpEnabled 判断 timedatectl 的开关值是否为「开」。
// 不同 systemd 版本在 `show --value` 上给 yes / true / 1 三种写法，按字面比 =
// 会把自己刚打开的开关判成没打开，然后误报「复检未通过」并回滚。
func ntpEnabled(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "yes", "true", "1", "on":
		return true
	}
	return false
}
