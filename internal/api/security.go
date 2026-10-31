package api

import (
	"net/http"
	"strings"
)

// 安全响应头中间件。
//
// 面板是自托管的，绝大多数实例直接挂在公网 IP 上，所以浏览器侧的防线
// 不能指望"用户会自己配 nginx 加头"。这里统一下发。
//
// 关于 CSP：UI 是单文件、内联 <script>/<style>，且用了 126 处内联
// on*="..." 事件属性。内联事件属性只能靠 'unsafe-inline' 放行——
// **不能**改用 nonce：按 CSP3，一旦 script-src 里出现 nonce 或 hash，
// 浏览器会忽略 'unsafe-inline'，126 个 onclick 会全部失效，界面直接瘫掉。
//
// 所以这里的策略是「不假装能挡 XSS，但把 XSS 的变现路径掐死」：
//   - 不加载任何外部脚本/样式/字体（无 CDN 依赖，全部同源）
//   - connect-src 'self'：即使被注入脚本，fetch/XHR/WebSocket 也发不到
//     攻击者域名，token 偷出去也没地方送
//   - form-action 'self'：表单不能被指向外部站点
//   - base-uri 'none'：防止注入 <base> 改写所有相对 URL 的指向
//   - object-src 'none'：不给插件类攻击面
//   - frame-ancestors 'none' + X-Frame-Options：禁止被别的页面套 iframe（点击劫持）
//
// 这比"什么都不加"实质性强得多：CSP 里最有价值的部分（外发限制、
// 外链限制、框架限制）都拿到了，代价只是内联执行这一项。
const cspPolicy = "default-src 'self'; " +
	"script-src 'self' 'unsafe-inline'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; " +
	"font-src 'self'; " +
	"connect-src 'self'; " +
	"object-src 'none'; " +
	"base-uri 'none'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'"

// hstsMaxAge HSTS 有效期。刻意不用 preload：preload 是不可逆的承诺
// （进了浏览器内置列表就很难撤），自托管面板不该替用户做这种决定。
const hstsMaxAge = "max-age=15552000" // 180 天

// securityHeaders 给所有响应加安全头，并按需下发 HSTS。
func (a *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		// 禁止浏览器嗅探 Content-Type：避免把上传/回显的内容当 HTML 执行
		h.Set("X-Content-Type-Options", "nosniff")
		// 老浏览器不认 frame-ancestors，双写一份
		h.Set("X-Frame-Options", "DENY")
		// 不把面板地址通过 Referer 泄漏给外部站点
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", cspPolicy)
		// 面板不需要任何设备能力
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		// 跨源打开时不给 window.opener，杜绝反向 window 引用
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("X-Permitted-Cross-Domain-Policies", "none")

		// 刻意不下发 Cross-Origin-Resource-Policy：/sub/<token> 会被
		// 各种订阅客户端拉取，其中一些是浏览器里的 Web 面板，CORP 会把
		// 它们挡掉。这里收益小于风险。
		if a.isHTTPS(r) {
			h.Set("Strict-Transport-Security", hstsMaxAge)
		}
		next.ServeHTTP(w, r)
	})
}

// isHTTPS 判断本次请求是否走的是 HTTPS。
//
// 直连时看 r.TLS；挂在反向代理后面时看 X-Forwarded-Proto，但**只在直连
// 对端是可信代理时才采信**——否则公网攻击者随手加一个
// `X-Forwarded-Proto: https` 就能让我们给纯 HTTP 连接下发 HSTS，
// 把用户后续的明文访问也顶到 https，造成访问不了。
func (a *Server) isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))
	if proto == "" {
		return false
	}
	if !ipIn(remoteIP(r), a.trustedProxies()) {
		return false
	}
	// 多级代理时可能写成 "https, http"，取最左（最靠近客户端）的一项
	if i := strings.IndexByte(proto, ','); i >= 0 {
		proto = strings.TrimSpace(proto[:i])
	}
	return strings.EqualFold(proto, "https")
}
