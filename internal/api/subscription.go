package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
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
		// shadowsocks 的服务端密码是入站级单值：xray 只有 2022 系列方法支持
		// 多用户（且凭据必须是 base64 密钥），sing-box 同样只认入站级 password。
		// 渲染时取的是**首个启用用户**，所以其余用户拿到的节点密码必然对不上。
		// 与其发一个永远连不上的配置让人排查半天，不如不发。
		if in.Protocol == "shadowsocks" && !a.isEffectiveSSClient(in.ID, client.ID) {
			continue
		}
		nodes = append(nodes, a.inboundToNode(in, client, host))
	}
	// 外部节点是**全局**的：一旦混进订阅，每个用户的订阅里都会冒出这些节点，
	// 而它们既不受本机配额约束、也不经过本机内核（是别人的服务器）。
	// 所以默认**不混**——只有明确要"把机场节点并进自己的订阅"时才在
	// 「外部节点」页打开开关，见 externalInSub。
	if a.externalInSub() {
		ext, _ := a.Store.ListExternalNodes()
		for _, e := range ext {
			if n, err := link.Parse(e.RawLink); err == nil {
				if n.Name == "" {
					n.Name = e.Name
				}
				nodes = append(nodes, n)
			}
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
			"port":       7890,
			"socks-port": 7891,
			"allow-lan":  false,
			"mode":       "rule",
			"log-level":  "info",
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

// isEffectiveSSClient 判断该用户是不是这个 shadowsocks 入站真正生效的那个。
//
// 判定顺序必须与渲染器一致：渲染器取「首个启用用户」的凭据写入入站级 password，
// 而它遍历的也是 InboundClientIDs（已按 client_id 排序）。两边取到同一个人，
// 这个函数才有意义。
func (a *Server) isEffectiveSSClient(inboundID, clientID int64) bool {
	ids, err := a.Store.InboundClientIDs(inboundID)
	if err != nil {
		return true // 查不到就不下结论，宁可多发一个节点也不要把人整个订阅清空
	}
	for _, id := range ids {
		c, err := a.Store.GetClient(id)
		if err != nil || !c.Enabled {
			continue
		}
		return c.ID == clientID
	}
	return false
}

// externalInSub 外部节点是否混入订阅输出。
// 默认关闭：外部节点是全局的，混进去等于给**每个**用户的订阅都塞上
// 别人的节点，而这些节点既不受本机配额约束、也不经过本机内核。
// 只有明确做「订阅聚合」时才打开。
func (a *Server) externalInSub() bool {
	v, _ := a.Store.GetSetting(externalInSubSetting)
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
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
	// Reality 公钥兼容多种写法：pbk / public_key / publicKey
	// 注意：Extra 取不到时是 nil（interface），不能直接与 "" 比较
	if pbk, _ := n.Extra["pbk"].(string); pbk == "" && in.TLS == "reality" {
		for _, k := range []string{"public_key", "publicKey", "pbk"} {
			if v := core.Field(in.Settings, k); v != "" {
				n.Extra["pbk"] = v
				break
			}
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
	// 回退：用请求 Host 但剥掉面板端口——节点端口由入站配置决定
	h := r.Host
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return h
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
