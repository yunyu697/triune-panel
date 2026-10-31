package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"triune/internal/authn"
	"triune/internal/store"
)

// loginAs 用指定账号登录，返回 token。
func loginAs(t *testing.T, a *Server, user, pass string) string {
	t.Helper()
	j := loginOnce(t, a, user, pass)
	tok, _ := j["token"].(string)
	if tok == "" {
		t.Fatalf("%s 登录未拿到 token: %v", user, j)
	}
	return tok
}

// do 以给定 token 发请求（token 为空 = 匿名）。
func do(a *Server, tok, method, path string, body any) *httptest.ResponseRecorder {
	var r *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		r = httptest.NewRequest(method, path, bytes.NewReader(b))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	r.RemoteAddr = "203.0.113.30:5555"
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}

// newRoleTestServer 建一个带三个角色的面板：owner(admin/admin)、
// op(admin)、guest(viewer)。
func newRoleTestServer(t *testing.T) *Server {
	t.Helper()
	a := newAuthTestServer(t)
	if _, err := authn.CreateUser(a.Store, "op", "op-password-1", authn.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if _, err := authn.CreateUser(a.Store, "guest", "guest-password-1", authn.RoleViewer); err != nil {
		t.Fatal(err)
	}
	return a
}

// TestRoleMatrix 逐条锁死「哪个角色能碰哪个端点」。
//
// 这张表是权限模型的可执行文档：新增端点时必须在这里表态，
// 否则「忘了包 write()」这种漏洞没人能发现——接口照样 200。
func TestRoleMatrix(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")
	op := loginAs(t, a, "op", "op-password-1")
	guest := loginAs(t, a, "guest", "guest-password-1")

	cases := []struct {
		name         string
		method, path string
		body         any
		denied       []string // 期望 403 的角色名
	}{
		{name: "只读状态", method: "GET", path: "/api/version"},
		{name: "只读入站", method: "GET", path: "/api/inbounds"},
		{name: "只读流量", method: "GET", path: "/api/traffic"},
		{name: "新建入站", method: "POST", path: "/api/inbounds", body: map[string]any{}, denied: []string{"guest"}},
		{name: "客户端列表", method: "GET", path: "/api/clients", denied: []string{"guest"}},
		{name: "设置", method: "GET", path: "/api/settings", denied: []string{"guest"}},
		{name: "审计", method: "GET", path: "/api/audit", denied: []string{"guest"}},
		{name: "导出目录", method: "GET", path: "/api/export/catalog", denied: []string{"guest"}},
		{name: "管理员列表", method: "GET", path: "/api/admins", denied: []string{"guest", "op"}},
		{name: "新建管理员", method: "POST", path: "/api/admins", body: map[string]any{}, denied: []string{"guest", "op"}},
		{name: "清空审计", method: "DELETE", path: "/api/audit", denied: []string{"guest", "op"}},
		{name: "面板回滚", method: "POST", path: "/api/panel/rollback", body: map[string]any{}, denied: []string{"guest", "op"}},
	}

	tokens := map[string]string{"owner": owner, "op": op, "guest": guest}
	for _, c := range cases {
		for name, tok := range tokens {
			wantDeny := false
			for _, d := range c.denied {
				if d == name {
					wantDeny = true
				}
			}
			w := do(a, tok, c.method, c.path, c.body)
			if wantDeny && w.Code != http.StatusForbidden {
				t.Errorf("%s [%s]: 应 403，实际 %d (%s)", c.name, name, w.Code, w.Body.String())
			}
			if !wantDeny && w.Code == http.StatusForbidden {
				t.Errorf("%s [%s]: 不应 403，实际被拒: %s", c.name, name, w.Body.String())
			}
		}
	}
}

// TestMeReturnsIdentity /api/me 必须如实反映当前身份，前端据此显隐。
func TestMeReturnsIdentity(t *testing.T) {
	a := newRoleTestServer(t)
	for _, c := range []struct{ user, pass, role string }{
		{"admin", "admin", authn.RoleOwner},
		{"op", "op-password-1", authn.RoleAdmin},
		{"guest", "guest-password-1", authn.RoleViewer},
	} {
		tok := loginAs(t, a, c.user, c.pass)
		w := do(a, tok, "GET", "/api/me", nil)
		if w.Code != 200 {
			t.Fatalf("%s: want 200, got %d", c.user, w.Code)
		}
		var j map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &j)
		if j["user"] != c.user || j["role"] != c.role {
			t.Fatalf("%s: 身份不对: %v", c.user, j)
		}
	}
}

// TestAuditRecordsRealActor 审计里的 Actor 必须是真实操作人。
//
// v0.9.6 之前永远是同一个 admin——「谁改的」这件事实际上无法追溯。
func TestAuditRecordsRealActor(t *testing.T) {
	a := newRoleTestServer(t)
	op := loginAs(t, a, "op", "op-password-1")

	w := do(a, op, "POST", "/api/inbounds", map[string]any{
		"name": "hk-01", "core": "singbox", "protocol": "vless", "port": 443,
	})
	if w.Code != 200 {
		t.Fatalf("创建入站应成功，实际 %d: %s", w.Code, w.Body.String())
	}

	list, err := a.Store.ListAudit(store.AuditFilter{Prefix: AuditInboundCreate})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("应有 1 条入站创建审计，实际 %d", len(list))
	}
	if list[0].Actor != "op" {
		t.Fatalf("Actor 应为 op，实际 %q", list[0].Actor)
	}
}

// TestRbacDenyAudited 越权尝试必须留痕：只读账号反复调写接口通常不是手滑。
func TestRbacDenyAudited(t *testing.T) {
	a := newRoleTestServer(t)
	guest := loginAs(t, a, "guest", "guest-password-1")

	if w := do(a, guest, "POST", "/api/inbounds", map[string]any{}); w.Code != http.StatusForbidden {
		t.Fatalf("viewer 写操作应 403，实际 %d", w.Code)
	}
	list, err := a.Store.ListAudit(store.AuditFilter{Prefix: AuditDeny})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Actor != "guest" {
		t.Fatalf("应有 1 条 guest 的越权审计，实际 %+v", list)
	}
	if !strings.Contains(list[0].Detail, "viewer") {
		t.Fatalf("审计详情应写明角色，实际 %q", list[0].Detail)
	}
}

// TestDisabledAdminSessionDiesImmediately 停用账号后，它已经登录的会话
// 必须在下一个请求就失效——否则「停用」等于空操作。
func TestDisabledAdminSessionDiesImmediately(t *testing.T) {
	a := newRoleTestServer(t)
	op := loginAs(t, a, "op", "op-password-1")
	if w := do(a, op, "GET", "/api/version", nil); w.Code != 200 {
		t.Fatalf("停用前应可用，实际 %d", w.Code)
	}

	u, err := a.Store.GetAdminByName("op")
	if err != nil {
		t.Fatal(err)
	}
	u.Enabled = false
	if err := a.Store.UpdateAdmin(u); err != nil {
		t.Fatal(err)
	}
	if w := do(a, op, "GET", "/api/version", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("停用后会话应失效，实际 %d", w.Code)
	}
}

// TestDeletedAdminSessionDiesImmediately 删除账号同理。
func TestDeletedAdminSessionDiesImmediately(t *testing.T) {
	a := newRoleTestServer(t)
	guest := loginAs(t, a, "guest", "guest-password-1")
	u, _ := a.Store.GetAdminByName("guest")
	if err := a.Store.DeleteAdmin(u.ID); err != nil {
		t.Fatal(err)
	}
	if w := do(a, guest, "GET", "/api/version", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("账号删除后会话应失效，实际 %d", w.Code)
	}
}

// TestPasswordChangeKicksOnlyThatAccount 改密只踢自己，不能连累别人。
//
// 这是从「全局 session_epoch」换成「每账号密码指纹」的核心动机：
// 多管理员下一个人的密码变更不该让全站掉线。
func TestPasswordChangeKicksOnlyThatAccount(t *testing.T) {
	a := newRoleTestServer(t)
	op := loginAs(t, a, "op", "op-password-1")
	guest := loginAs(t, a, "guest", "guest-password-1")

	u, _ := a.Store.GetAdminByName("op")
	if err := authn.SetPasswordFor(a.Store, u.ID, "op-new-password-2"); err != nil {
		t.Fatal(err)
	}
	if w := do(a, op, "GET", "/api/version", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("改密后该账号会话应失效，实际 %d", w.Code)
	}
	if w := do(a, guest, "GET", "/api/version", nil); w.Code != 200 {
		t.Fatalf("其他账号的会话不应受影响，实际 %d", w.Code)
	}
}

// TestChangeOwnPasswordRequiresOld 改自己密码必须验原密码（防会话劫持后夺号）。
func TestChangeOwnPasswordRequiresOld(t *testing.T) {
	a := newRoleTestServer(t)
	op := loginAs(t, a, "op", "op-password-1")

	if w := do(a, op, "PUT", "/api/me/password", map[string]string{
		"old": "wrong-old", "new": "whatever-1234",
	}); w.Code != http.StatusUnauthorized {
		t.Fatalf("原密码错误应 401，实际 %d (%s)", w.Code, w.Body.String())
	}
	if w := do(a, op, "PUT", "/api/me/password", map[string]string{
		"old": "op-password-1", "new": "op-password-2",
	}); w.Code != 200 {
		t.Fatalf("正确原密码应改密成功，实际 %d (%s)", w.Code, w.Body.String())
	}
	// 改完旧会话失效、新密码可用
	if w := do(a, op, "GET", "/api/version", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("改密后旧 token 应失效，实际 %d", w.Code)
	}
	loginAs(t, a, "op", "op-password-2")
}

// TestAdminCreateGeneratedPassword 新建管理员不填密码时由服务端生成，
// 且生成的密码必须真能用（只在这一次响应里出现）。
func TestAdminCreateGeneratedPassword(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")

	w := do(a, owner, "POST", "/api/admins", map[string]any{
		"username": "dave", "role": "viewer",
	})
	if w.Code != 200 {
		t.Fatalf("新建应成功，实际 %d (%s)", w.Code, w.Body.String())
	}
	var j map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &j)
	pass, _ := j["password"].(string)
	if len(pass) < authn.MinPasswordLen {
		t.Fatalf("应返回生成的密码，实际 %q", pass)
	}
	loginAs(t, a, "dave", pass)

	// 列表里绝不能出现密码哈希
	lw := do(a, owner, "GET", "/api/admins", nil)
	if strings.Contains(lw.Body.String(), "pass_hash") || strings.Contains(lw.Body.String(), pass) {
		t.Fatalf("管理员列表泄漏了凭据: %s", lw.Body.String())
	}
}

// TestAdminUpdateIsPartial 只改角色时，密码必须还在（整行覆盖会让账号被锁死）。
func TestAdminUpdateIsPartial(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")
	u, _ := a.Store.GetAdminByName("guest")

	w := do(a, owner, "PUT", "/api/admins/"+itoa(u.ID), map[string]any{"role": "admin"})
	if w.Code != 200 {
		t.Fatalf("改角色应成功，实际 %d (%s)", w.Code, w.Body.String())
	}
	// 密码仍然有效，且新角色生效
	tok := loginAs(t, a, "guest", "guest-password-1")
	if w := do(a, tok, "GET", "/api/clients", nil); w.Code == http.StatusForbidden {
		t.Fatal("升级为 admin 后不应再被 403")
	}
}

// TestOwnerProtected 超级管理员不能被删除、降级、停用，也不能再新建一个。
func TestOwnerProtected(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")
	u, _ := a.Store.GetAdminByName("admin")

	if w := do(a, owner, "DELETE", "/api/admins/"+itoa(u.ID), nil); w.Code != 400 {
		t.Fatalf("删除 owner 应 400，实际 %d (%s)", w.Code, w.Body.String())
	}
	if w := do(a, owner, "PUT", "/api/admins/"+itoa(u.ID), map[string]any{"role": "admin"}); w.Code != 400 {
		t.Fatalf("降级 owner 应 400，实际 %d (%s)", w.Code, w.Body.String())
	}
	if w := do(a, owner, "PUT", "/api/admins/"+itoa(u.ID), map[string]any{"enabled": false}); w.Code != 400 {
		t.Fatalf("停用 owner 应 400，实际 %d (%s)", w.Code, w.Body.String())
	}
	if w := do(a, owner, "POST", "/api/admins", map[string]any{
		"username": "root2", "password": "root2-password", "role": "owner",
	}); w.Code != 400 {
		t.Fatalf("新建第二个 owner 应 400，实际 %d (%s)", w.Code, w.Body.String())
	}
	// owner 仍然能用
	if w := do(a, owner, "GET", "/api/admins", nil); w.Code != 200 {
		t.Fatalf("owner 应仍可用，实际 %d", w.Code)
	}
}

// TestCannotDeleteOrDisableSelf 自伤保护：删自己/停自己会让面板失去操作者。
func TestCannotDeleteOrDisableSelf(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")
	u, _ := a.Store.GetAdminByName("admin")
	if w := do(a, owner, "DELETE", "/api/admins/"+itoa(u.ID), nil); w.Code != 400 {
		t.Fatalf("删除自己应 400，实际 %d", w.Code)
	}
}

// TestAdminUsernameConflict 改名撞车返回 409，而不是静默覆盖。
func TestAdminUsernameConflict(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")
	u, _ := a.Store.GetAdminByName("guest")
	w := do(a, owner, "PUT", "/api/admins/"+itoa(u.ID), map[string]any{"username": "op"})
	if w.Code != http.StatusConflict {
		t.Fatalf("改名冲突应 409，实际 %d (%s)", w.Code, w.Body.String())
	}
}

// TestSettingsPasswordChangesOnlySelf putSettings 里的 admin_pass 只能改
// 当前账号的密码，不能变成「改别人的」。
func TestSettingsPasswordChangesOnlySelf(t *testing.T) {
	a := newRoleTestServer(t)
	op := loginAs(t, a, "op", "op-password-1")

	w := do(a, op, "PUT", "/api/settings", map[string]string{"admin_pass": "op-brand-new-9"})
	if w.Code != 200 {
		t.Fatalf("改密应成功，实际 %d (%s)", w.Code, w.Body.String())
	}
	var j map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &j)
	if j["relogin"] != true {
		t.Fatalf("应要求重新登录，实际 %v", j)
	}
	loginAs(t, a, "op", "op-brand-new-9")
	// owner 的密码不受影响
	loginAs(t, a, "admin", "admin")
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
