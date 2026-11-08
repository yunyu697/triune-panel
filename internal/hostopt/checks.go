// 只读体检的扩展项：时钟同步、文件描述符水位、conntrack 水位、sysctl 覆盖检查，
// 以及「系统概览」。
//
// 这里的每一条都必须是纯读：不建目录、不写文件、不改 sysctl。唯一会执行的外部
// 命令是 `timedatectl show` / `chronyc tracking`（只查状态），并带 3 秒超时。
package hostopt

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 只读探测路径与依赖；测试全部指向临时目录。
var (
	readDir        = os.ReadDir
	cpuCount       = runtime.NumCPU
	diskFree       = diskFreeImpl
	procFileMax    = "/proc/sys/fs/file-max"
	procFileNr     = "/proc/sys/fs/file-nr"
	procSelfLimits = "/proc/self/limits"
	procCtMax      = "/proc/sys/net/netfilter/nf_conntrack_max"
	procCtCount    = "/proc/sys/net/netfilter/nf_conntrack_count"
	etcSysctlConf  = "/etc/sysctl.conf"
	procLoadAvg    = "/proc/loadavg"
	procMemInfo    = "/proc/meminfo"
	procIPv6Off    = "/proc/sys/net/ipv6/conf/all/disable_ipv6"
	sysClassNet    = "/sys/class/net"
	etcTimezone    = "/etc/timezone"
)

// fdWarnThreshold 是「高并发代理节点」的经验下限：xray / sing-box 每个连接占
// 2～3 个 fd，1024 的默认上限在几百并发时就会 too many open files。
const fdWarnThreshold = 65535

// SystemOverview 是系统概览（只读）。字段全部可为零值，前端按空值降级。
type SystemOverview struct {
	Load      []float64 `json:"load"`
	CPUCount  int       `json:"cpu_count"`
	MemTotal  uint64    `json:"mem_total"`
	MemAvail  uint64    `json:"mem_avail"`
	DiskTotal uint64    `json:"disk_total"`
	DiskFree  uint64    `json:"disk_free"`
	IPv6      bool      `json:"ipv6"`
	MTU       int       `json:"mtu"`
	MTUIf     string    `json:"mtu_if"`
	Warnings  []string  `json:"warnings"`
}

// systemOverview 汇总负载、内存、数据目录所在磁盘、IPv6 与默认网卡 MTU。
func systemOverview(dataDir string) SystemOverview {
	var s SystemOverview
	s.CPUCount = cpuCount()

	if b, err := readFile(procLoadAvg); err == nil {
		f := strings.Fields(string(b))
		for i := 0; i < 3 && i < len(f); i++ {
			if v, err := strconv.ParseFloat(f[i], 64); err == nil {
				s.Load = append(s.Load, v)
			}
		}
	} else {
		s.Warnings = append(s.Warnings, "读不到 "+procLoadAvg)
	}

	if b, err := readFile(procMemInfo); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			v, err := strconv.ParseUint(f[1], 10, 64)
			if err != nil {
				continue
			}
			switch f[0] {
			case "MemTotal:":
				s.MemTotal = v * 1024
			case "MemAvailable:":
				s.MemAvail = v * 1024
			}
		}
	} else {
		s.Warnings = append(s.Warnings, "读不到 "+procMemInfo)
	}

	if dataDir != "" {
		if total, free, err := diskFree(dataDir); err == nil {
			s.DiskTotal, s.DiskFree = total, free
		} else {
			s.Warnings = append(s.Warnings, "读不到磁盘容量："+err.Error())
		}
	}

	if b, err := readFile(procIPv6Off); err == nil {
		s.IPv6 = strings.TrimSpace(string(b)) == "0"
	}

	if iface := defaultIface(); iface != "" {
		s.MTUIf = iface
		if b, err := readFile(filepath.Join(sysClassNet, iface, "mtu")); err == nil {
			if v, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				s.MTU = v
			}
		}
	} else {
		s.Warnings = append(s.Warnings, "未能确定默认网卡，跳过 MTU 探测")
	}
	return s
}

// checkClock 判断时钟是否已同步。
//
// REALITY 握手、TLS 证书有效期都吃系统时间；VPS 时钟漂移的典型症状正是
// 「服务端一切正常、客户端连不上」，所以这一项值得单独体检。
func checkClock() Check {
	const name = "时钟同步（NTP）"
	if p, err := lookPath("timedatectl"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		out, err := runCmd(ctx, p, "show", "-p", "NTPSynchronized", "-p", "Timezone", "--value")
		if err != nil {
			return Check{Name: name, Warn: true, Detail: "timedatectl 执行失败，无法确认：" + err.Error()}
		}
		lines := nonEmptyLines(string(out))
		sync, tz := "", ""
		if len(lines) > 0 {
			sync = lines[0]
		}
		if len(lines) > 1 {
			tz = lines[1]
		}
		ok := strings.EqualFold(strings.TrimSpace(sync), "yes")
		detail := fmt.Sprintf("时区 %s，NTPSynchronized=%s", valueOr(tz, "未知"), valueOr(sync, "未知"))
		if !ok {
			detail += "；未同步会让 REALITY 握手与证书校验随缘失败，建议启用 systemd-timesyncd 或 chrony"
		}
		return Check{Name: name, OK: ok, Detail: detail}
	}
	if p, err := lookPath("chronyc"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		out, err := runCmd(ctx, p, "tracking")
		if err != nil {
			return Check{Name: name, Warn: true, Detail: "chronyc tracking 执行失败，无法确认：" + err.Error()}
		}
		text := string(out)
		ok := strings.Contains(text, "Leap status     : Normal") || strings.Contains(text, "Leap status : Normal")
		detail := "chronyc tracking：" + firstLineWith(text, "System time")
		if !ok {
			detail += "；chrony 未处于 Normal 状态"
		}
		return Check{Name: name, OK: ok, Detail: detail}
	}
	tz := ""
	if b, err := readFile(etcTimezone); err == nil {
		tz = strings.TrimSpace(string(b))
	}
	return Check{
		Name: name, Warn: true,
		Detail: "未安装 timedatectl / chronyc，无法确认时钟同步状态（时区 " + valueOr(tz, "未知") + "）",
	}
}

// checkFDLimit 检查进程与系统两级的文件描述符上限。
func checkFDLimit() Check {
	const name = "文件描述符上限"
	soft, hard, okLimits := readProcLimits()
	fmax, okMax := readUintFile(procFileMax)
	alloc, _, okNr := readFileNr()

	if !okLimits && !okMax {
		return Check{Name: name, Warn: true, Detail: "读不到 /proc/self/limits 与 fs.file-max，无法判断"}
	}

	var parts []string
	if okLimits {
		parts = append(parts, fmt.Sprintf("面板进程 soft=%d hard=%s", soft, humanOrUnlimited(hard)))
	}
	if okMax {
		parts = append(parts, fmt.Sprintf("系统 fs.file-max=%d", fmax))
	}
	if okNr {
		parts = append(parts, fmt.Sprintf("已分配 %d 个", alloc))
	}
	detail := strings.Join(parts, "，")

	// 系统级已用超过 80% 也是真问题：再开连接就会 ENFILE。
	if okNr && okMax && fmax > 0 && alloc*100/fmax > 80 {
		return Check{Name: name, OK: false, Detail: detail + "；已用超过 80%，可能出现 too many open files"}
	}
	if okLimits && soft > 0 && soft < fdWarnThreshold {
		return Check{
			Name: name, OK: false,
			Detail: detail + fmt.Sprintf("；soft 上限低于建议值 %d，高并发时会拒新连接（systemd 服务可设 LimitNOFILE=%d）", fdWarnThreshold, fdWarnThreshold),
		}
	}
	return Check{Name: name, OK: true, Detail: detail}
}

// checkConntrack 检查 NAT 连接跟踪表水位。
//
// 转发/NAT 型节点在 conntrack 满表时表现为「间歇性断流」，且内核只在 dmesg
// 里打一行 table full, dropping packet，用户很难自己定位。
func checkConntrack() Check {
	const name = "conntrack 连接跟踪水位"
	max, okMax := readUintFile(procCtMax)
	cnt, okCnt := readUintFile(procCtCount)
	if !okMax {
		return Check{
			Name: name, Warn: true,
			Detail: "未启用 nf_conntrack（没有 NAT / 端口转发时属正常；若做转发请确认模块已加载）",
		}
	}
	detail := fmt.Sprintf("nf_conntrack_max=%d", max)
	if okCnt {
		detail += fmt.Sprintf("，当前 %d（%.1f%%）", cnt, pct(cnt, max))
	}
	if okCnt && max > 0 && cnt*100/max > 80 {
		return Check{Name: name, OK: false, Detail: detail + "；接近满表，内核会丢包，建议调大 nf_conntrack_max"}
	}
	return Check{Name: name, OK: true, Detail: detail}
}

// checkSysctlConflicts 找出会在面板配置之后覆盖同名键的文件。
//
// systemd-sysctl 按文件名字典序加载 /etc/sysctl.d/*.conf，最后再读
// /etc/sysctl.conf。云厂商镜像经常在 99- 之后或 sysctl.conf 里把 cc/qdisc 写回去，
// 症状是「面板显示已应用，实际值没变」。
func checkSysctlConflicts() Check {
	const name = "sysctl 配置覆盖检查"
	own := filepath.Base(cfgPath)
	var hits []string

	// /etc/sysctl.conf 最后加载，任何同名键都会盖掉面板的配置。
	hits = append(hits, scanSysctlFile(etcSysctlConf, own)...)

	entries, err := readDir(sysctlDir)
	if err != nil {
		if len(hits) == 0 {
			return Check{Name: name, Warn: true, Detail: "读不到 " + sysctlDir + "，未做覆盖检查"}
		}
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".conf") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, n := range names {
		// 字典序在面板配置之后的文件才会覆盖它。
		if n == own || n < own {
			continue
		}
		hits = append(hits, scanSysctlFile(filepath.Join(sysctlDir, n), own)...)
	}

	if len(hits) == 0 {
		return Check{Name: name, OK: true, Detail: "没有更靠后的配置文件覆盖面板管理的 sysctl 键"}
	}
	shown := hits
	suffix := ""
	if len(shown) > 5 {
		shown, suffix = shown[:5], fmt.Sprintf(" 等 %d 处", len(hits))
	}
	return Check{
		Name: name, OK: false,
		Detail: "以下位置会覆盖面板写入的值（面板改完可能不生效）：" + strings.Join(shown, "；") + suffix,
	}
}

// scanSysctlFile 返回某个配置文件里设置面板管理键的行（跳过面板自己的配置）。
func scanSysctlFile(path, own string) []string {
	if filepath.Base(path) == own {
		return nil
	}
	b, err := readFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for i, line := range strings.Split(string(b), "\n") {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, ";") {
			continue
		}
		key, val, ok := strings.Cut(s, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if !isManagedKey(key) {
			continue
		}
		out = append(out, fmt.Sprintf("%s:%d %s=%s", filepath.Base(path), i+1, key, strings.TrimSpace(val)))
	}
	return out
}

// readProcLimits 解析 /proc/self/limits 里的 "Max open files" 一行。
func readProcLimits() (soft uint64, hard uint64, ok bool) {
	b, err := readFile(procSelfLimits)
	if err != nil {
		return 0, 0, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "Max open files") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 5 {
			return 0, 0, false
		}
		s, err1 := strconv.ParseUint(f[3], 10, 64)
		h, err2 := strconv.ParseUint(f[4], 10, 64)
		if err1 != nil {
			return 0, 0, false
		}
		if err2 != nil { // hard 可能是 unlimited
			h = 0
		}
		return s, h, true
	}
	return 0, 0, false
}

// readFileNr 解析 /proc/sys/fs/file-nr：已分配 / 未使用 / 上限。
func readFileNr() (alloc, unused uint64, ok bool) {
	b, err := readFile(procFileNr)
	if err != nil {
		return 0, 0, false
	}
	f := strings.Fields(string(b))
	if len(f) < 1 {
		return 0, 0, false
	}
	a, err := strconv.ParseUint(f[0], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	if len(f) > 1 {
		unused, _ = strconv.ParseUint(f[1], 10, 64)
	}
	return a, unused, true
}

func readUintFile(path string) (uint64, bool) {
	b, err := readFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func pct(a, b uint64) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) * 100 / float64(b)
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func firstLineWith(s, want string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, want) {
			return strings.TrimSpace(line)
		}
	}
	return valueOr(strings.TrimSpace(s), "无输出")
}

func humanOrUnlimited(v uint64) string {
	if v == 0 {
		return "unlimited"
	}
	return strconv.FormatUint(v, 10)
}
