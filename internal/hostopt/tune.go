// 可调网络参数：参数表、取值校验、差异预览、写入 + 复检 + 完整回滚。
//
// 设计要点：
//   - 参数表只在这里定义一份（managedParams），配置渲染、差异预览、覆盖检查、
//     回滚全部读同一份，避免「预览说改三项、实际改了五项」。
//   - 写入前后都以 /proc/sys 的实际值为准：先快照原值，写完再复检，
//     复检不符就整体回滚——「配置写了却没生效」是这一页最需要防的失败模式。
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

const expectedFileName = "hostopt-net.expected.json"

// 参数键：面板只碰这六个，不多不少。
const (
	keyQdisc     = "net.core.default_qdisc"
	keyCC        = "net.ipv4.tcp_congestion_control"
	keyRmem      = "net.core.rmem_max"
	keyWmem      = "net.core.wmem_max"
	keySomaxconn = "net.core.somaxconn"
	keyPortRange = "net.ipv4.ip_local_port_range"
)

// 取值边界：既防手滑，也避免把内核缓冲开到吃光内存。
const (
	minBuf       = 64 << 10  // 64 KiB
	maxBuf       = 256 << 20 // 256 MiB
	minSomaxconn = 128
	maxSomaxconn = 262144
)

// Tune 是面板能改的全部网络参数。字段用字节 / 个数 / 端口号，单位在前端展示。
type Tune struct {
	CC        string `json:"cc"`
	Qdisc     string `json:"qdisc"`
	RmemMax   int    `json:"rmem_max"`
	WmemMax   int    `json:"wmem_max"`
	Somaxconn int    `json:"somaxconn"`
	PortStart int    `json:"port_start"`
	PortEnd   int    `json:"port_end"`
}

// DefaultTune 是「VPS 跑代理节点」的稳妥默认值：
// BBR + fq、16 MiB 缓冲、8192 backlog、10240-60999 端口段（避开常用服务端口）。
func DefaultTune() Tune {
	return Tune{
		CC: "bbr", Qdisc: "fq",
		RmemMax: 16 << 20, WmemMax: 16 << 20,
		Somaxconn: 8192,
		PortStart: 10240, PortEnd: 60999,
	}
}

// TuneOptions 是前端下拉框的可选值，全部来自本机探测。
type TuneOptions struct {
	CC    []string `json:"cc"`
	Qdisc []string `json:"qdisc"`
}

// Param 是一行「现值 → 目标值」的差异。
type Param struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Current string `json:"current"`
	Value   string `json:"value"`
	Changed bool   `json:"changed"`
}

// managedParams 是唯一的参数表：键、显示名、读取路径。
func managedParams() []struct {
	Key   string
	Label string
	Path  func() string
} {
	return []struct {
		Key   string
		Label string
		Path  func() string
	}{
		{keyCC, "拥塞控制算法", ccPath},
		{keyQdisc, "默认队列规则", qdiscPath},
		{keyRmem, "TCP 接收缓冲上限", func() string { return filepath.Join(procSys, "net", "core", "rmem_max") }},
		{keyWmem, "TCP 发送缓冲上限", func() string { return filepath.Join(procSys, "net", "core", "wmem_max") }},
		{keySomaxconn, "全连接队列上限", func() string { return filepath.Join(procSys, "net", "core", "somaxconn") }},
		{keyPortRange, "本地端口范围", func() string { return filepath.Join(procSys, "net", "ipv4", "ip_local_port_range") }},
	}
}

// managedKeys 供覆盖检查复用同一份键表。
func managedKeys() []string {
	ps := managedParams()
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Key)
	}
	return out
}

func isManagedKey(key string) bool {
	for _, k := range managedKeys() {
		if k == key {
			return true
		}
	}
	return false
}

func keyPath(key string) string {
	for _, p := range managedParams() {
		if p.Key == key {
			return p.Path()
		}
	}
	return ""
}

func keyLabel(key string) string {
	for _, p := range managedParams() {
		if p.Key == key {
			return p.Label
		}
	}
	return key
}

// value 返回某个键在该方案下的目标值（写入 /proc/sys 的原始文本）。
func (t Tune) value(key string) string {
	switch key {
	case keyCC:
		return t.CC
	case keyQdisc:
		return t.Qdisc
	case keyRmem:
		return strconv.Itoa(t.RmemMax)
	case keyWmem:
		return strconv.Itoa(t.WmemMax)
	case keySomaxconn:
		return strconv.Itoa(t.Somaxconn)
	case keyPortRange:
		return fmt.Sprintf("%d %d", t.PortStart, t.PortEnd)
	}
	return ""
}

func (t Tune) equal(o Tune) bool {
	for _, k := range managedKeys() {
		if normVal(t.value(k)) != normVal(o.value(k)) {
			return false
		}
	}
	return true
}

// configText 渲染写入 /etc/sysctl.d 的配置：键值按参数表顺序，带一行来源注释。
func configText(t Tune) string {
	var b strings.Builder
	b.WriteString("# 由 Triune Panel 写入；要还原请用面板「撤销面板的改动」\n")
	for _, p := range managedParams() {
		fmt.Fprintf(&b, "%s=%s\n", p.Key, t.value(p.Key))
	}
	return b.String()
}

// currentTune 从 /proc/sys 读回当前生效值（只读）。
func currentTune() Tune {
	t := Tune{
		CC:    trimText(ccPath()),
		Qdisc: trimText(qdiscPath()),
	}
	t.RmemMax, _ = readInt(keyPath(keyRmem))
	t.WmemMax, _ = readInt(keyPath(keyWmem))
	t.Somaxconn, _ = readInt(keyPath(keySomaxconn))
	if a, b, ok := readPortRange(keyPath(keyPortRange)); ok {
		t.PortStart, t.PortEnd = a, b
	}
	return t
}

// PlanTune 给出「现值 → 目标值」的差异表，供前端在应用前预览。
func PlanTune(cur, want Tune) []Param {
	out := make([]Param, 0, len(managedKeys()))
	for _, p := range managedParams() {
		c := cur.value(p.Key)
		// 端口范围在 /proc 里是制表符分隔，展示前统一成空格。
		if p.Key == keyPortRange {
			c = normVal(c)
		}
		v := want.value(p.Key)
		out = append(out, Param{
			Key: p.Key, Label: p.Label,
			Current: c, Value: v,
			Changed: normVal(c) != normVal(v),
		})
	}
	return out
}

// Verify 是一条「期望 vs 实测」的复检结果。
type Verify struct {
	Key  string `json:"key"`
	Want string `json:"want"`
	Got  string `json:"got"`
	OK   bool   `json:"ok"`
}

// verifyTune 重新读 /proc/sys，确认写入的值真的生效了。
func verifyTune(want Tune) []Verify {
	out := make([]Verify, 0, len(managedKeys()))
	for _, k := range managedKeys() {
		got := normVal(trimText(keyPath(k)))
		w := normVal(want.value(k))
		out = append(out, Verify{Key: k, Want: w, Got: got, OK: got == w})
	}
	return out
}

// failedVerify 只返回复检不通过的那几项。
func failedVerify(want Tune) []Verify {
	var bad []Verify
	for _, v := range verifyTune(want) {
		if !v.OK {
			bad = append(bad, v)
		}
	}
	return bad
}

// verifyCheck 把「上次写入是否仍生效」做成一条体检项：
// 用户改了别处、或云厂商脚本又把值写回去时，这里会直接显示不符。
func verifyCheck(exp Tune, cur Tune) Check {
	const name = "面板写入的参数已生效"
	var bad []string
	for _, k := range managedKeys() {
		w, g := normVal(exp.value(k)), normVal(cur.value(k))
		if w != g {
			bad = append(bad, fmt.Sprintf("%s 期望 %s、实测 %s", k, w, valueOr(g, "空")))
		}
	}
	if len(bad) == 0 {
		return Check{Name: name, OK: true, Detail: "六个参数的实测值与面板写入的一致"}
	}
	return Check{Name: name, OK: false, Detail: "以下参数与面板写入的不一致（可能被其它配置覆盖或未生效）：" + strings.Join(bad, "；")}
}

// validateTune 校验目标值，并把「内核没这个算法」这类问题在写盘前就拦住。
func validateTune(t Tune, availCC []string) error {
	if len(availCC) > 0 && !containsFold(availCC, t.CC) {
		return fmt.Errorf("内核不支持拥塞控制算法 %q，可用：%s", t.CC, strings.Join(availCC, " "))
	}
	switch t.Qdisc {
	case "fq", "fq_codel", "cake":
	default:
		return fmt.Errorf("不支持的队列规则 %q（可选 fq / fq_codel / cake）", t.Qdisc)
	}
	for _, f := range []struct {
		name string
		v    int
	}{{"接收缓冲上限", t.RmemMax}, {"发送缓冲上限", t.WmemMax}} {
		if f.v < minBuf || f.v > maxBuf {
			return fmt.Errorf("%s需在 %d ~ %d 字节之间，当前 %d", f.name, minBuf, maxBuf, f.v)
		}
	}
	if t.Somaxconn < minSomaxconn || t.Somaxconn > maxSomaxconn {
		return fmt.Errorf("全连接队列上限需在 %d ~ %d 之间，当前 %d", minSomaxconn, maxSomaxconn, t.Somaxconn)
	}
	if t.PortStart < 1024 || t.PortEnd > 65535 || t.PortStart >= t.PortEnd {
		return fmt.Errorf("本地端口范围不合法：%d-%d（需 1024 ≤ 起点 < 终点 ≤ 65535）", t.PortStart, t.PortEnd)
	}
	return nil
}

// tuneOptions 汇总前端可选的 cc / qdisc。
func tuneOptions(availCC []string) TuneOptions {
	cc := append([]string{}, availCC...)
	if len(cc) == 0 {
		cc = []string{"bbr", "cubic"}
	}
	return TuneOptions{CC: cc, Qdisc: []string{"fq", "fq_codel", "cake"}}
}

// bbrOrig 是应用前的原值快照，写到 dataDir 供 Revert 审计与恢复。
// 连「配置文件原本是否存在 / 内容」都记下来，是为了做到完整回滚：
// 若原本就有同名配置，撤销时恢复它，而不是粗暴删除用户的文件。
//
// CurrentCC / DefaultQdisc 是 v1.5 及更早的字段，保留用于读旧备份。
type bbrOrig struct {
	ConfigExisted bool              `json:"config_existed"`
	ConfigContent []byte            `json:"config_content"`
	CurrentCC     string            `json:"current_cc"`
	DefaultQdisc  string            `json:"default_qdisc"`
	Params        map[string]string `json:"params,omitempty"`
}

func applyTune(dataDir string, want Tune) error {
	// 「先备份原值」：任何一个键读不到就干脆不应用，否则回滚时会把空串写回去。
	orig, err := snapshotOrig()
	if err != nil {
		return fmt.Errorf("读取原值失败，未改动系统: %w", err)
	}
	if err := mkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("创建数据目录失败，未改动系统: %w", err)
	}
	// 备份已存在时不覆盖：首次应用留下的才是真正的「原值」，改参数重试不能把它冲掉。
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
	if err := writeFile(tmp, []byte(configText(want)), 0o644); err != nil {
		_ = remove(tmp)
		return fmt.Errorf("写临时配置失败，未改动系统: %w", err)
	}
	if err := rename(tmp, cfgPath); err != nil {
		_ = remove(tmp)
		return fmt.Errorf("替换配置失败，未改动系统: %w", err)
	}

	// 生效：优先 sysctl -p；sysctl 不存在才退回直接写 /proc/sys。
	if err := activate(want); err != nil {
		return rollbackErr("使配置生效失败", err, rollback(dataDir, orig))
	}

	// 复检：写完必须回头读 /proc/sys。内核拒绝某个值（例如 cake 模块没加载）时
	// sysctl 可能只报一行警告，不回读就永远发现不了「配置写了却没生效」。
	if bad := failedVerify(want); len(bad) > 0 {
		return rollbackErr("复检未通过（写入的值没有全部生效）", fmt.Errorf("%s", describeVerify(bad)), rollback(dataDir, orig))
	}

	// 状态标记与期望值最后写：只有配置、生效、复检都过了，面板才认为「已应用」。
	if err := writeFile(appliedPath(dataDir), []byte("applied\n"), 0o600); err != nil {
		return rollbackErr("写状态标记失败", err, rollback(dataDir, orig))
	}
	if blob, err := json.MarshalIndent(want, "", "  "); err == nil {
		if err := writeFile(expectedPath(dataDir), blob, 0o600); err != nil {
			return rollbackErr("写期望值失败", err, rollback(dataDir, orig))
		}
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
	o := bbrOrig{Params: map[string]string{}}
	for _, k := range managedKeys() {
		p := keyPath(k)
		b, err := readFile(p)
		if err != nil {
			return bbrOrig{}, fmt.Errorf("读取 %s: %w", p, err)
		}
		o.Params[k] = strings.TrimSpace(string(b))
	}
	// 兼容字段：旧版备份格式只有这两个键，写出来便于人工比对。
	o.CurrentCC = o.Params[keyCC]
	o.DefaultQdisc = o.Params[keyQdisc]
	if b, err := readFile(cfgPath); err == nil {
		o.ConfigExisted = true
		o.ConfigContent = b
	}
	return o, nil
}

// activate 让刚写的配置真正生效。
func activate(want Tune) error {
	if sysctl, err := lookPath("sysctl"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := runCmd(ctx, sysctl, "-p", cfgPath); err != nil {
			return fmt.Errorf("sysctl -p %s: %w", cfgPath, err)
		}
		return nil
	}
	// sysctl 不存在：退回直接写 /proc/sys，不依赖任何外部命令。
	for _, k := range managedKeys() {
		if err := writeFile(keyPath(k), []byte(want.value(k)), 0o644); err != nil {
			return fmt.Errorf("写 %s: %w", keyPath(k), err)
		}
	}
	return nil
}

// rollback 是「失败零改动」的落地点：把系统状态恢复到应用之前。
//
// 只回滚系统侧（配置 + 运行值 + 标记 + 期望值）；dataDir 里的 orig 备份按产品
// 要求刻意保留，供审计追溯「面板曾经改过什么、原值是什么」。
func rollback(dataDir string, o bbrOrig) error {
	var errs []string
	if o.ConfigExisted {
		if err := writeFile(cfgPath, o.ConfigContent, 0o644); err != nil {
			errs = append(errs, "恢复原配置: "+err.Error())
		}
	} else if err := remove(cfgPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, "删除配置: "+err.Error())
	}
	for _, k := range managedKeys() {
		v, ok := o.Params[k]
		if !ok {
			continue
		}
		if err := writeFile(keyPath(k), []byte(v), 0o644); err != nil {
			errs = append(errs, "恢复 "+k+": "+err.Error())
		}
	}
	for _, p := range []string{appliedPath(dataDir), expectedPath(dataDir)} {
		if err := remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, "删除 "+filepath.Base(p)+": "+err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func revertTune(dataDir string) error {
	blob, err := readFile(origPath(dataDir))
	if err != nil {
		// 没有备份就绝不动配置文件：否则可能把用户自己写的 sysctl 配置删掉。
		return fmt.Errorf("缺少备份 %s，拒绝撤销（未删除任何配置）: %w", origPath(dataDir), err)
	}
	var o bbrOrig
	if err := json.Unmarshal(blob, &o); err != nil {
		return fmt.Errorf("备份文件损坏，拒绝撤销: %w", err)
	}
	// 旧版备份只有两个键，补进参数表。
	if len(o.Params) == 0 {
		o.Params = map[string]string{}
		if o.CurrentCC != "" {
			o.Params[keyCC] = o.CurrentCC
		}
		if o.DefaultQdisc != "" {
			o.Params[keyQdisc] = o.DefaultQdisc
		}
	}

	// 先恢复运行值；失败就停在这里，保留配置与标记，避免留下「已撤销却没恢复」的假象。
	for _, k := range managedKeys() {
		v, ok := o.Params[k]
		if !ok || v == "" {
			continue
		}
		if err := writeFile(keyPath(k), []byte(v), 0o644); err != nil {
			return fmt.Errorf("恢复 %s 失败: %w", k, err)
		}
	}

	// 完整回滚配置文件：原本有就还原内容，原本没有就删除。
	if o.ConfigExisted {
		if err := writeFile(cfgPath, o.ConfigContent, 0o644); err != nil {
			return fmt.Errorf("恢复原配置文件失败: %w", err)
		}
	} else if err := remove(cfgPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("删除配置文件失败: %w", err)
	}
	for _, p := range []string{appliedPath(dataDir), expectedPath(dataDir)} {
		if err := remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("删除 %s 失败: %w", filepath.Base(p), err)
		}
	}
	// orig 备份刻意保留，供审计。
	return nil
}

func describeVerify(bad []Verify) string {
	parts := make([]string, 0, len(bad))
	for _, v := range bad {
		parts = append(parts, fmt.Sprintf("%s 期望 %s、实测 %s", v.Key, v.Want, valueOr(v.Got, "空")))
	}
	return strings.Join(parts, "；")
}

// normVal 把「制表符分隔 / 多空格 / 换行」统一成单空格，便于比较与展示。
func normVal(s string) string { return strings.Join(strings.Fields(s), " ") }

func trimText(path string) string {
	b, err := readFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func readInt(path string) (int, bool) {
	b, err := readFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, false
	}
	return v, true
}

func readPortRange(path string) (start, end int, ok bool) {
	b, err := readFile(path)
	if err != nil {
		return 0, 0, false
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return 0, 0, false
	}
	a, e1 := strconv.Atoi(f[0])
	z, e2 := strconv.Atoi(f[1])
	if e1 != nil || e2 != nil {
		return 0, 0, false
	}
	return a, z, true
}
