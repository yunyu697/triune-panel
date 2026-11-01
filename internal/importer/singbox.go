package importer

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// 这一组函数把 **xray 格式** 的全局段（dns / routing）转成 sing-box 能吃的结构。
//
// 为什么要转：3x-ui 的导出是 xray 格式，直接塞进 sing-box 配置会被严格校验拒收。
// 转换只做「语义一一对应」的部分，剩下的带着警告跳过——宁可少搬一条规则，
// 也不要生成一份校验不过、还可能把流量劫错方向的配置。

// ToSingboxDNS 把 xray 的 dns 段转成 sing-box 的 dns 段。
//
// 支持：纯 IP / IP:端口（udp）、tcp://、https:// 与 tls://、带 domains 的服务器
// （转成 sing-box 的 dns rule）。不支持 geosite/geoip 域名集合（sing-box 1.12+
// 已改用 rule-set，语义无法一一对应）。
func ToSingboxDNS(dns map[string]any) (map[string]any, []string) {
	if len(dns) == 0 {
		return nil, nil
	}
	var warns []string
	servers := []any{}
	rules := []any{}
	seenTag := map[string]bool{}

	rawServers, _ := dns["servers"].([]any)
	for i, it := range rawServers {
		address, port, domains := "", 0, []string(nil)
		switch t := it.(type) {
		case string:
			address = t
		case map[string]any:
			address = asString(t["address"])
			port = asInt(t["port"])
			domains = asList(t["domains"])
		}
		address = strings.TrimSpace(address)
		if address == "" {
			continue
		}
		srv, tag, w := singboxDNSServer(address, port)
		if w != "" {
			warns = append(warns, w)
			continue
		}
		if seenTag[tag] {
			tag = fmt.Sprintf("%s-%d", tag, i+1)
		}
		seenTag[tag] = true
		srv["tag"] = tag
		servers = append(servers, srv)

		if len(domains) > 0 {
			rule := map[string]any{"server": tag}
			var exact, suffix []string
			for _, d := range domains {
				d = strings.TrimSpace(d)
				switch {
				case d == "":
				case strings.HasPrefix(d, "geosite:"):
					warns = append(warns, "DNS 服务器按 geosite 集合分流（"+d+"）无法转成 sing-box 规则（需 rule-set），该条分流未导入")
				case strings.HasPrefix(d, "domain:"):
					suffix = append(suffix, strings.TrimPrefix(d, "domain:"))
				case strings.HasPrefix(d, "full:"):
					exact = append(exact, strings.TrimPrefix(d, "full:"))
				case strings.HasPrefix(d, "."):
					suffix = append(suffix, strings.TrimPrefix(d, "."))
				default:
					suffix = append(suffix, d)
				}
			}
			if len(exact) > 0 {
				rule["domain"] = exact
			}
			if len(suffix) > 0 {
				rule["domain_suffix"] = suffix
			}
			if len(rule) > 1 {
				rules = append(rules, rule)
			}
		}
	}
	if len(servers) == 0 {
		if len(rawServers) > 0 {
			warns = append(warns, "DNS 段没有任何能转换的服务器，未导入")
		}
		return nil, warns
	}
	out := map[string]any{"servers": servers}
	if len(rules) > 0 {
		out["rules"] = rules
	}
	return out, warns
}

// singboxDNSServer 解析单条 xray DNS 服务器地址，返回 sing-box 的 server 对象与 tag。
func singboxDNSServer(address string, port int) (map[string]any, string, string) {
	scheme := ""
	rest := address
	if i := strings.Index(address, "://"); i > 0 {
		scheme = strings.ToLower(address[:i])
		rest = address[i+3:]
	}
	// host[:port][/path]
	path := ""
	if i := strings.Index(rest, "/"); i >= 0 {
		path = rest[i:]
		rest = rest[:i]
	}
	host := rest
	p := port
	if h, pp, ok := splitHostPortMaybe(rest); ok {
		host, p = h, pp
	}
	kind := "udp"
	switch scheme {
	case "", "udp":
		kind = "udp"
	case "tcp":
		kind = "tcp"
	case "tls":
		kind = "tls"
	case "https", "h3":
		kind = "https"
	case "quic":
		kind = "quic"
	case "local":
		kind = "local"
	default:
		return nil, "", fmt.Sprintf("DNS 服务器 %q 的协议 %q 无法转换，已跳过", address, scheme)
	}
	srv := map[string]any{"type": kind}
	if kind == "local" {
		return srv, "dns-local", ""
	}
	if host == "" {
		return nil, "", fmt.Sprintf("DNS 服务器 %q 解析不出主机，已跳过", address)
	}
	// sing-box 对「地址本身是域名」的服务器（DoH/DoT）要求另外指定
	// domain_resolver，否则启动直接 FATAL。没有可靠的解析器可指定时
	// 宁可跳过并提示，也不要生成一份起不来的配置。
	if net.ParseIP(host) == nil && kind != "local" {
		return nil, "", fmt.Sprintf("DNS 服务器 %q 的地址是域名，sing-box 需要额外的 domain_resolver 才能用它，未导入（请手工配置）", address)
	}
	srv["server"] = host
	if p > 0 && p != 53 {
		srv["server_port"] = p
	}
	if path != "" && kind == "https" {
		srv["path"] = path
	}
	return srv, "dns-" + sanitizeTag(host), ""
}

// splitHostPortMaybe 拆 "host:port"；没有端口时 ok=false。
func splitHostPortMaybe(s string) (string, int, bool) {
	i := strings.LastIndex(s, ":")
	if i <= 0 || strings.Contains(s[i:], "]") {
		return s, 0, false
	}
	p, err := strconv.Atoi(s[i+1:])
	if err != nil || p <= 0 || p > 65535 {
		return s, 0, false
	}
	return strings.Trim(s[:i], "[]"), p, true
}

func sanitizeTag(s string) string {
	return strings.NewReplacer(".", "-", ":", "-", "/", "-").Replace(s)
}

// ToSingboxRoute 把 xray 的 routing 段转成 sing-box 的 route 段。
func ToSingboxRoute(routing map[string]any) (map[string]any, []string) {
	if len(routing) == 0 {
		return nil, nil
	}
	var warns []string
	rawRules, _ := routing["rules"].([]any)
	rules := []any{}
	for _, it := range rawRules {
		r := asMap(it)
		if len(r) == 0 {
			continue
		}
		rule := map[string]any{}
		if dom := asList(r["domain"]); len(dom) > 0 {
			var exact, suffix, keywords []string
			for _, d := range dom {
				switch {
				case strings.HasPrefix(d, "geosite:"), strings.HasPrefix(d, "ext:"):
					warns = append(warns, "路由规则里的域名集合（"+d+"）需要 rule-set，未导入")
				case strings.HasPrefix(d, "domain:"):
					suffix = append(suffix, strings.TrimPrefix(d, "domain:"))
				case strings.HasPrefix(d, "full:"):
					exact = append(exact, strings.TrimPrefix(d, "full:"))
				case strings.HasPrefix(d, "regexp:"):
					// sing-box 的 domain_regex 语法不同（Go RE2 与 xray 一致的其实可用），
					// 但为稳妥起见不搬，避免规则静默失效。
					warns = append(warns, "正则域名规则（"+d+"）未导入")
				case strings.HasPrefix(d, "."):
					suffix = append(suffix, strings.TrimPrefix(d, "."))
				default:
					suffix = append(suffix, d)
				}
			}
			if len(exact) > 0 {
				rule["domain"] = exact
			}
			if len(suffix) > 0 {
				rule["domain_suffix"] = suffix
			}
			if len(keywords) > 0 {
				rule["domain_keyword"] = keywords
			}
		}
		if ip := asList(r["ip"]); len(ip) > 0 {
			var cidrs []string
			for _, c := range ip {
				if strings.HasPrefix(c, "geoip:") || strings.HasPrefix(c, "ext:") {
					warns = append(warns, "路由规则里的 IP 集合（"+c+"）需要 rule-set，未导入")
					continue
				}
				cidrs = append(cidrs, c)
			}
			if len(cidrs) > 0 {
				rule["ip_cidr"] = cidrs
			}
		}
		if tags := asList(r["inboundTag"]); len(tags) > 0 {
			rule["inbound"] = tags
		}
		if n := asString(r["network"]); n != "" {
			rule["network"] = n
		}
		if p := asInt(r["port"]); p > 0 {
			rule["port"] = p
		}
		if ps := asString(r["port"]); ps != "" && asInt(r["port"]) == 0 {
			rule["port_range"] = ps
		}
		if proto := asList(r["protocol"]); len(proto) > 0 {
			rule["protocol"] = proto
		}
		// 只保留 direct / block 两类（导入阶段已过滤）
		switch strings.ToLower(asString(r["outboundTag"])) {
		case "block", "blocked", "blackhole":
			rule["action"] = "reject"
		default:
			rule["outbound"] = "direct"
		}
		if len(rule) <= 1 {
			warns = append(warns, "有一条路由规则没有可转换的匹配条件，已跳过")
			continue
		}
		rules = append(rules, rule)
	}
	if len(rules) == 0 {
		if len(rawRules) > 0 {
			warns = append(warns, "路由规则一条都没能转换（多为 geosite/geoip 规则），未导入")
		}
		return nil, warns
	}
	out := map[string]any{"rules": rules}
	// xray 的 domainStrategy 不是 sing-box 的概念（后者用 default_domain_resolver
	// 指向 DNS 服务器），不做映射，留给用户在 DNS 段自行决定。
	return out, warns
}
