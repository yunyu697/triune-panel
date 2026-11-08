package hostopt

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const unsupportedReason = "BBR 只在 Linux 主机上可用"

var containerPattern = regexp.MustCompile(`(?i)(docker|kubepods|containerd|libpod|podman|lxc)`)

// linuxReport 是完整的 Linux 只读体检实现。
//
// 它刻意放在没有 build tag 的文件里：所有 IO 都经由可替换变量（readFile/stat/
// lookPath/runCmd/lookupEnv），因此 macOS 开发机 / CI 上也能用桩跑完整逻辑；
// 而生产环境只有编译为 Linux 的二进制（见 detect_linux.go）才会真正触碰 /proc。
// 这样「CI 四平台能编译」与「Linux 逻辑能被单测覆盖」两者可以兼得。
func linuxReport(dataDir string) Report {
	r := Report{Platform: "linux"}

	kernel, _ := readKernel()
	r.Kernel = kernel
	if b, err := readFile(ccPath()); err == nil {
		r.CurrentCC = firstToken(b)
	}
	if b, err := readFile(availPath()); err == nil {
		r.AvailableCC = strings.Fields(string(b))
	}
	if b, err := readFile(qdiscPath()); err == nil {
		r.DefaultQdisc = firstToken(b)
	}

	// BBR 可用：/sys/module/tcp_bbr 目录存在，或 available_cc 里列了 bbr。
	_, moduleErr := stat(bbrModuleDir)
	r.BBRModule = moduleErr == nil || containsFold(r.AvailableCC, "bbr")

	r.IsRoot = geteuid() == 0
	r.InContainer, _ = detectContainer()

	// tc qdisc show 是只读命令，但仍加 3 秒超时，避免卡死整个面板请求。
	detail, qOK, qWarn := probeQdisc(defaultIface())
	r.QdiscDetail = detail

	kOK, kKnown := kernelAtLeast49(r.Kernel)
	r.Checks = []Check{
		{
			Name:   "内核版本 ≥ 4.9",
			OK:     kOK,
			Warn:   !kKnown,
			Detail: kernelDetail(r.Kernel, kOK, kKnown),
		},
		{
			Name:   "BBR 可用",
			OK:     r.BBRModule,
			Detail: bbrDetail(r.BBRModule),
		},
		{
			Name:   "当前拥塞控制已是 bbr",
			OK:     strings.EqualFold(r.CurrentCC, "bbr"),
			Detail: fmt.Sprintf("当前 tcp_congestion_control = %q", valueOr(r.CurrentCC, "读取失败")),
		},
		{
			Name:   "默认 qdisc 为 fq/fq_codel",
			OK:     isFQ(r.DefaultQdisc),
			Detail: fmt.Sprintf("net.core.default_qdisc = %q", valueOr(r.DefaultQdisc, "读取失败")),
		},
		{
			Name:   "tc 实测 qdisc",
			OK:     qOK && isFQText(detail),
			Warn:   qWarn,
			Detail: detail,
		},
		{
			Name:   "当前不在容器里",
			OK:     !r.InContainer,
			Detail: containerDetail(r.InContainer),
		},
		{
			Name:   "面板具备 root 权限",
			OK:     r.IsRoot,
			Detail: rootDetail(r.IsRoot),
		},
	}

	// Suggested 始终给出：即使 ApplyEnabled=true，用户也该知道手动怎么做、怎么验证。
	r.Suggested = []string{
		"写入 BBR 配置：printf 'net.core.default_qdisc=fq\\nnet.ipv4.tcp_congestion_control=bbr\\n' > /etc/sysctl.d/99-bbr.conf",
		"使配置生效：sysctl --system",
		"验证结果：sysctl net.ipv4.tcp_congestion_control",
		"也可直接在面板点『应用』，面板会先备份原值再写配置",
	}

	// ApplyEnabled 严格只由「Linux + root + 非容器 + BBR 可用」四项决定：
	// 内核版本、当前 CC、qdisc 只是体检项，不作为放行条件（BBR 可用本身已隐含内核支持）。
	var reasons []string
	if r.InContainer {
		reasons = append(reasons, "检测到容器环境（docker/k8s/lxc/OpenVZ 等），容器内改 sysctl 通常无效且可能影响宿主机，已禁用应用")
	}
	if !r.IsRoot {
		reasons = append(reasons, "面板当前不是 root，无法写入 /etc/sysctl.d 与 /proc/sys")
	}
	if !r.BBRModule {
		reasons = append(reasons, "内核未提供 bbr 拥塞控制（tcp_available_congestion_control 无 bbr 且无 tcp_bbr 模块）")
	}
	r.ApplyEnabled = len(reasons) == 0
	r.Reason = strings.Join(reasons, "；")
	return r
}

// unsupportedReport 是非 Linux 平台的兜底报告：不放行应用，也不读任何 /proc。
func unsupportedReport(dataDir string) Report {
	return Report{
		ApplyEnabled: false,
		Reason:       unsupportedReason,
		Checks: []Check{{
			Name:   "平台支持",
			Warn:   true,
			Detail: "当前平台不是 Linux；BBR 依赖 Linux 内核的 tcp_bbr 与 /proc/sys，面板不会读取或修改任何系统配置",
		}},
		Suggested: []string{
			"BBR 仅在 Linux 主机上可用；请在 VPS 的 Linux 系统上部署面板后再查看",
			"Linux 下也可手动执行：printf 'net.core.default_qdisc=fq\\nnet.ipv4.tcp_congestion_control=bbr\\n' > /etc/sysctl.d/99-bbr.conf && sysctl --system",
		},
	}
}

// readKernel 优先读 /proc/version（纯 Go 文件读，不调 uname 命令），
// 读不到再交给各平台自己的 kernelReleaseFallback。
func readKernel() (string, bool) {
	if b, err := readFile(procVersion); err == nil {
		f := strings.Fields(string(b))
		for i := 0; i+1 < len(f); i++ {
			if f[i] == "version" {
				return f[i+1], true
			}
		}
	}
	return kernelReleaseFallback()
}

// kernelAtLeast49 解析 "5.15.0-91-generic" 这类内核版本号，判断是否 ≥ 4.9。
func kernelAtLeast49(ver string) (ok, known bool) {
	parts := strings.SplitN(ver, ".", 3)
	if len(parts) < 2 {
		return false, false
	}
	maj, e1 := strconv.Atoi(firstDigits(parts[0]))
	min, e2 := strconv.Atoi(firstDigits(parts[1]))
	if e1 != nil || e2 != nil {
		return false, false
	}
	return maj > 4 || (maj == 4 && min >= 9), true
}

func firstDigits(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}

// detectContainer 综合 cgroup / 哨兵文件 / 环境变量 / OpenVZ 判断是否在容器里。
// 这些检查全部只读，任何一个命中就禁用应用按钮。
func detectContainer() (bool, string) {
	if b, err := readFile(procCgroup); err == nil && containerPattern.Match(b) {
		return true, "/proc/1/cgroup 命中容器特征（docker/kubepods/containerd/libpod/podman/lxc）"
	}
	if _, err := stat(dockerEnv); err == nil {
		return true, "/.dockerenv 存在"
	}
	if _, err := stat(containerEnv); err == nil {
		return true, "/.containerenv 存在"
	}
	if _, err := stat(procVZDir); err == nil {
		return true, "/proc/vz 存在，疑似 OpenVZ"
	}
	if v, ok := lookupEnv("KUBERNETES_SERVICE_HOST"); ok && v != "" {
		return true, "KUBERNETES_SERVICE_HOST 已设置，处于 k8s Pod 内"
	}
	return false, ""
}

// defaultIface 从 /proc/net/route 里取默认路由（dest=00000000）对应的网卡名。
func defaultIface() string {
	b, err := readFile(procRoute)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[1] == "00000000" {
			return f[0]
		}
	}
	return ""
}

// probeQdisc 返回 (摘要, 是否拿到有效 tc 输出, 是否属于「无法确认」)。
func probeQdisc(iface string) (detail string, ok, warn bool) {
	tc, err := lookPath("tc")
	if err != nil {
		return "未安装 tc，无法确认当前 qdisc", false, true
	}
	if iface == "" {
		return "未能确定默认网卡，未运行 tc qdisc show", false, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := runCmd(ctx, tc, "qdisc", "show", "dev", iface)
	if err != nil {
		return fmt.Sprintf("tc qdisc show 执行失败：%v", err), false, true
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return "tc qdisc show 无输出", false, true
	}
	return s, true, false
}

func firstToken(b []byte) string {
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

func containsFold(xs []string, want string) bool {
	for _, x := range xs {
		if strings.EqualFold(x, want) {
			return true
		}
	}
	return false
}

func isFQ(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "fq" || s == "fq_codel"
}

func isFQText(s string) bool { return strings.Contains(strings.ToLower(s), "fq") }

func valueOr(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func kernelDetail(ver string, ok, known bool) string {
	if !known {
		return "未能确认内核版本，无法判断是否 ≥ 4.9（不影响其他检查）"
	}
	if ok {
		return fmt.Sprintf("内核 %s ≥ 4.9，支持 BBR", ver)
	}
	return fmt.Sprintf("内核 %s 低于 4.9，BBR 需要 4.9 及以上内核", ver)
}

func bbrDetail(ok bool) string {
	if ok {
		return "已检测到 bbr 可用（tcp_available_congestion_control 或 /sys/module/tcp_bbr）"
	}
	return "未检测到 bbr：请先 modprobe tcp_bbr 或升级到支持 BBR 的内核"
}

func containerDetail(in bool) string {
	if in {
		return "检测到容器/虚拟化环境，已禁用由面板应用 BBR"
	}
	return "未检测到容器特征，运行在宿主机上"
}

func rootDetail(root bool) string {
	if root {
		return "面板以 root 运行，具备写入权限"
	}
	return "面板未以 root 运行，无法写 /etc/sysctl.d 与 /proc/sys"
}
