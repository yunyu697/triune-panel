package api

import (
	"triune/internal/link"
)

// ==================== OpenWrt-momo 配置生成 ====================
//
// OpenWrt-momo（nikkinikki-org/OpenWrt-momo）是跑在 OpenWrt 上的 sing-box
// 透明代理。它的 profile **就是一份完整的 sing-box config.json**：momo 负责
// nftables 劫持、ip rule / 策略路由、cgroup 分流和定时重启，profile 只负责
// 提供内核要读的那些部分。
//
// 关键在于它按 **固定 tag** 去 profile 里找东西（/etc/config/momo 的 config
// core 段，以下是默认值）：
//
//	dns_inbound_tag        dns-in             type=direct，必须有 listen_port
//	redirect_inbound_tag   redirect-in        type=redirect，必须有 listen_port
//	tproxy_inbound_tag     tproxy-in          type=tproxy，必须有 listen_port
//	tun_inbound_tag        tun-in             必须有 interface_name
//	fake_ip_dns_server_tag fake-ip-dns-server 可选，IPv6 fakeip 要读 inet6_range
//
// momo.init 的启动检查会按 UCI 的 tcp_mode / udp_mode 逐项核对：默认
// tcp_mode=redirect、udp_mode=tun、DNS 劫持开着，所以这四个入站一个都不能少。
// 少一个的现象不是「配置报错」，而是 momo 日志里一句 "Check failed" 后直接
// 退出、sing-box 根本没起来。也要把 tproxy-in 一起生成，否则用户在 LuCI 里
// 把模式切成 tproxy 就起不来了。
//
// 另一半是 momo 的 mixin：启动时它会用 /etc/config/momo 的 mixin 段覆盖
// log / dns 顶层开关 / ntp / experimental.cache_file / experimental.clash_api，
// **不会**替我们补 inbounds 或 DNS servers。所以这两块必须由 profile 自带，
// 不能指望「momo 会加」。
//
// 与「主路由 / 旁路由」目标的区别：那两个目标是给独立的 sing-box 客户端用的，
// TUN 要自己接管路由（auto_route:true + strict_route:true）；momo 的路由是它
// 自己用 ip rule 铺的，profile 里必须 auto_route:false，否则两边抢路由表。
// 所以这里复用 genSingbox 的节点 / 规则集 / 分流管道，只替换 inbounds 与
// 补上 fakeip + 嗅探，不另起一套模板。

const (
	genTargetMomo = "openwrt-momo"
	momoFakeIPTag = "fake-ip-dns-server"
)

func validGenTarget(s string) bool {
	switch s {
	case "client", "main-router", "side-router", genTargetMomo:
		return true
	}
	return false
}

// momoInbounds 生成 momo 默认 tag 的四个入站 + 一个调试用的混合端口。
// 监听 "::" 而不是 0.0.0.0：momo 默认 ipv4_proxy / ipv6_proxy / 两种 DNS
// 劫持全开，只绑 IPv4 时 IPv6 侧的劫持会打到一个没人监听的端口。
func momoInbounds(caps sbCaps) []any {
	tun := map[string]any{
		"type":           "tun",
		"tag":            "tun-in",
		"interface_name": "momo",
		"address":        []any{"172.19.0.1/30", "fdfe:dcba:9876::1/126"},
		// momo 自己铺 ip rule / route table；这里打开会和它抢路由。
		"auto_route": false,
	}
	// 1.15 起 sing-tun 用自有协议栈，stack 废弃（1.17 移除）。
	if caps.TunStack {
		tun["stack"] = "mixed"
	}
	return []any{
		map[string]any{"type": "direct", "tag": "dns-in", "listen": "::", "listen_port": 1053},
		map[string]any{"type": "redirect", "tag": "redirect-in", "listen": "::", "listen_port": 7890},
		map[string]any{"type": "tproxy", "tag": "tproxy-in", "listen": "::", "listen_port": 7891},
		tun,
		map[string]any{"type": "mixed", "tag": "mixed-in", "listen": "::", "listen_port": 7892},
	}
}

// genMomo 在 sing-box 主路由模板的基础上换上 momo 专用的入站与 DNS。
//
// 之所以从 genSingbox("main-router", ...) 起步而不是复制一遍：节点出站、
// 规则集（download_detour:direct）、自定义规则、绕过节点服务器、私网直连这些
// 逻辑一旦复制就会各自漂移，而 momo 需要的只是「入口换掉 + DNS 走 fakeip」。
func genMomo(ver sbVersion, nodes []*link.Node, sets []normSet, customRules []string,
	warns *[]string, mir ruleMirrorSel, bypass bypassSpec) *generator {
	g := genSingbox("main-router", ver, nodes, sets, customRules, warns, mir, bypass)
	doc := g.doc

	doc["inbounds"] = momoInbounds(capsFor(ver))

	// DNS：追加 fakeip 服务器，并让 A/AAAA 查询落到它上面。
	//
	// TUN 接管后流量是以 IP 包进来的，要先 sniff 出域名才能按 rule_set 分流；
	// fakeip 让嗅探出来的域名不必真的解析就能进路由。节点服务器域名与国内域名
	// 仍走真实 DNS（genSingbox 的既有规则排在前面），否则「用隧道连隧道端点」。
	// HTTPS / SVCB 记录要挡掉：客户端拿到 HTTPS RR 会绕过 A/AAAA，fakeip
	// 映射不上，表现为部分 App 直接连不上。
	dns := doc["dns"].(map[string]any)
	servers, _ := dns["servers"].([]any)
	servers = append(servers, map[string]any{
		"type":        "fakeip",
		"tag":         momoFakeIPTag,
		"inet4_range": "198.18.0.0/15",
		"inet6_range": "fc00::/18",
	})
	dns["servers"] = servers

	oldDNS, _ := dns["rules"].([]any)
	dnsRules := []any{
		map[string]any{"query_type": "HTTPS", "action": "reject"},
	}
	dnsRules = append(dnsRules, oldDNS...)
	dnsRules = append(dnsRules,
		// clash_api 由 momo 的 mixin 注入；没开 clash_api 时这两条永不命中。
		map[string]any{"clash_mode": "direct", "server": "local"},
		map[string]any{"clash_mode": "global", "server": momoFakeIPTag},
		map[string]any{"query_type": []any{"A", "AAAA"}, "server": momoFakeIPTag},
	)
	dns["rules"] = dnsRules

	// 路由：先嗅探域名，再把 dns-in 进来的查询交给 sing-box 的 DNS 模块。
	// genSingbox 自带的 {"protocol":"dns","action":"hijack-dns"} 保留在后面，
	// 覆盖 TUN 里直接发出来的 DNS 报文。
	route := doc["route"].(map[string]any)
	oldRules, _ := route["rules"].([]any)
	routeRules := []any{
		map[string]any{"action": "sniff", "sniffer": []any{"http", "tls", "quic", "dns"}},
		map[string]any{"inbound": "dns-in", "action": "hijack-dns"},
		map[string]any{"clash_mode": "direct", "outbound": "direct"},
		map[string]any{"clash_mode": "global", "outbound": "PROXY"},
	}
	route["rules"] = append(routeRules, oldRules...)

	*warns = append(*warns,
		"OpenWrt-momo：已按 /etc/config/momo 的默认 tag 生成 dns-in / redirect-in / tproxy-in / tun-in "+
			"与 fake-ip-dns-server。改过 core 段里任何一个 tag 都要同步改这份配置，否则 momo 启动检查会直接退出。",
		"momo 自己接管 nftables 与策略路由，tun-in 必须是 auto_route:false；导入后在 LuCI → Momo 看 Profile Check，"+
			"并按 UCI 的 tcp_mode / udp_mode 选择对应入站。要求 OpenWrt 24.10+、内核 5.13+、sing-box 1.12+。",
	)
	return g
}
