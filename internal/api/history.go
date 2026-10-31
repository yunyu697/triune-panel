package api

// 登录历史：把审计里的 login 记录按人聚合成时间线。
//
// 会话表（/api/sessions）只回答「现在谁还连着」；「上周三凌晨谁从哪个 IP
// 登录过」只能从审计里翻。直接翻原始审计有两个问题：几百条 login 混在
// 入站/用户变更中间，而且成功与失败各占一行，看不出「这个人最近是不是
// 一直有人在试」。这里按人聚合：成功/失败次数、来源 IP、最近一次成功与
// 失败，以及逐条时间线。
//
// 数据源是审计（保留 90 天），所以能回溯的最大范围就是 90 天。

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"triune/internal/authn"
	"triune/internal/store"
)

// loginEvent 时间线上的一条记录。
type loginEvent struct {
	Time   string `json:"time"`
	IP     string `json:"ip"`
	Result string `json:"result"`
	Detail string `json:"detail"`
}

// loginHistory 一个人的登录画像。
type loginHistory struct {
	User     string       `json:"user"`
	Total    int          `json:"total"`
	OK       int          `json:"ok"`
	Fail     int          `json:"fail"`
	IPs      []string     `json:"ips"`
	LastOK   string       `json:"last_ok"`
	LastIP   string       `json:"last_ip"`
	LastFail string       `json:"last_fail"`
	Events   []loginEvent `json:"events"` // 最近 N 条，时间倒序
}

// historyScanLimit 一次最多读多少条审计。
//
// 90 天全量可能上万条，全读出来只为渲染一个卡片不值得。500 条覆盖小团队
// 的正常量；真超了会带 truncated=true，让前端说清「这不是全部」。
const historyScanLimit = 500

// historyEventsPerUser 每人保留的时间线条数。
const historyEventsPerUser = 20

// handleLoginHistory GET /api/login-history?days=7&user=<名>
//
// 只读账号强制只看自己：别人的登录时间与 IP 是情报，和会话列表同一个理由。
func (a *Server) handleLoginHistory(w http.ResponseWriter, r *http.Request) {
	me := a.currentUser(r)
	if me == nil {
		fail(w, 401, "unauthorized")
		return
	}
	q := r.URL.Query()
	days := 7
	if v, err := strconv.Atoi(q.Get("days")); err == nil && v > 0 {
		days = v
	}
	if days > store.AuditKeepDays {
		days = store.AuditKeepDays // 审计只留 90 天，要更久也没有
	}
	who := strings.TrimSpace(q.Get("user"))
	if me.Role == authn.RoleViewer {
		who = me.Username
	}

	since := time.Now().AddDate(0, 0, -days).Format(store.AuditTimeLayout)
	rows, err := a.Store.ListAudit(store.AuditFilter{
		Prefix: AuditLogin, Since: since, Limit: historyScanLimit,
	})
	if err != nil {
		fail(w, 500, err.Error())
		return
	}

	byUser := map[string]*loginHistory{}
	for _, e := range rows {
		name := e.Actor
		if name == "" {
			name = "anonymous"
		}
		if who != "" && name != who {
			continue
		}
		h := byUser[name]
		if h == nil {
			h = &loginHistory{User: name, IPs: []string{}, Events: []loginEvent{}}
			byUser[name] = h
		}
		h.Total++
		if e.Result == store.AuditFail {
			h.Fail++
			if h.LastFail == "" {
				h.LastFail = e.Time // 倒序遍历，第一次遇到就是最近的
			}
		} else {
			h.OK++
			if h.LastOK == "" {
				h.LastOK, h.LastIP = e.Time, e.IP
			}
			// 来源 IP 只统计成功的登录：失败尝试的 IP 大概率是攻击者，
			// 混进「这个人用过的 IP」里会让人误以为自己的账号被人用过。
			if e.IP != "" && !containsStr(h.IPs, e.IP) {
				h.IPs = append(h.IPs, e.IP)
			}
		}
		if len(h.Events) < historyEventsPerUser {
			h.Events = append(h.Events, loginEvent{
				Time: e.Time, IP: e.IP, Result: e.Result, Detail: e.Detail,
			})
		}
	}

	out := make([]*loginHistory, 0, len(byUser))
	for _, h := range byUser {
		out = append(out, h)
	}
	// 最近有动作的排前面：先看「昨天还登过」的人，而不是三个月没动的账号
	sort.Slice(out, func(i, j int) bool {
		return lastSeenOf(out[i]) > lastSeenOf(out[j])
	})

	writeJSON(w, 200, map[string]any{
		"days":      days,
		"scanned":   len(rows),
		"truncated": len(rows) >= historyScanLimit,
		"items":     out,
	})
}

// lastSeenOf 取这个人最近一次动作的时间（成功或失败）。
func lastSeenOf(h *loginHistory) string {
	if len(h.Events) > 0 {
		return h.Events[0].Time
	}
	return ""
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
