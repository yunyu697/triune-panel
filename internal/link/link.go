// Package link 实现代理分享链接的反向解析与安全导入。
//
// 这里只做一件事：把链接解析成**外部节点**（进订阅聚合），不碰本机任何内核配置。
// 链接是客户端视角的快照——没有 Reality 私钥、没有证书、没有多用户，
// 反推服务端必然缺东西，所以「链接 → 本地入站」这条路径已移除；
// 要建服务端请用 /api/inbounds/import-json（3x-ui / xray 的原始定义）。
package link

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Node 反向解析出的节点结构。
type Node struct {
	Protocol string // vless/vmess/trojan/shadowsocks/hysteria2/tuic
	Name     string
	Host     string
	Port     int
	Cred     string // uuid 或密码
	Security string // none/tls/reality
	SNI      string
	Network  string // tcp/ws/grpc/h2
	Path     string
	Extra    map[string]any `json:"extra,omitempty"` // 其余参数原样保留
	Raw      string         `json:"raw"`             // 原始链接
}

// Parse 解析单条分享链接。
func Parse(raw string) (*Node, error) {
	raw = strings.TrimSpace(raw)
	switch {
	case strings.HasPrefix(raw, "vless://"):
		return parseUserinfo(raw, "vless")
	case strings.HasPrefix(raw, "trojan://"):
		return parseUserinfo(raw, "trojan")
	case strings.HasPrefix(raw, "tuic://"):
		return parseUserinfo(raw, "tuic")
	case strings.HasPrefix(raw, "hysteria2://") || strings.HasPrefix(raw, "hy2://"):
		raw2 := strings.TrimPrefix(strings.TrimPrefix(raw, "hysteria2://"), "hy2://")
		return parseUserinfo("hysteria2://"+raw2, "hysteria2")
	case strings.HasPrefix(raw, "vmess://"):
		return parseVmess(raw)
	case strings.HasPrefix(raw, "ss://"):
		return parseSS(raw)
	default:
		return nil, fmt.Errorf("unsupported link scheme")
	}
}

// ParseMany 批量解析（兼容 base64 订阅内容与多行文本）。
func ParseMany(text string) ([]*Node, []error) {
	text = strings.TrimSpace(text)
	lines := strings.Split(text, "\n")
	if len(lines) == 1 && !strings.Contains(text, "://") {
		// 可能是 base64 编码的订阅
		if dec, err := base64.StdEncoding.DecodeString(text); err == nil && strings.Contains(string(dec), "://") {
			lines = strings.Split(string(dec), "\n")
		}
	}
	var nodes []*Node
	var errs []error
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		n, err := Parse(ln)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", trunc(ln, 40), err))
			continue
		}
		nodes = append(nodes, n)
	}
	return nodes, errs
}

func parseUserinfo(raw, proto string) (*Node, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	n := &Node{Protocol: proto, Raw: raw, Network: "tcp", Extra: map[string]any{}}
	n.Name = u.Fragment
	n.Host = u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	n.Port = port
	if u.User != nil {
		n.Cred = u.User.Username()
		if pw, ok := u.User.Password(); ok {
			n.Cred = n.Cred + ":" + pw
		}
	}
	q := u.Query()
	if q.Get("security") == "tls" || q.Get("security") == "reality" {
		n.Security = q.Get("security")
	} else if proto == "trojan" || proto == "hysteria2" || proto == "tuic" {
		n.Security = "tls"
	}
	n.SNI = q.Get("sni")
	if n.SNI == "" {
		n.SNI = q.Get("peer")
	}
	if nt := q.Get("type"); nt != "" {
		n.Network = nt
	}
	n.Path = q.Get("path")
	for k, v := range q {
		if len(v) > 0 && k != "security" && k != "sni" && k != "peer" && k != "type" && k != "path" {
			n.Extra[k] = v[0]
		}
	}
	if n.Port == 0 || n.Host == "" {
		return nil, fmt.Errorf("missing host/port")
	}
	return n, nil
}

func parseVmess(raw string) (*Node, error) {
	payload := strings.TrimPrefix(raw, "vmess://")
	data, err := base64.StdEncoding.DecodeString(payload + strings.Repeat("=", (4-len(payload)%4)%4))
	if err != nil {
		return nil, fmt.Errorf("vmess base64: %w", err)
	}
	var v struct {
		Add  string      `json:"add"`
		Port interface{} `json:"port"`
		ID   string      `json:"id"`
		Ps   string      `json:"ps"`
		Net  string      `json:"net"`
		TLS  string      `json:"tls"`
		Path string      `json:"path"`
		Host string      `json:"host"`
		Sni  string      `json:"sni"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("vmess json: %w", err)
	}
	port := 0
	switch p := v.Port.(type) {
	case float64:
		port = int(p)
	case string:
		port, _ = strconv.Atoi(p)
	}
	n := &Node{Protocol: "vmess", Raw: raw, Name: v.Ps, Host: v.Add, Port: port, Cred: v.ID,
		Network: v.Net, Path: v.Path, Security: "none", Extra: map[string]any{}}
	if v.TLS == "tls" {
		n.Security = "tls"
	}
	if v.Sni != "" {
		n.SNI = v.Sni
	} else {
		n.SNI = v.Host
	}
	if n.Network == "" {
		n.Network = "tcp"
	}
	return n, nil
}

func parseSS(raw string) (*Node, error) {
	// SIP002: ss://base64(method:password)@host:port#name 或 ss://base64(method:password@host:port)#name
	body := strings.TrimPrefix(raw, "ss://")
	n := &Node{Protocol: "shadowsocks", Raw: raw, Network: "tcp", Extra: map[string]any{}}
	if frag := strings.SplitN(body, "#", 2); len(frag) == 2 {
		n.Name, _ = url.QueryUnescape(frag[1])
		body = frag[0]
	}
	if q := strings.SplitN(body, "?", 2); len(q) == 2 {
		body = q[0]
	}
	var cred, hostport string
	if at := strings.LastIndex(body, "@"); at >= 0 {
		cred, hostport = body[:at], body[at+1:]
		if dec, err := base64.RawURLEncoding.DecodeString(cred); err == nil {
			cred = string(dec)
		} else if dec, err := base64.StdEncoding.DecodeString(cred); err == nil {
			cred = string(dec)
		}
	} else {
		// 旧格式整体 base64
		dec, err := base64.RawURLEncoding.DecodeString(body)
		if err != nil {
			return nil, fmt.Errorf("ss base64: %w", err)
		}
		parts := strings.SplitN(string(dec), "@", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("ss: bad format")
		}
		cred, hostport = parts[0], parts[1]
	}
	host, portStr, err := splitHostPort(hostport)
	if err != nil {
		return nil, err
	}
	n.Host = host
	n.Port = portStr
	// cred = method:password
	if idx := strings.Index(cred, ":"); idx >= 0 {
		n.Extra["method"] = cred[:idx]
		n.Cred = cred[idx+1:]
	} else {
		n.Cred = cred
	}
	if n.Port == 0 || n.Host == "" {
		return nil, fmt.Errorf("missing host/port")
	}
	return n, nil
}

func splitHostPort(s string) (string, int, error) {
	if strings.HasPrefix(s, "[") { // IPv6
		if end := strings.Index(s, "]"); end > 0 {
			host := s[1:end]
			port, _ := strconv.Atoi(strings.TrimPrefix(s[end+1:], ":"))
			return host, port, nil
		}
	}
	if at := strings.LastIndex(s, ":"); at >= 0 {
		port, err := strconv.Atoi(s[at+1:])
		if err != nil {
			return "", 0, err
		}
		return s[:at], port, nil
	}
	return "", 0, fmt.Errorf("no port in %q", s)
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
