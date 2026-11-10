package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"triune/internal/firewall"
	"triune/internal/model"
)

// ---- inbounds ----

func (a *Server) listInbounds(w http.ResponseWriter, r *http.Request) {
	ins, err := a.Store.ListInbounds(r.URL.Query().Get("core"))
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if ins == nil {
		ins = []*model.Inbound{}
	}
	// 库里已有的行也要归一化：保存期归一化只覆盖「这次之后」建/改的入站，
	// 而这次报错的那条入站早就躺在库里了。表单里那一栏是给人复制去手填客户端的，
	// 显示一个和私钥不配的公钥，等于把人送上同一条报错。
	// 只改返回给界面的副本，不落库——落库留给下一次保存，避免 GET 触发写。
	for _, in := range ins {
		normalizeRealitySettings(in)
	}
	writeJSON(w, 200, ins)
}

func (a *Server) createInbound(w http.ResponseWriter, r *http.Request) {
	// Clients 用指针：区分「没传这个字段」和「传了空数组」。
	//
	// 没传 = 面板自己新增入站，没人提过用户，顺手补一个（见 autouser.go）；
	// 传了空数组 = 调用方明确不要用户（例如导入时取消了「一并创建用户」），
	// 这时再自动补一个就是违背用户意图。
	var req struct {
		model.Inbound
		Clients *[]inboundClientSpec `json:"clients"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, err.Error())
		return
	}
	in := req.Inbound
	if in.Settings == "" {
		in.Settings = "{}"
	}
	normalizeRealitySettings(&in)
	if msg := a.validateInboundInput(&in, 0); msg != "" {
		fail(w, 400, msg)
		return
	}
	if !a.compatCoreAllowed(in.Core) {
		fail(w, 400, "mihomo 是兼容内核：它支持的协议 sing-box 全都支持，且缺少 vless / reality，"+
			"新建入站请选 sing-box 或 xray。若确实需要（例如接管机器上已有的 mihomo 配置），"+
			"请到「设置 → 组件更新」打开「显示兼容内核」。")
		return
	}
	// 内联 PEM → 落盘（xray 可内联，sing-box 只认路径，统一转成文件最省事）
	if err := a.materializeInboundPEM(&in); err != nil {
		fail(w, 400, "证书落盘失败: "+err.Error())
		return
	}
	if err := a.Store.CreateInbound(&in); err != nil {
		fail(w, 500, err.Error())
		return
	}
	// 入站建好之后再建用户并绑定；单个用户失败不影响入站本身（已建），
	// 但会写日志，避免「静默少了几个用户」。
	//
	// 请求里没提 clients 时自动补一个用户：新建入站最常见的下一句是「链接呢」，
	// 而没人绑定的入站导不出任何链接。名字是 8 位随机串，理由见 autouser.go。
	var specs []inboundClientSpec
	autoUser := ""
	if req.Clients != nil {
		specs = *req.Clients
	} else if s, ok := a.autoInboundClient(&in); ok {
		specs = []inboundClientSpec{s}
		autoUser = s.Name
	}
	made := a.createInboundClients(&in, specs)
	// 自动放行防火墙端口：仅非草稿、且开启自动管理时。草稿是导入预览，
	// 未参与渲染，待「取消草稿」转正时（updateInbound）再放行。
	if fwAuto(a) && !in.Draft {
		for _, proto := range firewall.ProtoList(in.Protocol, in.Transport) {
			if err := firewall.OpenPort(in.Port, proto); err != nil {
				log.Printf("[fw] 开放入站端口 %d/%s 失败: %v", in.Port, proto, err)
			}
		}
	}
	detail := fmt.Sprintf("%s/%s 端口 %d", in.Core, in.Protocol, in.Port)
	if in.Draft {
		detail += "（草稿）"
	}
	switch {
	case autoUser != "" && made > 0:
		detail += "，自动建用户 " + autoUser
	case made > 0:
		detail += fmt.Sprintf("，同时建 %d 个用户", made)
	}
	a.changed(r, AuditInboundCreate, in.Name, detail)
	writeJSON(w, 200, inboundCreated{Inbound: in, AutoClient: autoUser, Users: made})
}

// inboundCreated 建入站的响应。
//
// 入站字段仍然平铺在顶层（老调用方把它解成 model.Inbound 不受影响），只多给
// 两个字段说明「顺手建了谁」——界面要把这个名字说出来，否则用户得自己去用户页
// 才能发现多了一个人。
type inboundCreated struct {
	model.Inbound
	AutoClient string `json:"auto_client,omitempty"`
	Users      int    `json:"users"` // 本次真正建成的用户数（含导入带进来的）
}

// inboundClientSpec 建入站时顺带建的用户（从 JSON 填表时带上）。
//
// 字段刻意与 model.Client 同名同义，只多一个「可以留空」的语义：
// Credential 为空时按协议补一个（vless/vmess 用 UUID，trojan/ss 用密码），
// 这样调用方不用自己判断该生成什么。
type inboundClientSpec struct {
	Name       string     `json:"name"`
	Credential string     `json:"credential"`
	QuotaGB    float64    `json:"quota_gb"`
	ExpiresAt  *time.Time `json:"expires_at"`
	Enabled    *bool      `json:"enabled"`
}

// createInboundClients 建用户并绑定到入站，返回成功条数。
func (a *Server) createInboundClients(in *model.Inbound, specs []inboundClientSpec) int {
	if len(specs) == 0 {
		return 0
	}
	seen := map[string]bool{}
	made := 0
	for i, s := range specs {
		cred := strings.TrimSpace(s.Credential)
		if cred == "" {
			// 兜底：调用方没给凭据就按协议生成（与 parse-json 同一套规则）
			cred, _ = generateCredential(in.Protocol, nil)
			if cred == "" {
				cred = uuid4()
			}
		}
		// 同一份 JSON 里出现重复凭据时只建一次：两个用户共用一个 UUID
		// 在内核里等于同一个人，多建没意义还容易让人以为配置有问题。
		if seen[cred] {
			continue
		}
		seen[cred] = true
		name := strings.TrimSpace(s.Name)
		if name == "" {
			name = fmt.Sprintf("%s-user%d", in.Name, i+1)
		}
		cl := &model.Client{
			Name:       name,
			Credential: cred,
			SubToken:   token(),
			QuotaGB:    s.QuotaGB,
			ExpiresAt:  s.ExpiresAt,
			Enabled:    true,
		}
		if s.Enabled != nil {
			cl.Enabled = *s.Enabled
		}
		if err := a.Store.CreateClient(cl); err != nil {
			log.Printf("[inbound] 入站 %s(%d) 的用户 %s 创建失败: %v", in.Name, in.ID, name, err)
			continue
		}
		if err := a.Store.SetClientInbounds(cl.ID, []int64{in.ID}); err != nil {
			log.Printf("[inbound] 用户 %s 绑定入站 %d 失败: %v", name, in.ID, err)
			continue
		}
		made++
	}
	return made
}

// updateInbound 做的是「部分更新」：只覆盖请求里真正出现的键。
//
// 旧实现和 updateClient 是同一个毛病——把请求体解成完整的 model.Inbound 再
// 整行 UPDATE。`PUT {"name":"改名"}` 会把 core/protocol/port/tag/settings 全部
// 清成零值（settings 变 "{}"、port 变 0），而且返回 200。更糟的是内核切换守卫
// 拿零值去比，启用中的入站会收到「请使用 /api/switch-core」——调用方根本没想
// 换内核，这条错误只会让人以为是权限或状态问题。
func (a *Server) updateInbound(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		fail(w, 400, err.Error())
		return
	}

	old, err := a.Store.GetInbound(pathID(r))
	if err != nil {
		fail(w, 404, err.Error())
		return
	}
	in := *old
	for k, v := range raw {
		var ferr error
		switch k {
		case "name":
			ferr = json.Unmarshal(v, &in.Name)
		case "core":
			ferr = json.Unmarshal(v, &in.Core)
		case "protocol":
			ferr = json.Unmarshal(v, &in.Protocol)
		case "listen":
			ferr = json.Unmarshal(v, &in.Listen)
		case "port":
			ferr = json.Unmarshal(v, &in.Port)
		case "node_host":
			// 漏掉这一行的后果不是「改不了地址」，而是**开关一次入站就把地址
			// 清空**：前端 toggleInbound 会把整条入站 PUT 回来，白名单里没有的
			// 键被静默丢弃，于是 NodeHost 归零、导出的链接悄悄换回全局地址。
			ferr = json.Unmarshal(v, &in.NodeHost)
		case "tag":
			ferr = json.Unmarshal(v, &in.Tag)
		case "transport":
			ferr = json.Unmarshal(v, &in.Transport)
		case "tls":
			ferr = json.Unmarshal(v, &in.TLS)
		case "settings":
			ferr = json.Unmarshal(v, &in.Settings)
		case "enabled":
			ferr = json.Unmarshal(v, &in.Enabled)
		case "draft":
			ferr = json.Unmarshal(v, &in.Draft)
		}
		if ferr != nil {
			fail(w, 400, "字段 "+k+" 格式有误: "+ferr.Error())
			return
		}
	}
	if in.Settings == "" {
		in.Settings = "{}"
	}
	normalizeRealitySettings(&in)
	if msg := a.validateInboundInput(&in, old.ID); msg != "" {
		fail(w, 400, msg)
		return
	}
	// 从别的内核改成 mihomo 等于新建一个降级入站，同样拦下；
	// 但「本来就是 mihomo」的入站必须放行，否则老用户连改个名字都改不了。
	if in.Core != old.Core && !a.compatCoreAllowed(in.Core) {
		fail(w, 400, "mihomo 是兼容内核（协议是 sing-box 的子集，且不支持 vless / reality），不能切过去。"+
			"若确实需要，请到「设置 → 组件更新」打开「显示兼容内核」。")
		return
	}
	if err := a.materializeInboundPEM(&in); err != nil {
		fail(w, 400, "证书落盘失败: "+err.Error())
		return
	}
	// 运行中的入站不允许直接改内核 —— 必须走 /api/switch-core 安全切换流程
	// （预检 → 停旧 → 启新 → 健康检查 → 失败自动回滚），否则可能出现
	// 新内核配置从未校验过就上线的风险。
	if old.Core != in.Core && old.Enabled && !old.Draft {
		fail(w, 400, "运行中的入站切换内核请使用 POST /api/switch-core（含预检与健康检查）")
		return
	}
	if err := a.Store.UpdateInbound(&in); err != nil {
		fail(w, 500, err.Error())
		return
	}
	// 防火墙端口对账：草稿转正 / 端口或协议变化
	if fwAuto(a) {
		oldProtos := firewall.ProtoList(old.Protocol, old.Transport)
		newProtos := firewall.ProtoList(in.Protocol, in.Transport)
		// 草稿 → 正式：放行新端口
		if old.Draft && !in.Draft {
			a.openPorts(in.Port, newProtos)
		}
		// 端口或协议变化：关闭旧（无其他入站占用时）+ 开放新
		if (old.Port != in.Port || !sameProtos(oldProtos, newProtos)) && !in.Draft {
			if !a.portInUseByOthers(in.ID, old.Port, oldProtos) {
				a.closePorts(old.Port, oldProtos)
			}
			a.openPorts(in.Port, newProtos)
		}
	}
	detail := fmt.Sprintf("%s/%s 端口 %d", in.Core, in.Protocol, in.Port)
	if old.Draft && !in.Draft {
		detail += "（草稿转正）"
	}
	a.changed(r, AuditInboundUpdate, in.Name, detail, inboundDiff(old, &in)...)
	writeJSON(w, 200, in)
}

// validateInboundInput 校验入站的可提交性，返回空串表示通过。
//
// 三条都是「创建成功、应用时才炸」的输入问题，而炸出来的内核报错完全指不到
// 真因，所以前移到保存时：
//   - 端口越界：0 / 65536 / 负数都能存进去，直接让整份配置校验失败；
//   - 端口冲突：同端口同协议族的两个入站，任何一个内核都起不来（见 validatePortConflict）；
//   - tag 重复：xray 报 `existing tag found: dup` 并拒绝**整份**配置，用户看到的
//     是一串内核日志，而不是「你有两个入站重名了」。
//   - 节点地址填错：进了库就只影响导出的链接，内核侧一切正常，所以报错现场
//     永远在客户端（连不上），服务端日志干干净净。归一化后写回 in.NodeHost。
//
// 注意 tag 只要求在同一内核内唯一：三个内核各有一份独立配置，同名互不影响。
func (a *Server) validateInboundInput(in *model.Inbound, selfID int64) string {
	// 客户端连接地址：先归一化（剥掉协议 / 端口 / 路径），非法值直接拒。
	if h, msg := parseNodeHost(in.NodeHost); msg != "" {
		return msg
	} else {
		in.NodeHost = h
	}
	if in.Port < 1 || in.Port > 65535 {
		return fmt.Sprintf("端口 %d 不合法：必须是 1-65535", in.Port)
	}
	if msg := a.validatePortConflict(in, selfID); msg != "" {
		return msg
	}
	// REALITY 的 shortIds / spiderX 填错了内核不会在保存期报错，只会让客户端
	// 连不上（详见 reality.go 顶部）。这里提前拦，错误里点名是第几个、错在哪。
	if in.TLS == "reality" {
		if msg := validateRealitySettings(in.Settings); msg != "" {
			return msg
		}
	}
	if in.Tag == "" || in.Core == "" {
		// 空 tag 由 TagOf 生成 in-<id>；空 core 的入站本来就渲染不了，
		// 这里不拿「全内核」去比 tag，否则会把不同内核的同名入站误判成重名。
		return ""
	}
	others, err := a.Store.ListInbounds(in.Core)
	if err != nil {
		return ""
	}
	for _, o := range others {
		if o.ID == selfID {
			continue
		}
		if o.Tag == in.Tag {
			return fmt.Sprintf("入站名 %q 已被 #%d 使用（同一内核内 tag 必须唯一），请换一个名字",
				in.Tag, o.ID)
		}
	}
	return ""
}

// validatePortConflict 同端口 + 同协议族 → 保存期就拦下，返回空串表示不冲突。
//
// 为什么要前移：xray 的 `run -test` 和 sing-box 的 `check` 只校验配置语法，
// **不 bind socket**，所以两个入站抢同一端口能一路存库、渲染、替换配置，直到
// 内核启动失败才在健康检查里暴露——而那时配置已经被换掉，该内核下的所有节点
// 一起断，用户看到的是一句英文内核日志。
//
// 判据：
//   - 只有「已启用且非草稿」的入站才真的占端口，草稿（导入预览）和停用的不参与；
//   - 端口相同、listen 会抢同一个 socket、协议族有交集才算冲突；
//   - 一个 TCP 一个 UDP 可以共存（vless 443 + hysteria2 443 是常见搭配），
//     所以按 ProtoList 求交集，而不是简单比端口。
func (a *Server) validatePortConflict(in *model.Inbound, selfID int64) string {
	if !in.Enabled || in.Draft {
		return ""
	}
	all, err := a.Store.ListInbounds("") // 空 core = 全部：不同内核也抢同一个系统端口
	if err != nil {
		return ""
	}
	protos := firewall.ProtoList(in.Protocol, in.Transport)
	for _, o := range all {
		if o.ID == selfID || !o.Enabled || o.Draft || o.Port != in.Port {
			continue
		}
		if !listenOverlap(in.Listen, o.Listen) {
			continue
		}
		other := firewall.ProtoList(o.Protocol, o.Transport)
		if !firewall.ProtoOverlap(protos, other) {
			continue
		}
		return fmt.Sprintf("端口 %d 已被 #%d「%s」（%s/%s，占用 %s）使用：同一端口上同协议族只能有一个入站。"+
			"请换一个端口；若要共用这个端口，让其中一个改走另一种协议族（例如 hysteria2 用 UDP）",
			in.Port, o.ID, o.Name, o.Core, o.Protocol, strings.Join(other, "+"))
	}
	return ""
}

// listenOverlap 两个 listen 是否会抢同一个 socket。
// 空值 / 0.0.0.0 / :: 都表示「所有地址」，与任何具体地址都冲突。
func listenOverlap(a, b string) bool {
	norm := func(s string) string {
		switch strings.TrimSpace(s) {
		case "", "0.0.0.0", "::", "[::]", "*":
			return ""
		}
		return strings.TrimSpace(s)
	}
	na, nb := norm(a), norm(b)
	return na == "" || nb == "" || na == nb
}

func (a *Server) deleteInbound(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	in, _ := a.Store.GetInbound(id)
	if err := a.Store.DeleteInbound(id); err != nil {
		fail(w, 500, err.Error())
		return
	} // 回收防火墙端口：仅当该端口/协议不再被其它启用入站使用时
	if fwAuto(a) && in != nil && !in.Draft {
		protos := firewall.ProtoList(in.Protocol, in.Transport)
		if !a.portInUseByOthers(id, in.Port, protos) {
			a.closePorts(in.Port, protos)
		}
	}
	name := fmt.Sprintf("#%d", id)
	if in != nil {
		name = in.Name
	}
	a.changed(r, AuditInboundDelete, name, fmt.Sprintf("删除入站 #%d", id))
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

// fwAuto 是否启用入站端口的自动防火墙管理（默认开启，设置 fw_auto=0 关闭）。
func fwAuto(a *Server) bool {
	v, _ := a.Store.GetSetting("fw_auto")
	return v != "0"
}

// SyncFirewallPorts 启动时对账：为所有「已启用且非草稿」的入站端口补一次放行。
// 场景：面板升级到带自动放行能力的版本后，存量入站不会重新保存一遍，
// 不主动对账就会一直处于「已监听但防火墙未放行」的状态。
// 幂等：已放行的端口会被各后端自身跳过；无防火墙后端时直接空转。
func (a *Server) SyncFirewallPorts() {
	if !fwAuto(a) {
		return
	}
	ins, err := a.Store.ListInbounds("")
	if err != nil {
		return
	}
	seen := map[string]bool{}
	for _, in := range ins {
		if in.Draft || !in.Enabled {
			continue
		}
		for _, proto := range firewall.ProtoList(in.Protocol, in.Transport) {
			key := fmt.Sprintf("%d/%s", in.Port, proto)
			if seen[key] {
				continue
			}
			seen[key] = true
			if err := firewall.OpenPort(in.Port, proto); err != nil {
				log.Printf("[fw] 启动对账放行 %s 失败: %v", key, err)
			}
		}
	}
}

// openPorts / closePorts 按协议集合批量操作；单个失败只记日志，不影响主流程
// （防火墙是尽力而为的辅助，失败不该让一次入站保存整体失败）。
func (a *Server) openPorts(port int, protos []string) {
	for _, proto := range protos {
		if err := firewall.OpenPort(port, proto); err != nil {
			log.Printf("[fw] 开放端口 %d/%s 失败: %v", port, proto, err)
		}
	}
}

func (a *Server) closePorts(port int, protos []string) {
	for _, proto := range protos {
		if err := firewall.ClosePort(port, proto); err != nil {
			log.Printf("[fw] 回收端口 %d/%s 失败: %v", port, proto, err)
		}
	}
}

func sameProtos(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, x := range a {
		m[x]++
	}
	for _, y := range b {
		m[y]--
		if m[y] < 0 {
			return false
		}
	}
	return true
}

// portInUseByOthers 判断给定端口/协议集合是否仍被其它「已启用且非草稿」的入站使用，
// 用于删除/改端口时避免误关仍被占用的端口。protos 与对方的端口协议集合有交集
// 才算占用（例如同一端口上一个是 socks 双栈、另一个是 tcp only，tcp 不能收回）。
func (a *Server) portInUseByOthers(selfID int64, port int, protos []string) bool {
	others, err := a.Store.ListInbounds("")
	if err != nil {
		return false
	}
	want := map[string]bool{}
	for _, p := range protos {
		want[p] = true
	}
	for _, o := range others {
		if o.ID == selfID || !o.Enabled || o.Draft {
			continue
		}
		if o.Port != port {
			continue
		}
		for _, p := range firewall.ProtoList(o.Protocol, o.Transport) {
			if want[p] {
				return true
			}
		}
	}
	return false
}
