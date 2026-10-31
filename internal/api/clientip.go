package api

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

// TrustedProxiesSetting 可信反向代理网段的设置键。
//
// 默认空字符串 = 不信任任何代理，此时完全按 TCP 直连对端（RemoteAddr）
// 判定来源，行为与旧版本一致。面板挂在 nginx / Caddy / Traefik 后面时，
// RemoteAddr 恒为 127.0.0.1，必须把代理所在网段填进来，才会采信
// X-Forwarded-For / X-Real-IP，否则防爆破会退化成「一个人失败五次，
// 全世界都被锁」。
const TrustedProxiesSetting = "trusted_proxies"

// splitProxyList 按常见分隔符切分代理列表（逗号、空格、分号、换行都支持）。
func splitProxyList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		switch r {
		case ',', ';', ' ', '\t', '\n', '\r':
			return true
		}
		return false
	})
}

// parseTrustedProxies 解析可信代理网段列表。
//
// 支持 CIDR（10.0.0.0/8）与裸 IP（127.0.0.1，等价于 /32 或 /128）。
// 任何一项非法即整体报错——静默忽略错项会让用户以为配好了，实际没生效，
// 这类"看起来生效其实没生效"的安全配置最难排查。
func parseTrustedProxies(s string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, tok := range splitProxyList(s) {
		if strings.Contains(tok, "/") {
			_, n, err := net.ParseCIDR(tok)
			if err != nil {
				return nil, fmt.Errorf("网段 %q 格式不正确", tok)
			}
			out = append(out, n)
			continue
		}
		ip := net.ParseIP(tok)
		if ip == nil {
			return nil, fmt.Errorf("地址 %q 不是合法 IP 或网段", tok)
		}
		if v4 := ip.To4(); v4 != nil {
			ip = v4
		}
		bits := 128
		if ip.To4() != nil {
			bits = 32
		}
		out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
	}
	return out, nil
}

// ipIn 判断 ip 是否落在任一网段内。
func ipIn(ip net.IP, nets []*net.IPNet) bool {
	if ip == nil {
		return false
	}
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// remoteIP 从 RemoteAddr 取出对端 IP。
func remoteIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return nil
	}
	return net.ParseIP(host)
}

// clientIPFrom 在给定可信网段下计算真实来源 IP。
//
// 规则（与 nginx realip / Go 生态通行做法一致）：
//  1. 直连对端不在可信网段 → 一律用直连对端，完全不看任何转发头。
//     这条是安全底线：公网攻击者直接打面板时伪造 X-Forwarded-For 无效。
//  2. 直连对端可信 → 把 X-Forwarded-For 当作「客户端, 代理1, 代理2…」，
//     从右往左跳过可信代理，第一个不可信的即真实客户端（右侧由离我们最近
//     的代理追加，不可伪造；左侧可被客户端随意伪造，所以不能取最左）。
//  3. 全链路都可信（例如本机 nginx → 本机面板）→ 退回最左一项。
//  4. 没有 X-Forwarded-For 时再看 X-Real-IP。
func clientIPFrom(r *http.Request, trusted []*net.IPNet) string {
	peer := remoteIP(r)
	if peer == nil {
		return ""
	}
	if !ipIn(peer, trusted) {
		return peer.String()
	}

	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		chain := make([]net.IP, 0, 4)
		for _, part := range strings.Split(xff, ",") {
			if ip := net.ParseIP(strings.TrimSpace(part)); ip != nil {
				chain = append(chain, ip)
			}
		}
		if len(chain) > 0 {
			chain = append(chain, peer) // 直连对端补到最右
			for i := len(chain) - 1; i >= 0; i-- {
				if i == 0 || !ipIn(chain[i], trusted) {
					return chain[i].String()
				}
			}
		}
	}

	if xr := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); xr != nil {
		return xr.String()
	}
	return peer.String()
}

// clientIP 返回本次请求的真实来源 IP（读设置里的可信代理网段）。
func (a *Server) clientIP(r *http.Request) string {
	var trusted []*net.IPNet
	if raw, err := a.Store.GetSetting(TrustedProxiesSetting); err == nil && strings.TrimSpace(raw) != "" {
		// 解析失败时按「不信任任何代理」处理：宁可把来源记成代理地址，
		// 也不能因为配置写错就采信可伪造的请求头。
		trusted, _ = parseTrustedProxies(raw)
	}
	return clientIPFrom(r, trusted)
}
