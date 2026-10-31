package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"triune/internal/authn"
	"triune/internal/store"
)

// listSessions 以给定 token 拉登录设备列表。
func listSessions(t *testing.T, a *Server, tok string) []map[string]any {
	t.Helper()
	w := do(a, tok, "GET", "/api/sessions", nil)
	if w.Code != 200 {
		t.Fatalf("GET /api/sessions: want 200, got %d (%s)", w.Code, w.Body.String())
	}
	var out []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestSessionsListNeverLeaksToken 列表里只能有派生 sid，绝不能出现会话凭据本身。
//
// 拿到 token 就等于拿到这个账号——把它发给前端（哪怕只是「管理员」看）等于
// 让任何能瞄一眼屏幕的人都获得该账号的完整权限。
func TestSessionsListNeverLeaksToken(t *testing.T) {
	a := newRoleTestServer(t)
	tok := loginAs(t, a, "admin", "admin")

	w := do(a, tok, "GET", "/api/sessions", nil)
	if w.Code != 200 {
		t.Fatalf("want 200, got %d", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, tok) {
		t.Fatalf("响应里出现了会话凭据: %s", body)
	}
	var list []map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) != 1 {
		t.Fatalf("应有 1 个会话，实际 %d", len(list))
	}
	id, _ := list[0]["id"].(string)
	if len(id) != 16 {
		t.Fatalf("sid 应为 16 位十六进制，实际 %q", id)
	}
	if id == tok {
		t.Fatal("sid 不能等于 token")
	}
	if list[0]["current"] != true {
		t.Fatal("当前会话应标记 current=true")
	}
}

// TestSessionsViewerSeesOnlySelf 只读账号只看得到自己的会话。
//
// 别人什么时候从哪个 IP 登录，本身就是情报；角色不足就不该知道。
func TestSessionsViewerSeesOnlySelf(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")
	op := loginAs(t, a, "op", "op-password-1")
	guest := loginAs(t, a, "guest", "guest-password-1")

	if n := len(listSessions(t, a, owner)); n != 3 {
		t.Fatalf("owner 应看到 3 个会话，实际 %d", n)
	}
	if n := len(listSessions(t, a, op)); n != 3 {
		t.Fatalf("admin 应看到 3 个会话，实际 %d", n)
	}
	own := listSessions(t, a, guest)
	if len(own) != 1 {
		t.Fatalf("viewer 应只看到自己的 1 个会话，实际 %d", len(own))
	}
	if own[0]["user"] != "guest" {
		t.Fatalf("viewer 看到的应是自己的会话，实际 %v", own[0]["user"])
	}
}

// TestSessionRevokeCannotKickOwner admin 不能踢 owner 的会话——
// 否则一个被钓鱼的管理员就能把超级管理员锁在门外。
func TestSessionRevokeCannotKickOwner(t *testing.T) {
	a := newRoleTestServer(t)
	loginAs(t, a, "admin", "admin") // owner 的会话
	op := loginAs(t, a, "op", "op-password-1")

	var ownerSID string
	for _, s := range listSessions(t, a, op) {
		if s["user"] == "admin" {
			ownerSID, _ = s["id"].(string)
			if s["revocable"] != false {
				t.Fatalf("admin 眼中 owner 的会话应 revocable=false，实际 %v", s["revocable"])
			}
		}
	}
	if ownerSID == "" {
		t.Fatal("没找到 owner 的会话")
	}
	w := do(a, op, "DELETE", "/api/sessions/"+ownerSID, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("admin 踢 owner 应 403，实际 %d (%s)", w.Code, w.Body.String())
	}
	// owner 的会话仍在（列表里还能看到）
	if n := len(listSessions(t, a, op)); n != 2 {
		t.Fatalf("会话数不应变化，实际 %d", n)
	}
}

// TestSessionRevokeByOwner owner 可以踢任何人，且被踢的 token 立刻失效。
func TestSessionRevokeByOwner(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")
	op := loginAs(t, a, "op", "op-password-1")

	var opSID string
	for _, s := range listSessions(t, a, owner) {
		if s["user"] == "op" {
			opSID, _ = s["id"].(string)
			if s["revocable"] != true {
				t.Fatalf("owner 眼中 op 的会话应 revocable=true，实际 %v", s["revocable"])
			}
		}
	}
	if opSID == "" {
		t.Fatal("没找到 op 的会话")
	}
	if w := do(a, owner, "DELETE", "/api/sessions/"+opSID, nil); w.Code != 200 {
		t.Fatalf("owner 踢 admin 应 200，实际 %d (%s)", w.Code, w.Body.String())
	}
	if w := do(a, op, "GET", "/api/version", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("被踢的 token 应立即失效，实际 %d", w.Code)
	}
	// 必须留痕：谁踢了谁
	list, err := a.Store.ListAudit(store.AuditFilter{Prefix: AuditSession})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Actor != "admin" || list[0].Target != "op" {
		t.Fatalf("吊销会话应留痕（actor=admin, target=op），实际 %+v", list)
	}
}

// TestSessionRevokeOthers 「退出其他设备」只踢别的，自己还能用。
func TestSessionRevokeOthers(t *testing.T) {
	a := newRoleTestServer(t)
	first := loginAs(t, a, "op", "op-password-1")
	second := loginAs(t, a, "op", "op-password-1")

	if n := len(listSessions(t, a, second)); n != 2 {
		t.Fatalf("同一账号两次登录应有 2 个会话，实际 %d", n)
	}
	w := do(a, second, "DELETE", "/api/sessions", nil)
	if w.Code != 200 {
		t.Fatalf("want 200, got %d (%s)", w.Code, w.Body.String())
	}
	var j map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &j)
	if j["revoked"] != float64(1) {
		t.Fatalf("应吊销 1 个会话，实际 %v", j["revoked"])
	}
	if w := do(a, second, "GET", "/api/version", nil); w.Code != 200 {
		t.Fatalf("当前会话不该被踢，实际 %d", w.Code)
	}
	if w := do(a, first, "GET", "/api/version", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("其他设备的会话应失效，实际 %d", w.Code)
	}
}

// TestSessionRevokeSelf viewer 也能踢自己的会话（isSelf 放行）。
func TestSessionRevokeSelf(t *testing.T) {
	a := newRoleTestServer(t)
	loginAs(t, a, "guest", "guest-password-1") // 设备 A
	b := loginAs(t, a, "guest", "guest-password-1")

	var otherSID string
	for _, s := range listSessions(t, a, b) {
		if s["current"] == false {
			otherSID, _ = s["id"].(string)
		}
	}
	if otherSID == "" {
		t.Fatal("没找到另一个会话")
	}
	if w := do(a, b, "DELETE", "/api/sessions/"+otherSID, nil); w.Code != 200 {
		t.Fatalf("viewer 踢自己的会话应 200，实际 %d (%s)", w.Code, w.Body.String())
	}
}

// TestPasswordChangePurgesSessions 改密后该账号的会话从列表里消失（不只是 401）。
func TestPasswordChangePurgesSessions(t *testing.T) {
	a := newRoleTestServer(t)
	op := loginAs(t, a, "op", "op-password-1")
	loginAs(t, a, "op", "op-password-1")
	owner := loginAs(t, a, "admin", "admin")

	if n := len(listSessions(t, a, owner)); n != 3 {
		t.Fatalf("改密前应有 3 个会话，实际 %d", n)
	}
	u, _ := a.Store.GetAdminByName("op")
	if err := authn.SetPasswordFor(a.Store, u.ID, "op-new-password-2"); err != nil {
		t.Fatal(err)
	}
	// 直接调接口验证「改密会清理」这条路径
	op2 := loginAs(t, a, "op", "op-new-password-2")
	if w := do(a, op2, "PUT", "/api/me/password", map[string]string{
		"old": "op-new-password-2", "new": "op-password-3",
	}); w.Code != 200 {
		t.Fatalf("改密应成功，实际 %d (%s)", w.Code, w.Body.String())
	}
	// op 的会话（含刚登录的 op2）应全部消失，只剩 owner 那一个
	list := listSessions(t, a, owner)
	if len(list) != 1 {
		t.Fatalf("改密后应只剩 owner 的 1 个会话，实际 %d: %v", len(list), list)
	}
	if w := do(a, op, "GET", "/api/version", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("旧 token 应失效，实际 %d", w.Code)
	}
}

// TestDescribeUA UA 压缩成「浏览器 · 系统」，一眼能看出是不是自己的设备。
func TestDescribeUA(t *testing.T) {
	cases := []struct{ ua, want string }{
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36", "Chrome · macOS"},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36 Edg/120.0", "Edge · Windows"},
		{"Mozilla/5.0 (X11; Linux x86_64; rv:121.0) Gecko/20100101 Firefox/121.0", "Firefox · Linux"},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Version/17.0 Mobile/15E148 Safari/604.1", "Safari · iOS"},
		{"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 Chrome/120.0 Mobile Safari/537.36", "Chrome · Android"},
		{"curl/8.4.0", "命令行 · curl"},
		{"python-requests/2.31.0", "脚本 · Python"},
		{"Go-http-client/1.1", "脚本 · Go"},
		{"", "未知设备"},
		{"SomethingWeird/1.0", "未知设备"},
	}
	for _, c := range cases {
		if got := describeUA(c.ua); got != c.want {
			t.Errorf("describeUA(%q) = %q, want %q", c.ua, got, c.want)
		}
	}
}

// TestSessionSIDStable sid 由 token 派生：同 token 稳定，不同 token 不同。
func TestSessionSIDStable(t *testing.T) {
	if sessionSID("abc") != sessionSID("abc") {
		t.Fatal("同一 token 的 sid 应稳定")
	}
	if sessionSID("abc") == sessionSID("abd") {
		t.Fatal("不同 token 的 sid 应不同")
	}
	if len(sessionSID("abc")) != 16 {
		t.Fatalf("sid 应为 16 位，实际 %q", sessionSID("abc"))
	}
}

// TestSessionStoreConcurrent 并发读写不 panic（配合 -race 才真正有意义）。
func TestSessionStoreConcurrent(t *testing.T) {
	s := newSessionStore()
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			tok := "tok-" + string(rune('a'+i))
			s.Add(&session{Token: tok, SID: sessionSID(tok), UID: int64(i)})
			for j := 0; j < 50; j++ {
				_, _ = s.Get(tok)
				s.Touch(tok, time.Now().Add(time.Duration(j)*time.Second))
				_ = s.List()
			}
			s.DeleteByUID(int64(i))
		}(i)
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	if n := s.Count(); n != 0 {
		t.Fatalf("全部删除后应为 0，实际 %d", n)
	}
}
