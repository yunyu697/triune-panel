package api

import (
	"bytes"
	"net/http"
	"regexp"
	"strings"
)

// 安全响应头中间件。
//
// 面板是自托管的，绝大多数实例直接挂在公网 IP 上，所以浏览器侧的防线
// 不能指望"用户会自己配 nginx 加头"。这里统一下发。
//
// 关于 CSP：脚本已经全部从 HTML 里挪出来（内嵌 boot.js / qr.js / app.js，经
// /assets/*.js 同源加载），内联事件属性也换成了 document 级事件委托
// （见 webui/app.js 顶部说明）。所以 script-src 不再需要 'unsafe-inline'——
// 这一条以前同时放行了注入的 <script>，是这套界面最后一个执行型缺口。
//
// style-src 仍保留 'unsafe-inline'：界面里有 300 多处 style="..." 与一个
// 内联 <style> 块，拆出去收益远小于风险（样式注入的危害也远低于脚本执行）。
//
// 另外仍然保留「把 XSS 变现路径掐死」那几条：
//   - 不加载任何外部脚本/样式/字体（无 CDN 依赖，全部同源）
//   - connect-src 'self'：即使被注入脚本，fetch/XHR/WebSocket 也发不到
//     攻击者域名，token 偷出去也没地方送
//   - form-action 'self'：表单不能被指向外部站点
//   - base-uri 'none'：防止注入 <base> 改写所有相对 URL 的指向
//   - object-src 'none'：不给插件类攻击面
//   - frame-ancestors 'none' + X-Frame-Options：禁止被别的页面套 iframe（点击劫持）
//
// manifest-src / worker-src 本来会回落到 default-src 与 script-src（都是
// 'self'，PWA 能正常工作）。这里仍然显式写出来：将来若有人继续收紧
// default-src，service worker 和 manifest 会跟着一起悄悄失效，而那种故障的
// 表现只是"手机上加不到主屏"，极难归因。
const cspPolicy = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; " +
	"font-src 'self'; " +
	"connect-src 'self'; " +
	"manifest-src 'self'; " +
	"worker-src 'self'; " +
	"object-src 'none'; " +
	"base-uri 'none'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'"

// cspPolicyLegacyInline 是给「自定义 UI」用的放宽版。
//
// data/web/index.html 允许被替换成用户自己的构建产物（这是有意的功能）。
// 那种页面很可能仍用内联 <script> 或 onclick="..."，套严格策略会直接白屏，
// 所以只在检测到这种形态时对这一份响应放宽，内置 UI 永远走严格版。
var cspPolicyLegacyInline = strings.Replace(cspPolicy,
	"script-src 'self';", "script-src 'self' 'unsafe-inline';", 1)

// hasInlineScript 判断 HTML 里有没有「没有 src 的 <script ...>」，即内联脚本块。
// 不用正则：Go 的 RE2 不支持 (?!...) 这类前瞻。
func hasInlineScript(b []byte) bool {
	lower := bytes.ToLower(b)
	for {
		i := bytes.Index(lower, []byte("<script"))
		if i < 0 {
			return false
		}
		rest := lower[i:]
		end := bytes.IndexByte(rest, '>')
		if end < 0 {
			return false
		}
		if !bytes.Contains(rest[:end], []byte("src=")) {
			return true
		}
		lower = rest[end+1:]
	}
}

// inlineEventRe 匹配内联事件属性。HTML 里才会出现这种形态，
// 脚本里给 DOM 赋值的 onload = fn 不带引号，不会误判。
var inlineEventRe = regexp.MustCompile(`(?i)\son(click|change|input|submit|keydown|keyup|load|error|focus|blur|dblclick|mousedown|mouseup|mouseover|mouseout)="`)

// htmlNeedsInlineScript 判断这份 HTML 是否还依赖内联脚本或内联事件属性。
// 内置 UI 必须返回 false —— 这条由 security_test.go 里的用例守着。
func htmlNeedsInlineScript(b []byte) bool {
	return hasInlineScript(b) || inlineEventRe.Match(b)
}

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
