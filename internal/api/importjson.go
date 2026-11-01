package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
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
	model.CoreSingBox: {"vless", "vmess", "trojan", "shadowsocks", "hysteria2", "tuic", "anytls", "mixed", "socks"},
	model.CoreXray:    {"vless", "vmess", "trojan", "shadowsocks"},
	model.CoreMihomo:  {"mixed", "socks", "http", "shadowsocks", "vmess", "trojan", "tuic", "hysteria2"},
}

func coreSupportsInbound(core, protocol string) bool {
	for _, p := range importCoreProtocols[core] {
		if p == protocol {
			return true
		}
	}
	return false
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

	ibs, err := importer.Parse(req.Text)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}

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
	if req.DryRun {
		writeJSON(w, 200, map[string]any{
			"dry_run": true,
			"core":    core,
			"items":   previews,
			"summary": summary,
		})
		return
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
	})
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
