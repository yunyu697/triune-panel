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
	doc["outbounds"] = []any{map[string]any{"type": "direct", "tag": "direct"}}
	return json.MarshalIndent(doc, "", "  ")
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
		case "vless", "vmess":
			users = append(users, map[string]any{"name": c.Name, "uuid": c.Credential})
		case "trojan", "hysteria2", "anytls":
			users = append(users, map[string]any{"name": c.Name, "password": c.Credential})
		case "tuic":
			// TUIC 的认证是 uuid + password（v5），只给 password 会被拒收
			// （missing uuid for user N）
			users = append(users, map[string]any{
				"name": c.Name, "uuid": c.Credential, "password": c.Credential,
			})
		case "mixed", "socks":
			// sing-box 的 socks/mixed 用户只有 username/password 两个字段，
			// 多余的 name 会被严格校验拒收（unknown field "name"）
			users = append(users, map[string]any{"username": c.Name, "password": c.Credential})
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

	// transport
	// hysteria2/tuic/anytls 是自带传输的协议（QUIC / 自定义），
	// 没有 transport 字段；把表单里的 udp 或 tcp 原样写进去会被
	// 严格校验拒收（unknown field "transport"）。
	if !singboxHasTransport(in.Protocol) {
		// 跳过
	} else if in.Transport != "" && in.Transport != "tcp" && in.Transport != "udp" {
		tr := map[string]any{"type": in.Transport}
		switch in.Transport {
		case "ws":
			if p := core.Field(in.Settings, "path"); p != "" {
				tr["path"] = p
			}
		case "grpc":
			if sn := core.Field(in.Settings, "service_name"); sn != "" {
				tr["service_name"] = sn
			}
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
		if cp := core.Field(in.Settings, "certificate_path"); cp != "" {
			tls["certificate_path"] = cp
		}
		if kp := core.Field(in.Settings, "key_path"); kp != "" {
			tls["key_path"] = kp
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
			return nil, fmt.Errorf("Reality 入站缺少服务端私钥（private_key）：请在入站编辑里点「重新生成 Reality 密钥对」后保存")
		}
		reality := map[string]any{
			"enabled":     true,
			"handshake":   map[string]any{"server": core.Field(in.Settings, "handshake_server"), "server_port": 443},
			"private_key": priv,
		}
		if sid := core.Field(in.Settings, "short_id"); sid != "" {
			reality["short_id"] = []string{sid}
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
