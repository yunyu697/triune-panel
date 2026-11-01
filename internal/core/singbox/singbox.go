// Package singbox 实现 sing-box 内核适配器。
package singbox

import (
	"encoding/json"
	"fmt"
	"os/exec"

	"triune/internal/core"
	"triune/internal/model"
)

type SingBox struct{}

func init() { core.Register(&SingBox{}) }

func (s *SingBox) Name() string     { return model.CoreSingBox }
func (s *SingBox) Ext() string      { return "json" }
func (s *SingBox) FileName() string { return "config.json" }

func (s *SingBox) ValidateCmd(bin, cfg string) *exec.Cmd {
	return exec.Command(bin, "check", "-c", cfg)
}

func (s *SingBox) Render(ins []core.InboundWithClients, opts core.RenderOpts) ([]byte, error) {
	doc := map[string]any{
		"log": map[string]any{"level": "info", "timestamp": true},
	}
	if opts.StatsEnabled {
		// clash_api 的 /connections 含每连接上下行计数与 inboundUser，
		// 面板采样器据此做每用户流量统计
		doc["experimental"] = map[string]any{
			"clash_api": map[string]any{
				"external_controller": "127.0.0.1:9090",
				"secret":              opts.StatsSecret,
			},
		}
	}
	var inbounds []any
	for _, iw := range ins {
		ib, err := s.renderInbound(iw)
		if err != nil {
			return nil, fmt.Errorf("inbound %q: %w", iw.Inbound.Name, err)
		}
		inbounds = append(inbounds, ib)
	}
	if inbounds == nil {
		inbounds = []any{}
	}
	doc["inbounds"] = inbounds
	doc["outbounds"] = s.outbounds(opts)
	s.applyGlobal(doc, opts)
	return json.MarshalIndent(doc, "", "  ")
}

// outbounds 组装出站：direct + 需要时补 block（路由规则里 action=reject 不必补，
// 但导入的旧格式配置会用 outbound=block，所以两种都要兼容）。
func (s *SingBox) outbounds(opts core.RenderOpts) []any {
	out := []any{map[string]any{"type": "direct", "tag": "direct"}}
	if routeUsesBlock(opts.RouteJSON) {
		out = append(out, map[string]any{"type": "block", "tag": "block"})
	}
	return out
}

func routeUsesBlock(routeJSON string) bool {
	if routeJSON == "" {
		return false
	}
	var r map[string]any
	if err := json.Unmarshal([]byte(routeJSON), &r); err != nil {
		return false
	}
	rules, _ := r["rules"].([]any)
	for _, it := range rules {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if m["outbound"] == "block" || m["action"] == "reject" {
			return true
		}
	}
	return false
}

// applyGlobal 把导入并转换过的 DNS / 路由段并进文档。
func (s *SingBox) applyGlobal(doc map[string]any, opts core.RenderOpts) {
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
	if err := json.Unmarshal([]byte(opts.RouteJSON), &r); err == nil && len(r) > 0 {
		doc["route"] = r
	}
}

// renderInbound 渲染单个入站；inbound.Settings 中的键原样并入，
// 面板计算的字段最后覆盖，保证透传字段不会破坏基础结构。
func (s *SingBox) renderInbound(iw core.InboundWithClients) (map[string]any, error) {
	in := iw.Inbound
	obj := map[string]any{
		"type":        in.Protocol,
		"tag":         core.TagOf(in),
		"listen":      core.ListenOrAll(in.Listen),
		"listen_port": in.Port,
	}
	// 透传用户自带的协议级配置
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

	var users []any
	for _, c := range iw.Clients {
		if !c.Enabled {
			continue
		}
		switch in.Protocol {
		case "vless":
			u := map[string]any{"name": c.Name, "uuid": c.Credential}
			// flow（xtls-rprx-vision）是 vless 的用户级参数，3x-ui 导出的
			// vision 入站不带它客户端会退回普通 TLS，性能差一截
			if flow := core.Field(in.Settings, "flow"); flow != "" {
				u["flow"] = flow
			}
			users = append(users, u)
		case "vmess":
			users = append(users, map[string]any{"name": c.Name, "uuid": c.Credential})
		case "trojan", "hysteria2", "anytls":
			users = append(users, map[string]any{"name": c.Name, "password": c.Credential})
		case "tuic":
			// TUIC 的认证是 uuid + password（v5），只给 password 会被拒收
			// （missing uuid for user N）
			users = append(users, map[string]any{
				"name": c.Name, "uuid": c.Credential, "password": c.Credential,
			})
		case "mixed", "socks", "http":
			// sing-box 的 socks/mixed/http 用户只有 username/password 两个字段，
			// 多余的 name 会被严格校验拒收（unknown field "name"）。
			// 凭据在面板里存成 "user:pass"，这里必须拆开。
			u, p := core.SplitUserPass(c.Credential, c.Name)
			users = append(users, map[string]any{"username": u, "password": p})
		case "direct":
			// sing-box 的 direct 入站 = 端口转发，没有用户概念
		case "shadowsocks":
			// shadowsocks 的密码在入站级别（下方统一设置），不进 users 数组
		default:
			return nil, fmt.Errorf("protocol %q not supported by sing-box renderer", in.Protocol)
		}
	}
	if len(users) > 0 {
		obj["users"] = users
	}
	if in.Protocol == "shadowsocks" {
		method := core.Field(in.Settings, "method")
		if method == "" {
			method = "aes-256-gcm"
		}
		if len(iw.Clients) > 0 {
			obj["password"] = iw.Clients[0].Credential
		}
		obj["method"] = method
	}

	if in.Protocol == "direct" {
		// sing-box 的 direct 入站：override_address / override_port 就是
		// xray dokodemo-door 的 address / port
		if a := core.Field(in.Settings, "dd_address"); a != "" {
			obj["override_address"] = a
		}
		if p := core.FieldInt(in.Settings, "dd_port"); p > 0 {
			obj["override_port"] = p
		}
		if n := core.Field(in.Settings, "dd_network"); n != "" {
			obj["network"] = n
		}
	}

	// transport
	// hysteria2/tuic/anytls 是自带传输的协议（QUIC / 自定义），
	// 没有 transport 字段；把表单里的 udp 或 tcp 原样写进去会被
	// 严格校验拒收（unknown field "transport"）。
	if !singboxHasTransport(in.Protocol) {
		// 跳过
	} else if in.Transport != "" && in.Transport != "tcp" && in.Transport != "udp" {
		tr := map[string]any{}
		switch in.Transport {
		case "ws":
			tr["type"] = "ws"
			if p := core.Field(in.Settings, "path"); p != "" {
				tr["path"] = p
			}
			if h := core.Field(in.Settings, "ws_host"); h != "" {
				tr["headers"] = map[string]any{"Host": h}
			}
		case "grpc":
			tr["type"] = "grpc"
			if sn := core.Field(in.Settings, "service_name"); sn != "" {
				tr["service_name"] = sn
			}
		case "h2":
			// sing-box 把 HTTP/2 传输叫 http（不是 h2）
			tr["type"] = "http"
			if p := core.Field(in.Settings, "path"); p != "" {
				tr["path"] = p
			}
			if h := core.Field(in.Settings, "ws_host"); h != "" {
				tr["host"] = []string{h}
			}
		case "httpupgrade":
			tr["type"] = "httpupgrade"
			if p := core.Field(in.Settings, "path"); p != "" {
				tr["path"] = p
			}
			if h := core.Field(in.Settings, "ws_host"); h != "" {
				tr["host"] = h
			}
		case "quic":
			// sing-box 的 quic 传输必须跑在 TLS 上（否则启动直接 FATAL: quic: TLS required）
			if in.TLS != "tls" {
				return nil, fmt.Errorf("sing-box 的 quic 传输必须配 TLS：请把 TLS 设为 tls 并填好证书，或改用 ws/grpc 传输")
			}
			tr["type"] = "quic"
		default:
			// kcp / xhttp 在 sing-box 里没有对应实现。硬写会得到
			// "unknown transport type"，不如在这里说清楚。
			return nil, fmt.Errorf("sing-box 不支持传输 %q（可选 tcp/ws/grpc/h2/httpupgrade/quic）；如需该传输请改用 xray 内核", in.Transport)
		}
		obj["transport"] = tr
	}

	// TLS
	switch in.TLS {
	case "tls":
		tls := map[string]any{"enabled": true}
		if sn := core.Field(in.Settings, "server_name"); sn != "" {
			tls["server_name"] = sn
		}
		if alpn := core.FieldList(in.Settings, "alpn"); len(alpn) > 0 {
			tls["alpn"] = alpn
		}
		if cp := core.Field(in.Settings, "certificate_path"); cp != "" {
			tls["certificate_path"] = cp
		}
		if kp := core.Field(in.Settings, "key_path"); kp != "" {
			tls["key_path"] = kp
		}
		// sing-box 只认文件路径，不认内联 PEM。JSON 导入会自动把 PEM 落盘，
		// 走到这里说明是手工填的 settings，得说清楚怎么改。
		if _, has := tls["certificate_path"]; !has && core.Field(in.Settings, "certificate_pem") != "" {
			return nil, fmt.Errorf("sing-box 只支持证书文件路径（不支持内联 PEM）：请把证书写到磁盘并填写路径，或改用 xray 内核")
		}
		// 这些协议在 sing-box 里 TLS 是强制的，没证书只会得到
		// "missing certificate" 这种内核内部报错，这里提前说清楚。
		if _, hasCert := tls["certificate_path"]; !hasCert && singboxRequiresCert(in.Protocol) {
			return nil, fmt.Errorf("%s 入站必须配置 TLS 证书：请填写证书路径与私钥路径（或先用面板 ACME 签发）", in.Protocol)
		}
		obj["tls"] = tls
	case "reality":
		priv := core.Field(in.Settings, "private_key")
		if priv == "" {
			return nil, fmt.Errorf("Reality 入站缺少服务端私钥（private_key）：请在入站编辑里点「生成密钥对」后保存")
		}
		hs := core.Field(in.Settings, "handshake_server")
		if hs == "" {
			hs = core.Field(in.Settings, "server_name")
		}
		hp := core.FieldInt(in.Settings, "handshake_port")
		if hp <= 0 || hp > 65535 {
			hp = 443
		}
		reality := map[string]any{
			"enabled":     true,
			"handshake":   map[string]any{"server": hs, "server_port": hp},
			"private_key": priv,
		}
		// sing-box 的 short_id 是字符串数组（1.12+）；只配空短 ID 时不写，
		// 写 [""] 会被校验拒收
		if sids := core.ShortIDs(in.Settings); len(sids) > 0 && !(len(sids) == 1 && sids[0] == "") {
			reality["short_id"] = sids
		}
		obj["tls"] = map[string]any{"enabled": true, "reality": reality}
	}
	return obj, nil
}

// singboxHasTransport 判断该协议是否支持 transport 字段。
// hysteria2/tuic/anytls 自带传输层，写入 transport 会被严格校验拒收。
func singboxHasTransport(protocol string) bool {
	switch protocol {
	case "vless", "vmess", "trojan", "shadowsocks":
		return true
	}
	return false
}

// singboxRequiresCert 判断该协议在 sing-box 中是否强制要求证书。
func singboxRequiresCert(protocol string) bool {
	switch protocol {
	case "anytls", "hysteria2", "tuic":
		return true
	}
	return false
}
