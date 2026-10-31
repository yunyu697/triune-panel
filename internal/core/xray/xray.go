// Package xray 实现 Xray 内核适配器。
package xray

import (
	"encoding/json"
	"fmt"
	"os/exec"
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
	doc["outbounds"] = []any{map[string]any{"protocol": "freedom", "tag": "direct"}}
	return json.MarshalIndent(doc, "", "  ")
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
		obj["settings"] = map[string]any{"clients": clients, "decryption": "none"}
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
	default:
		return nil, fmt.Errorf("protocol %q not supported by xray renderer", in.Protocol)
	}

	// streamSettings
	stream := map[string]any{"network": "tcp"}
	if in.Transport != "" && in.Transport != "tcp" {
		stream["network"] = in.Transport
		switch in.Transport {
		case "ws":
			ws := map[string]any{}
			if p := core.Field(in.Settings, "path"); p != "" {
				ws["path"] = p
			}
			stream["wsSettings"] = ws
		case "grpc":
			g := map[string]any{}
			if sn := core.Field(in.Settings, "service_name"); sn != "" {
				g["serviceName"] = sn
			}
			stream["grpcSettings"] = g
		}
	}
	switch in.TLS {
	case "tls":
		stream["security"] = "tls"
		ts := map[string]any{}
		if sn := core.Field(in.Settings, "server_name"); sn != "" {
			ts["serverName"] = sn
		}
		if cp := core.Field(in.Settings, "certificate_path"); cp != "" {
			ts["certificates"] = []any{map[string]any{"certificateFile": cp, "keyFile": core.Field(in.Settings, "key_path")}}
		}
		stream["tlsSettings"] = ts
	case "reality":
		stream["security"] = "reality"
		rs := map[string]any{
			"privateKey":  core.Field(in.Settings, "private_key"),
			"dest":        core.Field(in.Settings, "handshake_server"),
			"serverNames": strings.Fields(core.Field(in.Settings, "server_names")),
		}
		if sid := core.Field(in.Settings, "short_id"); sid != "" {
			rs["shortIds"] = []string{sid}
		}
		stream["realitySettings"] = rs
	}
	obj["streamSettings"] = stream
	return obj, nil
}
