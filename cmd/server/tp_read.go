// tp 的只读查询命令：in / user / audit。
//
// 全部直连 SQLite，**不依赖面板进程**。服务起不来的时候正是最需要查数据的时候，
// 如果这些命令还要连 daemon，那 CLI 就白做了。
//
// 运行态信息（内核是否在跑、实时速率）这里拿不到——它只存在于 daemon 的内存里
//（proc.Manager 没有 pid 文件）。这类字段一律不显示，而不是显示一个可能过期的值。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"triune/internal/model"
	"triune/internal/store"
)

// ---- 入站 ----

func cmdIn(args []string) {
	if len(args) == 0 {
		die(2, "用法: tp in ls | tp in show <id> | tp in on|off <id> | tp in rm <id>")
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "ls", "list":
		cmdInList(rest)
	case "show":
		cmdInShow(rest)
	case "on", "enable":
		cmdInOn(rest)
	case "off", "disable":
		cmdInOff(rest)
	case "rm", "del", "delete":
		cmdInRm(rest)
	default:
		die(2, "未知动作 %q（可用: ls / show / on / off / rm）", verb)
	}
}

type inboundRow struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Port    int    `json:"port"`
	Proto   string `json:"proto"`
	Core    string `json:"core"`
	State   string `json:"state"`
	Clients int    `json:"clients"`
}

// inboundState 把 enabled/draft 两个布尔压成一个中文状态。
// 草稿优先：草稿即使 enabled 也不参与渲染，说"启用"会误导。
func inboundState(in *model.Inbound) string {
	switch {
	case in.Draft:
		return "草稿"
	case in.Enabled:
		return "启用"
	}
	return "停用"
}

// protoLabel 协议 + 传输 + TLS 合成一列，比三列省地方，也更容易一眼看全。
func protoLabel(in *model.Inbound) string {
	s := in.Protocol
	if in.Transport != "" && in.Transport != "tcp" {
		s += "+" + in.Transport
	}
	switch in.TLS {
	case "tls":
		s += "+tls"
	case "reality":
		s += "+reality"
	}
	return s
}

func cmdInList(args []string) {
	fs := flag.NewFlagSet("in ls", flag.ExitOnError)
	dataDir := fs.String("data", "", "数据目录（默认自动探测）")
	asJSON := fs.Bool("json", false, "以 JSON 输出")
	_ = tpParse(fs, args)

	st := openExistingStore(resolveDataDir(*dataDir))
	defer st.Close()

	ins, err := st.ListInbounds("")
	if err != nil {
		die(1, "读取入站失败: %v", err)
	}
	rows := make([]inboundRow, 0, len(ins))
	for _, in := range ins {
		ids, _ := st.InboundClientIDs(in.ID)
		rows = append(rows, inboundRow{
			ID: in.ID, Name: in.Name, Port: in.Port, Proto: protoLabel(in),
			Core: in.Core, State: inboundState(in), Clients: len(ids),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })

	if *asJSON {
		tpJSON(rows)
		return
	}
	fmt.Printf("Triune Panel · 入站（%d 个）\n\n", len(rows))
	table := make([][]string, 0, len(rows))
	for _, r := range rows {
		table = append(table, []string{
			fmt.Sprint(r.ID), fmt.Sprint(r.Port), r.Proto, r.Core,
			r.State, fmt.Sprint(r.Clients), r.Name,
		})
	}
	tpTable([]string{"ID", "端口", "协议", "内核", "状态", "用户", "名称"}, table)
}

func cmdInShow(args []string) {
	fs := flag.NewFlagSet("in show", flag.ExitOnError)
	dataDir := fs.String("data", "", "数据目录（默认自动探测）")
	asJSON := fs.Bool("json", false, "以 JSON 输出")
	_ = tpParse(fs, args)
	if fs.NArg() < 1 {
		die(2, "用法: tp in show <id>")
	}
	var id int64
	if _, err := fmt.Sscan(fs.Arg(0), &id); err != nil || id <= 0 {
		die(2, "入站 id 必须是正整数，收到 %q", fs.Arg(0))
	}

	st := openExistingStore(resolveDataDir(*dataDir))
	defer st.Close()

	in, err := st.GetInbound(id)
	if err != nil {
		die(1, "入站 %d 不存在: %v", id, err)
	}
	clientIDs, _ := st.InboundClientIDs(id)
	byID := map[int64]*model.Client{}
	if cs, err := st.ListClients(); err == nil {
		for _, c := range cs {
			byID[c.ID] = c
		}
	}
	names := make([]string, 0, len(clientIDs))
	for _, cid := range clientIDs {
		if c, ok := byID[cid]; ok {
			names = append(names, c.Name)
		}
	}
	sort.Strings(names)

	if *asJSON {
		tpJSON(map[string]any{"inbound": in, "clients": names})
		return
	}

	title := in.Name
	if title == "" {
		title = fmt.Sprintf("入站 %d", in.ID)
	}
	fmt.Printf("%s\n\n", title)
	tpField("ID", "%d", in.ID)
	tpField("端口", "%d", in.Port)
	tpField("监听", "%s", orDash(in.Listen))
	tpField("协议", "%s", protoLabel(in))
	tpField("内核", "%s", in.Core)
	tpField("状态", "%s", inboundState(in))
	if in.Tag != "" {
		tpField("标签", "%s", in.Tag)
	}
	if len(names) == 0 {
		tpField("用户", "无")
	} else {
		tpField("用户", "%d 个：%s", len(names), strings.Join(names, ", "))
	}
	tpField("创建", "%s", in.CreatedAt.Format("2006-01-02 15:04:05"))
	tpField("更新", "%s", in.UpdatedAt.Format("2006-01-02 15:04:05"))
	if s := prettyJSON(in.Settings); s != "" {
		fmt.Printf("\n  协议配置（原样透传给内核）:\n%s\n", s)
	}
	if in.Draft {
		fmt.Println("\n  ⚠ 这是导入草稿，不参与配置渲染——在面板里取消草稿并应用后才生效。")
	}
}

// prettyJSON 把单行 JSON 排成缩进形式；空对象/非法内容返回空串（不打印噪音）。
func prettyJSON(s string) string {
	if s == "" || s == "{}" {
		return ""
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return "    " + s // 排不了就原样给出来，别把数据吞了
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "    " + s
	}
	return "    " + strings.ReplaceAll(string(b), "\n", "\n    ")
}

// ---- 用户 ----

func cmdUser(args []string) {
	if len(args) == 0 {
		die(2, "用法: tp user ls | show <name> | link <name> | add <name> | set <name> | rm <name> | token <name>")
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "ls", "list":
		cmdUserList(rest)
	case "show":
		cmdUserShow(rest)
	case "link", "url":
		cmdUserLink(rest)
	case "add", "new":
		cmdUserAdd(rest)
	case "set", "edit":
		cmdUserSet(rest)
	case "rm", "del", "delete":
		cmdUserRm(rest)
	case "token", "reset-token":
		cmdUserToken(rest)
	default:
		die(2, "未知动作 %q（可用: ls / show / link / add / set / rm / token）", verb)
	}
}

type userRow struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	State     string   `json:"state"`
	QuotaGB   float64  `json:"quota_gb"`
	UsedBytes int64    `json:"used_bytes"`
	ExpiresAt string   `json:"expires_at,omitempty"`
	SubToken  string   `json:"sub_token"`
	Inbounds  []string `json:"inbounds"`
	Note      string   `json:"note,omitempty"`
}

// userState 判定用户当前处于什么状态。
//
// 顺序有意为之：停用优先于超限——面板把超配额的用户自动停用后 DB 里是
// enabled=false，这时说"超限"会让人以为还能救；显示"停用"再补一句原因更准确。
// 反过来，状态本身已经是"超限/过期"时不再重复备注，免得一列里说两遍。
func userState(c *model.Client, used int64, now time.Time) (string, string) {
	exceeded := c.QuotaGB > 0 && float64(used) >= c.QuotaGB*float64(1<<30)
	expired := c.ExpiresAt != nil && c.ExpiresAt.Before(now)
	switch {
	case !c.Enabled:
		// 停用可能是手动的，也可能是超配额/到期被自动停的，说明原因才有价值
		var notes []string
		if exceeded {
			notes = append(notes, "已超配额")
		}
		if expired {
			notes = append(notes, "已过期")
		}
		return "停用", strings.Join(notes, "，")
	case exceeded:
		return "超限", ""
	case expired:
		return "过期", ""
	}
	return "正常", ""
}

func quotaLabel(q float64) string {
	if q <= 0 {
		return "不限"
	}
	return fmt.Sprintf("%.0f GB", q)
}

func expiryLabel(c *model.Client, now time.Time) string {
	if c.ExpiresAt == nil {
		return "—"
	}
	d := c.ExpiresAt.Format("2006-01-02")
	if c.ExpiresAt.Before(now) {
		return d + "（已过期）"
	}
	return d
}

// collectUsers 把用户、流量、入站名一次装配好，ls 与 show 共用。
func collectUsers(st *store.Store) []userRow {
	clients, err := st.ListClients()
	if err != nil {
		die(1, "读取用户失败: %v", err)
	}
	traffic, _ := st.GetTrafficAll()
	inbounds, _ := st.ListInbounds("")
	inName := map[int64]string{}
	for _, in := range inbounds {
		inName[in.ID] = in.Name
	}

	now := time.Now()
	rows := make([]userRow, 0, len(clients))
	for _, c := range clients {
		var used int64
		if t, ok := traffic[c.ID]; ok && t != nil {
			used = t.UpBytes + t.DownBytes
		}
		state, note := userState(c, used, now)
		ids, _ := st.ClientInboundIDs(c.ID)
		names := make([]string, 0, len(ids))
		for _, id := range ids {
			if n := inName[id]; n != "" {
				names = append(names, n)
			} else {
				names = append(names, fmt.Sprintf("#%d", id))
			}
		}
		row := userRow{
			ID: c.ID, Name: c.Name, State: state, QuotaGB: c.QuotaGB,
			UsedBytes: used, SubToken: c.SubToken, Inbounds: names, Note: note,
		}
		if c.ExpiresAt != nil {
			row.ExpiresAt = c.ExpiresAt.Format(time.RFC3339)
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows
}

func cmdUserList(args []string) {
	fs := flag.NewFlagSet("user ls", flag.ExitOnError)
	dataDir := fs.String("data", "", "数据目录（默认自动探测）")
	asJSON := fs.Bool("json", false, "以 JSON 输出")
	_ = tpParse(fs, args)

	st := openExistingStore(resolveDataDir(*dataDir))
	defer st.Close()

	rows := collectUsers(st)
	if *asJSON {
		tpJSON(rows)
		return
	}
	now := time.Now()
	fmt.Printf("Triune Panel · 用户（%d 个）\n\n", len(rows))
	table := make([][]string, 0, len(rows))
	for _, r := range rows {
		// 一律写成「已用 / 配额」，没有配额就是「/ 不限」——
		// 同一列里有的带斜杠有的不带，扫一眼会以为漏了数据。
		used := humanSize(r.UsedBytes) + " / " + quotaLabel(r.QuotaGB)
		exp := "—"
		if r.ExpiresAt != "" {
			if t, err := time.Parse(time.RFC3339, r.ExpiresAt); err == nil {
				exp = t.Format("2006-01-02")
				if t.Before(now) {
					exp += " 已过期"
				}
			}
		}
		table = append(table, []string{
			r.Name, r.State, used, exp, shortToken(r.SubToken), r.Note,
		})
	}
	tpTable([]string{"名称", "状态", "已用 / 配额", "到期", "令牌", "备注"}, table)
	if len(rows) > 0 {
		fmt.Println("\n  完整令牌与凭据：tp user show <名称>")
	}
}

// shortToken 只显示令牌前 8 位，方便肉眼比对；完整令牌用 tp user show。
func shortToken(t string) string {
	if t == "" {
		return "—"
	}
	if len(t) <= 8 {
		return t
	}
	return t[:8] + "…"
}

// findUser 按名称（或 id）取用户，取不到就退出。
//
// 查不到时直接退出是给只读命令用的——`tp user show 打错名字` 应该报错，
// 而不是打印一片空白。需要「不存在就返回 nil」的场合（比如 add 前的重名
// 检查）用 lookupUser。
func findUser(st *store.Store, name string) *model.Client {
	c := lookupUser(st, name)
	if c == nil {
		die(1, "用户 %q 不存在（用 tp user ls 查看全部）", name)
	}
	return c
}

// lookupUser 按名称找用户，找不到时退化成按 id 找，都没有就返回 nil。
//
// 名称优先：人手上敲的是名字。退化成 id 是为了省掉「先 ls 拿 id」那一步。
func lookupUser(st *store.Store, name string) *model.Client {
	clients, err := st.ListClients()
	if err != nil {
		die(1, "读取用户失败: %v", err)
	}
	for _, c := range clients {
		if c.Name == name {
			return c
		}
	}
	var id int64
	if _, err := fmt.Sscan(name, &id); err == nil && id > 0 {
		for _, c := range clients {
			if c.ID == id {
				return c
			}
		}
	}
	return nil
}

func cmdUserShow(args []string) {
	fs := flag.NewFlagSet("user show", flag.ExitOnError)
	dataDir := fs.String("data", "", "数据目录（默认自动探测）")
	asJSON := fs.Bool("json", false, "以 JSON 输出")
	_ = tpParse(fs, args)
	if fs.NArg() < 1 {
		die(2, "用法: tp user show <name>")
	}

	st := openExistingStore(resolveDataDir(*dataDir))
	defer st.Close()

	c := findUser(st, fs.Arg(0))
	var row userRow
	for _, r := range collectUsers(st) {
		if r.ID == c.ID {
			row = r
			break
		}
	}
	base, warn := subBaseURL(st)

	if *asJSON {
		tpJSON(map[string]any{
			"client": c, "state": row.State, "used_bytes": row.UsedBytes,
			"inbounds": row.Inbounds, "sub_base": base, "warn": warn,
		})
		return
	}

	fmt.Printf("%s\n\n", c.Name)
	tpField("状态", "%s%s", row.State, noteSuffix(row.Note))
	tpField("配额", "%s", quotaLabel(c.QuotaGB))
	if c.QuotaGB > 0 {
		pct := float64(row.UsedBytes) / (c.QuotaGB * float64(1<<30)) * 100
		tpField("已用", "%s（%.1f%%）", humanSize(row.UsedBytes), pct)
	} else {
		tpField("已用", "%s", humanSize(row.UsedBytes))
	}
	tpField("到期", "%s", expiryLabel(c, time.Now()))
	if len(row.Inbounds) == 0 {
		tpField("入站", "无（该用户订阅里没有任何节点）")
	} else {
		tpField("入站", "%s", strings.Join(row.Inbounds, ", "))
	}
	// 凭据与令牌是机密，但本命令需要读数据库（等同 root 权限），
	// 面板里 owner 也看得到，所以照常显示，方便运维直接复制。
	tpField("凭据", "%s", c.Credential)
	tpField("令牌", "%s", c.SubToken)
	if base == "" {
		tpField("订阅", "—")
	} else {
		tpField("订阅", "%s/sub/%s", base, c.SubToken)
	}
	tpField("创建", "%s", c.CreatedAt.Format("2006-01-02 15:04:05"))
	if warn != "" {
		fmt.Printf("\n  ⚠ %s\n", warn)
	}
}

func noteSuffix(note string) string {
	if note == "" {
		return ""
	}
	return "（" + note + "）"
}

func cmdUserLink(args []string) {
	fs := flag.NewFlagSet("user link", flag.ExitOnError)
	dataDir := fs.String("data", "", "数据目录（默认自动探测）")
	asJSON := fs.Bool("json", false, "以 JSON 输出")
	qr := fs.Bool("qr", false, "同时用终端二维码显示通用订阅地址（需系统装有 qrencode）")
	_ = tpParse(fs, args)
	if fs.NArg() < 1 {
		die(2, "用法: tp user link <name> [--qr]")
	}

	st := openExistingStore(resolveDataDir(*dataDir))
	defer st.Close()

	c := findUser(st, fs.Arg(0))
	base, warn := subBaseURL(st)
	if base == "" {
		die(1, "%s\n  订阅地址 = <公开地址>/sub/<令牌>，没有公开地址就拼不出来。\n  到面板「设置 → 常用」里填 public_host（域名或公网 IP）。", warn)
	}
	base += "/sub/" + c.SubToken
	urls := []struct{ label, url string }{
		{"通用（base64）", base},
		{"Clash / mihomo", base + "?target=clash"},
		{"sing-box", base + "?target=singbox"},
	}
	if *asJSON {
		m := map[string]string{}
		for _, u := range urls {
			m[u.label] = u.url
		}
		tpJSON(m)
		return
	}
	if !c.Enabled {
		fmt.Printf("⚠ 用户 %s 已停用，订阅端点会返回 403——链接本身仍然正确。\n\n", c.Name)
	}
	fmt.Printf("%s 的订阅\n\n", c.Name)
	// 标签长度差得比较多（「通用（base64）」占 14 格，「sing-box」只有 8 格），
	// 固定 10 格会让第一个标签把值挤到脸上，所以按最宽的算。
	width := 10
	for _, u := range urls {
		if w := dwidth(u.label) + 2; w > width {
			width = w
		}
	}
	for _, u := range urls {
		tpFieldW(u.label, width, "%s", u.url)
	}
	fmt.Println("\n  客户端粘第一条即可，会自动识别；订阅里包含该用户所有启用入站。")
	if *qr {
		fmt.Println()
		printQR(base)
	}
}

// printQR 调系统 qrencode 画终端二维码。
//
// 不为一个命令引第三方 QR 库：qrencode 到处都能装，没有就明确说清楚，
// 比在二进制里塞一个几百行的编码器划算。
func printQR(url string) {
	path, err := exec.LookPath("qrencode")
	if err != nil {
		fmt.Println("  未安装 qrencode，跳过二维码。装一个即可：apt install qrencode")
		return
	}
	out, err := exec.Command(path, "-t", "ANSIUTF8", url).Output()
	if err != nil {
		fmt.Printf("  qrencode 执行失败: %v\n", err)
		return
	}
	fmt.Print(string(out))
}

// ---- 审计 ----

func cmdAudit(args []string) {
	fs := flag.NewFlagSet("audit", flag.ExitOnError)
	dataDir := fs.String("data", "", "数据目录（默认自动探测）")
	asJSON := fs.Bool("json", false, "以 JSON 输出")
	limit := fs.Int("limit", 30, "最多显示多少条（上限 500）")
	since := fs.String("since", "", "只看这段时间之内，如 30m / 24h / 7d")
	action := fs.String("action", "", "按动作前缀过滤，如 login / inbound")
	result := fs.String("result", "", "只看 ok 或 fail")
	_ = tpParse(fs, args)

	f := store.AuditFilter{Limit: *limit, Prefix: *action, Result: *result}
	if *since != "" {
		d, err := parseSpan(*since)
		if err != nil {
			die(2, "--since 格式不对: %v（示例: 30m / 24h / 7d）", err)
		}
		f.Since = time.Now().Add(-d).Format(store.AuditTimeLayout)
	}

	st := openExistingStore(resolveDataDir(*dataDir))
	defer st.Close()

	entries, err := st.ListAudit(f)
	if err != nil {
		die(1, "读取审计失败: %v", err)
	}
	if *asJSON {
		tpJSON(entries)
		return
	}
	fmt.Printf("Triune Panel · 审计（%d 条）\n\n", len(entries))
	table := make([][]string, 0, len(entries))
	for _, e := range entries {
		table = append(table, []string{e.Time, e.Actor, e.Action, e.Target, e.Result, e.IP})
	}
	tpTable([]string{"时间", "操作者", "动作", "对象", "结果", "来源 IP"}, table)
}

// ---- 公共小工具 ----

// parseSpan 解析时间跨度。Go 的 time.ParseDuration 不认「天」，
// 而审计查询里 7d / 30d 恰恰是最常用的两个跨度，这里补上。
func parseSpan(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		var days float64
		if _, err := fmt.Sscan(strings.TrimSuffix(s, "d"), &days); err != nil || days <= 0 {
			return 0, fmt.Errorf("无法解析 %q", s)
		}
		return time.Duration(days * 24 * float64(time.Hour)), nil
	}
	return time.ParseDuration(s)
}

// subBaseURL 拼订阅的基地址：<scheme>://<public_host>。
//
// 面板可能挂在纯 HTTP 上，也可能是 ACME 签了证书的 HTTPS，所以 scheme 跟着
// acme_enabled 走。没有 public_host 时返回空串 + 提示——用「当前请求的 Host」
// 那种回退在 CLI 里不成立（没有请求），硬编一个 127.0.0.1 只会生成废链接。
func subBaseURL(st *store.Store) (base, warn string) {
	host, _ := st.GetSetting("public_host")
	host = strings.TrimSpace(host)
	if host == "" {
		return "", "未设置 public_host，订阅地址拼不出来（到面板「设置 → 常用」里填域名或公网 IP）"
	}
	scheme := "http"
	if v, _ := st.GetSetting("acme_enabled"); v == "1" || strings.EqualFold(v, "true") {
		scheme = "https"
	}
	// public_host 允许用户填成 "https://example.com" 这种整串，统一剥掉协议头
	if i := strings.Index(host, "://"); i >= 0 {
		scheme = host[:i]
		host = host[i+3:]
	}
	host = strings.TrimSuffix(host, "/")
	return scheme + "://" + host, ""
}
