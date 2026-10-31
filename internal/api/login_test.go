package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"triune/internal/store"
)

func TestLoginLockout(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := New(st, nil, nil, dir)

	doLogin := func() int {
		body, _ := json.Marshal(map[string]string{"user": "admin", "pass": "wrong"})
		req := httptest.NewRequest("POST", "/api/login", bytes.NewReader(body))
		req.RemoteAddr = "10.1.1.1:55555"
		w := httptest.NewRecorder()
		a.handleLogin(w, req)
		return w.Code
	}

	for i := 1; i <= 4; i++ {
		if c := doLogin(); c != http.StatusUnauthorized {
			t.Fatalf("attempt %d: want 401, got %d", i, c)
		}
	}
	if c := doLogin(); c != http.StatusUnauthorized {
		t.Fatalf("attempt 5: want 401 (last allowed), got %d", c)
	}
	for i := 6; i <= 9; i++ {
		if c := doLogin(); c != http.StatusTooManyRequests {
			t.Fatalf("attempt %d: want 429 locked, got %d", i, c)
		}
	}

	// 不同来源不受影响
	body, _ := json.Marshal(map[string]string{"user": "admin", "pass": "wrong"})
	req := httptest.NewRequest("POST", "/api/login", bytes.NewReader(body))
	req.RemoteAddr = "10.1.1.2:55555"
	w := httptest.NewRecorder()
	a.handleLogin(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("other ip: want 401, got %d", w.Code)
	}
}

// loginOnce 用给定账密登录一次，返回响应 JSON。
func loginOnce(t *testing.T, a *Server, user, pass string) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"user": user, "pass": pass})
	req := httptest.NewRequest("POST", "/api/login", bytes.NewReader(body))
	req.RemoteAddr = "203.0.113.9:44444"
	w := httptest.NewRecorder()
	a.handleLogin(w, req)
	if w.Code != 200 {
		t.Fatalf("login: want 200, got %d: %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestLoginMustChangeAndLastLogin：默认密码登录返回 must_change=true 并带
// 上次登录信息；改密后 must_change 消失；最近登录 IP 被记录。
func TestLoginMustChangeAndLastLogin(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := New(st, nil, nil, dir)

	// 第一次登录（默认 admin/admin）：must_change=true，无上次登录
	j := loginOnce(t, a, "admin", "admin")
	if j["must_change"] != true {
		t.Fatalf("first login: want must_change=true, got %v", j["must_change"])
	}
	if j["last_ip"] != "" {
		t.Fatalf("first login: want empty last_ip, got %v", j["last_ip"])
	}

	// 第二次登录：上次登录信息应为本测试的来源 IP
	j = loginOnce(t, a, "admin", "admin")
	if j["last_ip"] != "203.0.113.9" {
		t.Fatalf("second login: want last_ip=203.0.113.9, got %v", j["last_ip"])
	}

	// 改密后 must_change 消失
	if err := st.SetSetting("admin_pass", "new-secret-pass"); err != nil {
		t.Fatal(err)
	}
	j = loginOnce(t, a, "admin", "new-secret-pass")
	if j["must_change"] != false {
		t.Fatalf("after change: want must_change=false, got %v", j["must_change"])
	}
}
