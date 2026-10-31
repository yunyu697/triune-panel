package api

import (
	"net/http"
	"time"
)

// startedAt 进程启动时间，供 /healthz 汇报运行时长。
var startedAt = time.Now()

// handleHealthz 免认证健康检查端点。
//
// 用途：systemd / uptime 监控 / 负载均衡 / 云厂商健康探针。刻意做成
// 免认证 + 极轻量（一次 SELECT 1），否则探针要带 token，token 又要随
// 改密轮换，运维脚本会很别扭。
//
// 只暴露三类信息：是否可用、版本号、DB 是否可查。版本号本来就在
// /api/public/info 公开，不算新增泄露面。
// DB 打不开时返回 503 —— 探活要能反映"进程活着但服务是坏的"，
// 只回 200 会让监控对着一堆 500 报错却显示健康。
func (a *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	body := map[string]any{
		"status":     "ok",
		"version":    Version,
		"uptime_sec": int64(time.Since(startedAt).Seconds()),
		"time":       time.Now().Format(time.RFC3339),
	}
	code := http.StatusOK
	if a.Store == nil {
		body["status"] = "degraded"
		body["db"] = "store not initialized"
		code = http.StatusServiceUnavailable
	} else if err := a.Store.Ping(); err != nil {
		body["status"] = "degraded"
		body["db"] = err.Error()
		code = http.StatusServiceUnavailable
	} else {
		body["db"] = "ok"
	}
	writeJSON(w, code, body)
}
