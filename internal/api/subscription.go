package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"triune/internal/core"
	"triune/internal/link"
	"triune/internal/model"

	"gopkg.in/yaml.v3"
)

// handleSub 订阅端点：GET /sub/{token}
// target 参数：留空=base64 链接列表；clash=Clash/mihomo YAML；singbox=sing-box outbounds JSON
func (a *Server) handleSub(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	client, err := a.Store.GetClientByToken(tok)
	if err != nil {
		http.Error(w, "subscription not found", 404)
		return
	}
	if !client.Enabled {
		http.Error(w, "subscription disabled", 403)
		return
	}
	// 已过期？
	if client.ExpiresAt != nil && client.ExpiresAt.Before(timeNow()) {
		http.Error(w, "subscription expired", 403)
		return
	}

	// 收集该用户的节点
	var nodes []*link.Node

	inboundIDs, _ := a.Store.ClientInboundIDs(client.ID)
	byID := map[int64]*model.Inbound{}
	ins, _ := a.Store.ListInbounds("")
	for _, in := range ins {
		byID[in.ID] = in
	}
	host := a.publicHost(r)
	for _, id := range inboundIDs {
		in, ok := byID[id]
		if !ok || !in.Enabled || in.Draft {
			continue
		}
		nodes = append(nodes, a.inboundToNode(in, client, host))
	}
	ext, _ := a.Store.ListExternalNodes()
	for _, e := range ext {
		if n, err := link.Parse(e.RawLink); err == nil {
			if n.Name == "" {
				n.Name = e.Name
			}
			nodes = append(nodes, n)
		}
	}

	target := r.URL.Query().Get("target")
	w.Header().Set("profile-update-interval", "12")
	// 真实用量（流量统计开启时）
	var up, down int64
	if traffic, err := a.Store.GetTrafficAll(); err == nil {
		if t, ok := traffic[client.ID]; ok {
			up, down = t.UpBytes, t.DownBytes
		}
	}
	w.Header().Set("subscription-userinfo",
		fmt.Sprintf("upload=%d; download=%d; total=%d; expire=%d",
			up, down, int64(client.QuotaGB*1024*1024*1024), expireUnix(client.ExpiresAt)))

	switch target {
	case "clash":
		proxies := []any{}
		for _, n := range nodes {
			if p, err := link.ToClash(n); err == nil {
				proxies = append(proxies, p)
			}
		}
		doc := map[string]any{
			"port":        7890,
			"socks-port":  7891,
			"allow-lan":   false,
			"mode":        "rule",
			"log-level":   "info",
			"proxy-groups": []any{map[string]any{
				"name":    "PROXY",
				"type":    "select",
				"proxies": proxyNames(nodes),
			}},
			"rules":   []any{"MATCH,PROXY"},
			"proxies": proxies,
		}
		out, _ := yaml.Marshal(doc)
		w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
		_, _ = w.Write(out)
	case "singbox":
		outbounds := []any{}
		for _, n := range nodes {
			if o, err := link.ToSingboxOutbound(n); err == nil {
				outbounds = append(outbounds, o)
			}
		}
		doc := map[string]any{
			"outbounds": append(outbounds, map[string]any{"type": "direct", "tag": "direct"}),
		}
		out, _ := json.MarshalIndent(doc, "", "  ")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(out)
	default:
		var lines []string
		for _, n := range nodes {
			if l := link.GenLink(n); l != "" {
				lines = append(lines, l)
			}
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString([]byte(strings.Join(lines, "\n")))))
	}
}

// inboundToNode 把“入站+用户”转为可分享的节点（反向出口）。
func (a *Server) inboundToNode(in *model.Inbound, c *model.Client, host string) *link.Node {
	n := &link.Node{
		Protocol: in.Protocol,
		Name:     in.Name,
		Host:     host,
		Port:     in.Port,
		Cred:     c.Credential,
		Network:  in.Transport,
		Security: in.TLS,
		SNI:      core.Field(in.Settings, "server_name"),
		Path:     core.Field(in.Settings, "path"),
		Extra:    map[string]any{},
	}
	for _, k := range []string{"flow", "pbk", "sid", "serviceName", "method"} {
		if v := core.Field(in.Settings, k); v != "" {
			n.Extra[k] = v
		}
	}
	if n.Security == "" {
		n.Security = "none"
	}
	return n
}

func (a *Server) publicHost(r *http.Request) string {
	if h, _ := a.Store.GetSetting("public_host"); h != "" {
		return h
	}
	return r.Host
}

func proxyNames(nodes []*link.Node) []string {
	var out []string
	for _, n := range nodes {
		if n.Name != "" {
			out = append(out, n.Name)
		}
	}
	return out
}
