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

	var users []any
	for _, c := range iw.Clients {
		if !c.Enabled {
			continue
		}
		switch in.Protocol {
		case "vless", "vmess":
			users = append(users, map[string]any{"name": c.Name, "uuid": c.Credential})
		case "trojan", "hysteria2", "tuic", "anytls":
			users = append(users, map[string]any{"name": c.Name, "password": c.Credential})
		case "mixed", "socks":
			users = append(users, map[string]any{"name": c.Name, "username": c.Name, "password": c.Credential})
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
	if in.Transport != "" && in.Transport != "tcp" {
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
		obj["tls"] = tls
	case "reality":
		obj["tls"] = map[string]any{
			"enabled": true,
			"reality": map[string]any{
				"enabled":     true,
				"handshake":   map[string]any{"server": core.Field(in.Settings, "handshake_server"), "server_port": 443},
				"private_key": core.Field(in.Settings, "private_key"),
			},
		}
	}
	return obj, nil
}
