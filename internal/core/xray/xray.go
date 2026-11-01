// Package xray 实现 Xray 内核适配器。
package xray

import (
	"encoding/json"
	"fmt"
	"net"
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
		if password != "" {
			ss["password"] = password
		}
		obj["settings"] = ss
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
		priv := core.Field(in.Settings, "private_key")
		if priv == "" {
			return nil, fmt.Errorf("Reality 入站缺少服务端私钥（private_key）：请在入站编辑里点「重新生成 Reality 密钥对」后保存")
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
			if _, _, err := net.SplitHostPort(dest); err != nil {
				dest = net.JoinHostPort(strings.Trim(dest, "[]"), "443")
			}
			rs["dest"] = dest
		}
		// serverNames 缺省回退：直接用握手目标的主机名，
		// 否则 xray 会认为 REALITY 配置不完整。
		names := strings.Fields(core.Field(in.Settings, "server_names"))
		if len(names) == 0 {
			names = strings.Fields(core.Field(in.Settings, "server_name"))
		}
		if len(names) == 0 && dest != "" {
			if host, _, err := net.SplitHostPort(dest); err == nil {
				names = []string{host}
			}
		}
		if len(names) > 0 {
			rs["serverNames"] = names
		}
		if sid := core.Field(in.Settings, "short_id"); sid != "" {
			rs["shortIds"] = []string{sid}
		} else {
			rs["shortIds"] = []string{""} // 空的短 ID 合法，留空时也必须给数组
		}
		stream["realitySettings"] = rs
	}
	obj["streamSettings"] = stream
	return obj, nil
}
