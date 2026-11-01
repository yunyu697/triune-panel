package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"triune/internal/importer"
	"triune/internal/model"
)

// JSON → 入站表单：只解析「入站本身」。
//
// 与 /api/inbounds/import-json（批量写库）刻意分工不同：
//   - 那个是「一次搬一批」，解析完直接落库，用户看不到中间过程；
//   - 这个是「把一条入站填进新增入站弹窗」，解析结果原样交给前端逐项回填，
//     每个字段用户都看得见、改得动，确认无误再自己点保存。
//
// 只解析入站是刻意的：单条入站没有 DNS / 路由这类全局段，混在一起只会让
// 「填表」变成「猜用户想要哪一半」。整份配置请继续用批量导入接口。

// fillClient 回填到表单里的一个用户。
type fillClient struct {
	Name       string  `json:"name"`
	Credential string  `json:"credential"`
	Kind       string  `json:"kind,omitempty"` // UUID / 密码 / 用户名:密码
	QuotaGB    float64 `json:"quota_gb"`
	ExpiresAt  string  `json:"expires_at,omitempty"` // RFC3339，前端原样回传
	// Auto 表示 JSON 里没有这个用户，是面板现生成的。
	Auto bool `json:"auto,omitempty"`
}

// fillItem 一条可直接回填表单的入站。
type fillItem struct {
	Name      string         `json:"name"`
	Protocol  string         `json:"protocol"`
	Listen    string         `json:"listen"`
	Port      int            `json:"port"`
	Transport string         `json:"transport"`
	TLS       string         `json:"tls"`
	Settings  map[string]any `json:"settings"`
	Clients   []fillClient   `json:"clients"`
	Warnings  []string       `json:"warnings,omitempty"`
	// Skip 表示这条填不进表单（协议不支持 / 端口非法），原因在 Warnings。
	Skip bool `json:"skip"`
}

// POST /api/inbounds/parse-json
//
//	{"text":"{...}","core":"xray"}
//
// 返回 items（可多选，前端让用户挑一条填入）；core 只影响「能不能落地」
// 的适配（如 xray 26 的 h2 → xhttp），不改变解析出的协议语义。
func (a *Server) handleParseJSON(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string `json:"text"`
		Core string `json:"core"`
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
		core = model.CoreXray // 3x-ui 是 xray 面板，默认按 xray
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
	// 3x-ui 全量备份里常混着一堆「特殊入站」（面板管理口、订阅口）。
	// 全塞进选择列表只会让人挨个点开找，先按端口/协议排个序更好用。
	items := make([]fillItem, 0, len(res.Inbounds))
	for _, ib := range res.Inbounds {
		items = append(items, fillOne(core, ib))
	}
	writeJSON(w, 200, map[string]any{
		"core":   core,
		"source": res.Source,
		"items":  items,
		// 全局段只提示存在、不导入：这个接口的职责就是「填单条入站」。
		"global_ignored": globalIgnoredNote(res.Global),
	})
}

// fillOne 把一条解析结果整理成表单可直接吃的形态。
func fillOne(core string, ib *importer.Inbound) fillItem {
	it := fillItem{
		Name:      ib.Name,
		Protocol:  ib.Protocol,
		Listen:    ib.Listen,
		Port:      ib.Port,
		Transport: ib.Transport,
		TLS:       ib.TLS,
		Warnings:  append([]string{}, ib.Warnings...),
	}
	if ib.Skip {
		it.Skip = true
		return it
	}
	// 目标内核的适配（h2/裸 quic → xhttp、dokodemo-door → direct 等）
	it.Warnings = append(it.Warnings, adaptToCore(core, ib)...)
	it.Transport, it.TLS = ib.Transport, ib.TLS
	if !coreSupportsInbound(core, ib.Protocol) {
		it.Skip = true
		it.Warnings = append(it.Warnings,
			fmt.Sprintf("%s 内核不支持 %s 协议，请换内核后再填", core, ib.Protocol))
		return it
	}

	settings := map[string]any{}
	for k, v := range ib.Settings {
		settings[k] = v
	}
	// sniffing / allocate 是 xray 入站顶层字段，只对 xray 有效
	if core == model.CoreXray {
		for k, v := range ib.PassThrough {
			settings[k] = v
		}
	}
	it.Settings = settings
	it.Clients = fillClients(ib, settings)
	if len(it.Clients) > 0 {
		// 解析器会提醒「这条入站没有客户端」，但填表流程会自动补一个用户，
		// 留着这句话只会让人以为还得手动加人——实际保存就有。
		it.Warnings = dropWarnings(it.Warnings, "没有客户端")
	}
	return it
}

// dropWarnings 去掉包含 substr 的提示（nil 安全）。
func dropWarnings(ws []string, substr string) []string {
	out := ws[:0]
	for _, w := range ws {
		if strings.Contains(w, substr) {
			continue
		}
		out = append(out, w)
	}
	return out
}

// fillClients 决定「保存时建哪几个用户」。
//
// 这是用户明确要的那条：JSON 里带 uuid 就照搬，没有就现生成一个——
// 否则用户填完表单点保存，会得到一个谁都连不上的空入站。
func fillClients(ib *importer.Inbound, settings map[string]any) []fillClient {
	out := make([]fillClient, 0, len(ib.Clients)+1)
	seen := map[string]bool{}
	kind := credentialKind(ib.Protocol, settings)
	for _, c := range ib.Clients {
		cred := strings.TrimSpace(c.Credential)
		auto := false
		if cred == "" {
			// 有用户条目但没凭据（3x-ui 里手工加的空用户）：补一个
			cred, _ = generateCredential(ib.Protocol, settings)
			auto = true
		}
		if cred == "" || seen[cred] {
			continue // 不用用户，或同一份 JSON 里重复的凭据
		}
		seen[cred] = true
		fc := fillClient{Name: c.Name, Credential: cred, Kind: kind, QuotaGB: c.QuotaGB, Auto: auto}
		if c.ExpiresAt != nil {
			fc.ExpiresAt = c.ExpiresAt.Format(time.RFC3339)
		}
		out = append(out, fc)
	}
	if len(out) > 0 {
		return out
	}
	// 一个用户都没有：能建就建一个（端口转发 / 免认证代理本来就不需要用户）
	if cred, kl := generateCredential(ib.Protocol, settings); cred != "" {
		out = append(out, fillClient{
			Name:       defaultUserName(ib),
			Credential: cred,
			Kind:       kl,
			Auto:       true,
		})
	}
	return out
}

// credentialKind 描述该协议的用户凭据是什么，纯粹给前端显示用。
func credentialKind(protocol string, settings map[string]any) string {
	switch protocol {
	case "vless", "vmess", "tuic":
		return "UUID"
	case "trojan", "shadowsocks", "hysteria2", "anytls":
		return "密码"
	case "socks", "http", "mixed":
		if settingString(settings, "account_auth") == "password" {
			return "用户名:密码"
		}
	}
	return ""
}

// generateCredential 按协议生成一份可用的新凭据。
//
// 凭据形态不能一刀切用 UUID：trojan / shadowsocks 要的是密码，socks/http
// 要的是 user:pass，塞个 UUID 进去内核会直接拒绝启动。
func generateCredential(protocol string, settings map[string]any) (string, string) {
	switch protocol {
	case "vless", "vmess", "tuic":
		return uuid4(), "UUID"
	case "trojan", "shadowsocks", "hysteria2", "anytls":
		return randSecret(12), "密码"
	case "socks", "http", "mixed":
		// 免认证的本地代理不需要用户；只有开了密码认证才建
		if settingString(settings, "account_auth") == "password" {
			return "u" + randSecret(3) + ":" + randSecret(12), "用户名:密码"
		}
	}
	return "", ""
}

// defaultUserName 自动用户的默认名字：带上是哪条入站的，后面好认。
func defaultUserName(ib *importer.Inbound) string {
	base := strings.TrimSpace(ib.Name)
	if base == "" {
		base = fmt.Sprintf("%s-%d", ib.Protocol, ib.Port)
	}
	return base + "-user1"
}

// randSecret 生成 n 字节随机数的十六进制串（2n 个字符）。
func randSecret(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// settingString 读 settings 里的字符串值（表单里也可能是数字/布尔）。
func settingString(m map[string]any, key string) string {
	switch v := m[key].(type) {
	case string:
		return strings.ToLower(strings.TrimSpace(v))
	case bool:
		if v {
			return "true"
		}
		return "false"
	}
	return ""
}

// globalIgnoredNote 说明「JSON 里还有全局段，但这里不导入」。
// 不说明的话，用户会以为整份配置都搬过来了。
func globalIgnoredNote(g *importer.Global) string {
	if g == nil {
		return ""
	}
	var parts []string
	if n := lenList(g.DNS, "servers"); n > 0 {
		parts = append(parts, fmt.Sprintf("%d 个 DNS 服务器", n))
	}
	if n := lenList(g.Routing, "rules"); n > 0 {
		parts = append(parts, fmt.Sprintf("%d 条路由规则", n))
	}
	if len(parts) == 0 {
		return ""
	}
	return "JSON 里还含 " + strings.Join(parts, "、") + "，这里只填单条入站，全局段未导入（需要的话用批量导入接口）"
}

func lenList(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	if arr, ok := m[key].([]any); ok {
		return len(arr)
	}
	return 0
}
