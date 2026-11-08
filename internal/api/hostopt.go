// Package api 的主机优化端点（当前是 TCP BBR）。
//
// 权限分层是刻意的：体检报告只读、管理员可看；「应用」和「撤销」会写
// /etc/sysctl.d，只允许超级管理员，且必须回传包里定义的确认字面量。
// 面板默认绝不修改系统——用户不点，任何代码路径都不会落到写操作上。
package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"triune/internal/hostopt"
)

// GET /api/host-opt —— 纯只读体检，不写文件、不改 sysctl。
func (a *Server) handleHostOpt(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, hostopt.Detect(a.DataDir))
}

type hostOptReq struct {
	Confirm string `json:"confirm"`
}

func (a *Server) handleHostOptApply(w http.ResponseWriter, r *http.Request) {
	a.doHostOpt(w, r, "apply", hostopt.Apply)
}

func (a *Server) handleHostOptRevert(w http.ResponseWriter, r *http.Request) {
	a.doHostOpt(w, r, "revert", hostopt.Revert)
}

func (a *Server) doHostOpt(w http.ResponseWriter, r *http.Request, action string,
	fn func(dataDir, confirm string) error) {
	var req hostOptReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, "请求体格式有误："+err.Error())
		return
	}
	if err := fn(a.DataDir, req.Confirm); err != nil {
		a.auditFail(r, AuditHostOpt, action, err.Error())
		switch {
		case errors.Is(err, hostopt.ErrConfirm):
			// 明确告诉前端确认串该是什么，而不是让它猜
			fail(w, 400, "需要显式确认：请原样回传确认串后重试（"+err.Error()+"）")
		case errors.Is(err, hostopt.ErrUnsupported):
			fail(w, 409, err.Error())
		default:
			fail(w, 500, err.Error())
		}
		return
	}
	a.auditOK(r, AuditHostOpt, action, "主机网络优化："+action)
	writeJSON(w, 200, hostopt.Detect(a.DataDir))
}
