// Package xray 实现 Xray 内核适配器。
package xray

import (
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"

	"triune/internal/core"
	"triune/internal/model"
)

type Xray struct{}

func init() { core.Register(&Xray{}) }

func (x *Xray) Name() string     { return model.CoreXray }
func (x *Xray) Ext() string      { return "json" }
func (x *Xray) FileName() string { return "config.json" }

func (x *Xray) ValidateCmd(bin, cfg string) *exec.Cmd {
	return exec.Command(bin, "run", "-test", "-config", cfg)
}

func (x *Xray) Render(ins []core.InboundWithClients, opts core.RenderOpts) ([]byte, error) {
	doc := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
	}
	if opts.StatsEnabled {
		// 本地 dokodemo-door API 入站 + StatsService：
		// 面板采样器用 `xray api statsquery --server=127.0.0.1:10085` 读取每用户计数
		doc["stats"] = map[string]any{}
		doc["api"] = map[string]any{"tag": "api", "services": []string{"StatsService"}}
		doc["policy"] = map[string]any{
			"levels": map[string]any{"0": map[string]any{"statsUserUplink": true, "statsUserDownlink": true}},
			"system": map[string]any{"statsInboundUplink": true, "statsInboundDownlink": true},
		}
		doc["routing"] = map[string]any{"rules": []any{
			map[string]any{"type": "field", "inboundTag": []string{"api"}, "outboundTag": "api"},
		}}
		doc["inbounds"] = []any{
			map[string]any{
				"tag": "api", "listen": "127.0.0.1", "port": 10085,
				"protocol": "dokodemo-door", "settings": map[string]any{"address": "127.0.0.1"},
			},
		}
	}
	var inbounds []any
	for _, iw := range ins {
		ib, err := x.renderInbound(iw)
		if err != nil {
			return nil, fmt.Errorf("inbound %q: %w", iw.Inbound.Name, err)
		}
		inbounds = append(inbounds, ib)
	}
	if doc["inbounds"] != nil {
		existing := doc["inbounds"].([]any)
		doc["inbounds"] = append(existing, inbounds...)
	} else {
		if inbounds == nil {
			inbounds = []any{}
		}
		doc["inbounds"] = inbounds
	}
	doc["outbounds"] = x.outbounds(opts)
	x.applyGlobal(doc, opts)
	return json.MarshalIndent(doc, "", "  ")
}

// outbounds 组装出站：默认只有 direct；导入的路由里出现 block 时补 blackhole，
// 否则那条规则会因为「找不到出站」而让整份配置校验失败。
func (x *Xray) outbounds(opts core.RenderOpts) []any {
	out := []any{map[string]any{"protocol": "freedom", "tag": "direct"}}
	if x.routeNeedsBlock(opts.RouteJSON) {
		out = append(out, map[string]any{"protocol": "blackhole", "tag": "block"})
	}
	return out
}

// routeNeedsBlock 判断导入的路由规则是否引用了 block 出站。
func (x *Xray) routeNeedsBlock(routeJSON string) bool {
	if routeJSON == "" {
		return false
	}
	var r map[string]any
	if err := json.Unmarshal([]byte(routeJSON), &r); err != nil {
		return false
	}
	rules, _ := r["rules"].([]any)
	for _, it := range rules {
		if m, ok := it.(map[string]any); ok {
			if tag, _ := m["outboundTag"].(string); tag == "block" {
				return true
			}
		}
	}
	return false
}

// applyGlobal 把导入的 DNS / 路由段并进文档。
//
// 路由的规则**追加**在统计 API 规则之后（API 规则必须优先，否则面板读不到计数）；
// 其它字段（domainStrategy 等）以导入值为准。
func (x *Xray) applyGlobal(doc map[string]any, opts core.RenderOpts) {
	if opts.DNSJSON != "" {
		var d map[string]any
		if err := json.Unmarshal([]byte(opts.DNSJSON), &d); err == nil && len(d) > 0 {
			doc["dns"] = d
		}
	}
	if opts.RouteJSON == "" {
		return
	}
	var r map[string]any
	if err := json.Unmarshal([]byte(opts.RouteJSON), &r); err != nil || len(r) == 0 {
		return
	}
	imported, _ := r["rules"].([]any)
	delete(r, "rules")
	cur, ok := doc["routing"].(map[string]any)
	if !ok || cur == nil {
		cur = map[string]any{}
		doc["routing"] = cur
	}
	// 先把导入段自己的顶层字段（domainStrategy 等）并进来，
	// 但 **不能覆盖** 统计 API 已写好的 rules —— 规则单独追加。
	for k, v := range r {
		cur[k] = v
	}
	existing, _ := cur["rules"].([]any)
	if len(imported) > 0 {
		cur["rules"] = append(existing, imported...)
	} else if existing != nil {
		cur["rules"] = existing
	}
}

func (x *Xray) renderInbound(iw core.InboundWithClients) (map[string]any, error) {
	in := iw.Inbound
	obj := map[string]any{
		"tag":    core.TagOf(in),
		"listen": core.ListenOrAll(in.Listen),
		"port":   in.Port,
	}
	if in.Settings != "" && in.Settings != "{}" {
		var extra map[string]any
		if err := json.Unmarshal([]byte(in.Settings), &extra); err != nil {
			return nil, fmt.Errorf("invalid settings json: %w", err)
		}
		for k, v := range extra {
			obj[k] = v
		}
	}
	// 面板托管/客户端专用的键不透传到顶层（由下方结构化消费或剔除）
	core.StripManagedKeys(obj)

	switch in.Protocol {
	case "vless":
		obj["protocol"] = "vless"
		var clients []any
		for _, c := range iw.Clients {
			if !c.Enabled {
				continue
			}
			cl := map[string]any{"id": c.Credential, "email": c.Name}
			if flow := core.Field(in.Settings, "flow"); flow != "" {
				cl["flow"] = flow
			}
			clients = append(clients, cl)
		}
		vs := map[string]any{"clients": clients, "decryption": "none"}
		// fallbacks：3x-ui 里常用来把非代理流量回落给本地网站
		if fb := core.FieldRaw(in.Settings, "fallbacks"); fb != nil {
			if arr, ok := fb.([]any); ok && len(arr) > 0 {
				vs["fallbacks"] = arr
			}
		}
		obj["settings"] = vs
	case "vmess":
		obj["protocol"] = "vmess"
		var clients []any
		for _, c := range iw.Clients {
			if !c.Enabled {
				continue
			}
			clients = append(clients, map[string]any{"id": c.Credential, "email": c.Name, "alterId": 0})
		}
		obj["settings"] = map[string]any{"clients": clients}
	case "trojan":
		obj["protocol"] = "trojan"
		var clients []any
		for _, c := range iw.Clients {
			if !c.Enabled {
				continue
			}
			clients = append(clients, map[string]any{"password": c.Credential, "email": c.Name})
		}
		obj["settings"] = map[string]any{"clients": clients}
	case "shadowsocks":
		// xray 的 shadowsocks 服务端密码是入站级单值（2022 系列方法
		// 才支持多用户），与 sing-box 渲染保持一致：取首个有效用户。
		obj["protocol"] = "shadowsocks"
		method := core.Field(in.Settings, "method")
		if method == "" {
			method = "aes-256-gcm"
		}
		password := ""
		for _, c := range iw.Clients {
			if c.Enabled {
				password = c.Credential
				break
			}
		}
		ss := map[string]any{"method": method, "network": "tcp,udp"}
		if n := core.Field(in.Settings, "ss_network"); n != "" {
			ss["network"] = n // 3x-ui 里可以是 tcp / udp / tcp,udp
		}
		if password != "" {
			ss["password"] = password
		}
		obj["settings"] = ss
	case "socks", "http":
		// xray 的 socks/http 用 accounts（{user,pass}），不是 clients。
		// auth 必须显式给：不给 auth 时 xray 认为要密码但 accounts 为空，启动即报错。
		obj["protocol"] = in.Protocol
		var accounts []any
		for _, c := range iw.Clients {
			if !c.Enabled {
				continue
			}
			u, p := core.SplitUserPass(c.Credential, c.Name)
			accounts = append(accounts, map[string]any{"user": u, "pass": p})
		}
		as := map[string]any{"accounts": accounts}
		auth := core.Field(in.Settings, "account_auth")
		if auth == "" {
			if len(accounts) > 0 {
				auth = "password"
			} else {
				auth = "noauth"
			}
		}
		if auth == "noauth" {
			as["auth"] = "noauth"
			delete(as, "accounts")
		} else {
			as["auth"] = "password"
		}
		// udp 只有 socks 有；http 写了会被判未知字段
		if in.Protocol == "socks" && core.FieldInt(in.Settings, "udp") == 1 {
			as["udp"] = true
		}
		obj["settings"] = as
	case "dokodemo-door":
		// 端口转发：把本机端口转到 address:port。常用来把 CDN 回源、
		// 内网服务暴露出去，面板不需要「用户」概念。
		obj["protocol"] = "dokodemo-door"
		ds := map[string]any{
			"address": core.Field(in.Settings, "dd_address"),
			"port":    core.FieldInt(in.Settings, "dd_port"),
		}
		if ds["address"] == "" {
			return nil, fmt.Errorf("dokodemo-door 入站缺少目标地址（dd_address）")
		}
		if n := core.Field(in.Settings, "dd_network"); n != "" {
			ds["network"] = n
		}
		if core.FieldInt(in.Settings, "dd_follow_redirect") == 1 {
			ds["followRedirect"] = true
		}
		obj["settings"] = ds
	case "wireguard":
		// WireGuard 参数是一整块密钥 + peer 列表，面板不做结构化建模，
		// 导入时原样存在 xray_raw_settings 里，这里直接透传。
		obj["protocol"] = "wireguard"
		raw := core.FieldRaw(in.Settings, "xray_raw_settings")
		m, ok := raw.(map[string]any)
		if !ok || len(m) == 0 {
			return nil, fmt.Errorf("WireGuard 入站缺少原始 settings（secretKey/peers）")
		}
		clean := map[string]any{}
		for k, v := range m {
			clean[k] = v
		}
		for _, k := range core.ManagedSettingsKeys {
			delete(clean, k)
		}
		obj["settings"] = clean
	default:
		return nil, fmt.Errorf("protocol %q not supported by xray renderer", in.Protocol)
	}

	// streamSettings
	// 传输名与内核字段一一对应。写错 network 时 xray 只会在启动时抛
	// 「unknown transport protocol」，所以这里对未知值直接报错，不猜。
	stream := map[string]any{"network": "tcp"}
	switch tr := in.Transport; tr {
	case "", "tcp", "raw":
		// 默认
	case "ws":
		stream["network"] = "ws"
		ws := map[string]any{}
		if p := core.Field(in.Settings, "path"); p != "" {
			ws["path"] = p
		}
		// Host 头：CDN 前置时客户端要按它握手，服务端匹配同一值
		if h := core.Field(in.Settings, "ws_host"); h != "" {
			ws["headers"] = map[string]any{"Host": h}
		}
		stream["wsSettings"] = ws
	case "grpc":
		stream["network"] = "grpc"
		g := map[string]any{}
		if sn := core.Field(in.Settings, "service_name"); sn != "" {
			g["serviceName"] = sn
		}
		stream["grpcSettings"] = g
	case "h2":
		// Xray 26 起移除了独立 h2 传输（迁移到 XHTTP 的 stream-one 模式），
		// 硬写 network=h2 会直接启动失败。这里给出可执行的改法，不猜。
		return nil, fmt.Errorf("Xray 26 起已移除 h2 传输（合并到 XHTTP 的 stream-one 模式）：请把传输改成 xhttp、模式选 stream-one")
	case "quic":
		// 同理：裸 QUIC 传输也并入了 XHTTP（H3）。需要 TLS + ALPN h3。
		return nil, fmt.Errorf("Xray 26 起已移除裸 QUIC 传输（迁移到 XHTTP 的 stream-one + H3）：请把传输改成 xhttp、模式选 stream-one，并在 TLS 里把 ALPN 设为 h3")
	case "httpupgrade":
		stream["network"] = "httpupgrade"
		hu := map[string]any{}
		if p := core.Field(in.Settings, "path"); p != "" {
			hu["path"] = p
		}
		if h := core.Field(in.Settings, "ws_host"); h != "" {
			hu["host"] = h
		}
		stream["httpupgradeSettings"] = hu
	case "xhttp":
		stream["network"] = "xhttp"
		xh := map[string]any{}
		if p := core.Field(in.Settings, "path"); p != "" {
			xh["path"] = p
		}
		if h := core.Field(in.Settings, "ws_host"); h != "" {
			xh["host"] = h
		}
		if m := core.Field(in.Settings, "xhttp_mode"); m != "" {
			xh["mode"] = m
		}
		stream["xhttpSettings"] = xh
	case "kcp":
		stream["network"] = "kcp"
		k := map[string]any{}
		if sd := core.Field(in.Settings, "kcp_seed"); sd != "" {
			k["seed"] = sd
		}
		if h := core.Field(in.Settings, "kcp_header"); h != "" {
			k["header"] = map[string]any{"type": h}
		}
		stream["kcpSettings"] = k
	default:
		return nil, fmt.Errorf("xray 不支持传输 %q（可选 tcp/ws/grpc/httpupgrade/xhttp/kcp）", tr)
	}
	// PROXY protocol：前置了 nginx/haproxy 时需要，3x-ui 的 tcpSettings 会带
	if core.FieldInt(in.Settings, "accept_proxy_protocol") == 1 {
		stream["tcpSettings"] = map[string]any{"acceptProxyProtocol": true}
	}
	switch in.TLS {
	case "tls":
		stream["security"] = "tls"
		ts := map[string]any{}
		if sn := core.Field(in.Settings, "server_name"); sn != "" {
			ts["serverName"] = sn
		}
		if alpn := core.FieldList(in.Settings, "alpn"); len(alpn) > 0 {
			ts["alpn"] = alpn
		}
		if cp := core.Field(in.Settings, "certificate_path"); cp != "" {
			ts["certificates"] = []any{map[string]any{"certificateFile": cp, "keyFile": core.Field(in.Settings, "key_path")}}
		} else if pem := core.Field(in.Settings, "certificate_pem"); pem != "" {
			// 内联 PEM：xray 直接吃 PEM 文本数组，不需要落盘
			cert := map[string]any{"certificate": []string{pem}}
			if kp := core.Field(in.Settings, "key_pem"); kp != "" {
				cert["key"] = []string{kp}
			}
			ts["certificates"] = []any{cert}
		}
		stream["tlsSettings"] = ts
	case "reality":
		stream["security"] = "reality"
		priv := core.Field(in.Settings, "private_key")
		if priv == "" {
			return nil, fmt.Errorf("Reality 入站缺少服务端私钥（private_key）：请在入站编辑里点「生成密钥对」后保存")
		}
		rs := map[string]any{
			"privateKey": priv,
			"show":       false,
		}
		// xray 的 dest（新版本报错里叫 target）必须是 host:port，
		// 只写域名（面板表单的默认值就是裸域名）会被判为非法目标，
		// 表现为「切换内核预检失败：please fill in a valid value for "target"」。
		dest := core.Field(in.Settings, "handshake_server")
		if dest == "" {
			dest = core.Field(in.Settings, "server_name")
		}
		if dest != "" {
			port := core.FieldInt(in.Settings, "handshake_port")
			if port <= 0 || port > 65535 {
				port = 443
			}
			if _, _, err := net.SplitHostPort(dest); err != nil {
				dest = net.JoinHostPort(strings.Trim(dest, "[]"), strconv.Itoa(port))
			}
			rs["dest"] = dest
		}
		// serverNames 缺省回退：直接用握手目标的主机名，
		// 否则 xray 会认为 REALITY 配置不完整。
		names := core.FieldList(in.Settings, "server_names")
		if len(names) == 0 {
			names = core.FieldList(in.Settings, "server_name")
		}
		if len(names) == 0 && dest != "" {
			if host, _, err := net.SplitHostPort(dest); err == nil {
				names = []string{host}
			}
		}
		if len(names) > 0 {
			rs["serverNames"] = names
		}
		// 多短 ID：3x-ui 常配多个（含空串做兼容），按顺序全给过去
		rs["shortIds"] = core.ShortIDs(in.Settings)
		if sx := core.Field(in.Settings, "spider_x"); sx != "" {
			rs["spiderX"] = sx
		}
		if mtd := core.FieldInt(in.Settings, "max_time_diff"); mtd > 0 {
			rs["maxTimeDiff"] = mtd
		}
		stream["realitySettings"] = rs
	}
	obj["streamSettings"] = stream
	return obj, nil
}
