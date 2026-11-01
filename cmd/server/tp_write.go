// tp 的写操作：全部经 unix 套接字交给 daemon 执行。
//
// 为什么 CLI 不自己改库再自己拉起内核：内核进程状态只存在 daemon 的
// proc.Manager 内存里，没有 pid 文件。两个进程各自拉内核会抢端口，而且
// daemon 的内存状态会和磁盘对不上——表现为「面板显示运行中，实际早死了」。
// 所以写操作只有一个执行者，CLI 只负责把意图传过去。
//
// 通道细节见 internal/api/control.go（对端 uid 校验 + 路由白名单）。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"triune/internal/api"
	"triune/internal/model"
	"triune/internal/store"
)

// ---- 通道 ----

// controlCandidates 控制套接字的所有候选位置。
//
// 顺序直接取自 api 包——daemon 和 CLI 各写一份路径规则，迟早会走岔。
func controlCandidates(dataDir string) []string {
	return api.ControlSocketCandidates(dataDir)
}

// controlSockWith 定位控制套接字：显式 --sock > 环境变量 > 自动探测。
func controlSockWith(flagVal, dataDir string) (string, error) {
	if p := strings.TrimSpace(flagVal); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("指定的套接字不存在: %s", p)
		}
		return p, nil
	}
	cands := controlCandidates(dataDir)
	for _, p := range cands {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("找不到控制套接字（找过：%s）\n"+
		"  写操作需要面板进程在场——它才是真正改动内核的那个进程。\n"+
		"  面板没在跑？先 sudo tp start；手动前台跑的用 --sock 指定路径。",
		strings.Join(cands, "、"))
}

// controlCall 发一个请求，返回状态码和响应体。
func controlCall(sock, method, path string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "http://unix"+path, rd)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{
		// apply 要渲染 + 调内核校验 + 重启进程，慢一点是正常的；
		// 但也不能无限等——卡住时要能报错退出，而不是挂在终端上。
		Timeout: 120 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			},
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, data, nil
}

// controlMust 要求这次调用成功，失败时把服务端的 error 翻成人话再退出。
func controlMust(sock, method, path string, body any) map[string]any {
	code, data, err := controlCall(sock, method, path, body)
	if err != nil {
		die(1, "连不上面板（%s）：%v\n  面板还在跑吗？sudo tp status 看看。", sock, err)
	}
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	msg, _ := out["error"].(string)
	if msg == "" {
		msg = strings.TrimSpace(string(data))
	}
	switch code {
	case http.StatusOK:
		return out
	case http.StatusForbidden:
		die(1, "面板拒绝了这次操作：%s\n  控制套接字只接受 root 或面板运行账号，试试 sudo。", msg)
	case http.StatusNotFound:
		die(1, "%s", msg)
	case http.StatusUnprocessableEntity:
		// 422 = 业务上的失败（渲染/校验不过）。运行中的配置没被改动，
		// 这句一定要说出来，否则用户会以为节点已经坏了。
		if hint, _ := out["hint"].(string); hint != "" {
			die(1, "%s\n  %s", msg, hint)
		}
		die(1, "%s", msg)
	}
	if msg == "" {
		msg = fmt.Sprintf("HTTP %d", code)
	}
	die(1, "%s", msg)
	return nil
}

// ---- 应用与回滚 ----

// cmdApply 把配置重新渲染并生效。
//
// 不带参数时只应用「有启用且非草稿入站」的内核——与启动时的内核恢复同一
// 口径。把三个内核都 apply 一遍会给没有入站的内核也起一个空进程，白占内存。
func cmdApply(args []string) {
	fs := flag.NewFlagSet("apply", flag.ExitOnError)
	dataDir := fs.String("data", "", "数据目录（默认自动探测）")
	sockFlag := fs.String("sock", "", "控制套接字路径（默认自动探测）")
	note := fs.String("note", "", "写进修订记录的备注")
	_ = tpParse(fs, args)

	dir := resolveDataDir(*dataDir)
	st := openExistingStore(dir)
	defer st.Close()

	var targets []string
	if fs.NArg() > 0 {
		c, err := coreByName(fs.Arg(0))
		if err != nil {
			die(2, "%v", err)
		}
		targets = []string{c}
	} else {
		targets = coresWithEnabledInbounds(st)
	}
	if len(targets) == 0 {
		fmt.Println("  没有需要应用的内核（三个内核都没有启用中的入站）。")
		return
	}

	sock, err := controlSockWith(*sockFlag, dir)
	if err != nil {
		die(1, "%v", err)
	}
	n := *note
	if n == "" {
		n = "tp apply"
	}
	for _, c := range targets {
		applyOne(sock, c, n)
	}
}

// applyOne 应用单个内核并把结果打出来。
func applyOne(sock, coreName, note string) {
	res := controlMust(sock, "POST", "/api/apply", map[string]any{"core": coreName, "note": note})
	rev := int64Of(res["revision_id"])
	restarted, _ := res["restarted"].(bool)
	validated, _ := res["validated"].(bool)

	var flags []string
	if validated {
		flags = append(flags, "已校验")
	} else {
		flags = append(flags, "未校验（没装内核二进制）")
	}
	if restarted {
		flags = append(flags, "已重启")
	} else {
		flags = append(flags, "未重启（进程没起）")
	}
	fmt.Printf("  ✔ %-8s 已应用 · 修订 #%d · %s\n", coreName, rev, strings.Join(flags, " · "))
}

func cmdRollback(args []string) {
	fs := flag.NewFlagSet("rollback", flag.ExitOnError)
	dataDir := fs.String("data", "", "数据目录（默认自动探测）")
	sockFlag := fs.String("sock", "", "控制套接字路径（默认自动探测）")
	_ = tpParse(fs, args)
	if fs.NArg() < 1 {
		die(2, "用法: tp rollback <内核名>（singbox / xray / mihomo）")
	}
	name, err := coreByName(fs.Arg(0))
	if err != nil {
		die(2, "%v", err)
	}
	sock, err := controlSockWith(*sockFlag, resolveDataDir(*dataDir))
	if err != nil {
		die(1, "%v", err)
	}
	res := controlMust(sock, "POST", "/api/rollback", map[string]any{"core": name})
	from, _ := res["restored_from"].(string)
	fmt.Printf("  ✔ %s 已回滚\n    恢复自 %s\n", name, from)
	fmt.Println("    注意：回滚的是配置文件，数据库里的入站/用户没有变。")
}

// coresWithEnabledInbounds 找出「该在跑」的内核，与 RestoreCores 同口径。
func coresWithEnabledInbounds(st *store.Store) []string {
	var out []string
	for _, name := range coreNames() {
		ins, err := st.ListInbounds(name)
		if err != nil {
			continue
		}
		for _, in := range ins {
			if in.Enabled && !in.Draft {
				out = append(out, name)
				break
			}
		}
	}
	return out
}

// ---- 入站 ----

// cmdInOn / cmdInOff 启停入站。默认顺手应用配置——只翻一个数据库开关而
// 不应用，用户会以为节点已经停了/开了，实际端口还在服务。这是危险的方向。
func cmdInOn(args []string)  { inSetEnabled(args, true) }
func cmdInOff(args []string) { inSetEnabled(args, false) }

func inSetEnabled(args []string, enable bool) {
	fs := flag.NewFlagSet("in on/off", flag.ExitOnError)
	dataDir := fs.String("data", "", "数据目录（默认自动探测）")
	sockFlag := fs.String("sock", "", "控制套接字路径（默认自动探测）")
	noApply := fs.Bool("no-apply", false, "只改数据库，不应用配置（之后再跑 tp apply）")
	_ = tpParse(fs, args)
	if fs.NArg() < 1 {
		die(2, "用法: tp in %s <id>", verbFor(enable))
	}
	dir := resolveDataDir(*dataDir)
	st := openExistingStore(dir)
	in, err := st.GetInbound(parseInboundID(fs.Arg(0)))
	st.Close()
	if err != nil {
		die(1, "入站不存在: %v", err)
	}
	if in.Enabled == enable {
		fmt.Printf("  入站 %s 已经是「%s」，无需操作。\n", in.Name, stateWord(enable))
		return
	}

	sock, err := controlSockWith(*sockFlag, dir)
	if err != nil {
		die(1, "%v", err)
	}
	controlMust(sock, "PUT", fmt.Sprintf("/api/inbounds/%d", in.ID),
		map[string]any{"enabled": enable})
	fmt.Printf("  ✔ 入站 %s 已%s\n", in.Name, stateWord(enable))

	if *noApply {
		fmt.Println("    还没生效——配置改完了，但要跑 tp apply 才会写进内核。")
		return
	}
	applyOne(sock, in.Core, fmt.Sprintf("tp in %s %d", verbFor(enable), in.ID))
}

func stateWord(enabled bool) string {
	if enabled {
		return "启用"
	}
	return "停用"
}

func verbFor(enabled bool) string {
	if enabled {
		return "on"
	}
	return "off"
}

func cmdInRm(args []string) {
	fs := flag.NewFlagSet("in rm", flag.ExitOnError)
	dataDir := fs.String("data", "", "数据目录（默认自动探测）")
	sockFlag := fs.String("sock", "", "控制套接字路径（默认自动探测）")
	yes := fs.Bool("yes", false, "跳过确认")
	noApply := fs.Bool("no-apply", false, "只删数据库记录，不应用配置")
	_ = tpParse(fs, args)
	if fs.NArg() < 1 {
		die(2, "用法: tp in rm <id> [--yes]")
	}
	dir := resolveDataDir(*dataDir)
	st := openExistingStore(dir)
	id := parseInboundID(fs.Arg(0))
	in, err := st.GetInbound(id)
	if err != nil {
		st.Close()
		die(1, "入站 %d 不存在: %v", id, err)
	}
	ids, _ := st.InboundClientIDs(in.ID)
	st.Close()

	warn := ""
	if in.Enabled && !in.Draft {
		warn = "\n  ⚠ 这个入站正在服务，删掉之后端口会关、该入站的用户全部断流。"
	}
	if len(ids) > 0 {
		warn += fmt.Sprintf("\n  ⚠ 它还绑着 %d 个用户，关联关系会一并删除（用户本身不删）。", len(ids))
	}
	if !confirm(*yes, "删除入站 %s（#%d，端口 %d）？%s", in.Name, in.ID, in.Port, warn) {
		die(1, "已取消")
	}

	sock, err := controlSockWith(*sockFlag, dir)
	if err != nil {
		die(1, "%v", err)
	}
	controlMust(sock, "DELETE", fmt.Sprintf("/api/inbounds/%d", in.ID), nil)
	fmt.Printf("  ✔ 已删除入站 %s\n", in.Name)
	if *noApply {
		fmt.Println("    还没生效——跑 tp apply 才会从内核配置里去掉。")
		return
	}
	applyOne(sock, in.Core, fmt.Sprintf("tp in rm %d", in.ID))
}

// parseInboundID 接受纯数字 id。
func parseInboundID(s string) int64 {
	var id int64
	if _, err := fmt.Sscan(strings.TrimSpace(s), &id); err != nil || id <= 0 {
		die(2, "入站 id 必须是正整数，收到 %q（用 tp in ls 看 id）", s)
	}
	return id
}

// ---- 用户 ----

func cmdUserAdd(args []string) {
	fs := flag.NewFlagSet("user add", flag.ExitOnError)
	dataDir := fs.String("data", "", "数据目录（默认自动探测）")
	sockFlag := fs.String("sock", "", "控制套接字路径（默认自动探测）")
	quota := fs.Float64("quota", 0, "流量配额（GB，0 = 不限）")
	expires := fs.String("expires", "", "到期日，如 2026-12-31（默认不过期）")
	inbounds := fs.String("inbounds", "", "绑定到哪些入站：id 或名称，逗号分隔（默认不绑）")
	disabled := fs.Bool("disabled", false, "建好后先停用")
	noApply := fs.Bool("no-apply", false, "只写数据库，不应用配置")
	asJSON := fs.Bool("json", false, "以 JSON 输出")
	_ = tpParse(fs, args)
	if fs.NArg() < 1 {
		die(2, "用法: tp user add <名称> [--quota 50] [--expires 2026-12-31] [--inbounds 1,2]")
	}
	name := strings.TrimSpace(fs.Arg(0))
	if name == "" {
		die(2, "名称不能为空")
	}
	dir := resolveDataDir(*dataDir)
	st := openExistingStore(dir)
	defer st.Close()

	if existing := lookupUser(st, name); existing != nil {
		die(1, "用户 %q 已存在（id=%d）", name, existing.ID)
	}
	ids := resolveInboundRefs(st, *inbounds)
	var exp *time.Time
	if *expires != "" {
		exp = parseExpiry(*expires)
	}

	body := map[string]any{
		"name":        name,
		"quota_gb":    *quota,
		"inbound_ids": ids,
	}
	if exp != nil {
		body["expires_at"] = exp.Format(time.RFC3339)
	}
	sock, err := controlSockWith(*sockFlag, dir)
	if err != nil {
		die(1, "%v", err)
	}
	res := controlMust(sock, "POST", "/api/clients", body)

	if *disabled {
		// 建的时候 enabled 恒为 true（createClient 里写死），要停用就紧跟一次更新。
		// 不直接改库：写操作只有 daemon 一个执行者。
		controlMust(sock, "PUT", fmt.Sprintf("/api/clients/%d", int64Of(res["id"])),
			map[string]any{"enabled": false})
	}

	if *asJSON {
		tpJSON(res)
		return
	}
	fmt.Printf("  ✔ 已创建用户 %s\n", name)
	printUserCreds(st, res)
	if !*noApply && len(ids) > 0 {
		for _, c := range coresForInboundIDs(st, ids) {
			applyOne(sock, c, "tp user add "+name)
		}
	}
}

// printUserCreds 打印新建用户时真正需要抄走的东西。
func printUserCreds(st *store.Store, v map[string]any) {
	cred, _ := v["credential"].(string)
	tok, _ := v["sub_token"].(string)
	tpField("凭据", "%s", orDash(cred))
	tpField("订阅令牌", "%s", orDash(tok))
	if tok == "" {
		return
	}
	if base, warn := subBaseURL(st); base != "" {
		tpField("订阅地址", "%s/sub/%s", base, tok)
	} else {
		tpField("订阅地址", "%s", warn)
	}
}

func cmdUserRm(args []string) {
	fs := flag.NewFlagSet("user rm", flag.ExitOnError)
	dataDir := fs.String("data", "", "数据目录（默认自动探测）")
	sockFlag := fs.String("sock", "", "控制套接字路径（默认自动探测）")
	yes := fs.Bool("yes", false, "跳过确认")
	noApply := fs.Bool("no-apply", false, "只删数据库记录，不应用配置")
	_ = tpParse(fs, args)
	if fs.NArg() < 1 {
		die(2, "用法: tp user rm <名称> [--yes]")
	}
	dir := resolveDataDir(*dataDir)
	st := openExistingStore(dir)
	defer st.Close()

	c := findUser(st, fs.Arg(0))
	// 注意是 ClientInboundIDs（用户 → 入站），不是 InboundClientIDs
	// （入站 → 用户）。两者都收一个 int64，传错了编译器不会拦，
	// 症状是「删了用户但内核配置没变」——因为算出来的内核集合是空的。
	ins, _ := st.ClientInboundIDs(c.ID)
	if !confirm(*yes, "删除用户 %s（id=%d）？删掉后订阅链接立即失效，绑定的 %d 个入站也会解绑。",
		c.Name, c.ID, len(ins)) {
		die(1, "已取消")
	}
	sock, err := controlSockWith(*sockFlag, dir)
	if err != nil {
		die(1, "%v", err)
	}
	controlMust(sock, "DELETE", fmt.Sprintf("/api/clients/%d", c.ID), nil)
	fmt.Printf("  ✔ 已删除用户 %s\n", c.Name)
	if *noApply {
		fmt.Println("    还没生效——跑 tp apply 才会从内核配置里去掉。")
		return
	}
	for _, cn := range coresForInboundIDs(st, ins) {
		applyOne(sock, cn, "tp user rm "+c.Name)
	}
}

func cmdUserToken(args []string) {
	fs := flag.NewFlagSet("user token", flag.ExitOnError)
	dataDir := fs.String("data", "", "数据目录（默认自动探测）")
	sockFlag := fs.String("sock", "", "控制套接字路径（默认自动探测）")
	_ = tpParse(fs, args)
	if fs.NArg() < 1 {
		die(2, "用法: tp user token <名称>")
	}
	dir := resolveDataDir(*dataDir)
	st := openExistingStore(dir)
	defer st.Close()

	c := findUser(st, fs.Arg(0))
	sock, err := controlSockWith(*sockFlag, dir)
	if err != nil {
		die(1, "%v", err)
	}
	res := controlMust(sock, "POST", fmt.Sprintf("/api/clients/%d/reset-token", c.ID), nil)
	tok, _ := res["sub_token"].(string)
	fmt.Printf("  ✔ %s 的订阅令牌已重置（旧链接立即失效）\n", c.Name)
	tpField("新令牌", "%s", tok)
	if base, warn := subBaseURL(st); base != "" {
		tpField("订阅地址", "%s/sub/%s", base, tok)
	} else {
		tpField("订阅地址", "%s", warn)
	}
	// 令牌只影响订阅 URL，不进内核配置，所以不需要 apply。
	fmt.Println("\n  内核配置不受影响，不用重新应用。")
}

func cmdUserSet(args []string) {
	fs := flag.NewFlagSet("user set", flag.ExitOnError)
	dataDir := fs.String("data", "", "数据目录（默认自动探测）")
	sockFlag := fs.String("sock", "", "控制套接字路径（默认自动探测）")
	quota := fs.Float64("quota", -1, "流量配额（GB，0 = 不限）")
	expires := fs.String("expires", "", "到期日，如 2026-12-31；传 none 表示取消到期")
	inbounds := fs.String("inbounds", "", "重绑入站：id 或名称，逗号分隔；传 none 表示清空")
	enable := fs.Bool("enable", false, "启用")
	disable := fs.Bool("disable", false, "停用")
	noApply := fs.Bool("no-apply", false, "只写数据库，不应用配置")
	_ = tpParse(fs, args)
	if fs.NArg() < 1 {
		die(2, "用法: tp user set <名称> [--quota 50] [--expires 2026-12-31|none] [--enable|--disable] [--inbounds 1,2|none]")
	}
	if *enable && *disable {
		die(2, "--enable 和 --disable 只能选一个")
	}
	dir := resolveDataDir(*dataDir)
	st := openExistingStore(dir)
	defer st.Close()

	c := findUser(st, fs.Arg(0))
	body := map[string]any{}
	if *quota >= 0 {
		body["quota_gb"] = *quota
	}
	if *expires != "" {
		if strings.EqualFold(*expires, "none") {
			body["expires_at"] = nil // 显式 null = 清除到期时间
		} else {
			body["expires_at"] = parseExpiry(*expires).Format(time.RFC3339)
		}
	}
	if *enable {
		body["enabled"] = true
	}
	if *disable {
		body["enabled"] = false
	}
	var newIDs []int64
	rebind := *inbounds != ""
	if rebind {
		if strings.EqualFold(*inbounds, "none") {
			newIDs = []int64{}
		} else {
			newIDs = resolveInboundRefs(st, *inbounds)
		}
	}
	// ClientInboundIDs 是「用户 → 入站」，别写成 InboundClientIDs。
	oldIDs, _ := st.ClientInboundIDs(c.ID)

	if len(body) == 0 && !rebind {
		die(2, "没有任何要改的字段（--quota / --expires / --enable / --disable / --inbounds）")
	}

	sock, err := controlSockWith(*sockFlag, dir)
	if err != nil {
		die(1, "%v", err)
	}
	if len(body) > 0 {
		controlMust(sock, "PUT", fmt.Sprintf("/api/clients/%d", c.ID), body)
	}
	if rebind {
		controlMust(sock, "PUT", fmt.Sprintf("/api/clients/%d/inbounds", c.ID),
			map[string]any{"inbound_ids": newIDs})
	}

	var changed []string
	for _, k := range []string{"quota_gb", "expires_at", "enabled"} {
		if _, ok := body[k]; ok {
			changed = append(changed, k)
		}
	}
	if rebind {
		changed = append(changed, "inbounds")
	}
	fmt.Printf("  ✔ 已更新用户 %s（%s）\n", c.Name, strings.Join(changed, "、"))

	// 只有「启用状态」和「绑定关系」会影响内核配置；配额和到期日是 ops
	// 采集器在管，改完不用动内核。重启内核会断掉所有在线连接，能不动就不动。
	_, touchedEnabled := body["enabled"]
	if !rebind && !touchedEnabled {
		fmt.Println("    配额/到期不写进内核配置，无需重新应用。")
		return
	}
	if *noApply {
		fmt.Println("    还没生效——跑 tp apply 才会写进内核。")
		return
	}
	ids := append(append([]int64{}, oldIDs...), newIDs...)
	for _, cn := range coresForInboundIDs(st, ids) {
		applyOne(sock, cn, "tp user set "+c.Name)
	}
}

// requireUser 按名字取用户，取不到就退出。
func requireUser(st *store.Store, name string) *model.Client {
	return findUser(st, name)
}

// resolveInboundRefs 把 "1,主力节点" 这样的写法解析成 id 列表。
//
// 同时认 id 和名称：脚本里用 id 稳定，人手上敲名称更自然。
func resolveInboundRefs(st *store.Store, spec string) []int64 {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil
	}
	all, err := st.ListInbounds("")
	if err != nil {
		die(1, "读取入站失败: %v", err)
	}
	byName := map[string]*model.Inbound{}
	for _, in := range all {
		byName[in.Name] = in
	}
	var out []int64
	for _, part := range strings.Split(spec, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		if id := parseIDOrZero(p); id > 0 {
			if in, err := st.GetInbound(id); err != nil || in == nil {
				die(1, "入站 id %d 不存在", id)
			}
			out = append(out, id)
			continue
		}
		in, ok := byName[p]
		if !ok {
			die(1, "找不到叫 %q 的入站（用 tp in ls 看）", p)
		}
		out = append(out, in.ID)
	}
	return out
}

// coresForInboundIDs 找出这些入站涉及的内核（去重、保持稳定顺序）。
func coresForInboundIDs(st *store.Store, ids []int64) []string {
	seen := map[string]bool{}
	for _, id := range ids {
		if in, err := st.GetInbound(id); err == nil && in != nil {
			seen[in.Core] = true
		}
	}
	var out []string
	for _, n := range coreNames() {
		if seen[n] {
			out = append(out, n)
		}
	}
	return out
}

// parseExpiry 解析到期日。只认 YYYY-MM-DD，并且落到当天 23:59:59 ——
// 「到期日 12-31」应该覆盖整个 12-31，而不是当天零点就失效。
func parseExpiry(s string) *time.Time {
	s = strings.TrimSpace(s)
	t, err := time.ParseInLocation("2006-01-02", s, time.Local)
	if err != nil {
		die(2, "到期日格式不对：%q（要 2026-12-31 这种写法）", s)
	}
	end := t.Add(24*time.Hour - time.Second)
	return &end
}

func parseIDOrZero(s string) int64 {
	var id int64
	if _, err := fmt.Sscan(s, &id); err != nil || id <= 0 {
		return 0
	}
	return id
}

func int64Of(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	}
	return 0
}

// coreNames 三个内核的固定顺序（与 core.All() 一致）。
func coreNames() []string { return []string{"singbox", "xray", "mihomo"} }

func coreByName(name string) (string, error) {
	for _, n := range coreNames() {
		if n == name {
			return n, nil
		}
	}
	return "", fmt.Errorf("未知内核 %q（可用：%s）", name, strings.Join(coreNames(), " / "))
}
