// Package importer 把外部面板导出的配置解析为面板入站。
//
// 目前支持两种输入，都是 xray 生态里的「入站定义」：
//   - 3x-ui / x-ui 的入站导出（数组，settings/streamSettings 是**字符串**形式的 JSON）；
//   - 原生 xray 配置里的 inbounds 段（对象，字段本身就是 JSON）。
//
// 设计原则：**能映的映，映不了的带警告保留原始值**。绝不静默丢弃字段，
// 也绝不猜——猜错的配置会让用户在「看着导入成功」之后踩更深的坑。
package importer

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Client 导入的客户端（对应面板 model.Client 的子集）。
type Client struct {
	Name       string     `json:"name"`
	Credential string     `json:"credential"`
	SubToken   string     `json:"sub_token,omitempty"`
	QuotaGB    float64    `json:"quota_gb"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	Enabled    bool       `json:"enabled"`
}

// Inbound 导入的入站（对应面板 model.Inbound 的子集 + 警告）。
type Inbound struct {
	Name      string `json:"name"`
	Protocol  string `json:"protocol"`
	Listen    string `json:"listen"`
	Port      int    `json:"port"`
	Tag       string `json:"tag"`
	Transport string `json:"transport"`
	TLS       string `json:"tls"`
	// Settings 是面板的协议级配置（拍平后的键值，见 README「入站设置字段」）。
	Settings map[string]any `json:"settings"`
	// PassThrough 是 xray 入站顶层字段（sniffing / allocate 等），
	// 只有目标内核是 xray 时才并入 settings —— sing-box 严格校验未知字段。
	PassThrough map[string]any `json:"-"`
	Clients     []Client       `json:"clients"`
	Warnings    []string       `json:"warnings,omitempty"`
	// Skip 表示这条不能导入（原因在 Warnings 里），导入时跳过而不是建半截。
	Skip bool `json:"skip"`
}

// Source 标识输入形态，用于前端提示。
const (
	Source3xui  = "3x-ui"
	SourceXray  = "xray"
	SourceArray = "array"
)

// Parse 解析文本为若干入站。
func Parse(text string) ([]*Inbound, error) {
	t := strings.TrimSpace(text)
	if t == "" {
		return nil, fmt.Errorf("内容为空")
	}
	// 3x-ui 的「备份」是 SQLite 数据库，不是 JSON。这种情况给出可执行的建议，
	// 而不是一句 "invalid character 'S'"。
	if strings.HasPrefix(t, "SQLite format 3") {
		return nil, fmt.Errorf("这是 3x-ui 的数据库备份（SQLite），不是 JSON；请在 3x-ui 的入站列表里导出 JSON，或粘贴 xray 配置的 inbounds 段")
	}
	dec := json.NewDecoder(strings.NewReader(t))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("不是合法 JSON：%w", err)
	}

	items, source, err := splitList(root)
	if err != nil {
		return nil, err
	}
	var out []*Inbound
	for _, m := range items {
		if m == nil {
			continue
		}
		ib, warns := mapOne(m, source)
		ib.Warnings = append(ib.Warnings, warns...)
		out = append(out, ib)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("没有解析到任何入站（根节点既不是数组，也没有 inbounds 字段）")
	}
	// 同一批里可能有同名入站，先在这里标出来，API 层再决定加后缀
	seen := map[string]int{}
	for _, ib := range out {
		seen[ib.Name]++
		if seen[ib.Name] > 1 {
			ib.Warnings = append(ib.Warnings, fmt.Sprintf("同批次里有 %d 条同名入站「%s」，导入时会自动加序号", seen[ib.Name], ib.Name))
		}
	}
	return out, nil
}

// splitList 把根节点拆成入站对象列表，并判断来源形态。
func splitList(root any) ([]map[string]any, string, error) {
	switch v := root.(type) {
	case []any:
		return toMaps(v), SourceArray, nil
	case map[string]any:
		if arr, ok := v["inbounds"].([]any); ok {
			return toMaps(arr), SourceXray, nil
		}
		// 3x-ui 的导出里可能包一层 {"data":[...]} 或 {"obj":[...]}
		for _, k := range []string{"data", "obj", "items", "list"} {
			if arr, ok := v[k].([]any); ok {
				if looksLikeInbounds(arr) {
					return toMaps(arr), Source3xui, nil
				}
			}
		}
		if _, ok := v["protocol"]; ok {
			return []map[string]any{v}, "single", nil
		}
		return nil, "", fmt.Errorf("JSON 结构不认识：既不是入站数组，也没有 inbounds 字段")
	}
	return nil, "", fmt.Errorf("JSON 顶层必须是对象或数组")
}

func looksLikeInbounds(arr []any) bool {
	for _, it := range arr {
		if m, ok := it.(map[string]any); ok {
			if _, has := m["protocol"]; has {
				return true
			}
		}
	}
	return false
}

func toMaps(arr []any) []map[string]any {
	out := make([]map[string]any, 0, len(arr))
	for _, it := range arr {
		if m, ok := it.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// mapOne 把一条 xray/3x-ui 入站映射为面板入站。
func mapOne(m map[string]any, source string) (*Inbound, []string) {
	var warns []string
	ib := &Inbound{Settings: map[string]any{}}

	ib.Protocol = strings.ToLower(asString(m["protocol"]))
	ib.Port = asInt(m["port"])
	ib.Listen = asString(m["listen"])
	ib.Tag = asString(m["tag"])
	ib.Name = firstNonEmpty(asString(m["remark"]), ib.Tag, fmt.Sprintf("%s-%d", ib.Protocol, ib.Port))
	ib.Transport = "tcp"
	ib.TLS = "none"

	if !supportedProtocol(ib.Protocol) {
		ib.Skip = true
		warns = append(warns, fmt.Sprintf("协议 %q 面板不支持（支持 vless/vmess/trojan/shadowsocks），已跳过", ib.Protocol))
		return ib, warns
	}
	if ib.Port <= 0 || ib.Port > 65535 {
		ib.Skip = true
		warns = append(warns, fmt.Sprintf("端口 %d 不合法，已跳过", ib.Port))
		return ib, warns
	}

	settings := asMap(m["settings"])
	stream := asMap(m["streamSettings"])
	if len(stream) == 0 {
		// 3x-ui 里没有 streamSettings 时默认就是 tcp + 无 TLS
		stream = map[string]any{}
	}

	// ---- 传输 ----
	network := strings.ToLower(asString(stream["network"]))
	switch network {
	case "", "tcp", "raw":
		ib.Transport = "tcp"
		if tcpSet := asMap(stream["tcpSettings"]); len(tcpSet) > 0 {
			if hdr := asMap(tcpSet["header"]); asString(hdr["type"]) == "http" {
				warns = append(warns, "TCP+HTTP 伪装（tcpSettings.header.type=http）面板不生成，已按纯 TCP 导入")
			}
			if asBool(tcpSet["acceptProxyProtocol"]) {
				ib.Settings["accept_proxy_protocol"] = true
			}
		}
	case "ws":
		ib.Transport = "ws"
		ws := asMap(stream["wsSettings"])
		if p := asString(ws["path"]); p != "" {
			ib.Settings["path"] = p
		}
		if h := asString(ws["host"]); h != "" {
			ib.Settings["ws_host"] = h
		}
	case "grpc":
		ib.Transport = "grpc"
		g := asMap(stream["grpcSettings"])
		if sn := asString(g["serviceName"]); sn != "" {
			ib.Settings["service_name"] = sn
		}
		if asBool(g["multiMode"]) {
			warns = append(warns, "gRPC multiMode 面板不生成，导入后如需请手工补")
		}
	case "h2", "http":
		ib.Transport = "tcp"
		warns = append(warns, "h2 传输面板不支持，已按 tcp 建草稿；若你的客户端依赖 h2 请改用 ws 或手工配置")
	case "httpupgrade", "xhttp", "splithttp", "kcp", "mkcp", "quic", "domainsocket":
		ib.Transport = "tcp"
		warns = append(warns, fmt.Sprintf("传输 %s 面板不支持，已按 tcp 建草稿，请手工核对", network))
	default:
		ib.Transport = "tcp"
		warns = append(warns, fmt.Sprintf("未知传输 %q，已按 tcp 导入", network))
	}

	// ---- TLS / REALITY ----
	security := strings.ToLower(asString(stream["security"]))
	switch security {
	case "tls", "xtls":
		ib.TLS = "tls"
		ts := asMap(stream["tlsSettings"])
		if sn := asString(ts["serverName"]); sn != "" {
			ib.Settings["server_name"] = sn
		}
		if alpn := asList(ts["alpn"]); len(alpn) > 0 {
			ib.Settings["alpn"] = strings.Join(alpn, ",")
		}
		if certs, ok := ts["certificates"].([]any); ok && len(certs) > 0 {
			c0 := asMap(certs[0])
			if f := asString(c0["certificateFile"]); f != "" {
				ib.Settings["certificate_path"] = f
			} else if len(asList(c0["certificate"])) > 0 {
				warns = append(warns, "证书是内联 PEM（没有文件路径），面板只支持证书文件路径，请把证书落到磁盘后填写路径")
			}
			if f := asString(c0["keyFile"]); f != "" {
				ib.Settings["key_path"] = f
			}
		}
		if security == "xtls" {
			warns = append(warns, "xray 的 xtls 已并入 tls，导入后按 tls 渲染")
		}
	case "reality":
		ib.TLS = "reality"
		rs := asMap(stream["realitySettings"])
		dest := asString(rs["dest"])
		if dest == "" {
			dest = asString(rs["target"])
		}
		host, port := splitHostPort(dest, 443)
		if host != "" {
			ib.Settings["handshake_server"] = host
			if port != 443 {
				ib.Settings["handshake_port"] = port
			}
		}
		names := asList(rs["serverNames"])
		if len(names) == 0 {
			if sn := asString(rs["serverName"]); sn != "" {
				names = []string{sn}
			}
		}
		if len(names) > 0 {
			ib.Settings["server_names"] = strings.Join(names, ",")
		}
		if pk := asString(rs["privateKey"]); pk != "" {
			ib.Settings["private_key"] = pk
		} else {
			warns = append(warns, "3x-ui 的导出里没有 Reality 私钥（privateKey 为空）：导入后请点「生成密钥对」，并让客户端改用新公钥")
		}
		sids := asListKeepEmpty(rs["shortIds"])
		if len(sids) > 0 {
			// 必须是**数组**而不是逗号串：xray 的 "" 是合法短 ID，
			// 拼成字符串会把它当空项丢掉，导致只配了空 sid 的客户端连不上。
			ib.Settings["short_ids"] = sids
			for _, s := range sids {
				if s != "" {
					ib.Settings["short_id"] = s // 兼容老字段：渲染时优先读 short_ids
					break
				}
			}
		}
		if sx := asString(rs["spiderX"]); sx != "" {
			ib.Settings["spider_x"] = sx
		}
		if mtd := asInt(rs["maxTimeDiff"]); mtd > 0 {
			ib.Settings["max_time_diff"] = mtd
		}
		// settings.publicKey 是客户端侧参数，顺手存下来供分享链接使用
		if sub := asMap(rs["settings"]); len(sub) > 0 {
			if pub := asString(sub["publicKey"]); pub != "" {
				ib.Settings["pbk"] = pub
			}
		}
	case "", "none":
		ib.TLS = "none"
	default:
		ib.TLS = "none"
		warns = append(warns, fmt.Sprintf("未知 security %q，已按无 TLS 导入", security))
	}

	// ---- 协议级配置 ----
	switch ib.Protocol {
	case "shadowsocks":
		method := asString(settings["method"])
		if method == "" {
			method = "aes-256-gcm"
		}
		ib.Settings["method"] = method
		if netw := asString(settings["network"]); netw != "" {
			ib.Settings["ss_network"] = netw
		}
	case "vless":
		if fb, ok := settings["fallbacks"].([]any); ok && len(fb) > 0 {
			ib.Settings["fallbacks"] = fb
		}
	}

	// ---- 客户端 ----
	ib.Clients = mapClients(settings, ib.Protocol)
	// flow 是 vless 入站级参数（面板按入站渲染），取客户端里唯一的非空值
	if ib.Protocol == "vless" {
		flows := map[string]bool{}
		for _, c := range rawClients(settings) {
			if f := asString(c["flow"]); f != "" {
				flows[f] = true
			}
		}
		if len(flows) == 1 {
			for f := range flows {
				ib.Settings["flow"] = f
			}
		} else if len(flows) > 1 {
			warns = append(warns, "多个客户端用了不同 flow，面板按入站统一渲染，已取第一个")
			keys := make([]string, 0, len(flows))
			for f := range flows {
				keys = append(keys, f)
			}
			sort.Strings(keys)
			ib.Settings["flow"] = keys[0]
		}
	}
	if len(ib.Clients) == 0 {
		warns = append(warns, "这条入站没有客户端（3x-ui 里可能是空入站），导入后需要手动加用户")
	}

	// ---- 顶层透传（仅 xray 目标内核会用到）----
	pt := map[string]any{}
	if sn := asMap(m["sniffing"]); len(sn) > 0 && asBool(sn["enabled"]) {
		pt["sniffing"] = sn
	}
	if al := asMap(m["allocate"]); len(al) > 0 {
		pt["allocate"] = al
	}
	if len(pt) > 0 {
		ib.PassThrough = pt
	}

	// 3x-ui 来源时，把没映射到的协议级键记一条提示，避免用户以为"全都导进来了"
	if source == Source3xui || source == "single" {
		var unknown []string
		for k := range settings {
			switch k {
			case "clients", "decryption", "fallbacks", "method", "password", "network":
			default:
				unknown = append(unknown, k)
			}
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			warns = append(warns, "以下 3x-ui 设置项面板不生成，已忽略："+strings.Join(unknown, ", "))
		}
	}
	return ib, warns
}

// rawClients 取 settings.clients（3x-ui 里是对象数组）。
func rawClients(settings map[string]any) []map[string]any {
	arr, ok := settings["clients"].([]any)
	if !ok {
		return nil
	}
	return toMaps(arr)
}

// mapClients 把 3x-ui / xray 的 clients 映射为面板客户端。
//
// 凭据字段按协议不同：vless/vmess 是 id、trojan 是 password、
// shadowsocks 2022 也是 password，socks/http 是 user/pass。
func mapClients(settings map[string]any, protocol string) []Client {
	var out []Client
	for i, c := range rawClients(settings) {
		cred := asString(c["id"])
		if cred == "" {
			cred = asString(c["password"])
		}
		if cred == "" {
			u, p := asString(c["user"]), asString(c["pass"])
			if u != "" || p != "" {
				cred = u + ":" + p
			}
		}
		if cred == "" {
			continue
		}
		name := asString(c["email"])
		if name == "" {
			name = asString(c["name"])
		}
		if name == "" {
			name = fmt.Sprintf("%s-user%d", protocol, i+1)
		}
		cl := Client{
			Name:       name,
			Credential: cred,
			SubToken:   asString(c["subId"]),
			Enabled:    true,
		}
		if v, ok := c["enable"]; ok {
			cl.Enabled = asBool(v)
		}
		// 3x-ui: totalGB 是**字节**；面板 QuotaGB 是 GB
		if g := asFloat(c["totalGB"]); g > 0 {
			cl.QuotaGB = round2(g / (1 << 30))
		}
		// 3x-ui: expiryTime 是毫秒时间戳，0 = 永不过期
		if ms := asInt64(c["expiryTime"]); ms > 0 {
			t := time.UnixMilli(ms).UTC()
			cl.ExpiresAt = &t
		}
		out = append(out, cl)
	}
	// 单密码协议（shadowsocks / trojan / vless 单用户形态）可能只有顶层 password/id
	if len(out) == 0 {
		if p := asString(settings["password"]); p != "" {
			out = append(out, Client{Name: "imported", Credential: p, Enabled: true})
		}
	}
	return out
}

func supportedProtocol(p string) bool {
	switch p {
	case "vless", "vmess", "trojan", "shadowsocks":
		return true
	}
	return false
}

// ---- 取值辅助：3x-ui 的 settings/streamSettings 是字符串，xray 配置是对象 ----

func asMap(v any) map[string]any {
	switch t := v.(type) {
	case map[string]any:
		return t
	case string:
		s := strings.TrimSpace(t)
		if s == "" || s == "null" {
			return nil
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err == nil {
			return m
		}
	}
	return nil
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case json.Number:
		return t.String()
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case bool:
		return strconv.FormatBool(t)
	}
	return ""
}

func asInt(v any) int { return int(asInt64(v)) }

func asInt64(v any) int64 {
	switch t := v.(type) {
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return n
		}
		if f, err := t.Float64(); err == nil {
			return int64(f)
		}
	case float64:
		return int64(t)
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64); err == nil {
			return n
		}
	case int64:
		return t
	case int:
		return int64(t)
	}
	return 0
}

func asFloat(v any) float64 {
	switch t := v.(type) {
	case json.Number:
		f, _ := t.Float64()
		return f
	case float64:
		return t
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f
	}
	return 0
}

func asBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true" || t == "1"
	case json.Number:
		return t.String() != "0"
	case float64:
		return t != 0
	}
	return false
}

func asList(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		if s := asString(v); s != "" {
			return []string{s}
		}
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, it := range arr {
		if s := asString(it); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// asListKeepEmpty 保留空字符串元素（shortIds 里的 "" 是有意义的值）。
func asListKeepEmpty(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		if s := asString(v); s != "" {
			return []string{s}
		}
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, it := range arr {
		out = append(out, asString(it))
	}
	return out
}

// splitHostPort 拆 "host:port"；没有端口时用 defPort。
func splitHostPort(s string, defPort int) (string, int) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", defPort
	}
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "https://")
	if i := strings.LastIndex(s, ":"); i > 0 && !strings.Contains(s[i:], "]") {
		host := s[:i]
		if p, err := strconv.Atoi(s[i+1:]); err == nil && p > 0 && p <= 65535 {
			return strings.Trim(host, "[]"), p
		}
	}
	return strings.Trim(s, "[]"), defPort
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func round2(f float64) float64 {
	return float64(int64(f*100+0.5)) / 100
}
