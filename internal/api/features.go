package api

// v0.8.5 功能补充：
//   - 订阅 token 重置（POST /api/clients/{id}/reset-token）
//   - 订阅 URL 聚合（POST /api/external-nodes/import-url）：
//     拉取远端订阅（base64 链接列表 / 明文链接 / Clash YAML），解析为外部节点，
//     仅聚合进本面板订阅输出，不生成本地任何配置。

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"triune/internal/link"
	"triune/internal/model"

	"gopkg.in/yaml.v3"
)

// POST /api/clients/{id}/reset-token
func (a *Server) resetSubToken(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	cs, err := a.Store.ListClients()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	var target *model.Client
	for _, c := range cs {
		if c.ID == id {
			target = c
			break
		}
	}
	if target == nil {
		fail(w, 404, "用户不存在")
		return
	}
	target.SubToken = token()
	if err := a.Store.UpdateClient(target); err != nil {
		fail(w, 500, err.Error())
		return
	}
	a.auditOK(r, AuditClientToken, target.Name, fmt.Sprintf("重置用户 #%d 的订阅令牌（旧链接失效）", target.ID))
	writeJSON(w, 200, map[string]any{"ok": "1", "sub_token": target.SubToken,
		"msg": "订阅令牌已重置，旧链接立即失效"})
}

// POST /api/external-nodes/import-url {"url":"...","prefix":""}
func (a *Server) importSubURL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL    string `json:"url"`
		Prefix string `json:"prefix"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.URL) == "" {
		fail(w, 400, "需要 url 字段")
		return
	}
	client := &http.Client{Timeout: 20 * time.Second}
	req2, err := http.NewRequestWithContext(r.Context(), "GET", req.URL, nil)
	if err != nil {
		fail(w, 400, "URL 无效: "+err.Error())
		return
	}
	// 模拟客户端 UA：部分订阅后端按 UA 返回不同格式
	req2.Header.Set("User-Agent", "clash-meta/1.19.0")
	resp, err := client.Do(req2)
	if err != nil {
		fail(w, 502, "拉取失败: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		fail(w, 502, fmt.Sprintf("拉取失败: HTTP %d", resp.StatusCode))
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		fail(w, 502, "读取失败: "+err.Error())
		return
	}
	// Clash YAML 单独处理；命中后不再按链接逐行解析（避免把 YAML 行误当链接产生噪音报错）
	clashProxies, isClash := extractClashProxies(string(body))
	text := string(body)
	if isClash {
		text = ""
	} else {
		text = decodeSubBody(string(body))
	}
	if strings.TrimSpace(text) == "" && !isClash {
		fail(w, 422, "订阅内容为空或无法解析（支持 base64 链接列表、明文分享链接、Clash YAML）")
		return
	}

	nodes, errs := link.ParseMany(text)
	if isClash {
		for i, pm := range clashProxies {
			n, cerr := link.FromClash(pm)
			if cerr != nil {
				errs = append(errs, fmt.Errorf("clash proxy #%d: %v", i+1, cerr))
				continue
			}
			nodes = append(nodes, n)
		}
	}
	if len(nodes) == 0 {
		fail(w, 422, "没有可解析的节点: "+joinErrs(errs))
		return
	}

	prefix := strings.TrimSpace(req.Prefix)
	existing, _ := a.Store.ListExternalNodes()
	seen := map[string]bool{}
	for _, e := range existing {
		seen[e.RawLink] = true
	}
	var created, skipped, failed []string
	for _, n := range nodes {
		if n.Raw == "" {
			if l := link.GenLink(n); l != "" {
				n.Raw = l
			} else {
				failed = append(failed, n.Name+": 无法生成链接")
				continue
			}
		}
		if seen[n.Raw] {
			skipped = append(skipped, n.Name+"（已存在）")
			continue
		}
		seen[n.Raw] = true
		name := n.Name
		if prefix != "" {
			name = prefix + "-" + name
		}
		en := &model.ExternalNode{Name: orDefault(name, n.Host), RawLink: n.Raw}
		if err := a.Store.CreateExternalNode(en); err != nil {
			failed = append(failed, n.Name+": "+err.Error())
			continue
		}
		created = append(created, name+"（"+n.Protocol+"）")
	}
	writeJSON(w, 200, map[string]any{"created": created, "skipped": skipped, "failed": failed, "parse_errors": errStrings(errs)})
	if len(created) > 0 {
		a.auditOK(r, AuditExternal, req.URL,
			fmt.Sprintf("拉取远端订阅，新增 %d 个节点（跳过 %d，失败 %d）", len(created), len(skipped), len(failed)))
	}
}

// decodeSubBody 尽力把订阅响应还原成明文链接列表：
// 先试整段 base64（std/url，补齐 padding），失败则原样返回。
func decodeSubBody(body string) string {
	s := strings.TrimSpace(body)
	if s == "" {
		return ""
	}
	// 已经是明文链接
	if strings.HasPrefix(s, "vless://") || strings.HasPrefix(s, "vmess://") ||
		strings.HasPrefix(s, "trojan://") || strings.HasPrefix(s, "ss://") ||
		strings.HasPrefix(s, "hysteria2://") || strings.HasPrefix(s, "hysteria://") ||
		strings.HasPrefix(s, "tuic://") {
		return s
	}
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if data, err := enc.DecodeString(s); err == nil {
			out := string(data)
			if strings.Contains(out, "://") {
				return out
			}
		}
	}
	return s
}

// extractClashProxies 判断 body 是否为 Clash YAML 并抽取 proxies 数组。
func extractClashProxies(body string) ([]map[string]any, bool) {
	if !strings.Contains(body, "proxies:") {
		return nil, false
	}
	var doc struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil || len(doc.Proxies) == 0 {
		return nil, false
	}
	return doc.Proxies, true
}
