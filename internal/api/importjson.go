package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"triune/internal/importer"
	"triune/internal/model"
)

// JSON 配置导入：把 3x-ui / x-ui 的入站导出或原生 xray 的 inbounds 段
// 变成面板入站。
//
// 为什么不再做「分享链接 → 服务端入站」：链接是客户端视角的快照，
// 里面没有 Reality 私钥、没有证书、没有多用户，反向只能猜。
// JSON 则是**服务端的原始定义**，要什么有什么——差别不是工作量，是信息量。
//
// 两个硬约束：
//  1. 导入的入站一律 enabled=false：不点启用就绝不会进内核配置，
//     导入动作本身零风险；
//  2. 映不了的字段带警告回显，不静默丢。用户看到「h2 已按 tcp 建草稿」
//     才知道要自己去核对，而不是等到 apply 时报一句看不懂的 FATAL。

const maxImportJSONBytes = 8 << 20 // 8 MB：3x-ui 全量导出通常 <1MB

// importCoreProtocols 各内核能渲染的入站协议（与 UI 的 IB_SPECS 一致）。
var importCoreProtocols = map[string][]string{
	model.CoreSingBox: {"vless", "vmess", "trojan", "shadowsocks", "hysteria2", "tuic",
		"anytls", "mixed", "socks", "http", "direct"},
	model.CoreXray:   {"vless", "vmess", "trojan", "shadowsocks", "socks", "http", "dokodemo-door", "wireguard"},
	model.CoreMihomo: {"mixed", "socks", "http", "shadowsocks", "vmess", "trojan", "tuic", "hysteria2"},
}

// importCoreTransports 各内核能渲染的传输。
// xray 26 起移除了 h2 与裸 quic（并入 xhttp），所以这里不再列它们。
var importCoreTransports = map[string][]string{
	model.CoreXray:    {"tcp", "ws", "grpc", "httpupgrade", "xhttp", "kcp"},
	model.CoreSingBox: {"tcp", "ws", "grpc", "h2", "httpupgrade", "quic"},
	model.CoreMihomo:  {"tcp"},
}

func coreSupportsInbound(core, protocol string) bool {
	for _, p := range importCoreProtocols[core] {
		if p == protocol {
			return true
		}
	}
	return false
}

func coreSupportsTransport(core, transport string) bool {
	if transport == "" {
		return true
	}
	for _, t := range importCoreTransports[core] {
		if t == transport {
			return true
		}
	}
	return false
}

// adaptToCore 把入站调整到目标内核能渲染的形态，返回因此产生的提示。
//
// 只做「语义等价」的改名（dokodemo-door ↔ direct）和「明确不可用就退回 tcp」
// 两种处理，别的一律交给上层判 Skip —— 猜着改比不改更危险。
func adaptToCore(core string, ib *importer.Inbound) []string {
	var warns []string
	if core == model.CoreSingBox && ib.Protocol == "dokodemo-door" {
		ib.Protocol = "direct"
		warns = append(warns, "dokodemo-door 在 sing-box 下按 direct 入站导入（语义相同：端口转发）")
	}
	// Xray 26 移除了 h2 与裸 QUIC 传输（都并入 XHTTP），照搬会启动即 FATAL。
	// 在导入阶段就转成等价形态，并说明客户端也要改。
	if core == model.CoreXray && (ib.Transport == "h2" || ib.Transport == "quic") {
		old := ib.Transport
		ib.Transport = "xhttp"
		ib.Settings["xhttp_mode"] = "stream-one"
		if old == "h2" {
			warns = append(warns, "Xray 26 起移除了 h2 传输，已转成 xhttp（mode=stream-one）；客户端需同步改成 xhttp，否则连不上")
		} else {
			warns = append(warns, "Xray 26 起移除了裸 QUIC 传输，已转成 xhttp（mode=stream-one）；要走 H3 请在 TLS 里把 ALPN 设为 h3 并配好证书")
			// quic 独有的参数在 xhttp 下没有对应项，别留着误导
			delete(ib.Settings, "quic_security")
			delete(ib.Settings, "quic_key")
			delete(ib.Settings, "quic_header")
		}
	}
	if core == model.CoreSingBox && ib.Transport == "quic" && ib.TLS != "tls" {
		warns = append(warns, "sing-box 的 quic 传输必须配 TLS（否则内核启动即报 quic: TLS required），请补上证书或改用 ws/grpc")
	}
	if !coreSupportsTransport(core, ib.Transport) {
		warns = append(warns, fmt.Sprintf("%s 内核不支持 %s 传输，已按 tcp 导入；客户端需同步改传输，否则连不上", core, ib.Transport))
		ib.Transport = "tcp"
	}
	return warns
}

// importPreview 预览/结果里的单条入站。
type importPreview struct {
	Name      string                `json:"name"`
	Core      string                `json:"core"`
	Protocol  string                `json:"protocol"`
	Listen    string                `json:"listen"`
	Port      int                   `json:"port"`
	Transport string                `json:"transport"`
	TLS       string                `json:"tls"`
	Clients   []importPreviewClient `json:"clients"`
	Warnings  []string              `json:"warnings,omitempty"`
	Skip      bool                  `json:"skip"`
	ID        int64                 `json:"id,omitempty"`
}

type importPreviewClient struct {
	Name       string  `json:"name"`
	Credential string  `json:"credential"` // 预览时打码
	QuotaGB    float64 `json:"quota_gb"`
	ExpiresAt  string  `json:"expires_at,omitempty"`
	Enabled    bool    `json:"enabled"`
}

// POST /api/inbounds/import-json
//
//	{"text":"[...]","core":"xray","dry_run":true}
//
// dry_run=true 只解析并回显，不写库；前端「预览」用这个。
func (a *Server) handleImportJSON(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text   string `json:"text"`
		Core   string `json:"core"`
		DryRun bool   `json:"dry_run"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxImportJSONBytes)).Decode(&req); err != nil {
		fail(w, 400, "请求体解析失败（上限 8 MB）："+err.Error())
		return
	}
	if len(req.Text) > maxImportJSONBytes {
		fail(w, 400, "内容过大（上限 8 MB）")
		return
	}
	core := strings.ToLower(strings.TrimSpace(req.Core))
	if core == "" {
		core = model.CoreXray // 3x-ui 是 xray 面板，默认按 xray 导入
	}
	if _, ok := importCoreProtocols[core]; !ok {
		fail(w, 400, "未知内核: "+core+"（可选 singbox / xray / mihomo）")
		return
	}

	res, err := importer.ParseFull(req.Text)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	ibs := res.Inbounds

	existing, _ := a.Store.ListInbounds("")
	usedName := map[string]bool{}
	usedTag := map[string]bool{}
	for _, in := range existing {
		usedName[in.Name] = true
		if in.Tag != "" {
			usedTag[in.Tag] = true
		}
	}
	usedSub := map[string]bool{}

	var previews []importPreview
	importable := 0
	for _, ib := range ibs {
		if !ib.Skip {
			if w := adaptToCore(core, ib); len(w) > 0 {
				ib.Warnings = append(ib.Warnings, w...)
			}
		}
		p := importPreview{
			Name:      ib.Name,
			Core:      core,
			Protocol:  ib.Protocol,
			Listen:    ib.Listen,
			Port:      ib.Port,
			Transport: ib.Transport,
			TLS:       ib.TLS,
			Warnings:  append([]string{}, ib.Warnings...),
			Skip:      ib.Skip,
		}
		if !p.Skip && !coreSupportsInbound(core, ib.Protocol) {
			p.Skip = true
			p.Warnings = append(p.Warnings, fmt.Sprintf("协议 %s 在 %s 内核下不支持渲染，请换 xray/singbox 后再导入", ib.Protocol, core))
		}
		for _, c := range ib.Clients {
			pc := importPreviewClient{
				Name:       c.Name,
				Credential: maskCredential(c.Credential),
				QuotaGB:    c.QuotaGB,
				Enabled:    c.Enabled,
			}
			if c.ExpiresAt != nil {
				pc.ExpiresAt = c.ExpiresAt.Format("2006-01-02")
			}
			p.Clients = append(p.Clients, pc)
		}
		if !p.Skip {
			importable++
		}
		previews = append(previews, p)
	}

	summary := map[string]any{
		"total":      len(previews),
		"importable": importable,
		"skipped":    len(previews) - importable,
	}
	plan := planGlobal(core, res.Global)
	var globalPreview map[string]any
	if plan != nil {
		globalPreview = map[string]any{
			"dns_servers":   countList(plan.DNS, "servers"),
			"routing_rules": countList(plan.Route, "rules"),
			"warnings":      plan.Warnings,
			// dry_run 时 false，真正写库后才 true —— 让「预览」和「结果」语义一致
			"applied": !req.DryRun && plan.ok(),
		}
	}
	if req.DryRun {
		writeJSON(w, 200, map[string]any{
			"dry_run": true,
			"core":    core,
			"items":   previews,
			"summary": summary,
			"global":  globalPreview,
		})
		return
	}
	// 全局段是「整机唯一」的：重复导入会覆盖上一次的 DNS/路由，
	// 所以在结果里明确说一句，避免用户以为会叠加。
	if plan != nil && plan.ok() {
		if err := a.saveGlobal(core, plan); err != nil {
			log.Printf("import-json: 保存全局段失败: %v", err)
		} else if globalPreview != nil {
			globalPreview["applied"] = true
		}
	}

	// ---- 真正写库 ----
	var failed []string
	for i := range previews {
		p := &previews[i]
		if p.Skip {
			continue
		}
		ib := ibs[i]

		settings := map[string]any{}
		for k, v := range ib.Settings {
			settings[k] = v
		}
		// sniffing / allocate 是 xray 入站顶层字段，只对 xray 生效
		if core == model.CoreXray {
			for k, v := range ib.PassThrough {
				settings[k] = v
			}
		}
		// 内联 PEM → 落盘成文件（面板与 sing-box 都只认文件路径）
		if err := a.materializePEM(ib.Name, ib.Port, settings); err != nil {
			failed = append(failed, ib.Name+": 内联证书落盘失败: "+err.Error())
			continue
		}
		raw, err := json.Marshal(settings)
		if err != nil {
			failed = append(failed, ib.Name+": settings 序列化失败")
			continue
		}
		name := uniqueName(ib.Name, usedName)
		tag := ""
		if ib.Tag != "" {
			tag = ib.Tag
			for n := 2; usedTag[tag]; n++ {
				tag = fmt.Sprintf("%s-%d", ib.Tag, n)
			}
			usedTag[tag] = true
		}

		in := &model.Inbound{
			Name:      name,
			Core:      core,
			Protocol:  ib.Protocol,
			Listen:    ib.Listen,
			Port:      ib.Port,
			Tag:       tag,
			Transport: ib.Transport,
			TLS:       ib.TLS,
			Settings:  string(raw),
			Enabled:   false, // 关键：导入不等于启用
			Draft:     false,
		}
		if err := a.Store.CreateInbound(in); err != nil {
			failed = append(failed, ib.Name+": "+err.Error())
			continue
		}
		p.ID = in.ID
		p.Name = name

		for _, c := range ib.Clients {
			sub := c.SubToken
			if sub == "" || usedSub[sub] {
				sub = token()
			}
			usedSub[sub] = true
			cl := &model.Client{
				Name:       uniqueClientName(c.Name, name),
				Credential: c.Credential,
				SubToken:   sub,
				QuotaGB:    c.QuotaGB,
				ExpiresAt:  c.ExpiresAt,
				Enabled:    c.Enabled,
			}
			if err := a.Store.CreateClient(cl); err != nil {
				log.Printf("import-json: 用户 %s 创建失败: %v", c.Name, err)
				continue
			}
			// 关联是「用户 → 入站」方向
			if err := a.Store.SetClientInbounds(cl.ID, []int64{in.ID}); err != nil {
				log.Printf("import-json: 关联用户失败: %v", err)
			}
		}
	}

	a.auditOK(r, AuditImport, "json-"+core,
		fmt.Sprintf("JSON 导入 %d 条（跳过 %d，失败 %d）", importable, summary["skipped"], len(failed)))
	writeJSON(w, 200, map[string]any{
		"dry_run": false,
		"core":    core,
		"items":   previews,
		"failed":  failed,
		"summary": summary,
		"global":  globalPreview,
	})
}

// ---- 全局段（DNS / 路由）----

// globalPlan 是一个内核下「全局段要怎么落库」的结果。
type globalPlan struct {
	DNS      map[string]any
	Route    map[string]any
	Warnings []string
}

func (p *globalPlan) ok() bool { return len(p.DNS) > 0 || len(p.Route) > 0 }

// planGlobal 按目标内核转换全局段。
//
// xray 直接吃原生格式；sing-box 需要转换（xray 的 dns/routing 结构与 sing-box
// 完全不同）；mihomo 的规则体系是「代理组 + 规则集」，无法从 xray 规则安全推导，
// 只提示不导入。
func planGlobal(core string, g *importer.Global) *globalPlan {
	if g == nil {
		return nil
	}
	switch core {
	case model.CoreXray:
		return &globalPlan{DNS: g.DNS, Route: g.Routing, Warnings: g.Warnings}
	case model.CoreSingBox:
		dns, w1 := importer.ToSingboxDNS(g.DNS)
		route, w2 := importer.ToSingboxRoute(g.Routing)
		warns := append(append([]string{}, g.Warnings...), w1...)
		warns = append(warns, w2...)
		return &globalPlan{DNS: dns, Route: route, Warnings: warns}
	default:
		return &globalPlan{Warnings: append(append([]string{}, g.Warnings...),
			"mihomo 的 DNS/路由是另一套结构（nameserver + 代理组规则），未导入，请手工配置")}
	}
}

// saveGlobal 写入全局段设置（每个内核一份）。
func (a *Server) saveGlobal(core string, p *globalPlan) error {
	if len(p.DNS) > 0 {
		b, err := json.Marshal(p.DNS)
		if err != nil {
			return err
		}
		if err := a.Store.SetSetting(core+"_dns_json", string(b)); err != nil {
			return err
		}
	}
	if len(p.Route) > 0 {
		b, err := json.Marshal(p.Route)
		if err != nil {
			return err
		}
		if err := a.Store.SetSetting(core+"_route_json", string(b)); err != nil {
			return err
		}
	}
	return nil
}

// countList 数一下某字段里的条目数（nil 安全）。
func countList(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	if arr, ok := m[key].([]any); ok {
		return len(arr)
	}
	return 0
}

// materializePEM 把内联 PEM 证书落盘成文件，返回写好的路径。
//
// 面板与 sing-box 都按文件路径工作（sing-box 根本不支持内联 PEM），
// 所以导入时统一转成文件：data/certs/imported/<name>-<port>/{cert,key}.pem。
// 私钥文件权限 0600 —— 它和面板数据库里的密码同等敏感。
func (a *Server) materializePEM(name string, port int, settings map[string]any) error {
	pem, _ := settings["certificate_pem"].(string)
	key, _ := settings["key_pem"].(string)
	if pem == "" && key == "" {
		return nil
	}
	if _, ok := settings["certificate_path"]; ok {
		return nil // 已经有文件路径，以文件为准
	}
	slug := sanitizePathPart(name)
	if slug == "" {
		slug = "imported"
	}
	dir := filepath.Join(a.DataDir, "certs", "imported", fmt.Sprintf("%s-%d", slug, port))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if pem != "" {
		p := filepath.Join(dir, "fullchain.pem")
		if err := os.WriteFile(p, []byte(pem), 0o644); err != nil {
			return err
		}
		settings["certificate_path"] = p
	}
	if key != "" {
		p := filepath.Join(dir, "privkey.pem")
		if err := os.WriteFile(p, []byte(key), 0o600); err != nil {
			return err
		}
		settings["key_path"] = p
	}
	// 落盘后就不再需要把 PEM 留在 settings 里（避免同一份密钥存两处）
	delete(settings, "certificate_pem")
	delete(settings, "key_pem")
	return nil
}

// materializeInboundPEM 把入站 settings 里的内联 PEM 落盘并换成文件路径。
// create/update 都会调用，这样 UI 里直接粘贴 PEM 也能用（sing-box 只认文件路径）。
func (a *Server) materializeInboundPEM(in *model.Inbound) error {
	if in.Settings == "" || !strings.Contains(in.Settings, "_pem") {
		return nil
	}
	var s map[string]any
	if err := json.Unmarshal([]byte(in.Settings), &s); err != nil {
		return nil // settings 本身非法，交给后续渲染报错
	}
	if s["certificate_pem"] == nil && s["key_pem"] == nil {
		return nil
	}
	if err := a.materializePEM(in.Name, in.Port, s); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	in.Settings = string(b)
	return nil
}

// sanitizePathPart 只保留文件名安全字符，避免入站名里的 / 或 .. 逃出目录。
func sanitizePathPart(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ' || r == '.':
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

// uniqueName 处理重名：3x-ui 里的备注可以重复，面板按名字认人会很痛苦。
func uniqueName(name string, used map[string]bool) string {
	if !used[name] && name != "" {
		used[name] = true
		return name
	}
	for n := 2; ; n++ {
		cand := fmt.Sprintf("%s (%d)", name, n)
		if !used[cand] {
			used[cand] = true
			return cand
		}
	}
}

// uniqueClientName 用户名的归属是入站，重名不算错，但加个后缀更好认。
func uniqueClientName(name, inbound string) string {
	if name == "" {
		return inbound + "-user"
	}
	return name
}

// maskCredential 预览时打码：截断一半 + 长度提示。
// 预览内容常被截图发群里，明文 UUID/密码没必要整段露出来。
func maskCredential(s string) string {
	if s == "" {
		return ""
	}
	if i := strings.Index(s, ":"); i > 0 && i < len(s)-1 {
		// user:pass 形态（socks/http）
		return s[:min(i, 4)] + "…:***"
	}
	n := len(s)
	if n <= 8 {
		return strings.Repeat("*", n)
	}
	return s[:6] + "…" + fmt.Sprintf("(%d 字符)", n)
}
