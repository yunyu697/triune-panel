// Clash/mihomo YAML 代理条目反解析为统一节点结构。
// 供「订阅 URL 聚合」使用：拉取远端订阅（常见 Clash 格式）后转换为可再导出的节点。

package link

import (
	"fmt"
	"strconv"
	"strings"
)

// FromClash 把一个 Clash proxies 条目转换为统一节点。
// 支持类型：ss / vmess / vless / trojan / hysteria2 / tuic。
func FromClash(m map[string]any) (*Node, error) {
	typ := str(m["type"])
	n := &Node{
		Name:   str(m["name"]),
		Host:   str(m["server"]),
		Extra:  map[string]any{},
	}
	if n.Host == "" {
		return nil, fmt.Errorf("缺少 server 字段")
	}
	port, err := strconv.Atoi(str(m["port"]))
	if err != nil {
		// 端口偶尔是数字类型
		if f, ok := m["port"].(float64); ok {
			port = int(f)
		} else if i, ok := m["port"].(int); ok {
			port = i
		} else {
			return nil, fmt.Errorf("端口无效")
		}
	}
	n.Port = port

	switch typ {
	case "ss":
		n.Protocol = "shadowsocks"
		n.Cred = str(m["password"])
		n.Extra["method"] = str(m["cipher"])
	case "vmess":
		n.Protocol = "vmess"
		n.Cred = str(m["uuid"])
	case "vless":
		n.Protocol = "vless"
		n.Cred = str(m["uuid"])
		if f := str(m["flow"]); f != "" {
			n.Extra["flow"] = f
		}
	case "trojan":
		n.Protocol = "trojan"
		n.Cred = str(m["password"])
	case "hysteria2":
		n.Protocol = "hysteria2"
		n.Cred = str(m["password"])
	case "tuic":
		n.Protocol = "tuic"
		n.Cred = str(m["uuid"]) + ":" + str(m["password"])
	default:
		return nil, fmt.Errorf("暂不支持类型 %q", typ)
	}
	if n.Cred == "" {
		return nil, fmt.Errorf("缺少凭据（uuid/password）")
	}

	// 传输层
	n.Network = str(m["network"])
	if n.Network == "" || n.Network == "tcp" {
		n.Network = "tcp"
	}
	if n.Network == "ws" {
		if ws, ok := m["ws-opts"].(map[string]any); ok {
			n.Path = str(ws["path"])
			if hs, ok := ws["headers"].(map[string]any); ok {
				if h := str(hs["Host"]); h != "" && n.SNI == "" {
					n.SNI = h
				}
			}
		}
	}
	if n.Network == "grpc" {
		if g, ok := m["grpc-opts"].(map[string]any); ok {
			if sn := str(g["grpc-service-name"]); sn != "" {
				n.Extra["serviceName"] = sn
			}
		}
	}

	// TLS
	if str(m["tls"]) == "true" || m["tls"] == true || n.Security == "" {
		if b, ok := m["tls"].(bool); ok && b {
			n.Security = "tls"
		}
	}
	switch {
	case str(m["tls"]) == "true" || m["tls"] == true:
		n.Security = "tls"
	case typ == "trojan" || typ == "hysteria2" || typ == "tuic":
		n.Security = "tls" // 这三族协议天然 TLS
	}
	if ro, ok := m["reality-opts"].(map[string]any); ok {
		n.Security = "reality"
		if pk := str(ro["public-key"]); pk != "" {
			n.Extra["pbk"] = pk
		}
		if sid := str(ro["short-id"]); sid != "" {
			n.Extra["sid"] = sid
		}
	}
	if sni := str(m["servername"]); sni != "" {
		n.SNI = sni
	}
	if sni := str(m["sni"]); sni != "" {
		n.SNI = sni
	}
	if fp := str(m["client-fingerprint"]); fp != "" {
		n.Extra["fp"] = fp
	}
	if n.Security == "" {
		n.Security = "none"
	}
	// 名字清洗：订阅里常见 emoji/竖线，GenLink 会做 URL 编码，这里仅去首尾空白
	n.Name = strings.TrimSpace(n.Name)
	return n, nil
}
