// Package importer 把外部面板导出的配置解析为面板入站。
//
// 支持的输入形态（都是 xray 生态里的定义）：
//   - 3x-ui / x-ui 的入站导出（数组，settings/streamSettings 是**字符串**形式的 JSON）；
//   - 原生 xray 配置（对象，含 inbounds 段，顺带把 dns / routing 全局段带进来）；
//   - 单个入站对象。
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

// Global 是 xray 配置里的全局段（DNS / 路由 / 出站），不属于任何一条入站。
//
// 入站导入如果只搬 inbounds，用户会得到「入站一模一样、但分流规则和 DNS
// 全没了」的配置——表面上导入成功，实际行为完全不同。所以这些也要搬，
// 搬不了的（出站代理链）必须显式说明，而不是悄悄扔掉。
type Global struct {
	DNS       map[string]any `json:"dns,omitempty"`
	Routing   map[string]any `json:"routing,omitempty"`
	Outbounds []any          `json:"outbounds,omitempty"`
	// Warnings 是全局段的映射提示（与单条入站的 Warnings 分开）。
	Warnings []string `json:"warnings,omitempty"`
}

// Result 一次解析的完整结果。
type Result struct {
	Inbounds []*Inbound `json:"inbounds"`
	Global   *Global    `json:"global,omitempty"`
	Source   string     `json:"source"`
}

// Parse 只取入站（兼容旧调用点/测试）。
func Parse(text string) ([]*Inbound, error) {
	r, err := ParseFull(text)
	if err != nil {
		return nil, err
	}
	return r.Inbounds, nil
}

// ParseFull 解析文本为若干入站 + 全局段。
func ParseFull(text string) (*Result, error) {
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
	return &Result{Inbounds: out, Global: globalOf(root), Source: source}, nil
}

// globalOf 抽取根节点里的 dns / routing / outbounds。
//
// 只搬「能安全搬」的部分：
//   - DNS 原样带走（规则里引用的 geosite/geoip 在 xray 下同样有效）；
//   - 路由规则只保留指向 direct / block 的（面板渲染的出站只有 direct 与
//     block 两个）。指向代理出站的规则会连带整条代理链，照搬等于把一台
//     落地机的出站配置搬到本机，会直接把流量绕一圈甚至断掉，所以丢弃并提示。
func globalOf(root any) *Global {
	m, ok := root.(map[string]any)
	if !ok {
		return nil
	}
	g := &Global{}
	if d := asMap(m["dns"]); len(d) > 0 {
		g.DNS = d
	}
	if r := asMap(m["routing"]); len(r) > 0 {
		if rules, ok := r["rules"].([]any); ok && len(rules) > 0 {
			kept, dropped := filterRoutingRules(rules)
			if len(kept) > 0 {
				cp := map[string]any{}
				for k, v := range r {
					cp[k] = v
				}
				cp["rules"] = kept
				g.Routing = cp
			}
			if dropped > 0 {
				g.Warnings = append(g.Warnings, fmt.Sprintf("路由规则里有 %d 条指向代理出站（面板只有 direct / block 出站），已跳过；如需分流请在出站侧手工配置", dropped))
			}
		}
	}
	if ob, ok := m["outbounds"].([]any); ok && len(ob) > 0 {
		g.Outbounds = ob
		var proxy int
		for _, it := range ob {
			switch asString(asMap(it)["protocol"]) {
			case "", "freedom", "blackhole", "dns":
			default:
				proxy++
			}
		}
		if proxy > 0 {
			g.Warnings = append(g.Warnings, fmt.Sprintf("配置里有 %d 个代理出站（proxy/其他协议），面板只渲染 direct 出站，未导入", proxy))
		}
	}
	if g.DNS == nil && g.Routing == nil && len(g.Outbounds) == 0 {
		return nil
	}
	return g
}

// filterRoutingRules 只保留指向内置出站的规则，并把 block 统一成 blackhole 语义。
func filterRoutingRules(rules []any) (kept []any, dropped int) {
	for _, it := range rules {
		r := asMap(it)
		if len(r) == 0 {
			continue
		}
		switch strings.ToLower(asString(r["outboundTag"])) {
		case "", "direct", "freedom":
			cp := map[string]any{}
			for k, v := range r {
				cp[k] = v
			}
			delete(cp, "outboundTag") // 空 outboundTag = 默认 direct
			kept = append(kept, cp)
		case "block", "blocked", "blackhole":
			cp := map[string]any{}
			for k, v := range r {
				cp[k] = v
			}
			cp["outboundTag"] = "block"
			kept = append(kept, cp)
		default:
			dropped++
		}
	}
	return kept, dropped
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
		// sing-box 的入站长得像 {"type":"vless","listen_port":443,...}。
		// 别让用户对着「结构不认识」自己猜——直接说清楚这里只吃 xray 系定义。
		if _, isType := v["type"]; isType {
			if _, p1 := v["listen_port"]; p1 {
				return nil, "", fmt.Errorf("这看起来是 sing-box 的入站（type / listen_port），本面板只解析 xray / 3x-ui 的入站定义（protocol / port + settings + streamSettings）")
			}
			if _, p2 := v["listen"]; p2 {
				return nil, "", fmt.Errorf("这看起来是 sing-box 的入站（type / listen），本面板只解析 xray / 3x-ui 的入站定义（protocol / port + settings + streamSettings）")
			}
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

	if !protocolKnown(ib.Protocol) {
		ib.Skip = true
		warns = append(warns, fmt.Sprintf("协议 %q 面板不支持（支持 vless/vmess/trojan/shadowsocks/socks/http/dokodemo-door/wireguard），已跳过", ib.Protocol))
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
	// 传输名统一成面板/内核都认的短名：tcp ws grpc h2 httpupgrade xhttp kcp quic。
	// 具体某个内核能不能渲染由 API 层按矩阵提示（xray 全支持，sing-box 没有 kcp/xhttp）。
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
		// 传输名保持中立的 h2：xray 26 已移除它、sing-box 里叫 http，
		// 具体怎么落地由 API 层按目标内核决定（转 xhttp 或原样保留）。
		ib.Transport = "h2"
		h2 := asMap(stream["httpSettings"])
		if h2 == nil {
			h2 = asMap(stream["h2Settings"])
		}
		if p := asString(h2["path"]); p != "" {
			ib.Settings["path"] = p
		}
		if hosts := asList(h2["host"]); len(hosts) > 0 {
			ib.Settings["ws_host"] = hosts[0]
		}
	case "httpupgrade":
		ib.Transport = "httpupgrade"
		hu := asMap(stream["httpupgradeSettings"])
		if p := asString(hu["path"]); p != "" {
			ib.Settings["path"] = p
		}
		if h := asString(hu["host"]); h != "" {
			ib.Settings["ws_host"] = h
		}
	case "xhttp", "splithttp":
		ib.Transport = "xhttp"
		xh := asMap(stream["xhttpSettings"])
		if xh == nil {
			xh = asMap(stream["splithttpSettings"])
		}
		if p := asString(xh["path"]); p != "" {
			ib.Settings["path"] = p
		}
		if h := asString(xh["host"]); h != "" {
			ib.Settings["ws_host"] = h
		}
		if m := asString(xh["mode"]); m != "" {
			ib.Settings["xhttp_mode"] = m
		}
	case "kcp", "mkcp":
		ib.Transport = "kcp"
		k := asMap(stream["kcpSettings"])
		if s := asString(k["seed"]); s != "" {
			ib.Settings["kcp_seed"] = s
		}
		if h := asMap(k["header"]); len(h) > 0 {
			if t := asString(h["type"]); t != "" {
				ib.Settings["kcp_header"] = t
			}
		}
	case "quic":
		// 同样保持中立：sing-box 有 quic 传输，xray 26 已并入 xhttp。
		ib.Transport = "quic"
		q := asMap(stream["quicSettings"])
		if s := asString(q["security"]); s != "" {
			ib.Settings["quic_security"] = s
		}
		if k := asString(q["key"]); k != "" {
			ib.Settings["quic_key"] = k
		}
		if h := asMap(q["header"]); len(h) > 0 {
			if t := asString(h["type"]); t != "" {
				ib.Settings["quic_header"] = t
			}
		}
	case "domainsocket":
		ib.Transport = "tcp"
		warns = append(warns, "domainsocket 传输面板不支持，已按 tcp 建草稿")
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
			}
			if f := asString(c0["keyFile"]); f != "" {
				ib.Settings["key_path"] = f
			}
			// 内联 PEM（certificate / key 是字符串数组，xray 与 3x-ui 都常见）：
			// 面板按文件路径工作，这里先把 PEM 留下来，写库时落盘成文件——
			// 比丢一条「面板不支持」有用得多。
			if ib.Settings["certificate_path"] == nil {
				if pem := joinPEM(c0["certificate"]); pem != "" {
					ib.Settings["certificate_pem"] = pem
				}
			}
			if ib.Settings["key_path"] == nil {
				if pem := joinPEM(c0["key"]); pem != "" {
					ib.Settings["key_pem"] = pem
				}
			}
			if ib.Settings["certificate_path"] == nil && ib.Settings["certificate_pem"] == nil {
				warns = append(warns, "证书既没有文件路径也没有内联 PEM，导入后需手工填证书")
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
	case "socks", "http":
		// xray 的 socks/http 入站：auth=none|password，socks 还有 udp 开关
		if a := strings.ToLower(asString(settings["auth"])); a != "" {
			if a == "none" || a == "noauth" {
				ib.Settings["account_auth"] = "noauth"
			} else {
				ib.Settings["account_auth"] = "password"
			}
		}
		if asBool(settings["udp"]) {
			ib.Settings["udp"] = true
		}
	case "dokodemo-door", "tunnel":
		// 端口转发：目标地址/端口 + 是否跟随目标地址
		if a := asString(settings["address"]); a != "" {
			ib.Settings["dd_address"] = a
		}
		if p := asInt(settings["port"]); p > 0 {
			ib.Settings["dd_port"] = p
		}
		if n := asString(settings["network"]); n != "" {
			ib.Settings["dd_network"] = n
		}
		if asBool(settings["followRedirect"]) {
			ib.Settings["dd_follow_redirect"] = true
		}
		if ib.Protocol == "tunnel" {
			ib.Protocol = "dokodemo-door"
			warns = append(warns, "tunnel 协议已按 dokodemo-door（端口转发）导入")
		}
	case "wireguard":
		// wireguard 入站没有「用户」概念，参数是一整块密钥与 peer 列表。
		// 面板不做结构化建模（各内核字段差异大），原样透传给 xray 渲染。
		clean := map[string]any{}
		for k, v := range settings {
			if k == "clients" || k == "accounts" {
				continue
			}
			clean[k] = v
		}
		if len(clean) > 0 {
			ib.Settings["xray_raw_settings"] = clean
		}
		if asString(settings["secretKey"]) == "" {
			warns = append(warns, "WireGuard 入站缺少 secretKey，导入后需手工补")
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
		switch ib.Protocol {
		case "dokodemo-door", "wireguard":
			// 端口转发 / WireGuard 本来就没有面板用户，不提示
		default:
			warns = append(warns, "这条入站没有客户端（3x-ui 里可能是空入站），导入后需要手动加用户")
		}
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
			case "clients", "decryption", "fallbacks", "method", "password", "network",
				// socks / http
				"accounts", "auth", "udp",
				// dokodemo-door / tunnel
				"address", "port", "followRedirect", "timeout",
				// wireguard（整段原样透传给 xray）
				"secretKey", "mtu", "peers", "noKernelTun", "reserved", "workers", "domainStrategy":
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
	// xray 的 socks / http 用 accounts（{user, pass}），不是 clients
	if protocol == "socks" || protocol == "http" {
		if arr, ok := settings["accounts"].([]any); ok {
			for i, it := range toMaps(arr) {
				u, p := asString(it["user"]), asString(it["pass"])
				if u == "" && p == "" {
					continue
				}
				out = append(out, Client{
					Name:       firstNonEmpty(u, fmt.Sprintf("%s-user%d", protocol, i+1)),
					Credential: u + ":" + p,
					Enabled:    true,
				})
			}
			return out
		}
	}
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

func protocolKnown(p string) bool {
	switch p {
	case "vless", "vmess", "trojan", "shadowsocks", "socks", "http",
		"dokodemo-door", "tunnel", "wireguard":
		return true
	}
	return false
}

// joinPEM 把 xray 的 certificate / key 数组（PEM 文本）拼回一个字符串。
// 有 base64 形态的也会被认（老版本导出过 base64）。
func joinPEM(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case []any:
		parts := make([]string, 0, len(t))
		for _, it := range t {
			if s := strings.TrimSpace(asString(it)); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
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
