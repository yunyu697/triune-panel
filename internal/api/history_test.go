package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"triune/internal/store"
)

// loginHistoryOf 拉登录历史并按用户名取一条。
func loginHistoryOf(t *testing.T, a *Server, tok, query, user string) (map[string]any, map[string]any) {
	t.Helper()
	w := do(a, tok, "GET", "/api/login-history"+query, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/login-history%s: want 200, got %d (%s)", query, w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	items, _ := resp["items"].([]any)
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m["user"] == user {
			return m, resp
		}
	}
	return nil, resp
}

// TestLoginHistoryAggregatesByUser 成功与失败按人聚到一起，来源 IP 只算成功的那次。
func TestLoginHistoryAggregatesByUser(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")

	// guest 先输错两次，再成功一次
	for i := 0; i < 2; i++ {
		if w := do(a, "", "POST", "/api/login",
			map[string]any{"user": "guest", "pass": "wrong-password"}); w.Code != http.StatusUnauthorized {
			t.Fatalf("错误密码应 401，实际 %d", w.Code)
		}
	}
	loginAs(t, a, "guest", "guest-password-1")

	h, _ := loginHistoryOf(t, a, owner, "?days=1", "guest")
	if h == nil {
		t.Fatal("登录历史里找不到 guest")
	}
	if h["ok"] != float64(1) || h["fail"] != float64(2) {
		t.Fatalf("guest 应 1 成功 2 失败，实际 ok=%v fail=%v", h["ok"], h["fail"])
	}
	if h["last_ok"] == "" || h["last_fail"] == "" {
		t.Fatalf("应同时有最近成功与最近失败时间，实际 %v", h)
	}
	// 来源 IP 只统计成功登录：两次失败用的是 do() 的 203.0.113.30，
	// 成功走 loginAs（203.0.113.9），列表里只应出现后者。
	ips, _ := h["ips"].([]any)
	if len(ips) != 1 || ips[0] != "203.0.113.9" {
		t.Fatalf("来源 IP 应只有成功登录的那一个，实际 %v", ips)
	}
	events, _ := h["events"].([]any)
	if len(events) != 3 {
		t.Fatalf("时间线应有 3 条，实际 %d", len(events))
	}
	// 时间线倒序：最新一条（成功）在最前
	first, _ := events[0].(map[string]any)
	if first["result"] != store.AuditOK {
		t.Fatalf("时间线应倒序，最新一条应是成功，实际 %v", first)
	}
}

// TestLoginHistoryViewerSeesOnlySelf 只读账号只看自己的登录记录。
func TestLoginHistoryViewerSeesOnlySelf(t *testing.T) {
	a := newRoleTestServer(t)
	loginAs(t, a, "admin", "admin")
	guest := loginAs(t, a, "guest", "guest-password-1")

	w := do(a, guest, "GET", "/api/login-history?days=7", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	items, _ := resp["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("viewer 应只看到自己的 1 条记录，实际 %d: %s", len(items), w.Body.String())
	}
	m, _ := items[0].(map[string]any)
	if m["user"] != "guest" {
		t.Fatalf("viewer 看到的应是自己的记录，实际 %v", m["user"])
	}
	// 即使显式指定别人也无效
	w = do(a, guest, "GET", "/api/login-history?days=7&user=admin", nil)
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	items, _ = resp["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("viewer 不能通过 user 参数越权查看，实际 %d 条", len(items))
	}
}

// TestLoginHistoryRespectsDays 时间窗外的记录不出现，窗内的出现。
//
// 直接往审计里写一条 10 天前的登录：days=1 查不到，days=30 查得到。
func TestLoginHistoryRespectsDays(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")

	old := time.Now().AddDate(0, 0, -10).Format(store.AuditTimeLayout)
	if err := a.Store.AddAudit(store.AuditEntry{
		Time: old, Actor: "op", Action: AuditLogin, Result: store.AuditOK,
		IP: "198.51.100.9", Detail: "登录成功",
	}); err != nil {
		t.Fatal(err)
	}

	if h, _ := loginHistoryOf(t, a, owner, "?days=1", "op"); h != nil {
		t.Fatalf("days=1 不该出现 10 天前的记录，实际 %v", h)
	}
	h, _ := loginHistoryOf(t, a, owner, "?days=30", "op")
	if h == nil {
		t.Fatal("days=30 应包含 10 天前的记录")
	}
	ips, _ := h["ips"].([]any)
	if len(ips) != 1 || ips[0] != "198.51.100.9" {
		t.Fatalf("来源 IP 应为 198.51.100.9，实际 %v", ips)
	}
}

// TestLoginHistoryCapsDays 天数上限跟审计保留期一致（90 天），不能无限回溯。
func TestLoginHistoryCapsDays(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")
	w := do(a, owner, "GET", "/api/login-history?days=99999", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["days"] != float64(store.AuditKeepDays) {
		t.Fatalf("days 应被夹到 %d，实际 %v", store.AuditKeepDays, resp["days"])
	}
}
