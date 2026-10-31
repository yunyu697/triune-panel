package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"triune/internal/authn"
	"triune/internal/store"
)

// protectedRoutes 所有需要登录才能访问的端点。
//
// 加新路由时必须同步这张表——这是「有没有人忘了包 a.auth()」的唯一防线。
// 断言方式：无 token 时必须是 401，绝不能是 200/404/405（那说明路由压根没注册，
// 或者注册了但漏了鉴权）。
var protectedRoutes = []struct{ method, path string }{
	{"GET", "/api/auth2fa"}, {"POST", "/api/auth2fa/totp/setup"},
	{"POST", "/api/auth2fa/totp/enable"},
	{"POST", "/api/auth2fa/email/enable"},
	{"POST", "/api/auth2fa/email/test"},
	{"POST", "/api/auth2fa/disable"},
	{"POST", "/api/logout"},
	{"GET", "/api/me"},
	{"PUT", "/api/me/password"},
	{"GET", "/api/admins"},
	{"POST", "/api/admins"},
	{"PUT", "/api/admins/1"},
	{"DELETE", "/api/admins/1"},
	{"GET", "/api/status"},
	{"GET", "/api/inbounds"},
	{"POST", "/api/inbounds"},
	{"PUT", "/api/inbounds/1"},
	{"DELETE", "/api/inbounds/1"},
	{"GET", "/api/inbounds/1/export"},
	{"GET", "/api/inbounds/1/check"},
	{"GET", "/api/export/catalog"},
	{"POST", "/api/export/config"},
	{"GET", "/api/clients"},
	{"POST", "/api/clients"},
	{"PUT", "/api/clients/1"},
	{"DELETE", "/api/clients/1"},
	{"PUT", "/api/clients/1/inbounds"},
	{"POST", "/api/clients/1/reset-token"},
	{"POST", "/api/config/diff"},
	{"POST", "/api/external-nodes/import-url"},
	{"GET", "/api/external-nodes"},
	{"POST", "/api/external-nodes"},
	{"DELETE", "/api/external-nodes/1"},
	{"POST", "/api/import"},
	{"POST", "/api/apply"},
	{"POST", "/api/apply/dry-run"},
	{"POST", "/api/rollback"},
	{"POST", "/api/switch-core"},
	{"GET", "/api/revisions"},
	{"GET", "/api/logs"},
	{"GET", "/api/settings"},
	{"PUT", "/api/settings"},
	{"GET", "/api/audit"},
	{"DELETE", "/api/audit"},
	{"GET", "/api/traffic"},
	{"GET", "/api/traffic/daily"},
	{"GET", "/api/traffic/live"},
	{"POST", "/api/tools/reality-keypair"},
	{"GET", "/api/tools/uuid"},
	{"GET", "/api/version"},
	{"POST", "/api/self-update"},
	{"GET", "/api/components"},
	{"GET", "/api/updates/versions"},
	{"POST", "/api/cores/singbox/update"},
	{"POST", "/api/cores/singbox/rollback"},
	{"GET", "/api/panel/backups"},
	{"POST", "/api/panel/rollback"},
	{"GET", "/api/certs"},
	{"POST", "/api/certs/issue"},
}

func newAuthTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st, nil, nil, dir)
}

// TestEveryProtectedRouteRejectsAnonymous 匿名访问所有受保护端点都必须 401。
//
// 这条测试的价值在「新增路由时忘了包 auth」——那种漏洞在功能测试里
// 完全看不出来，接口照样 200，只是谁都能调。
func TestEveryProtectedRouteRejectsAnonymous(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()

	for _, r := range protectedRoutes {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			req := httptest.NewRequest(r.method, r.path, strings.NewReader("{}"))
			req.RemoteAddr = "203.0.113.20:1234"
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code == http.StatusNotFound || w.Code == http.StatusMethodNotAllowed {
				t.Fatalf("%s %s 未注册（404/405）——路由表与测试不同步", r.method, r.path)
			}
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s 匿名访问应 401，实际 %d: %s", r.method, r.path, w.Code, w.Body.String())
			}
		})
	}
}

// TestPublicRoutesStayOpen 免认证端点不能被误伤。
func TestPublicRoutesStayOpen(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()

	cases := []struct{ method, path string }{
		{"GET", "/healthz"},
		{"GET", "/api/public/info"},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(c.method, c.path, nil))
		if w.Code == http.StatusUnauthorized {
			t.Errorf("%s %s 应免认证，实际 401", c.method, c.path)
		}
		if w.Code != http.StatusOK {
			t.Errorf("%s %s 应 200，实际 %d", c.method, c.path, w.Code)
		}
	}
}

// TestAuthRejectsBadTokens 各种不合法 token 一律 401，且不能泄漏细节。
func TestAuthRejectsBadTokens(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()

	cases := []struct{ name, header string }{
		{"空token", "Bearer "},
		{"随机token", "Bearer deadbeefdeadbeefdeadbeefdeadbeef"},
		{"缺Bearer前缀", "deadbeef"},
		{"Basic认证", "Basic YWRtaW46YWRtaW4="},
		{"只有空格", "Bearer    "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/settings", nil)
			req.Header.Set("Authorization", c.header)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("应 401，实际 %d", w.Code)
			}
		})
	}
}

// TestAuthRejectsExpiredSession 会话过期后必须失效。
func TestAuthRejectsExpiredSession(t *testing.T) {
	a := newAuthTestServer(t)
	tok := loginForToken(t, a)

	// 手动把会话改成已过期（模拟 24 小时后的状态）
	v, ok := a.sessions.Load(tok)
	if !ok {
		t.Fatal("登录后应存在会话")
	}
	s := v.(session)
	s.exp = time.Now().Add(-time.Second)
	a.sessions.Store(tok, s)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/settings", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	a.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("过期会话应 401，实际 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "expired") {
		t.Errorf("应提示会话过期，实际: %s", w.Body.String())
	}
}

// TestAuthInvalidatedByPasswordChange 改密后旧会话立即失效（密码指纹机制）。
//
// 这是「密码泄漏后改密能踢掉攻击者会话」的唯一保障——
// 会话存在内存里，改密不会逐个清理，只能靠指纹比对。
func TestAuthInvalidatedByPasswordChange(t *testing.T) {
	a := newAuthTestServer(t)
	tok := loginForToken(t, a)

	// 改密前可用
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/settings", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	a.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("改密前应可用，实际 %d: %s", w.Code, w.Body.String())
	}

	if err := authn.SetPassword(a.Store, "brand-new-password-2026"); err != nil {
		t.Fatal(err)
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/settings", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	a.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("改密后旧会话必须失效，实际 %d", w.Code)
	}
}

// TestAuthAcceptsValidSession 正向用例，避免上面全是"永远 401"的假绿。
func TestAuthAcceptsValidSession(t *testing.T) {
	a := newAuthTestServer(t)
	tok := loginForToken(t, a)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/settings", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	a.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("有效会话应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
}

// TestAuthResponseDoesNotLeakSessionInternals 401 响应不能回显 token 或会话细节。
func TestAuthResponseDoesNotLeakSessionInternals(t *testing.T) {
	a := newAuthTestServer(t)
	const bad = "0123456789abcdef0123456789abcdef"
	req := httptest.NewRequest("GET", "/api/settings", nil)
	req.Header.Set("Authorization", "Bearer "+bad)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	if strings.Contains(w.Body.String(), bad) {
		t.Fatalf("响应体回显了 token: %s", w.Body.String())
	}
}

// TestLogoutDestroysSession 登出必须由服务端销毁会话。
//
// 只清前端 localStorage 是假登出：token 在剩余有效期（24h）内仍可被使用。
// 这条测试锁死"服务端真的删了"。
func TestLogoutDestroysSession(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	// 登出前可用
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/settings", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("登出前应可用，实际 %d", w.Code)
	}

	// 登出
	w = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/api/logout", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("登出应 200，实际 %d: %s", w.Code, w.Body.String())
	}

	// 登出后同一个 token 必须失效
	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/settings", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("登出后 token 应失效，实际 %d", w.Code)
	}

	// 必须留痕
	list, err := a.Store.ListAudit(store.AuditFilter{Prefix: AuditLogout})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("应有 1 条登出审计，实际 %d", len(list))
	}
	if list[0].Result != store.AuditOK {
		t.Fatalf("登出审计结果应为 ok: %+v", list[0])
	}
}

// TestLogoutDoesNotAffectOtherSessions 登出只销毁当前会话，
// 不能把同一账号在别的设备上的会话一起踢掉。
func TestLogoutDoesNotAffectOtherSessions(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()

	tokA := loginForToken(t, a) // 设备 A
	tokB := loginForToken(t, a) // 设备 B

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/logout", nil)
	req.Header.Set("Authorization", "Bearer "+tokA)
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("登出应 200，实际 %d", w.Code)
	}

	// A 已失效
	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/settings", nil)
	req.Header.Set("Authorization", "Bearer "+tokA)
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("设备 A 的 token 应失效，实际 %d", w.Code)
	}
	// B 不受影响
	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/settings", nil)
	req.Header.Set("Authorization", "Bearer "+tokB)
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("设备 B 的会话不应被登出影响，实际 %d", w.Code)
	}
}
