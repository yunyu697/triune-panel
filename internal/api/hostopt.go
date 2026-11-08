// Package api 的主机优化端点（网络参数体检 + 可选应用/撤销）。
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
	Confirm string        `json:"confirm"`
	Tune    *hostopt.Tune `json:"tune"`
}

func (a *Server) handleHostOptApply(w http.ResponseWriter, r *http.Request) {
	var req hostOptReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, "请求体格式有误："+err.Error())
		return
	}
	// 不带 tune 时按默认方案（BBR + fq + 16 MiB 缓冲 …），兼容旧前端。
	tune := hostopt.DefaultTune()
	if req.Tune != nil {
		tune = *req.Tune
	}
	if err := hostopt.Apply(a.DataDir, req.Confirm, tune); err != nil {
		a.hostOptFail(w, r, "apply", err)
		return
	}
	a.auditOK(r, AuditHostOpt, "apply", "主机网络优化：apply")
	writeJSON(w, 200, hostopt.Detect(a.DataDir))
}

func (a *Server) handleHostOptRevert(w http.ResponseWriter, r *http.Request) {
	var req hostOptReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, "请求体格式有误："+err.Error())
		return
	}
	if err := hostopt.Revert(a.DataDir, req.Confirm); err != nil {
		a.hostOptFail(w, r, "revert", err)
		return
	}
	a.auditOK(r, AuditHostOpt, "revert", "主机网络优化：revert")
	writeJSON(w, 200, hostopt.Detect(a.DataDir))
}

func (a *Server) hostOptFail(w http.ResponseWriter, r *http.Request, action string, err error) {
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
}
