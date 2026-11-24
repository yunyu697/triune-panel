package api

import (
	"net"
	"strconv"
	"strings"
)

// SettingLANDNS 记住「局域网专用域名交给谁解析」（见 lanResolver）。
//
// 与规则集下载源同一种模式：生成页填一次，值落库，下次打开沿用。
const SettingLANDNS = "lan_dns"

// lanResolver 是「.lan / .local / .home.arpa / .localdomain 交给谁解析」的选择。
//
// 这四类名字（见 localOnlySuffixes）只有局域网里的解析器答得出来：路由器上的
// dnsmasq、Pi-hole、AdGuard Home。公网 DNS 对它们一律回 NXDOMAIN，而面板没有
// 任何办法替你猜出那台机器是谁——「本机正在用的解析器」在旁路由拓扑里恰恰是
// 公网（跨过主路由的 dnsmasq），猜错的代价是**所有**查询都可能走错。所以宁可
// 留空并明说，也不给一个看起来能用、实际解不出任何名字的默认值。
//
// 之所以不是一个「走系统解析器」的布尔开关：`router` 目标下 mihomo 自己就会
// listen 0.0.0.0:53 并 dns-hijack any:53，此时「系统解析器」指回自己就是一个
// DNS 环。必须允许填具体地址，并且要把这个环挡在保存之前（见 parseLANResolver）。
type lanResolver struct {
	// raw 是规范化后的原值："" | "system" | "10.0.0.1" | "10.0.0.1:5353" | "[fd00::1]:53"
	//
	// 保留字符串而不是解析成结构体：它要被原样写进三种产物（mihomo 的
	// nameserver-policy、sing-box 新旧两代 DNS 服务器），而这三种写法的地址
	// 形态并不一致——拆开再拼回去只会多一处出错的地方。
	raw string
}

func (l lanResolver) empty() bool { return l.raw == "" }

func (l lanResolver) isSystem() bool { return l.raw == "system" }

// hostPort 拆出主机与端口（省略端口时为 53）。
func (l lanResolver) hostPort() (string, int) {
	if h, p, err := net.SplitHostPort(l.raw); err == nil {
		n, _ := strconv.Atoi(p)
		return h, n
	}
	return l.raw, 53
}

// label 给提示文案与产物注释用。
func (l lanResolver) label() string {
	if l.isSystem() {
		return "系统解析器"
	}
	return l.raw
}

// mihomoValue 是写进 nameserver 的值：`system` 是 mihomo 自己的关键字，
// 其余按「裸地址即 UDP」理解，带端口就写 host:port。
func (l lanResolver) mihomoValue() string { return l.raw }

// parseLANResolver 校验并规范化用户填的局域网 DNS。
//
// 校验全部前移到保存期。写进库再生成就晚了：产物是一份**看起来完全正常**的
// 配置，症状只有「某个主机名解析不了」，与没填这个设置项一模一样——那正是
// 这个设置项要解决的问题，不能让它自己再制造一次。
func parseLANResolver(v string) (lanResolver, string) {
	s := strings.TrimSpace(v)
	if s == "" {
		return lanResolver{}, ""
	}
	if strings.EqualFold(s, "system") {
		return lanResolver{raw: "system"}, ""
	}
	host, port, ok := splitHostPortDefault(s, 53)
	if !ok {
		// 分清「格式不对」和「填了个域名」：后者是这一项最容易犯、也最难自查的
		// 错——填完之后配置看着完全正常，只是永远解析不出结果。
		if !strings.Contains(s, ":") && net.ParseIP(s) == nil {
			return lanResolver{}, "必须填 IP 地址：DNS 服务器写成域名就要先解析它，而解析它就是这一项要解决的问题"
		}
		return lanResolver{}, "请填 IP（如 10.0.0.1）、IP:端口（如 10.0.0.1:5353），或 system"
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return lanResolver{}, "必须填 IP 地址：DNS 服务器写成域名就要先解析它，而解析它就是这一项要解决的问题"
	}
	if ip.IsUnspecified() {
		return lanResolver{}, "0.0.0.0 / :: 是「本机所有地址」的通配写法，不是一个能回答查询的服务器"
	}
	if ip.IsLoopback() && port == 53 {
		return lanResolver{}, "这就是这份配置自己的 DNS 监听（router 目标会把内核的 DNS 挂到 :53），" +
			"填它会形成 DNS 环——所有查询都转回自己，一个名字也解析不出来"
	}
	out := ip.String()
	// IPv6 一律带方括号写，否则 `fd00::1:53` 会被当成一个地址而不是地址+端口。
	if port != 53 || strings.Contains(out, ":") {
		out = net.JoinHostPort(out, strconv.Itoa(port))
	}
	return lanResolver{raw: out}, ""
}

// splitHostPortDefault 拆 host[:port]，省略端口时用 def。
//
// 不用 net.SplitHostPort 直接判：它对 `10.0.0.1` 会报 missing port，对裸 IPv6
// `fd00::1` 会报 too many colons——这两种恰恰都是合法写法。
func splitHostPortDefault(s string, def int) (string, int, bool) {
	if ip := net.ParseIP(s); ip != nil {
		return s, def, true
	}
	// `[fd00::1]` 这种「带方括号但没端口」的写法会被人从别处粘进来，
	// net.SplitHostPort 对它报 missing port——收下它，规范化时再统一成 host:port。
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		if ip := net.ParseIP(s[1 : len(s)-1]); ip != nil {
			return ip.String(), def, true
		}
	}
	h, p, err := net.SplitHostPort(s)
	if err != nil {
		return "", 0, false
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 {
		return "", 0, false
	}
	return h, n, true
}
