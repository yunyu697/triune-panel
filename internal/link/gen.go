package link

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// GenLink 由节点结构生成分享链接（订阅输出用）。
func GenLink(n *Node) string {
	switch n.Protocol {
	case "vless", "trojan", "tuic":
		q := urlValues(n)
		if n.Protocol == "vless" && n.Extra["flow"] != nil {
			q.Set("flow", fmt.Sprint(n.Extra["flow"]))
		}
		cred := n.Cred
		if n.Protocol == "tuic" && strings.Contains(n.Cred, ":") {
			// tuic: uuid:password
			cred = n.Cred
		}
		u := url.URL{
			Scheme:   n.Protocol,
			User:     url.User(cred),
			Host:     joinHostPort(n.Host, n.Port),
			RawQuery: q.Encode(),
			Fragment: n.Name,
		}
		return u.String()
	case "hysteria2":
		q := urlValues(n)
		u := url.URL{
			Scheme:   "hysteria2",
			User:     url.User(n.Cred),
			Host:     joinHostPort(n.Host, n.Port),
			RawQuery: q.Encode(),
			Fragment: n.Name,
		}
		return u.String()
	case "vmess":
		return genVmess(n)
	case "shadowsocks":
		return genSS(n)
	}
	return ""
}

func urlValues(n *Node) url.Values {
	q := url.Values{}
	if n.Security == "tls" || n.Security == "reality" {
		q.Set("security", n.Security)
	} else if n.Security != "none" && n.Security != "" {
		q.Set("security", n.Security)
	}
	if n.SNI != "" {
		q.Set("sni", n.SNI)
	}
	if n.Network != "" && n.Network != "tcp" {
		q.Set("type", n.Network)
	}
	if n.Path != "" {
		q.Set("path", n.Path)
	}
	for k, v := range n.Extra {
		if s, ok := v.(string); ok && s != "" {
			q.Set(k, s)
		}
	}
	return q
}

func genVmess(n *Node) string {
	payload := map[string]any{
		"v":    "2",
		"ps":   n.Name,
		"add":  n.Host,
		"port": strconv.Itoa(n.Port),
		"id":   n.Cred,
		"net":  n.Network,
		"tls":  n.Security,
	}
	if n.Path != "" {
		payload["path"] = n.Path
	}
	if n.SNI != "" {
		payload["sni"] = n.SNI
		payload["host"] = n.SNI
	}
	data, _ := json.Marshal(payload)
	return "vmess://" + base64.StdEncoding.EncodeToString(data)
}

func genSS(n *Node) string {
	method, _ := n.Extra["method"].(string)
	if method == "" {
		method = "aes-256-gcm"
	}
	userInfo := base64.RawURLEncoding.EncodeToString([]byte(method + ":" + n.Cred))
	return fmt.Sprintf("ss://%s@%s#%s", userInfo, joinHostPort(n.Host, n.Port), url.PathEscape(n.Name))
}

// ToClash 把节点转换为 clash/mihomo 代理配置对象。
func ToClash(n *Node) (map[string]any, error) {
	name := n.Name
	if name == "" {
		name = n.Host + ":" + strconv.Itoa(n.Port)
	}
	switch n.Protocol {
	case "vless":
		p := map[string]any{
			"name": name, "type": "vless", "server": n.Host, "port": n.Port,
			"uuid": n.Cred, "udp": true,
		}
		applyTLS(p, n)
		applyNet(p, n)
		if f, ok := n.Extra["flow"].(string); ok && f != "" {
			p["flow"] = f
		}
		if n.Security == "reality" {
			p["tls"] = true
			p["reality-opts"] = map[string]any{"public-key": str(n.Extra["pbk"]), "short-id": str(n.Extra["sid"])}
		}
		return p, nil
	case "vmess":
		p := map[string]any{
			"name": name, "type": "vmess", "server": n.Host, "port": n.Port,
			"uuid": n.Cred, "alterId": 0, "cipher": "auto", "udp": true,
		}
		applyTLS(p, n)
		applyNet(p, n)
		return p, nil
	case "trojan":
		p := map[string]any{
			"name": name, "type": "trojan", "server": n.Host, "port": n.Port,
			"password": n.Cred, "udp": true,
		}
		if n.SNI != "" {
			p["sni"] = n.SNI
		}
		applyNet(p, n)
		return p, nil
	case "shadowsocks":
		method := str(n.Extra["method"])
		if method == "" {
			method = "aes-256-gcm"
		}
		return map[string]any{
			"name": name, "type": "ss", "server": n.Host, "port": n.Port,
			"cipher": method, "password": n.Cred, "udp": true,
		}, nil
	case "hysteria2":
		p := map[string]any{
			"name": name, "type": "hysteria2", "server": n.Host, "port": n.Port,
			"password": n.Cred,
		}
		if n.SNI != "" {
			p["sni"] = n.SNI
		}
		return p, nil
	case "tuic":
		cred := strings.SplitN(n.Cred, ":", 2)
		p := map[string]any{
			"name": name, "type": "tuic", "server": n.Host, "port": n.Port,
			"uuid": cred[0], "password": "", "udp": true,
		}
		if len(cred) == 2 {
			p["password"] = cred[1]
		}
		if n.SNI != "" {
			p["sni"] = n.SNI
		}
		return p, nil
	}
	return nil, fmt.Errorf("unsupported protocol %q for clash export", n.Protocol)
}

// ToSingboxOutbound 把节点转换为 sing-box outbound 对象。
func ToSingboxOutbound(n *Node) (map[string]any, error) {
	name := n.Name
	if name == "" {
		name = n.Host + ":" + strconv.Itoa(n.Port)
	}
	switch n.Protocol {
	case "vless":
		o := map[string]any{
			"type": "vless", "tag": name, "server": n.Host, "server_port": n.Port,
			"uuid": n.Cred,
		}
		applyTLSOut(o, n)
		applyNetOut(o, n)
		if f, ok := n.Extra["flow"].(string); ok && f != "" {
			o["flow"] = f
		}
		return o, nil
	case "vmess":
		o := map[string]any{
			"type": "vmess", "tag": name, "server": n.Host, "server_port": n.Port,
			"uuid": n.Cred, "security": "auto",
		}
		applyTLSOut(o, n)
		applyNetOut(o, n)
		return o, nil
	case "trojan":
		o := map[string]any{
			"type": "trojan", "tag": name, "server": n.Host, "server_port": n.Port,
			"password": n.Cred,
		}
		applyTLSOut(o, n)
		applyNetOut(o, n)
		return o, nil
	case "shadowsocks":
		method := str(n.Extra["method"])
		if method == "" {
			method = "aes-256-gcm"
		}
		return map[string]any{
			"type": "shadowsocks", "tag": name, "server": n.Host, "server_port": n.Port,
			"method": method, "password": n.Cred,
		}, nil
	case "hysteria2":
		o := map[string]any{
			"type": "hysteria2", "tag": name, "server": n.Host, "server_port": n.Port,
			"password": n.Cred,
		}
		applyTLSOut(o, n)
		return o, nil
	case "tuic":
		cred := strings.SplitN(n.Cred, ":", 2)
		o := map[string]any{
			"type": "tuic", "tag": name, "server": n.Host, "server_port": n.Port,
			"uuid": cred[0], "password": "",
		}
		if len(cred) == 2 {
			o["password"] = cred[1]
		}
		applyTLSOut(o, n)
		return o, nil
	}
	return nil, fmt.Errorf("unsupported protocol %q for sing-box export", n.Protocol)
}

func applyTLS(p map[string]any, n *Node) {
	if n.Security == "tls" || n.Security == "reality" {
		p["tls"] = true
		if n.SNI != "" {
			p["servername"] = n.SNI
		}
		if n.Security == "reality" {
			p["reality-opts"] = map[string]any{
				"public-key": str(n.Extra["pbk"]),
				"short-id":   str(n.Extra["sid"]),
			}
		}
	}
}

func applyNet(p map[string]any, n *Node) {
	switch n.Network {
	case "ws":
		p["network"] = "ws"
		ws := map[string]any{}
		if n.Path != "" {
			ws["path"] = n.Path
		}
		p["ws-opts"] = ws
	case "grpc":
		p["network"] = "grpc"
		if sn := str(n.Extra["serviceName"]); sn != "" {
			p["grpc-opts"] = map[string]any{"grpc-service-name": sn}
		}
	}
}

func applyTLSOut(o map[string]any, n *Node) {
	if n.Security == "tls" || n.Security == "reality" {
		tls := map[string]any{"enabled": true}
		if n.SNI != "" {
			tls["server_name"] = n.SNI
		}
		if n.Security == "reality" {
			tls["reality"] = map[string]any{
				"enabled":    true,
				"public_key": str(n.Extra["pbk"]),
				"short_id":   str(n.Extra["sid"]),
			}
			// sing-box 的 reality 客户端要求 uTLS
			tls["utls"] = map[string]any{"enabled": true, "fingerprint": "chrome"}
		}
		o["tls"] = tls
	}
}

func applyNetOut(o map[string]any, n *Node) {
	switch n.Network {
	case "ws":
		tr := map[string]any{"type": "ws"}
		if n.Path != "" {
			tr["path"] = n.Path
		}
		o["transport"] = tr
	case "grpc":
		tr := map[string]any{"type": "grpc"}
		if sn := str(n.Extra["serviceName"]); sn != "" {
			tr["service_name"] = sn
		}
		o["transport"] = tr
	}
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func joinHostPort(host string, port int) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		return "[" + host + "]:" + strconv.Itoa(port)
	}
	return host + ":" + strconv.Itoa(port)
}
