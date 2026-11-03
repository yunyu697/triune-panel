package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"triune/internal/authn"
	"triune/internal/model"
)

// ---- clients ----

// errNoPerUserRateLimit 是收到 rate_limit_mbps 时的拒绝说明。
//
// 三个内核都没有每用户带宽限制能力（xray 的 policy.levels 只有超时/统计开关/
// bufferSize，sing-box 与 mihomo 的 users 只有名字和凭据）。这个字段曾经能写、
// 能读、能存库，但内核完全不消费——用户填了「100」以为限速了，其实没有，
// 这比没有这个功能更糟。所以现在直接拒绝，并告诉调用方该往哪走。
const errNoPerUserRateLimit = "不支持每用户带宽限速：sing-box / xray / mihomo 均无此能力，rate_limit_mbps 填了也不会生效。" +
	"用量控制请用 quota_gb（超限会自动停用该用户）；带宽限制需要在内核之外做（tc/HTB、上游限速或云厂商带宽上限）。"

// rejectRateLimit 在请求体已解码后调用：带了非零的 rate_limit_mbps 就写 400。
// 用指针是为了区分「没传」和「显式传 0」——后者等价于不设，放行。
func rejectRateLimit(w http.ResponseWriter, v *int) bool {
	if v == nil || *v == 0 {
		return true
	}
	fail(w, http.StatusBadRequest, errNoPerUserRateLimit)
	return false
}

func (a *Server) listClients(w http.ResponseWriter, r *http.Request) {
	cs, err := a.Store.ListClients()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if cs == nil {
		cs = []*model.Client{}
	}
	// 只读账号拿不到凭据与订阅令牌：这两样等价于「能连上节点」的钥匙。
	// 置空而不是不返回字段——前端据此渲染「—」并带上 masked 标记，
	// 免得看起来像「这个用户没配凭据」。
	masked := !a.roleAtLeast(r, authn.RoleAdmin)
	out := make([]map[string]any, 0, len(cs))
	for _, c := range cs {
		ids, _ := a.Store.ClientInboundIDs(c.ID)
		if masked {
			cc := *c
			cc.Credential, cc.SubToken = "", ""
			out = append(out, map[string]any{"client": &cc, "inbound_ids": ids, "masked": true})
			continue
		}
		out = append(out, map[string]any{"client": c, "inbound_ids": ids, "masked": false})
	}
	writeJSON(w, 200, out)
}

func (a *Server) createClient(w http.ResponseWriter, r *http.Request) {
	var req struct {
		model.Client
		RateLimitMbps *int    `json:"rate_limit_mbps"` // 已废弃：仅用于给出明确拒绝
		InboundIDs    []int64 `json:"inbound_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if !rejectRateLimit(w, req.RateLimitMbps) {
		return
	}
	if req.Credential == "" {
		req.Credential = uuid4()
	}
	if req.SubToken == "" {
		req.SubToken = token()
	}
	req.Client.Enabled = true
	if err := a.Store.CreateClient(&req.Client); err != nil {
		fail(w, 500, err.Error())
		return
	}
	_ = a.Store.SetClientInbounds(req.Client.ID, req.InboundIDs)
	a.changed(r, AuditClientCreate, req.Client.Name,
		fmt.Sprintf("新建用户 #%d，绑定 %d 个入站", req.Client.ID, len(req.InboundIDs)))
	writeJSON(w, 200, req.Client)
}

// updateClient 做的是「部分更新」：只覆盖请求里真正出现的键。
//
// 旧实现把请求体直接解成完整的 model.Client 再整行 UPDATE，于是
// `PUT {"name":"x","quota_gb":5}` 会把没传的 credential、sub_token 清成空串、
// enabled 置为 false，而且返回 200——用户的订阅链接和登录凭据就这么静默没了，
// 面板上还看不出问题（列表里只是「凭据变空」）。现在先读当前记录，
// 再按请求里出现的键逐项覆盖，未出现的字段保持原值。
func (a *Server) updateClient(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		fail(w, 400, err.Error())
		return
	}
	// 已废弃字段：带了非零值就拒绝（见 errNoPerUserRateLimit）
	if v, ok := raw["rate_limit_mbps"]; ok {
		var n int
		if err := json.Unmarshal(v, &n); err == nil && !rejectRateLimit(w, &n) {
			return
		}
	}

	c, err := a.Store.GetClient(pathID(r))
	if err != nil {
		fail(w, 404, err.Error())
		return
	}
	before := *c // 变更前快照，用于通知里的字段级 diff
	for k, v := range raw {
		var ferr error
		switch k {
		case "name":
			ferr = json.Unmarshal(v, &c.Name)
		case "credential":
			ferr = json.Unmarshal(v, &c.Credential)
		case "sub_token":
			ferr = json.Unmarshal(v, &c.SubToken)
		case "quota_gb":
			ferr = json.Unmarshal(v, &c.QuotaGB)
		case "enabled":
			ferr = json.Unmarshal(v, &c.Enabled)
		case "expires_at":
			// 显式传 null = 清除到期时间；键不出现 = 保持不变
			if string(v) == "null" {
				c.ExpiresAt = nil
			} else {
				var t time.Time
				if ferr = json.Unmarshal(v, &t); ferr == nil {
					c.ExpiresAt = &t
				}
			}
		}
		if ferr != nil {
			fail(w, 400, "字段 "+k+" 格式有误: "+ferr.Error())
			return
		}
	}
	if err := a.Store.UpdateClient(c); err != nil {
		fail(w, 500, err.Error())
		return
	}
	a.changed(r, AuditClientUpdate, c.Name, fmt.Sprintf("修改用户 #%d", c.ID), clientDiff(&before, c)...)
	writeJSON(w, 200, c)
}

func (a *Server) deleteClient(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	if err := a.Store.DeleteClient(id); err != nil {
		fail(w, 500, err.Error())
		return
	}
	a.changed(r, AuditClientDelete, fmt.Sprintf("#%d", id), fmt.Sprintf("删除用户 #%d", id))
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

func (a *Server) setClientInbounds(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InboundIDs []int64 `json:"inbound_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, err.Error())
		return
	}
	id := pathID(r)
	before, _ := a.Store.ClientInboundIDs(id)
	if err := a.Store.SetClientInbounds(id, req.InboundIDs); err != nil {
		fail(w, 500, err.Error())
		return
	}
	a.changed(r, AuditClientUpdate, fmt.Sprintf("#%d", id),
		fmt.Sprintf("调整用户 #%d 的入站绑定，共 %d 个", id, len(req.InboundIDs)),
		"绑定入站: "+fmtIDs(before)+" → "+fmtIDs(req.InboundIDs))
	writeJSON(w, 200, map[string]string{"ok": "1"})
}
