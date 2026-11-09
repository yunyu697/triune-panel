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

// hostOptFixReq 是单项修复/回退的请求体。key 是体检项对应的动作标识
// （clock / fd / conntrack），必须与 hostopt 里的注册表一致。
type hostOptFixReq struct {
	Confirm string `json:"confirm"`
	Key     string `json:"key"`
}

// POST /api/host-opt/fix —— 执行一项体检项修复。
//
// 与 apply/revert 同一套闸门：ownerOnly + 精确确认串 + 先备份原值 + 回读复检，
// 失败零改动。区别只在粒度——这里修的是「时钟没开对时」「fd 上限太低」
// 「conntrack 快满表」这类单点问题，不动六个网络参数。
func (a *Server) handleHostOptFix(w http.ResponseWriter, r *http.Request) {
	var req hostOptFixReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, "请求体格式有误："+err.Error())
		return
	}
	if err := hostopt.ApplyFix(a.DataDir, req.Confirm, req.Key); err != nil {
		a.hostOptFail(w, r, "fix:"+req.Key, err)
		return
	}
	a.auditOK(r, AuditHostOpt, "fix", "主机网络优化：修复 "+req.Key)
	writeJSON(w, 200, hostopt.Detect(a.DataDir))
}

// POST /api/host-opt/fix/revert —— 按修复记录回退。
func (a *Server) handleHostOptFixRevert(w http.ResponseWriter, r *http.Request) {
	var req hostOptFixReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, "请求体格式有误："+err.Error())
		return
	}
	if err := hostopt.RevertFix(a.DataDir, req.Confirm, req.Key); err != nil {
		a.hostOptFail(w, r, "unfix:"+req.Key, err)
		return
	}
	a.auditOK(r, AuditHostOpt, "unfix", "主机网络优化：回退修复 "+req.Key)
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
