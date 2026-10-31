// 一次性运维命令：reset-password / status / doctor / backup。
//
// 这些命令**直接读写 SQLite 或文件系统**，不依赖面板进程在跑（Tier 1）。
// 之所以不提供 apply / 切换内核这类命令：内核进程由 daemon 托管，CLI 裸跑会
// 抢端口、让 daemon 的内存状态与磁盘不一致，必须走 daemon 的 API（后续版本
// 用 unix socket 承接）。
package main

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"triune/internal/authn"
	"triune/internal/backup"
	"triune/internal/core"
	"triune/internal/firewall"
	"triune/internal/store"
)

// ---- 公共辅助 ----

// die 打印错误并以指定退出码结束（0 成功 / 1 执行失败 / 2 用法错误）。
func die(code int, format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(code)
}

// resolveDataDir 决定命令使用的数据目录：
// --data 显式指定 > 环境变量 TRIUNE_DATA > ./data（存在）> /opt/triune-panel/data（存在）。
//
// 这样 `sudo triune-panel status` 在任意目录下都能找到 systemd 安装的实例。
func resolveDataDir(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if v := os.Getenv("TRIUNE_DATA"); v != "" {
		return v
	}
	const def = "./data"
	const installed = "/opt/triune-panel/data"
	has := func(p string) bool {
		_, err := os.Stat(filepath.Join(p, "triune.db"))
		return err == nil
	}
	if has(def) {
		return def
	}
	if has(installed) {
		return installed
	}
	return def
}

// openExistingStore 打开已有数据库；不存在时直接报错，避免在错误路径上
// 新建一个空库（reset-password 走错目录时尤其危险）。
func openExistingStore(dir string) *store.Store {
	dbPath := filepath.Join(dir, "triune.db")
	if _, err := os.Stat(dbPath); err != nil {
		die(1, "未找到数据库 %s\n  请用 --data 指定数据目录（systemd 安装通常在 /opt/triune-panel/data）", dbPath)
	}
	st, err := store.Open(dir)
	if err != nil {
		die(1, "打开数据库失败: %v", err)
	}
	return st
}

// dwidth 估算字符串显示宽度（CJK 记 2 格），用于对齐中文标签。
func dwidth(s string) int {
	w := 0
	for _, r := range s {
		if r >= 0x2E80 {
			w += 2
		} else {
			w++
		}
	}
	return w
}

func padLabel(s string, width int) string {
	if d := dwidth(s); d < width {
		return s + strings.Repeat(" ", width-d)
	}
	return s
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 3; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

// ---- reset-password ----

// cmdResetPassword 重置管理密码——忘记密码时唯一的离线入口。
//
// 设计取舍：
//   - 密码只从标准输入或随机生成，不接受命令行参数（会留在 shell 历史 / ps）；
//   - 默认同时关闭 2FA：丢手机和忘密码往往是同一场事故，否则改完密码仍进不去；
//   - 重置后递增会话纪元，所有已登录会话（含攻击者持有的）立即失效。
func cmdResetPassword(args []string) {
	fs := flag.NewFlagSet("reset-password", flag.ExitOnError)
	dataDir := fs.String("data", "", "数据目录（默认自动探测）")
	user := fs.String("user", "", "管理用户名（默认保持原值）")
	fromStdin := fs.Bool("password-stdin", false, "从标准输入读取新密码")
	keep2FA := fs.Bool("keep-2fa", false, "保留登录二次验证（默认关闭）")
	quiet := fs.Bool("quiet", false, "不打印新密码（配合 --password-stdin）")
	_ = fs.Parse(args)

	if fs.NArg() > 0 {
		die(2, "不要把密码作为命令行参数（会留在 shell 历史）。请改用:\n  echo '新密码' | triune-panel reset-password --password-stdin")
	}

	dir := resolveDataDir(*dataDir)
	st := openExistingStore(dir)
	defer st.Close()

	var pass string
	if *fromStdin {
		raw, err := io.ReadAll(io.LimitReader(os.Stdin, 4096))
		if err != nil {
			die(1, "读取标准输入失败: %v", err)
		}
		pass = string(raw)
		if i := strings.IndexAny(pass, "\r\n"); i >= 0 {
			pass = pass[:i] // 只取第一行，兼容 echo 的换行
		}
		if pass == "" {
			die(1, "标准输入为空，未做任何修改")
		}
	} else {
		var err error
		pass, err = authn.RandomPassword(16)
		if err != nil {
			die(1, "生成随机密码失败: %v", err)
		}
	}

	if *user != "" {
		if err := st.SetSetting(authn.SettingUser, *user); err != nil {
			die(1, "写入用户名失败: %v", err)
		}
	}
	if err := authn.SetPassword(st, pass); err != nil {
		die(1, "重置密码失败: %v", err)
	}
	if !*keep2FA {
		if err := authn.Disable2FA(st); err != nil {
			fmt.Fprintf(os.Stderr, "警告: 关闭二次验证失败: %v\n", err)
		}
	}

	fmt.Println("✔ 已重置管理密码")
	fmt.Printf("  %s%s\n", padLabel("数据目录", 10), dir)
	fmt.Printf("  %s%s\n", padLabel("用户名", 10), authn.User(st))
	if !*quiet {
		fmt.Printf("  %s%s\n", padLabel("新密码", 10), pass)
	}
	fmt.Println()
	fmt.Println("  所有已登录会话已失效，请用新密码重新登录。")
	if !*keep2FA {
		fmt.Println("  已同时关闭登录二次验证（TOTP / 邮箱）；如需保留请加 --keep-2fa。")
	}
}

// ---- status ----

type countStat struct {
	Total   int `json:"total"`
	Enabled int `json:"enabled"`
	Draft   int `json:"draft"`
}

// clientStat 用户没有草稿概念，单独一个结构避免 JSON 里出现无意义的 draft。
type clientStat struct {
	Total   int `json:"total"`
	Enabled int `json:"enabled"`
}

type coreInfo struct {
	Name      string `json:"name"`
	Bin       string `json:"bin"`
	BinExists bool   `json:"bin_exists"`
}

type certInfo struct {
	Domain    string `json:"domain"`
	NotAfter  string `json:"not_after"`
	DaysLeft  int    `json:"days_left"`
	ExpiringS bool   `json:"expiring_soon"`
}

type applyInfo struct {
	Time   string `json:"time"`
	Core   string `json:"core"`
	Status string `json:"status"`
	Note   string `json:"note"`
}

type statusReport struct {
	Version         string     `json:"version"`
	DataDir         string     `json:"data_dir"`
	DBSize          int64      `json:"db_size_bytes"`
	Inbounds        countStat  `json:"inbounds"`
	Clients         clientStat `json:"clients"`
	Cores           []coreInfo `json:"cores"`
	Certs           []certInfo `json:"certs"`
	LastApply       *applyInfo `json:"last_apply,omitempty"`
	LastLoginIP     string     `json:"last_login_ip"`
	LastLoginTime   string     `json:"last_login_time"`
	Firewall        string     `json:"firewall"`
	PublicHost      string     `json:"public_host"`
	StatsEnabled    bool       `json:"stats_enabled"`
	FwAuto          bool       `json:"fw_auto"`
	TwoFAMode       string     `json:"auth_2fa_mode"`
	FactoryDefault  bool       `json:"factory_default"`
	LegacyPlaintext bool       `json:"legacy_plaintext_password"`
}

// cmdStatus 只读汇总面板状态（不执行内核命令，秒回）。
func cmdStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	dataDir := fs.String("data", "", "数据目录（默认自动探测）")
	asJSON := fs.Bool("json", false, "以 JSON 输出（便于脚本）")
	_ = fs.Parse(args)

	dir := resolveDataDir(*dataDir)
	st := openExistingStore(dir)
	defer st.Close()

	rep := statusReport{Version: Version, DataDir: dir, Firewall: firewall.DetectBackend()}
	// SQLite 用 WAL：未 checkpoint 时数据主要在 triune.db-wal 里，一并计入
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if fi, err := os.Stat(filepath.Join(dir, "triune.db"+suffix)); err == nil {
			rep.DBSize += fi.Size()
		}
	}

	ins, _ := st.ListInbounds("")
	for _, in := range ins {
		rep.Inbounds.Total++
		if in.Draft {
			rep.Inbounds.Draft++
			continue
		}
		if in.Enabled {
			rep.Inbounds.Enabled++
		}
	}
	clients, _ := st.ListClients()
	for _, c := range clients {
		rep.Clients.Total++
		if c.Enabled {
			rep.Clients.Enabled++
		}
	}

	for _, c := range core.All() {
		bin, _ := st.GetSetting("bin_" + c.Name())
		ci := coreInfo{Name: c.Name(), Bin: bin}
		if bin != "" {
			if _, err := os.Stat(bin); err == nil {
				ci.BinExists = true
			}
		}
		rep.Cores = append(rep.Cores, ci)
	}

	rep.Certs = scanCerts(filepath.Join(dir, "certs"))

	if revs, err := st.ListRevisions("", 1); err == nil && len(revs) > 0 {
		r := revs[0]
		rep.LastApply = &applyInfo{
			Time:   r.CreatedAt.Format("2006-01-02 15:04:05"),
			Core:   r.Core,
			Status: r.Status,
			Note:   r.Note,
		}
	}
	rep.LastLoginIP, _ = st.GetSetting("login_last_ip")
	rep.LastLoginTime, _ = st.GetSetting("login_last_time")
	rep.PublicHost, _ = st.GetSetting("public_host")
	if v, _ := st.GetSetting("stats_enabled"); v == "1" {
		rep.StatsEnabled = true
	}
	if v, _ := st.GetSetting("fw_auto"); v != "0" {
		rep.FwAuto = true
	}
	rep.TwoFAMode, _ = st.GetSetting("auth_2fa_mode")
	rep.FactoryDefault = authn.IsFactoryDefault(st)
	if p, _ := st.GetSetting(authn.SettingPlain); p != "" {
		rep.LegacyPlaintext = true
	}

	if *asJSON {
		b, _ := json.MarshalIndent(rep, "", "  ")
		fmt.Println(string(b))
		return
	}

	fmt.Printf("Triune Panel %s\n\n", rep.Version)
	fmt.Printf("  %s%s   %s\n", padLabel("数据目录", 10), rep.DataDir, humanSize(rep.DBSize))
	fmt.Printf("  %s%d 个（启用 %d / 草稿 %d）\n", padLabel("入站", 10), rep.Inbounds.Total, rep.Inbounds.Enabled, rep.Inbounds.Draft)
	fmt.Printf("  %s%d 个（启用 %d）\n", padLabel("用户", 10), rep.Clients.Total, rep.Clients.Enabled)
	for _, c := range rep.Cores {
		state := "未配置"
		switch {
		case c.Bin == "":
		case c.BinExists:
			state = c.Bin
		default:
			state = c.Bin + "（文件不存在）"
		}
		fmt.Printf("  %s%-8s %s\n", padLabel("内核", 10), c.Name, state)
	}
	if len(rep.Certs) == 0 {
		fmt.Printf("  %s%s\n", padLabel("证书", 10), "无")
	}
	for _, c := range rep.Certs {
		note := fmt.Sprintf("%s 到期 %s（剩 %d 天）", c.Domain, c.NotAfter, c.DaysLeft)
		if c.ExpiringS {
			note += "  ⚠ 即将到期"
		}
		fmt.Printf("  %s%s\n", padLabel("证书", 10), note)
	}
	if rep.LastApply != nil {
		fmt.Printf("  %s%s · %s · %s\n", padLabel("上次应用", 10), rep.LastApply.Time, rep.LastApply.Core, rep.LastApply.Status)
	}
	if rep.LastLoginIP != "" {
		fmt.Printf("  %s%s · %s\n", padLabel("最近登录", 10), rep.LastLoginIP, rep.LastLoginTime)
	}
	fmt.Printf("  %s%s\n", padLabel("防火墙", 10), rep.Firewall)
	fmt.Printf("  %s%s\n", padLabel("公开地址", 10), orDash(rep.PublicHost))
	fmt.Printf("  %s%s\n", padLabel("二次验证", 10), orDash(rep.TwoFAMode))

	if rep.FactoryDefault {
		fmt.Println("\n  ⚠ 管理密码仍是出厂默认（admin/admin），请尽快修改。")
	}
	if rep.LegacyPlaintext {
		fmt.Println("  ⚠ 检测到遗留明文密码，下次登录会自动升级为哈希。")
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// scanCerts 读取 data/certs/<domain>/fullchain.pem 的到期时间。
func scanCerts(dir string) []certInfo {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []certInfo
	now := time.Now()
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name(), "fullchain.pem"))
		if err != nil {
			continue
		}
		blk, _ := pem.Decode(raw)
		if blk == nil {
			continue
		}
		crt, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			continue
		}
		days := int(crt.NotAfter.Sub(now).Hours() / 24)
		out = append(out, certInfo{
			Domain:    e.Name(),
			NotAfter:  crt.NotAfter.Format("2006-01-02"),
			DaysLeft:  days,
			ExpiringS: days < 14,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Domain < out[j].Domain })
	return out
}

// ---- doctor ----

type checkItem struct {
	Level  string // ok | warn | fail
	Title  string
	Detail string
}

// cmdDoctor 环境自检：把「装不上 / 连不通 / 打不开」的常见原因一次列清。
func cmdDoctor(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	dataDir := fs.String("data", "", "数据目录（默认自动探测）")
	asJSON := fs.Bool("json", false, "以 JSON 输出")
	_ = fs.Parse(args)

	dir := resolveDataDir(*dataDir)
	var items []checkItem
	add := func(level, title, detail string) {
		items = append(items, checkItem{level, title, detail})
	}

	// 1. 数据目录
	if fi, err := os.Stat(dir); err != nil {
		add("fail", "数据目录", fmt.Sprintf("%s 不可访问: %v", dir, err))
	} else if !fi.IsDir() {
		add("fail", "数据目录", fmt.Sprintf("%s 不是目录", dir))
	} else if !writable(dir) {
		add("fail", "数据目录", fmt.Sprintf("%s 不可写（检查属主/权限，面板需要写配置与备份）", dir))
	} else {
		add("ok", "数据目录", dir)
	}

	// 2. 数据库
	var st *store.Store
	if _, err := os.Stat(filepath.Join(dir, "triune.db")); err != nil {
		add("fail", "数据库", "未找到 triune.db（全新安装可忽略；已装过则检查 --data）")
	} else if s, err := store.Open(dir); err != nil {
		add("fail", "数据库", err.Error())
	} else {
		st = s
		defer st.Close()
		if err := st.IntegrityCheck(); err != nil {
			add("fail", "数据库", err.Error())
		} else {
			add("ok", "数据库", "quick_check 通过")
		}
	}

	if st != nil {
		// 3. 密码状态
		switch {
		case authn.IsFactoryDefault(st):
			add("warn", "管理密码", "仍是出厂默认 admin/admin，登录后请立即修改")
		default:
			if p, _ := st.GetSetting(authn.SettingPlain); p != "" {
				add("warn", "管理密码", "存在遗留明文密码，下次登录会自动升级为 bcrypt 哈希")
			} else {
				add("ok", "管理密码", "已使用 bcrypt 哈希存储")
			}
		}
		// 4. 二次验证
		mode, _ := st.GetSetting("auth_2fa_mode")
		if mode == "" {
			add("ok", "二次验证", "未开启")
		} else {
			add("ok", "二次验证", "已开启（"+mode+"）")
		}
		// 5. 公开地址
		if h, _ := st.GetSetting("public_host"); h == "" {
			add("warn", "公开地址", "public_host 未设置，订阅/导出会回退为请求 Host（内网访问时节点地址不可用）")
		} else {
			add("ok", "公开地址", h)
		}
	}

	// 6. 内核二进制
	for _, c := range core.All() {
		name := c.Name()
		var bin string
		if st != nil {
			bin, _ = st.GetSetting("bin_" + name)
		}
		if bin == "" {
			add("warn", "内核 "+name, "未配置二进制路径（设置页可一键安装）")
			continue
		}
		if _, err := os.Stat(bin); err != nil {
			add("fail", "内核 "+name, fmt.Sprintf("路径不存在: %s", bin))
			continue
		}
		if v, err := execVersion(bin); err != nil {
			add("warn", "内核 "+name, fmt.Sprintf("%s 无法执行: %v", bin, err))
		} else {
			add("ok", "内核 "+name, v)
		}
	}

	// 7. 防火墙
	if b := firewall.DetectBackend(); b == firewall.BackendNone {
		add("ok", "防火墙", "未检测到生效的本机防火墙（云安全组仍需在控制台放行）")
	} else {
		add("ok", "防火墙", "后端 "+b)
	}

	// 8. 入站端口监听情况
	if st != nil {
		ins, _ := st.ListInbounds("")
		for _, in := range ins {
			if in.Draft || !in.Enabled || in.Port <= 0 {
				continue
			}
			proto := firewall.ProtoOf(in.Protocol, in.Transport)
			listening := portListening(in.Port, proto)
			title := fmt.Sprintf("端口 %d/%s", in.Port, proto)
			switch {
			case listening:
				add("ok", title, in.Name+" 正在监听")
			default:
				add("warn", title, in.Name+" 未监听（未 apply 或内核未运行）")
			}
		}
	}

	// 9. 代理环境变量（会污染公网 IP 探测与组件下载）
	var proxyEnv []string
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy"} {
		if v := os.Getenv(k); v != "" {
			proxyEnv = append(proxyEnv, k+"="+v)
		}
	}
	if len(proxyEnv) > 0 {
		add("warn", "代理变量", strings.Join(proxyEnv, " ")+"（可能影响 IP 探测与更新下载）")
	} else {
		add("ok", "代理变量", "未设置")
	}

	// 10. 磁盘空间
	if free, err := diskFree(dir); err == nil {
		if free < 1<<30 {
			add("warn", "磁盘空间", fmt.Sprintf("剩余 %s，建议清理（配置备份与内核二进制需要空间）", humanSize(free)))
		} else {
			add("ok", "磁盘空间", "剩余 "+humanSize(free))
		}
	}

	if *asJSON {
		b, _ := json.MarshalIndent(items, "", "  ")
		fmt.Println(string(b))
	} else {
		fmt.Printf("Triune Panel %s · 自检 %s\n\n", Version, dir)
		for _, it := range items {
			mark := "  ok  "
			switch it.Level {
			case "warn":
				mark = " warn "
			case "fail":
				mark = " fail "
			}
			fmt.Printf("[%s] %s  %s\n", mark, padLabel(it.Title, 14), it.Detail)
		}
		var warns, fails int
		for _, it := range items {
			switch it.Level {
			case "warn":
				warns++
			case "fail":
				fails++
			}
		}
		fmt.Printf("\n%d 项通过，%d 项警告，%d 项失败\n", len(items)-warns-fails, warns, fails)
	}

	for _, it := range items {
		if it.Level == "fail" {
			os.Exit(1)
		}
	}
}

func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".wtest-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	_ = os.Remove(name)
	return true
}

func execVersion(bin string) (string, error) {
	cmd := exec.Command(bin, "-version")
	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = cmd.CombinedOutput()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return "", fmt.Errorf("执行超时（5s）")
	}
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if line == "" {
		line = "可执行"
	}
	return line, err
}

// portListening 判断端口是否已在监听：能成功绑定说明没人监听（随即释放）。
func portListening(port int, proto string) bool {
	addr := fmt.Sprintf(":%d", port)
	if proto == "udp" {
		pc, err := net.ListenPacket("udp", addr)
		if err != nil {
			return true
		}
		pc.Close()
		return false
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return true
	}
	ln.Close()
	return false
}

func diskFree(dir string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}

// ---- backup ----

func cmdBackup(args []string) {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	dataDir := fs.String("data", "", "数据目录（默认自动探测）")
	outDir := fs.String("out", "", "输出目录（默认 <数据目录>/backups）")
	keep := fs.Int("keep", backup.DefaultKeep, "保留份数")
	_ = fs.Parse(args)

	dir := resolveDataDir(*dataDir)
	res, err := backup.Create(backup.Options{DataDir: dir, OutDir: *outDir, Keep: *keep})
	if err != nil {
		die(1, "备份失败: %v", err)
	}
	fmt.Println("✔ 备份完成")
	fmt.Printf("  %s%s\n", padLabel("归档", 10), res.Path)
	fmt.Printf("  %s%s\n", padLabel("大小", 10), humanSize(res.Size))
	fmt.Printf("  %s%d 份\n", padLabel("保留", 10), *keep)
	if len(res.Removed) > 0 {
		fmt.Printf("  %s%d 份旧归档已轮转删除\n", padLabel("清理", 10), len(res.Removed))
	}
}
