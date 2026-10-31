package api

// 鉴权与授权（Authentication & Authorization）。
//
// 认证：auth() 校验 token → 加载账号 → 比对密码指纹，然后把账号放进
// request context。后续所有 handler 通过 currentUser(r) 拿到「谁在操作」，
// 审计日志的 Actor 就来自这里——这是「按人追溯」的根。
//
// 授权：三层包装，写清楚每个端点的最低角色，而不是靠方法名猜。
//   - auth(h)      任意登录账号（含 viewer，只读端点用）
//   - write(h)     owner + admin（所有会改状态、会吐出凭据的端点）
//   - ownerOnly(h) 仅 owner（账号管理、清空审计、整面板回滚）
//
// 刻意做成「显式声明」而不是「按 HTTP 方法自动推断」：GET 也可能泄漏
// 凭据（导出配置、客户端列表里带订阅令牌），靠方法推断一定会漏。

import (
	"context"
	"net/http"

	"triune/internal/authn"
	"triune/internal/store"
)

type ctxKey int

const ctxUserKey ctxKey = iota

// currentUser 返回本次请求的登录账号；匿名请求返回 nil。
func (a *Server) currentUser(r *http.Request) *store.AdminUser {
	u, _ := r.Context().Value(ctxUserKey).(*store.AdminUser)
	return u
}

// withUser 把账号塞进 context。
func withUser(r *http.Request, u *store.AdminUser) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ctxUserKey, u))
}

// actorName 审计用的操作人名称；匿名请求返回空串。
func (a *Server) actorName(r *http.Request) string {
	if u := a.currentUser(r); u != nil {
		return u.Username
	}
	return ""
}

// roleAtLeast 当前账号角色是否达到 min 要求。
func (a *Server) roleAtLeast(r *http.Request, min string) bool {
	u := a.currentUser(r)
	if u == nil {
		return false
	}
	return authn.RoleRank(u.Role) >= authn.RoleRank(min)
}

// deny 统一的 403 响应 + 留痕。
//
// 越权尝试必须记审计：只读账号反复调写接口，通常不是手滑，而是有人在
// 试探权限边界；不记的话面板上完全看不出来。
func (a *Server) deny(w http.ResponseWriter, r *http.Request, need string) {
	a.auditFail(r, AuditDeny, r.Method+" "+r.URL.Path,
		"角色 "+a.roleOf(r)+" 越权访问（需要 "+need+"）")
	fail(w, http.StatusForbidden, "当前账号角色无权执行此操作（需要 "+need+" 及以上）")
}

func (a *Server) roleOf(r *http.Request) string {
	if u := a.currentUser(r); u != nil {
		return u.Role
	}
	return "anonymous"
}

// write 写操作：owner 与 admin 可用，viewer 403。
func (a *Server) write(next http.HandlerFunc) http.HandlerFunc {
	return a.auth(func(w http.ResponseWriter, r *http.Request) {
		if !a.roleAtLeast(r, authn.RoleAdmin) {
			a.deny(w, r, "管理员")
			return
		}
		next(w, r)
	})
}

// ownerOnly 仅超级管理员可用。
func (a *Server) ownerOnly(next http.HandlerFunc) http.HandlerFunc {
	return a.auth(func(w http.ResponseWriter, r *http.Request) {
		if !a.roleAtLeast(r, authn.RoleOwner) {
			a.deny(w, r, "超级管理员")
			return
		}
		next(w, r)
	})
}
