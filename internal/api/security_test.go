package api

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"triune/internal/store"
	"triune/internal/webui"
)

// newSecurityTestServer 起一个带真实路由的最小 Server（不需要 Pipe/ProcMgr，
// 因为这里只验证响应头，不触发内核操作）。
func newSecurityTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st, nil, nil, dir)
}

// TestSecurityHeadersOnEveryResponse 安全头必须出现在所有响应上——
// 包括免认证的 /healthz 和 401 错误响应，否则错误页仍可能被嗅探执行。
func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	a := newSecurityTestServer(t)
	h := a.Handler()

	want := map[string]string{
		"X-Content-Type-Options":            "nosniff",
		"X-Frame-Options":                   "DENY",
		"Referrer-Policy":                   "no-referrer",
		"Cross-Origin-Opener-Policy":        "same-origin",
		"X-Permitted-Cross-Domain-Policies": "none",
	}
	cases := []struct {
		name string
		req  *http.Request
	}{
		{"healthz", httptest.NewRequest("GET", "/healthz", nil)},
		{"401", httptest.NewRequest("GET", "/api/status", nil)},
		{"404", httptest.NewRequest("GET", "/api/nope", nil)},
		{"页面", httptest.NewRequest("GET", "/", nil)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, c.req)
			for k, v := range want {
				if got := w.Header().Get(k); got != v {
					t.Errorf("%s: %s = %q, want %q", c.name, k, got, v)
				}
			}
			if w.Header().Get("Content-Security-Policy") == "" {
				t.Errorf("%s: 缺少 CSP", c.name)
			}
			if w.Header().Get("Permissions-Policy") == "" {
				t.Errorf("%s: 缺少 Permissions-Policy", c.name)
			}
		})
	}
}

// TestCSPLocksDownExfiltration 校验 CSP 里真正起作用的那几条。
// 脚本已经从 HTML 里挪出去了，但「外发路径」这几条仍然是最后一道防线：
// 万一哪天又漏出一处注入点，至少 token 发不出去。
func TestCSPLocksDownExfiltration(t *testing.T) {
	a := newSecurityTestServer(t)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	csp := w.Header().Get("Content-Security-Policy")

	must := []string{
		"default-src 'self'",
		"connect-src 'self'",     // 注入脚本也发不出去
		"form-action 'self'",     // 表单不能指向外部
		"base-uri 'none'",        // 防止 <base> 改写相对 URL
		"object-src 'none'",      // 无插件攻击面
		"frame-ancestors 'none'", // 防点击劫持
		"img-src 'self' data:",   // 界面里用到 data: 图标
		"manifest-src 'self'",    // PWA 清单（写死，防止将来收紧 default-src 时连带失效）
		"worker-src 'self'",      // service worker（同上）
	}
	for _, m := range must {
		if !strings.Contains(csp, m) {
			t.Errorf("CSP 缺少 %q\n实际: %s", m, csp)
		}
	}
	// 关键约束：内置 UI 已经没有任何内联脚本，不需要 nonce/hash。一旦加上，
	// 等于给「带 nonce 的 <script>」重新开了执行口子——注入者只要能把 nonce
	// 读出来（同源页面里就有）就又能执行。这条断言防止有人"顺手加个 nonce"。
	if strings.Contains(csp, "nonce-") || strings.Contains(csp, "sha256-") {
		t.Errorf("CSP 不能带 nonce/hash（等于重新放行内联脚本）: %s", csp)
	}
	if strings.Contains(csp, "unsafe-eval") {
		t.Errorf("CSP 不应允许 unsafe-eval: %s", csp)
	}
}

// TestCSPNoInlineScriptForBuiltinUI 内置 UI 必须完全没有内联脚本/内联事件属性，
// 所以 script-src 里不该再出现 'unsafe-inline'。
//
// 这条以前是反过来的：UI 有上百处内联 onclick，只能开 'unsafe-inline'，而那一项
// 同时放行了注入的 <script>。脚本外置 + 事件委托之后才收得回来——谁再往
// HTML 里塞一个 onclick，这条就会红。
func TestCSPNoInlineScriptForBuiltinUI(t *testing.T) {
	a := newSecurityTestServer(t)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	csp := w.Header().Get("Content-Security-Policy")
	if strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
		t.Fatalf("script-src 不应再含 'unsafe-inline': %s", csp)
	}
	if !strings.Contains(csp, "script-src 'self';") {
		t.Fatalf("script-src 应为 'self': %s", csp)
	}
	// style-src 仍需放开：界面里有 300+ 处 style="..."
	if !strings.Contains(csp, "style-src 'self' 'unsafe-inline'") {
		t.Fatalf("style-src 仍需 'unsafe-inline': %s", csp)
	}

	// 内置 UI 直接从二进制取——页面不再落盘，检查的就是实际会发出的那一份。
	if htmlNeedsInlineScript(webui.EmbeddedPage()) {
		t.Fatal("内置 UI 不应再包含内联脚本或内联事件属性（收紧 CSP 后会白屏）")
	}
}

// TestCustomUIGetsRelaxedCSP 自定义 UI（data/web/index.html 被替换）仍可能用
// 内联脚本/事件属性，对这份响应要放宽，否则一收紧就把人家界面打死。
func TestCustomUIGetsRelaxedCSP(t *testing.T) {
	a := newSecurityTestServer(t)
	page := filepath.Join(a.DataDir, "web", "index.html")
	if err := os.MkdirAll(filepath.Dir(page), 0o755); err != nil {
		t.Fatal(err)
	}
	// 自定义界面必须显式声明 custom 才会被服务（见 webui.CustomUIMarker）：
	// 「文件存在」或「没有标记」都不再足以接管界面，否则历史残留的旧页面
	// 会悄悄压住新界面。
	if err := os.WriteFile(page, []byte(`<!DOCTYPE html><!-- triune-panel-web/custom --><html><body>
		<button onclick="doIt()">go</button><script>function doIt(){}</script>
		</body></html>`), 0o644); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("首页应 200，实际 %d", w.Code)
	}
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
		t.Fatalf("自定义 UI 应拿到放宽的 CSP，实际: %s", csp)
	}
}

// TestAssetScriptsServed 前端脚本必须能从 /assets 取到且带正确的 Content-Type。
// 收紧 CSP 后它们是唯一的脚本来源，404 就等于白屏。
func TestAssetScriptsServed(t *testing.T) {
	a := newSecurityTestServer(t)
	h := a.Handler()
	for _, name := range []string{"app.js", "qr.js", "boot.js"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/assets/"+name, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s 应 200，实际 %d", name, w.Code)
		}
		if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
			t.Fatalf("%s 的 Content-Type 应是 javascript，实际 %q", name, ct)
		}
		if w.Body.Len() == 0 {
			t.Fatalf("%s 内容为空", name)
		}
	}
	// 不存在的资源要 404，别落到首页兜底里返回 HTML
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/assets/nope.js", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("不存在的脚本应 404，实际 %d", w.Code)
	}
}

// TestHSTSOnlyOverHTTPS HSTS 只在确实走 HTTPS 时下发。
// 在纯 HTTP 上下发 HSTS 会让浏览器此后强制 https，直接把面板锁死。
func TestHSTSOnlyOverHTTPS(t *testing.T) {
	a := newSecurityTestServer(t)
	h := a.Handler()

	t.Run("纯HTTP不下发", func(t *testing.T) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
		if got := w.Header().Get("Strict-Transport-Security"); got != "" {
			t.Fatalf("纯 HTTP 不应下发 HSTS，实际 %q", got)
		}
	})

	t.Run("直连TLS下发", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/healthz", nil)
		r.TLS = &tls.ConnectionState{}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if got := w.Header().Get("Strict-Transport-Security"); !strings.Contains(got, "max-age=") {
			t.Fatalf("TLS 直连应下发 HSTS，实际 %q", got)
		}
	})

	t.Run("可信代理转发https下发", func(t *testing.T) {
		if err := a.Store.SetSetting(TrustedProxiesSetting, "127.0.0.1"); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("GET", "/healthz", nil)
		r.RemoteAddr = "127.0.0.1:5555"
		r.Header.Set("X-Forwarded-Proto", "https")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if got := w.Header().Get("Strict-Transport-Security"); got == "" {
			t.Fatal("可信代理声明 https 时应下发 HSTS")
		}
	})

	t.Run("不可信来源伪造https不下发", func(t *testing.T) {
		if err := a.Store.SetSetting(TrustedProxiesSetting, "127.0.0.1"); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("GET", "/healthz", nil)
		r.RemoteAddr = "203.0.113.9:4444" // 公网直连，不在可信网段
		r.Header.Set("X-Forwarded-Proto", "https")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if got := w.Header().Get("Strict-Transport-Security"); got != "" {
			t.Fatalf("不可信来源伪造 X-Forwarded-Proto 不得触发 HSTS，实际 %q", got)
		}
	})

	t.Run("未配置可信代理时忽略转发头", func(t *testing.T) {
		if err := a.Store.SetSetting(TrustedProxiesSetting, ""); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("GET", "/healthz", nil)
		r.RemoteAddr = "127.0.0.1:5555"
		r.Header.Set("X-Forwarded-Proto", "https")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if got := w.Header().Get("Strict-Transport-Security"); got != "" {
			t.Fatalf("未配置可信代理时应忽略 X-Forwarded-Proto，实际 %q", got)
		}
	})
}

// TestHSTSProtoChain 多级代理时 X-Forwarded-Proto 可能是 "https, http"。
func TestHSTSProtoChain(t *testing.T) {
	a := newSecurityTestServer(t)
	if err := a.Store.SetSetting(TrustedProxiesSetting, "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/healthz", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Forwarded-Proto", "https, http")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	if got := w.Header().Get("Strict-Transport-Security"); got == "" {
		t.Fatal("多级代理时应取最左（最靠近客户端）的 https")
	}
}
