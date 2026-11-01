package api

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"triune/internal/store"
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
// 内联执行挡不住（UI 有 126 处 on*=），但外发路径必须堵死。
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
	// 关键约束：一旦出现 nonce/hash，浏览器会忽略 'unsafe-inline'，
	// UI 的 126 个 onclick 会全部失效。这条断言防止有人"顺手加个 nonce"。
	if strings.Contains(csp, "nonce-") || strings.Contains(csp, "sha256-") {
		t.Errorf("CSP 不能带 nonce/hash（会废掉内联事件处理器）: %s", csp)
	}
	if strings.Contains(csp, "unsafe-eval") {
		t.Errorf("CSP 不应允许 unsafe-eval: %s", csp)
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
