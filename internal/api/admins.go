package api

// 管理员账号管理（多管理员 + 角色）。
//
// 权限模型：只有 owner 能进这里。owner 唯一，由迁移播种，不能被删除、
// 降级或停用——否则面板会永久失去「管理管理员」的能力，只能靠命令行救。
//
// 几个刻意的设计：
//   - 新建时密码留空 = 服务端生成随机密码并**只返回这一次**，避免
//     「管理员随手填 12345678」这种最坏实践；
//   - 改自己密码走 /api/me/password，需要校验原密码；owner 重置别人的
//     密码不需要原密码（那是找回，不是改密）；
//   - 停用/删除/改密都会让该账号的在线会话在下一次请求时失效，因为
//     auth() 每次请求都回库校验账号状态与密码指纹。

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"triune/internal/authn"
	"triune/internal/store"
)

// adminView 对外的账号视图：只暴露该暴露的，哈希与 TOTP 密钥永不下发。
type adminView struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	Enabled   bool   `json:"enabled"`
	HasTOTP   bool   `json:"has_totp"`
	LastIP    string `json:"last_ip"`
	LastLogin string `json:"last_login"`
	CreatedAt string `json:"created_at"`
}

func toAdminView(u *store.AdminUser) adminView {
	// created_at 由 SQLite 的 CURRENT_TIMESTAMP 写入（UTC），驱动按 UTC 解析，
	// 这里转本地时区再格式化，否则面板上会看到「8 小时前创建」。
	created := ""
	if !u.CreatedAt.IsZero() {
		created = u.CreatedAt.Local().Format("2006-01-02 15:04:05")
	}
	return adminView{
		ID: u.ID, Username: u.Username, Role: u.Role, Enabled: u.Enabled,
		HasTOTP: u.TotpSecret != "", LastIP: u.LastIP, LastLogin: u.LastLogin,
		CreatedAt: created,
	}
}

// GET /api/me —— 当前登录身份。前端据此决定显隐与按钮可用性。
//
// 注意：前端拿它只做「体验层」控制，真正的权限判定全在服务端；
// 改前端改不掉 403。
func (a *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u := a.currentUser(r)
	if u == nil {
		fail(w, 401, "unauthorized")
		return
	}
	writeJSON(w, 200, map[string]any{
		"user": u.Username, "role": u.Role,
		"last_ip": u.LastIP, "last_login": u.LastLogin,
		// 密码哈希为空 = 从未设置过密码，前端强制弹改密框
		"must_change": u.PassHash == "",
		"has_totp":    u.TotpSecret != "",
	})
}

// PUT /api/me/password  body: {"old":"...","new":"..."}
//
// 改自己的密码必须验原密码：否则一个被劫持的会话（比如没锁屏的电脑）
// 就能直接把主人踢出去。
func (a *Server) handleChangeOwnPassword(w http.ResponseWriter, r *http.Request) {
	u := a.currentUser(r)
	if u == nil {
		fail(w, 401, "unauthorized")
		return
	}
	var req struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, "请求格式有误")
		return
	}
	// 出厂默认账号允许不填原密码（此时原密码是 admin，用户已经在登录页
	// 输过一次；再要一次只会让人以为改密失败）。
	if u.PassHash != "" {
		got, _, err := authn.Authenticate(a.Store, u.Username, req.Old)
		if err != nil || got == nil || got.ID != u.ID {
			a.auditFail(r, AuditPassword, u.Username, "原密码校验失败")
			fail(w, 401, "原密码不正确")
			return
		}
	}
	if err := authn.SetPasswordFor(a.Store, u.ID, req.New); err != nil {
		fail(w, 400, err.Error())
		return
	}
	a.auditOK(r, AuditPassword, u.Username, "本账号密码已修改，该账号的旧会话全部失效")
	// 会话因密码指纹变化立即失效，前端收到 relogin 后回登录页
	writeJSON(w, 200, map[string]any{"ok": 1, "relogin": true})
}

// GET /api/admins
func (a *Server) handleAdminsList(w http.ResponseWriter, r *http.Request) {
	list, err := a.Store.ListAdmins()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	out := make([]adminView, 0, len(list))
	for _, u := range list {
		out = append(out, toAdminView(u))
	}
	writeJSON(w, 200, out)
}

// POST /api/admins  body: {"username":"...","password":"...","role":"admin"}
//
// password 留空 → 服务端生成 16 位随机密码，随响应返回一次（不落库明文）。
func (a *Server) handleAdminCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, "请求格式有误")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Role == "" {
		req.Role = authn.RoleAdmin
	}
	generated := ""
	if req.Password == "" {
		p, err := authn.RandomPassword(16)
		if err != nil {
			fail(w, 500, "生成随机密码失败："+err.Error())
			return
		}
		req.Password, generated = p, p
	}
	u, err := authn.CreateUser(a.Store, req.Username, req.Password, req.Role)
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, store.ErrUsernameTaken) {
			code = http.StatusConflict
		}
		fail(w, code, err.Error())
		return
	}
	// 回读一次拿到 created_at（INSERT 不会回填默认值，否则响应里是 0001-01-01）
	if fresh, err := a.Store.GetAdmin(u.ID); err == nil {
		u = fresh
	}
	a.auditOK(r, AuditAdmin, u.Username, "新建管理员，角色 "+u.Role)
	resp := map[string]any{"ok": 1, "admin": toAdminView(u)}
	if generated != "" {
		// 只在这一次响应里出现；前端必须提示用户立刻保存
		resp["password"] = generated
	}
	writeJSON(w, 200, resp)
}

// PUT /api/admins/{id}  body 任意子集: {"username","password","role","enabled"}
//
// 与 inbound/client 一样走「部分更新」：前端只提交它改过的字段，
// 没出现的键保持原值。整行覆盖会让「改个角色」把密码哈希一起清掉。
func (a *Server) handleAdminUpdate(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		fail(w, 400, "请求格式有误")
		return
	}
	u, err := a.Store.GetAdmin(pathID(r))
	if err != nil {
		fail(w, 404, err.Error())
		return
	}
	me := a.currentUser(r)

	var newPass string
	renamed := false
	for k, v := range raw {
		var ferr error
		switch k {
		case "username":
			var name string
			if ferr = json.Unmarshal(v, &name); ferr == nil {
				name = strings.TrimSpace(name)
				if name != u.Username {
					if ferr = authn.ValidateUsername(name); ferr == nil {
						u.Username, renamed = name, true
					}
				}
			}
		case "role":
			var role string
			if ferr = json.Unmarshal(v, &role); ferr == nil {
				if u.Role == authn.RoleOwner && role != authn.RoleOwner {
					fail(w, 400, "超级管理员不能降级：面板必须保留一个能管理账号的账号")
					return
				}
				if role == authn.RoleOwner && u.Role != authn.RoleOwner {
					fail(w, 400, "超级管理员账号唯一，不能把其他账号提升为超级管理员")
					return
				}
				if !authn.ValidRole(role) {
					ferr = errors.New("角色无效")
				} else {
					u.Role = role
				}
			}
		case "enabled":
			var on bool
			if ferr = json.Unmarshal(v, &on); ferr == nil {
				if !on && u.ID == me.ID {
					fail(w, 400, "不能停用自己的账号")
					return
				}
				if !on && u.Role == authn.RoleOwner {
					n, _ := a.Store.CountOwners()
					if n <= 1 {
						fail(w, 400, "这是最后一个启用的超级管理员，停用后将无人能管理账号")
						return
					}
				}
				u.Enabled = on
			}
		case "password":
			if ferr = json.Unmarshal(v, &newPass); ferr != nil {
				break
			}
			if newPass != "" {
				if len(newPass) < authn.MinPasswordLen {
					fail(w, 400, "密码至少 8 位")
					return
				}
				h, herr := authn.Hash(newPass)
				if herr != nil {
					fail(w, 500, herr.Error())
					return
				}
				u.PassHash = h
			}
		}
		if ferr != nil {
			fail(w, 400, "字段 "+k+" 格式有误: "+ferr.Error())
			return
		}
	}
	if err := a.Store.UpdateAdmin(u); err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, store.ErrUsernameTaken) {
			code = http.StatusConflict
		}
		fail(w, code, err.Error())
		return
	}
	detail := "更新管理员"
	if renamed {
		detail += "（改名）"
	}
	if newPass != "" {
		detail += "（重置密码）"
	}
	detail += "，角色 " + u.Role
	if !u.Enabled {
		detail += "，已停用"
	}
	a.auditOK(r, AuditAdmin, u.Username, detail)
	writeJSON(w, 200, map[string]any{"ok": 1, "admin": toAdminView(u)})
}

// DELETE /api/admins/{id}
func (a *Server) handleAdminDelete(w http.ResponseWriter, r *http.Request) {
	u, err := a.Store.GetAdmin(pathID(r))
	if err != nil {
		fail(w, 404, err.Error())
		return
	}
	me := a.currentUser(r)
	if u.ID == me.ID {
		fail(w, 400, "不能删除自己的账号")
		return
	}
	if u.Role == authn.RoleOwner {
		fail(w, 400, "超级管理员不能删除")
		return
	}
	if err := a.Store.DeleteAdmin(u.ID); err != nil {
		fail(w, 500, err.Error())
		return
	}
	a.auditOK(r, AuditAdmin, u.Username, "删除管理员（该账号的在线会话立即失效）")
	writeJSON(w, 200, map[string]any{"ok": 1})
}
