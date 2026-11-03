package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"triune/internal/authn"
	"triune/internal/store"
)

// ---- 登录防爆破 ----

const (
	maxLoginFails   = 5                // 连续失败次数阈值
	loginLockPeriod = 10 * time.Minute // 锁定时长
)

type loginFail struct {
	count int
	until time.Time
}

// loginKey 防爆破计数键 = 真实来源 IP。
// 挂在反向代理后面时会按 trusted_proxies 采信 X-Forwarded-For，
// 否则所有请求的来源都是 127.0.0.1，一次误操作就能把全站锁死。
func (a *Server) loginKey(r *http.Request) string {
	return a.clientIP(r)
}

// loginLocked 判断该来源是否处于锁定状态，返回剩余秒数。
func (a *Server) loginLocked(key string) int {
	v, ok := a.loginFails.Load(key)
	if !ok {
		return 0
	}
	f := v.(*loginFail)
	if f.until.IsZero() {
		return 0 // 从未锁定过：放行，失败计数继续累积
	}
	if f.until.After(time.Now()) {
		return int(time.Until(f.until).Seconds())
	}
	a.loginFails.Delete(key) // 锁定已过期，清零
	return 0
}

// recordLoginFail 记录一次登录失败，达到阈值锁定该来源。
func (a *Server) recordLoginFail(key string) {
	f := &loginFail{}
	if v, ok := a.loginFails.Load(key); ok {
		f = v.(*loginFail)
	}
	f.count++
	if f.count >= maxLoginFails {
		f.until = time.Now().Add(loginLockPeriod)
		f.count = 0
		log.Printf("[auth] 来源 %s 连续登录失败已达 %d 次，锁定 %s", key, maxLoginFails, loginLockPeriod)
	}
	a.loginFails.Store(key, f)
}

func (a *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	key := a.loginKey(r)
	if remain := a.loginLocked(key); remain > 0 {
		a.auditFail(r, AuditLogin, "", fmt.Sprintf("来源处于锁定期，拒绝登录（剩余约 %d 分钟）", remain/60+1))
		fail(w, http.StatusTooManyRequests,
			fmt.Sprintf("登录失败次数过多，已临时锁定，请约 %d 分钟后再试", remain/60+1))
		return
	}
	var req struct {
		User string `json:"user"`
		Pass string `json:"pass"`
		Code string `json:"code"` // 2FA 第二步：动态口令 / 邮箱验证码
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, "bad request")
		return
	}
	// 密码校验：哈希优先；命中遗留明文槽则在本次登录后静默升级为哈希。
	u, legacy, err := authn.Authenticate(a.Store, req.User, req.Pass)
	if err != nil && !errors.Is(err, authn.ErrDisabled) {
		fail(w, 500, err.Error())
		return
	}
	if u == nil {
		a.recordLoginFail(key)
		// 登录失败没有「当前用户」，Actor 记的是**被尝试的用户名**：
		// 「有人在猜 alice 的密码」和「有人在猜 root 的密码」是两条不同的线索。
		a.auditFailAs(req.User, r, AuditLogin, req.User, "用户名或密码错误")
		fail(w, 401, "用户名或密码错误")
		return
	}
	if errors.Is(err, authn.ErrDisabled) {
		// 密码对了才走到这里，所以告诉对方「账号停用」不额外泄漏信息
		a.recordLoginFail(key)
		a.auditFailAs(u.Username, r, AuditLogin, u.Username, "账号已停用，拒绝登录")
		fail(w, http.StatusForbidden, authn.ErrDisabled.Error())
		return
	}
	if legacy {
		if err := authn.MigrateLegacy(a.Store, u); err != nil {
			log.Printf("迁移遗留明文密码失败: %v", err)
		} else {
			log.Printf("已把遗留明文密码升级为 bcrypt 哈希")
		}
	}

	// 二次验证：账密正确后按 auth_2fa_mode 进入第二步。
	// TOTP 密钥是**每账号**的：账号没绑验证器就跳过这一步，
	// 不会因为别的管理员开了 TOTP 就把自己也锁在外面。
	mode, _ := a.Store.GetSetting("auth_2fa_mode")
	switch mode {
	case "totp":
		if u.TotpSecret != "" {
			if strings.TrimSpace(req.Code) == "" {
				writeJSON(w, 200, map[string]any{"need_2fa": "totp"})
				return
			}
			if !verifyTOTP(u.TotpSecret, req.Code) {
				a.recordLoginFail(key)
				a.auditFailAs(u.Username, r, AuditLogin, u.Username, "动态口令不正确")
				fail(w, 401, "动态口令不正确")
				return
			}
		}
	case "email":
		to, _ := a.Store.GetSetting("auth_email")
		if to != "" {
			if strings.TrimSpace(req.Code) == "" {
				masked, err := a.sendLoginCode(to)
				if err != nil {
					fail(w, 500, "验证码发送失败："+err.Error())
					return
				}
				writeJSON(w, 200, map[string]any{"need_2fa": "email", "email": masked})
				return
			}
			if err := a.checkEmailCode(req.Code); err != nil {
				a.recordLoginFail(key)
				a.auditFailAs(u.Username, r, AuditLogin, u.Username, "邮箱验证码校验失败")
				fail(w, 401, err.Error())
				return
			}
		}
	}

	a.loginFails.Delete(key) // 登录成功，清零失败计数

	// 上次成功登录的来源与时间：先读旧值（随本次响应返回给用户，便于发现
	// 异常访问），再写入本次的（概览页常驻展示）。多管理员之后这是**按人**
	// 记录的，不再是全局最后一个登录者。
	prevIP, prevTime := u.LastIP, u.LastLogin
	if err := a.Store.TouchAdminLogin(u.ID, key); err != nil {
		log.Printf("[auth] 记录登录来源失败: %v", err)
	}

	t := token()
	now := time.Now()
	a.sessions.Add(&session{
		Token: t, SID: sessionSID(t), UID: u.ID,
		FP:      authn.Fingerprint(u.PassHash),
		IP:      key,
		UA:      r.UserAgent(),
		Created: now, LastSeen: now,
		Exp: now.Add(sessionTTL),
	})
	// 密码哈希为空 = 这个账号从未设置过密码（出厂默认），首次登录强制改密
	mustChange := u.PassHash == ""
	detail := "登录成功"
	if mode != "" {
		detail += "（已通过 " + mode + " 二次验证）"
	}
	a.auditAs(u.Username, r, AuditLogin, u.Username, store.AuditOK, detail)
	writeJSON(w, 200, map[string]any{
		"token": t, "must_change": mustChange,
		"user": u.Username, "role": u.Role,
		"last_ip": prevIP, "last_time": prevTime,
	})
}

// ---- logout ----

// handleLogout 注销当前会话。
//
// 会话是服务端内存里的 token，登出必须由服务端删除。前端只清 localStorage
// 只是"自己看不见了"——token 在剩余有效期（24h）内仍可被任何拿到它的人使用。
// 这也是「怀疑 token 泄漏」时唯一的止血手段（改密会踢掉所有会话，
// 代价是所有设备都要重新登录）。
func (a *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	t := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	a.sessions.Delete(t)
	a.auditOK(r, AuditLogout, "", "主动登出")
	writeJSON(w, 200, map[string]any{"ok": true})
}
