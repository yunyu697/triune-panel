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

	// 输出格式：显式 target 优先，没给就按 User-Agent 猜。
	//
	// 只给 base64 链接列表时，Clash/mihomo 系客户端会**整条订阅都拉不动**
	// （它只吃 YAML），而面板界面上给出的订阅地址就是这个不带参数的 URL。
	// 于是「把订阅地址填进 Clash Verge → 拉取失败 → 看起来像节点不通」。
	// 猜 UA 是各家订阅服务的通行做法，也让那一个地址对所有客户端都可用。
	target := r.URL.Query().Get("target")
	if target == "" {
		target = targetFromUA(r.UserAgent())
	}
	// 节点名在客户端配置里就是 tag / proxy name，重名会被内核判死。
	// 入站名不要求唯一，两个「香港-01」就会撞，见 dedupeNodeNames。
	dedupeNodeNames(nodes)
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
		// 只把**真正导出成功**的节点写进 proxies，proxy-group 也从同一批里取名字。
		// 分开算过一次的话，group 里会挂着 proxies 中不存在的名字，
		// mihomo 直接报 `proxy 'xxx' not found`。
		var exported []*link.Node
		proxies := []any{}
		for _, n := range nodes {
			p, err := link.ToClash(n)
			if err != nil {
				continue
			}
			proxies = append(proxies, p)
			exported = append(exported, n)
		}
		if len(proxies) == 0 {
			// 返回一份 proxies 为空的 YAML 等于让客户端自己去撞墙，
			// 不如说清楚：这些协议压根没有 clash 侧的 proxy 类型。
			skipped, _ := splitExport(nodes, "mihomo")
			http.Error(w, noExportMsg(skipped, "mihomo"), 422)
			return
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
				"proxies": proxyNames(exported),
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
		if len(outbounds) == 0 {
			skipped, _ := splitExport(nodes, "singbox")
			http.Error(w, noExportMsg(skipped, "singbox"), 422)
			return
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

// targetFromUA 按 User-Agent 判断客户端想要的订阅格式。
//
// 只认「确定要 YAML / JSON」的客户端，其它（浏览器、v2rayN、Shadowrocket、
// NekoBox 这些吃 base64 链接列表的）一律返回空串，走默认的链接列表——
// 猜错方向比不猜更糟：给 v2rayN 塞一份 YAML，它同样拉不动。
func targetFromUA(ua string) string {
	s := strings.ToLower(ua)
	switch {
	// sing-box 内核自身与使用它的图形客户端
	case strings.Contains(s, "sing-box"), strings.Contains(s, "singbox"):
		return "singbox"
	// Clash / mihomo 系：clash-verge、clashx、FlClash、mihomo 本体、stash
	case strings.Contains(s, "clash"), strings.Contains(s, "mihomo"), strings.Contains(s, "stash"):
		return "clash"
	}
	return ""
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
//
// 这里最容易错的地方是**键名与渲染器不对称**：表单/导入写进 settings 的是
// `short_ids`（数组）和 `server_names`（可能是数组也可能是逗号串），而分享链接
// 要的是单值 `sid` / `sni`。以前只读 `sid` / `server_name`，于是面板上明明填了
// 短 ID 和 SNI，导出的节点里两者都是空的——服务端 shortIds=["ab12"] 只认 ab12，
// 客户端发空 short-id，REALITY 握手必然失败。表现就是"建完节点连不上"，
// 而同机器上 3x-ui 的节点（链接里 sid/sni 齐全）是通的。
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
	for _, k := range []string{"flow", "pbk", "sid", "serviceName", "method", "fp"} {
		if v := core.Field(in.Settings, k); v != "" {
			n.Extra[k] = v
		}
	}
	// ws 的 Host 头（CDN 前置时必须原样带给客户端）
	if h := core.Field(in.Settings, "ws_host"); h != "" {
		n.Extra["host"] = h
	}
	// REALITY 公钥兼容多种写法：pbk / public_key / publicKey。
	// 注意：Extra 取不到时是 nil（interface），不能直接与 "" 比较。
	if pbk, _ := n.Extra["pbk"].(string); pbk == "" && in.TLS == "reality" {
		for _, k := range []string{"public_key", "publicKey", "pbk"} {
			if v := core.Field(in.Settings, k); v != "" {
				n.Extra["pbk"] = v
				break
			}
		}
	}
	if in.TLS == "reality" {
		a.completeRealityNode(n, in)
	}
	if n.Security == "" {
		n.Security = "none"
	}
	return n
}

// completeRealityNode 把 REALITY 入站缺的客户端参数补齐。
//
// 三个来源与三个兜底，缺任何一个客户端都连不上，而失败现象完全一样
// （握手超时 / 连接被重置），在客户端侧看不出是哪一项的问题：
//
//	SNI    : server_name → server_names 第一个  → 握手目标的主机名
//	短 ID  : sid → short_ids 里第一个非空的      → 空串（服务端 shortIds 含 "" 时合法）
//	公钥   : 一律由 private_key 现算（settings 里的 pbk 可能是旧值，不采信）
//
// 公钥现算是关键：私钥本来就在库里（服务端要用），X25519 的公钥由私钥唯一确定，
// 所以"只填了私钥、没点生成公钥"的入站也能导出可用的节点，而不是发一个
// 永远握不上手的链接出去。
func (a *Server) completeRealityNode(n *link.Node, in *model.Inbound) {
	if n.SNI == "" {
		if names := core.FieldList(in.Settings, "server_names"); len(names) > 0 {
			n.SNI = names[0]
		}
	}
	if n.SNI == "" {
		// 与渲染器的兜底对称：缺 serverNames 时 xray 用 dest 的主机名
		if dest := core.RealityDest(in); dest != "" {
			if h, _, err := net.SplitHostPort(dest); err == nil {
				n.SNI = h
			} else {
				n.SNI = dest
			}
		}
	}
	if sid, _ := n.Extra["sid"].(string); sid == "" {
		for _, s := range core.ShortIDs(in.Settings) {
			if s != "" {
				n.Extra["sid"] = s
				break
			}
		}
	}
	// 公钥一律以私钥为准，**不用** settings 里存的 pbk。
	//
	// X25519 的公钥由私钥唯一确定，而存下来的 pbk 可能是上一代密钥的残留：
	// 换过密钥对没同步、从 3x-ui 导入时带了别的公钥、或先填 pbk 后改私钥。
	// 以前只在 pbk 为空时才现算，于是「私钥换了、pbk 没跟着换」的入站会导出
	// 一份 SNI/短 ID 全对、只有公钥是错的配置——客户端报
	// REALITY authentication failed，服务端日志一切正常，两边都看不出问题。
	// 私钥本来就存在库里（服务端要用），以它为准不可能更差。
	if priv := core.Field(in.Settings, "private_key"); priv != "" {
		if pub, err := realityPubFromPriv(priv); err == nil {
			n.Extra["pbk"] = pub
		}
	}
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

// isLoopbackHost 判断导出的节点地址是不是「只有本机能用」。
// 空串、localhost、127.0.0.0/8、::1、0.0.0.0 都算——拿到别的机器上必然连不通。
func isLoopbackHost(h string) bool {
	h = strings.TrimSpace(h)
	if h == "" || strings.EqualFold(h, "localhost") {
		return true
	}
	if ip := net.ParseIP(strings.Trim(h, "[]")); ip != nil {
		return ip.IsLoopback() || ip.IsUnspecified()
	}
	return false
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
