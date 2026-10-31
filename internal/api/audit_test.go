package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"triune/internal/store"
)

// loginForToken 登录拿 token（默认密码），顺便产生一条审计。
func loginForToken(t *testing.T, a *Server) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"user": "admin", "pass": "admin"})
	req := httptest.NewRequest("POST", "/api/login", bytes.NewReader(body))
	req.RemoteAddr = "203.0.113.5:1234"
	w := httptest.NewRecorder()
	a.handleLogin(w, req)
	if w.Code != 200 {
		t.Fatalf("login: want 200, got %d: %s", w.Code, w.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return out["token"].(string)
}

func TestAuditLoginRecorded(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := New(st, nil, nil, dir)

	// 失败一次
	bad, _ := json.Marshal(map[string]string{"user": "admin", "pass": "wrong"})
	req := httptest.NewRequest("POST", "/api/login", bytes.NewReader(bad))
	req.RemoteAddr = "198.51.100.7:9999"
	w := httptest.NewRecorder()
	a.handleLogin(w, req)
	if w.Code != 401 {
		t.Fatalf("want 401, got %d", w.Code)
	}
	// 成功一次
	loginForToken(t, a)

	list, err := st.ListAudit(store.AuditFilter{Prefix: AuditLogin})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 login entries, got %d: %+v", len(list), list)
	}
	// 倒序：最新的是成功
	if list[0].Result != store.AuditOK || list[1].Result != store.AuditFail {
		t.Fatalf("结果顺序不对: %+v", list)
	}
	// 来源 IP 必须记下来（反代下也要是真实客户端，不能是 127.0.0.1）
	if list[1].IP != "198.51.100.7" {
		t.Fatalf("失败记录 IP 不对: %q", list[1].IP)
	}
	if list[0].IP != "203.0.113.5" {
		t.Fatalf("成功记录 IP 不对: %q", list[0].IP)
	}
	// 审计里绝不能出现密码明文
	for _, e := range list {
		if e.Detail == "wrong" || e.Target == "wrong" {
			t.Fatalf("审计泄露了密码: %+v", e)
		}
	}
}

func TestAuditSettingsRecordsKeysNotValues(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := New(st, nil, nil, dir)
	tok := loginForToken(t, a)

	body, _ := json.Marshal(map[string]string{
		"smtp_pass":    "super-secret-value",
		"public_host":  "panel.example.com",
		"stats_secret": "another-secret",
	})
	req := httptest.NewRequest("PUT", "/api/settings", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	a.putSettings(w, req)
	if w.Code != 200 {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}

	list, err := st.ListAudit(store.AuditFilter{Prefix: AuditSettings})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("want 1 settings entry, got %d", len(list))
	}
	e := list[0]
	// 只记键名
	if e.Target != "public_host,smtp_pass,stats_secret" {
		t.Fatalf("应记录排序后的键名，got %q", e.Target)
	}
	// 绝不能把值写进去
	if bytes.Contains([]byte(e.Detail+e.Target), []byte("super-secret-value")) ||
		bytes.Contains([]byte(e.Detail+e.Target), []byte("another-secret")) {
		t.Fatalf("审计泄露了设置值: %+v", e)
	}
}

func TestAuditEndpointRequiresAuthAndLists(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := New(st, nil, nil, dir)
	h := a.Handler()

	// 未带 token → 401
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/audit", nil))
	if w.Code != 401 {
		t.Fatalf("未认证应 401, got %d", w.Code)
	}

	tok := loginForToken(t, a)
	w = httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/audit?prefix=login&limit=10", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var list []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("响应不是数组: %v: %s", err, w.Body.String())
	}
	if len(list) != 1 {
		t.Fatalf("want 1 entry, got %d", len(list))
	}
}

// TestAuditClearLeavesTrace 清空审计后必须留下「谁清的」这一条。
func TestAuditClearLeavesTrace(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := New(st, nil, nil, dir)
	tok := loginForToken(t, a)

	req := httptest.NewRequest("DELETE", "/api/audit", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.RemoteAddr = "203.0.113.9:1111"
	w := httptest.NewRecorder()
	a.handleAuditClear(w, req)
	if w.Code != 200 {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}

	list, _ := st.ListAudit(store.AuditFilter{})
	if len(list) != 1 {
		t.Fatalf("清空后应只剩清空记录, got %d: %+v", len(list), list)
	}
	if list[0].Action != "audit.clear" {
		t.Fatalf("want audit.clear, got %q", list[0].Action)
	}
	if list[0].IP != "203.0.113.9" {
		t.Fatalf("清空记录 IP 不对: %q", list[0].IP)
	}
}
