package api

import (
	"log"
	"net/http"
	"strconv"

	"triune/internal/authn"
	"triune/internal/store"
)

// ---- 审计日志 ----
//
// 设计原则：
//   - **旁路**：写审计失败只打日志，绝不让主操作失败。审计是"事后追查"，
//     不是"事前拦截"，为了记一笔而让改配置报错是本末倒置。
//   - **只记动作与结果**：不记密码、不记密钥、不记完整配置内容。detail
//     只放「改了哪些键」「对象名」这类可公开的线索。
//   - **动作名分层**：`login` / `inbound.create` / `settings.update`，
//     前缀可用来筛选（`?prefix=inbound`）。

// 常用动作名（集中定义，避免各处手写字符串拼错导致筛选失灵）。
const (
	AuditLogin         = "login"
	AuditLogout        = "logout"
	AuditPassword      = "settings.password"
	AuditSettings      = "settings.update"
	AuditInboundCreate = "inbound.create"
	AuditInboundUpdate = "inbound.update"
	AuditInboundDelete = "inbound.delete"
	AuditClientCreate  = "client.create"
	AuditClientUpdate  = "client.update"
	AuditClientDelete  = "client.delete"
	AuditClientToken   = "client.reset-token"
	AuditApply         = "apply"
	AuditDryRun        = "apply.dry-run"
	AuditRollback      = "rollback"
	AuditSwitchCore    = "switch-core"
	AuditImport        = "import"
	AuditExternal      = "external"
	AuditAuth2FA       = "auth2fa"
	AuditCert          = "cert.issue"
	AuditUpdate        = "update"
)

// audit 记录一条审计。r 可为 nil（CLI / 后台任务等无请求上下文的场景）。
func (a *Server) audit(r *http.Request, action, target, result, detail string) {
	if a.Store == nil {
		return
	}
	e := store.AuditEntry{
		Actor:  authn.User(a.Store),
		Action: action,
		Target: target,
		Result: result,
		Detail: detail,
	}
	if r != nil {
		e.IP = a.clientIP(r)
	}
	if err := a.Store.AddAudit(e); err != nil {
		log.Printf("[audit] 写入失败 action=%s: %v", action, err)
	}
}

// auditOK 成功事件的简写。
func (a *Server) auditOK(r *http.Request, action, target, detail string) {
	a.audit(r, action, target, store.AuditOK, detail)
}

// auditFail 失败事件的简写。
func (a *Server) auditFail(r *http.Request, action, target, detail string) {
	a.audit(r, action, target, store.AuditFail, detail)
}

// GET /api/audit?limit=&offset=&prefix=&result=
func (a *Server) handleAuditList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.AuditFilter{
		Prefix: q.Get("prefix"),
		Result: q.Get("result"),
	}
	if v, err := strconv.Atoi(q.Get("limit")); err == nil {
		f.Limit = v
	}
	if v, err := strconv.Atoi(q.Get("offset")); err == nil {
		f.Offset = v
	}
	list, err := a.Store.ListAudit(f)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, list)
}

// DELETE /api/audit：清空审计记录。
// 清空本身也要留痕——否则「删掉证据」就成了无声操作。先清空再补记一条，
// 于是清空后列表里只剩这一条，说明是谁在什么时候清的。
func (a *Server) handleAuditClear(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.ClearAudit(); err != nil {
		fail(w, 500, err.Error())
		return
	}
	a.auditOK(r, "audit.clear", "", "清空全部审计记录")
	writeJSON(w, 200, map[string]string{"ok": "1"})
}
