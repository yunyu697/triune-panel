// Package api 提供 HTTP API 与订阅服务。
package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"triune/internal/authn"
	"triune/internal/core"
	"triune/internal/firewall"
	"triune/internal/link"
	"triune/internal/model"
	"triune/internal/ops"
	"triune/internal/proc"
	"triune/internal/store"
)

type Server struct {
	Store      *store.Store
	Pipe       *core.PipelineManager
	ProcMgr    *proc.Manager
	DataDir    string
	CertMgr    CertManager    // 可为 nil（未启用 ACME）
	Coll       *ops.Collector // 流量采样器（实时速率查询用），可为 nil
	sessions   *sessionStore  // 登录会话表（见 sessions.go）
	loginFails sync.Map       // ip -> *loginFail（防爆破）
	totpStaged sync.Map       // uid -> 待绑定的 TOTP 密钥（每账号一份，重启即丢）
	emailCode  emailCodeState // 登录邮箱验证码（2FA）
}

// session 结构已迁到 sessions.go（带 IP / UA / 活跃时间，供「登录设备」页使用）。

// CertManager ACME 证书管理器接口（internal/certmgr 实现）。
type CertManager interface {
	Status() []map[string]any
	CheckNow() // 手动触发一次签发/续期检查
}

func New(s *store.Store, pipe *core.PipelineManager, pm *proc.Manager, dataDir string) *Server {
	return &Server{
		Store: s, Pipe: pipe, ProcMgr: pm, DataDir: dataDir,
		sessions: newSessionStore(),
	}
}

func (a *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// ---- 免认证 ----
	mux.HandleFunc("POST /api/login", a.handleLogin)
	mux.HandleFunc("GET /healthz", a.handleHealthz)            // 免认证探活，供 systemd / 监控使用
	mux.HandleFunc("GET /metrics", a.handleMetrics)            // Prometheus 指标，凭 metrics_token 访问（默认关闭）
	mux.HandleFunc("GET /api/public/info", a.handlePublicInfo) // 免认证，仅版本号
	mux.HandleFunc("GET /sub/{token}", a.handleSub)            // 订阅，凭 sub_token

	// ---- 任意登录账号（含只读 viewer）：只看不改，且响应不吐凭据 ----
	mux.HandleFunc("POST /api/logout", a.auth(a.handleLogout))
	mux.HandleFunc("GET /api/me", a.auth(a.handleMe))
	mux.HandleFunc("PUT /api/me/password", a.auth(a.handleChangeOwnPassword))
	// 登录设备：服务端按角色过滤（viewer 只看到自己的会话）
	mux.HandleFunc("GET /api/sessions", a.auth(a.handleSessionsList))
	mux.HandleFunc("DELETE /api/sessions", a.auth(a.handleSessionsRevokeOthers))
	mux.HandleFunc("DELETE /api/sessions/{id}", a.auth(a.handleSessionRevoke))
	// 登录历史：从审计的 login 记录按人聚合（viewer 只看到自己）
	mux.HandleFunc("GET /api/login-history", a.auth(a.handleLoginHistory))
	mux.HandleFunc("GET /api/auth2fa", a.auth(a.handleAuth2FAStatus))
	mux.HandleFunc("GET /api/status", a.auth(a.handleStatus))
	mux.HandleFunc("GET /api/inbounds", a.auth(a.listInbounds))
	mux.HandleFunc("GET /api/inbounds/{id}/check", a.auth(a.checkInbound))
	mux.HandleFunc("GET /api/external-nodes", a.auth(a.listExternal))
	mux.HandleFunc("GET /api/revisions", a.auth(a.listRevisions))
	mux.HandleFunc("GET /api/logs", a.auth(a.handleLogs))
	mux.HandleFunc("GET /api/traffic", a.auth(a.handleTraffic))
	mux.HandleFunc("GET /api/traffic/daily", a.auth(a.handleTrafficDaily))
	mux.HandleFunc("GET /api/traffic/live", a.auth(a.handleTrafficLive))
	mux.HandleFunc("GET /api/version", a.auth(a.handleVersion))
	mux.HandleFunc("GET /api/components", a.auth(a.handleComponents))
	mux.HandleFunc("GET /api/updates/versions", a.auth(a.handleUpdateVersions))
	mux.HandleFunc("GET /api/certs", a.auth(a.handleCerts))
	mux.HandleFunc("GET /api/tools/uuid", a.auth(a.handleUUID))

	// ---- 管理员（owner + admin）：会改状态，或响应里带凭据 ----
	mux.HandleFunc("POST /api/auth2fa/totp/setup", a.write(a.handleAuth2FATotpSetup))
	mux.HandleFunc("POST /api/auth2fa/totp/enable", a.write(a.handleAuth2FATotpEnable))
	mux.HandleFunc("POST /api/auth2fa/email/enable", a.write(a.handleAuth2FAEmailEnable))
	mux.HandleFunc("POST /api/auth2fa/email/test", a.write(a.handleAuth2FAEmailTest))
	mux.HandleFunc("POST /api/auth2fa/disable", a.write(a.handleAuth2FADisable))
	mux.HandleFunc("POST /api/inbounds", a.write(a.createInbound))
	mux.HandleFunc("PUT /api/inbounds/{id}", a.write(a.updateInbound))
	mux.HandleFunc("DELETE /api/inbounds/{id}", a.write(a.deleteInbound))
	// 导出接口会把节点凭据（UUID / 密码 / 私钥）整段吐出来，只读角色不能碰
	mux.HandleFunc("GET /api/inbounds/{id}/export", a.write(a.handleInboundExport))
	mux.HandleFunc("GET /api/export/catalog", a.write(a.handleExportCatalog))
	mux.HandleFunc("POST /api/export/config", a.write(a.handleExportConfig))
	// 自检会调用本机内核二进制，属于「能让服务器跑东西」的接口，按 write 档
	mux.HandleFunc("POST /api/export/verify", a.write(a.handleExportVerify))

	// 保存的客户端配置：内容含节点凭据，整组按 write 档
	mux.HandleFunc("GET /api/configs", a.write(a.handleConfigsList))
	mux.HandleFunc("POST /api/configs", a.write(a.handleConfigSave))
	mux.HandleFunc("GET /api/configs/{id}", a.write(a.handleConfigGet))
	mux.HandleFunc("PUT /api/configs/{id}", a.write(a.handleConfigUpdate))
	mux.HandleFunc("DELETE /api/configs/{id}", a.write(a.handleConfigDelete))
	mux.HandleFunc("GET /api/configs/{id}/download", a.write(a.handleConfigDownload))

	// 自定义规则标签：共享的运维经验，同样只在写档以上可见
	mux.HandleFunc("GET /api/rule-tags", a.write(a.handleTagsList))
	mux.HandleFunc("POST /api/rule-tags", a.write(a.handleTagCreate))
	mux.HandleFunc("PUT /api/rule-tags/{id}", a.write(a.handleTagUpdate))
	mux.HandleFunc("DELETE /api/rule-tags/{id}", a.write(a.handleTagDelete))
	// 客户端列表：只读账号也放行，但响应是**脱敏副本**（凭据与订阅令牌置空）。
	// 之前整条挡在 write 档外，代价是只读账号连「谁用了多少流量」都看不到；
	// 真正敏感的只有那两列，把整页关掉属于用大炮打蚊子。
	mux.HandleFunc("GET /api/clients", a.auth(a.listClients))
	mux.HandleFunc("POST /api/clients", a.write(a.createClient))
	mux.HandleFunc("PUT /api/clients/{id}", a.write(a.updateClient))
	mux.HandleFunc("DELETE /api/clients/{id}", a.write(a.deleteClient))
	mux.HandleFunc("PUT /api/clients/{id}/inbounds", a.write(a.setClientInbounds))
	mux.HandleFunc("POST /api/clients/{id}/reset-token", a.write(a.resetSubToken))
	mux.HandleFunc("POST /api/config/diff", a.write(a.handleConfigDiff))
	mux.HandleFunc("POST /api/external-nodes/import-url", a.write(a.importSubURL))
	mux.HandleFunc("POST /api/external-nodes", a.write(a.addExternal))
	mux.HandleFunc("DELETE /api/external-nodes/{id}", a.write(a.deleteExternal))
	mux.HandleFunc("POST /api/import", a.write(a.handleImport))
	mux.HandleFunc("POST /api/apply", a.write(a.handleApply))
	mux.HandleFunc("POST /api/apply/dry-run", a.write(a.handleDryRun))
	mux.HandleFunc("POST /api/rollback", a.write(a.handleRollback))
	mux.HandleFunc("POST /api/switch-core", a.write(a.handleSwitchCore))
	// 设置里含 stats_secret / metrics_token / SMTP 账号，不给只读角色看
	mux.HandleFunc("GET /api/settings", a.write(a.getSettings))
	mux.HandleFunc("PUT /api/settings", a.write(a.putSettings))
	mux.HandleFunc("GET /api/audit", a.write(a.handleAuditList))
	mux.HandleFunc("POST /api/tools/reality-keypair", a.write(a.handleRealityKeypair))
	mux.HandleFunc("POST /api/self-update", a.write(a.handleSelfUpdate))
	// 变更通知：发一条测试事件，把错误原样返回（配错了要能立刻发现）
	mux.HandleFunc("POST /api/notify/test", a.write(a.handleNotifyTest))
	mux.HandleFunc("POST /api/cores/{name}/update", a.write(a.handleCoreUpdate))
	mux.HandleFunc("POST /api/cores/{name}/rollback", a.write(a.handleCoreRollback))
	mux.HandleFunc("GET /api/panel/backups", a.write(a.handlePanelBackups))
	mux.HandleFunc("POST /api/certs/issue", a.write(a.handleCertsIssue))

	// ---- 仅超级管理员：账号管理、销毁审计、整面板回滚 ----
	mux.HandleFunc("GET /api/admins", a.ownerOnly(a.handleAdminsList))
	mux.HandleFunc("POST /api/admins", a.ownerOnly(a.handleAdminCreate))
	mux.HandleFunc("PUT /api/admins/{id}", a.ownerOnly(a.handleAdminUpdate))
	mux.HandleFunc("DELETE /api/admins/{id}", a.ownerOnly(a.handleAdminDelete))
	mux.HandleFunc("DELETE /api/audit", a.ownerOnly(a.handleAuditClear))
	mux.HandleFunc("POST /api/panel/rollback", a.ownerOnly(a.handlePanelRollback))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// 禁止中间层/浏览器长期缓存页面：升级面板后旧 UI 常驻缓存会导致
		// "明明升级了，界面还是旧的"。API 走 JSON 不受影响。
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, filepath.Join(a.DataDir, "web", "index.html"))
	})
	// 安全响应头包在最外层：连 401/404 这类错误响应也要带上，
	// 否则错误页仍可能被浏览器按 HTML 嗅探执行。
	return a.securityHeaders(mux)
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

func token() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (a *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if t == "" {
			fail(w, 401, "unauthorized")
			return
		}
		s, ok := a.sessions.Get(t)
		if !ok {
			fail(w, 401, "unauthorized")
			return
		}
		if s.Exp.Before(time.Now()) {
			a.sessions.Delete(t)
			fail(w, 401, "session expired")
			return
		}
		// 每次请求都回库取账号：账号被删、被停用、密码被改都必须立刻生效。
		// 只信内存里的会话副本会让「停用一个管理员」变成空操作——他还坐在
		// 已经登录的浏览器里继续操作。
		u, err := a.Store.GetAdmin(s.UID)
		if err != nil || !u.Enabled || authn.Fingerprint(u.PassHash) != s.FP {
			a.sessions.Delete(t)
			fail(w, 401, "session expired")
			return
		}
		// 记「最后活跃」：列表里能区分「还在用」与「登录后就没动过」
		a.sessions.Touch(t, time.Now())
		next(w, withUser(r, u))
	}
}

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

// ---- status ----

func (a *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	type coreStatus struct {
		Core       string `json:"core"`
		BinSet     bool   `json:"bin_set"`
		BinFound   bool   `json:"bin_found"`
		Configured bool   `json:"config_exists"`
		Running    bool   `json:"running"`
	}
	var out []coreStatus
	for _, c := range core.All() {
		cs := coreStatus{Core: c.Name()}
		bin, _ := a.Store.GetSetting("bin_" + c.Name())
		if bin != "" {
			cs.BinSet = true
			cs.BinFound = fileExists(bin)
		}
		if p, _ := a.Pipe.ConfigPath(c.Name()); p != "" && fileExists(p) {
			cs.Configured = true
		}
		cs.Running, _ = a.ProcMgr.Status(c.Name())
		out = append(out, cs)
	}
	writeJSON(w, 200, map[string]any{"cores": out})
}

func fileExists(p string) bool {
	st, err := statFile(p)
	return err == nil && !st.IsDir()
}

// ---- inbounds ----

func (a *Server) listInbounds(w http.ResponseWriter, r *http.Request) {
	ins, err := a.Store.ListInbounds(r.URL.Query().Get("core"))
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if ins == nil {
		ins = []*model.Inbound{}
	}
	writeJSON(w, 200, ins)
}

func (a *Server) createInbound(w http.ResponseWriter, r *http.Request) {
	var in model.Inbound
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if in.Settings == "" {
		in.Settings = "{}"
	}
	if err := a.Store.CreateInbound(&in); err != nil {
		fail(w, 500, err.Error())
		return
	}
	// 自动放行防火墙端口：仅非草稿、且开启自动管理时。草稿是导入预览，
	// 未参与渲染，待「取消草稿」转正时（updateInbound）再放行。
	if fwAuto(a) && !in.Draft {
		proto := firewall.ProtoOf(in.Protocol, in.Transport)
		if err := firewall.OpenPort(in.Port, proto); err != nil {
			log.Printf("[fw] 开放入站端口 %d/%s 失败: %v", in.Port, proto, err)
		}
	}
	detail := fmt.Sprintf("%s/%s 端口 %d", in.Core, in.Protocol, in.Port)
	if in.Draft {
		detail += "（草稿）"
	}
	a.changed(r, AuditInboundCreate, in.Name, detail)
	writeJSON(w, 200, in)
}

// updateInbound 做的是「部分更新」：只覆盖请求里真正出现的键。
//
// 旧实现和 updateClient 是同一个毛病——把请求体解成完整的 model.Inbound 再
// 整行 UPDATE。`PUT {"name":"改名"}` 会把 core/protocol/port/tag/settings 全部
// 清成零值（settings 变 "{}"、port 变 0），而且返回 200。更糟的是内核切换守卫
// 拿零值去比，启用中的入站会收到「请使用 /api/switch-core」——调用方根本没想
// 换内核，这条错误只会让人以为是权限或状态问题。
func (a *Server) updateInbound(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		fail(w, 400, err.Error())
		return
	}

	old, err := a.Store.GetInbound(pathID(r))
	if err != nil {
		fail(w, 404, err.Error())
		return
	}
	in := *old
	for k, v := range raw {
		var ferr error
		switch k {
		case "name":
			ferr = json.Unmarshal(v, &in.Name)
		case "core":
			ferr = json.Unmarshal(v, &in.Core)
		case "protocol":
			ferr = json.Unmarshal(v, &in.Protocol)
		case "listen":
			ferr = json.Unmarshal(v, &in.Listen)
		case "port":
			ferr = json.Unmarshal(v, &in.Port)
		case "tag":
			ferr = json.Unmarshal(v, &in.Tag)
		case "transport":
			ferr = json.Unmarshal(v, &in.Transport)
		case "tls":
			ferr = json.Unmarshal(v, &in.TLS)
		case "settings":
			ferr = json.Unmarshal(v, &in.Settings)
		case "enabled":
			ferr = json.Unmarshal(v, &in.Enabled)
		case "draft":
			ferr = json.Unmarshal(v, &in.Draft)
		}
		if ferr != nil {
			fail(w, 400, "字段 "+k+" 格式有误: "+ferr.Error())
			return
		}
	}
	if in.Settings == "" {
		in.Settings = "{}"
	}
	// 运行中的入站不允许直接改内核 —— 必须走 /api/switch-core 安全切换流程
	// （预检 → 停旧 → 启新 → 健康检查 → 失败自动回滚），否则可能出现
	// 新内核配置从未校验过就上线的风险。
	if old.Core != in.Core && old.Enabled && !old.Draft {
		fail(w, 400, "运行中的入站切换内核请使用 POST /api/switch-core（含预检与健康检查）")
		return
	}
	if err := a.Store.UpdateInbound(&in); err != nil {
		fail(w, 500, err.Error())
		return
	}
	// 防火墙端口对账：草稿转正 / 端口或协议变化
	if fwAuto(a) {
		oldProto := firewall.ProtoOf(old.Protocol, old.Transport)
		newProto := firewall.ProtoOf(in.Protocol, in.Transport)
		// 草稿 → 正式：放行新端口
		if old.Draft && !in.Draft {
			if err := firewall.OpenPort(in.Port, newProto); err != nil {
				log.Printf("[fw] 转正放行端口 %d/%s 失败: %v", in.Port, newProto, err)
			}
		}
		// 端口或协议变化：关闭旧（无其他入站占用时）+ 开放新
		if (old.Port != in.Port || oldProto != newProto) && !in.Draft {
			if !a.portInUseByOthers(in.ID, old.Port, oldProto) {
				if err := firewall.ClosePort(old.Port, oldProto); err != nil {
					log.Printf("[fw] 回收旧端口 %d/%s 失败: %v", old.Port, oldProto, err)
				}
			}
			if err := firewall.OpenPort(in.Port, newProto); err != nil {
				log.Printf("[fw] 开放新端口 %d/%s 失败: %v", in.Port, newProto, err)
			}
		}
	}
	detail := fmt.Sprintf("%s/%s 端口 %d", in.Core, in.Protocol, in.Port)
	if old.Draft && !in.Draft {
		detail += "（草稿转正）"
	}
	a.changed(r, AuditInboundUpdate, in.Name, detail, inboundDiff(old, &in)...)
	writeJSON(w, 200, in)
}

func (a *Server) deleteInbound(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	in, _ := a.Store.GetInbound(id)
	if err := a.Store.DeleteInbound(id); err != nil {
		fail(w, 500, err.Error())
		return
	} // 回收防火墙端口：仅当该端口/协议不再被其它启用入站使用时
	if fwAuto(a) && in != nil && !in.Draft {
		proto := firewall.ProtoOf(in.Protocol, in.Transport)
		if !a.portInUseByOthers(id, in.Port, proto) {
			if err := firewall.ClosePort(in.Port, proto); err != nil {
				log.Printf("[fw] 回收入站端口 %d/%s 失败: %v", in.Port, proto, err)
			}
		}
	}
	name := fmt.Sprintf("#%d", id)
	if in != nil {
		name = in.Name
	}
	a.changed(r, AuditInboundDelete, name, fmt.Sprintf("删除入站 #%d", id))
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

// fwAuto 是否启用入站端口的自动防火墙管理（默认开启，设置 fw_auto=0 关闭）。
func fwAuto(a *Server) bool {
	v, _ := a.Store.GetSetting("fw_auto")
	return v != "0"
}

// SyncFirewallPorts 启动时对账：为所有「已启用且非草稿」的入站端口补一次放行。
// 场景：面板升级到带自动放行能力的版本后，存量入站不会重新保存一遍，
// 不主动对账就会一直处于「已监听但防火墙未放行」的状态。
// 幂等：已放行的端口会被各后端自身跳过；无防火墙后端时直接空转。
func (a *Server) SyncFirewallPorts() {
	if !fwAuto(a) {
		return
	}
	ins, err := a.Store.ListInbounds("")
	if err != nil {
		return
	}
	seen := map[string]bool{}
	for _, in := range ins {
		if in.Draft || !in.Enabled {
			continue
		}
		proto := firewall.ProtoOf(in.Protocol, in.Transport)
		key := fmt.Sprintf("%d/%s", in.Port, proto)
		if seen[key] {
			continue
		}
		seen[key] = true
		if err := firewall.OpenPort(in.Port, proto); err != nil {
			log.Printf("[fw] 启动对账放行 %s 失败: %v", key, err)
		}
	}
}

// portInUseByOthers 判断给定端口/协议是否仍被其它「已启用且非草稿」的入站使用，
// 用于删除/改端口时避免误关仍被占用的端口。
func (a *Server) portInUseByOthers(selfID int64, port int, proto string) bool {
	others, err := a.Store.ListInbounds("")
	if err != nil {
		return false
	}
	for _, o := range others {
		if o.ID == selfID || !o.Enabled || o.Draft {
			continue
		}
		if o.Port == port && firewall.ProtoOf(o.Protocol, o.Transport) == proto {
			return true
		}
	}
	return false
}

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

// ---- external nodes ----

func (a *Server) listExternal(w http.ResponseWriter, r *http.Request) {
	ns, err := a.Store.ListExternalNodes()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if ns == nil {
		ns = []*model.ExternalNode{}
	}
	writeJSON(w, 200, ns)
}

func (a *Server) addExternal(w http.ResponseWriter, r *http.Request) {
	var n model.ExternalNode
	if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if _, err := link.Parse(n.RawLink); err != nil {
		fail(w, 400, "链接无法解析: "+err.Error())
		return
	}
	if err := a.Store.CreateExternalNode(&n); err != nil {
		fail(w, 500, err.Error())
		return
	}
	a.auditOK(r, AuditExternal, n.Name, "新增外部节点")
	writeJSON(w, 200, n)
}

func (a *Server) deleteExternal(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	if err := a.Store.DeleteExternalNode(id); err != nil {
		fail(w, 500, err.Error())
		return
	}
	a.auditOK(r, AuditExternal, fmt.Sprintf("#%d", id), fmt.Sprintf("删除外部节点 #%d", id))
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

// ---- import ----

// handleImport 反向导入分享链接。
// mode=external: 仅聚合进订阅（最安全，不生成任何本地配置）
// mode=inbound:  生成本地入站草稿（draft=true，需手动启用并 apply）
func (a *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string `json:"text"`
		Mode string `json:"mode"` // external | inbound
		Core string `json:"core"` // mode=inbound 时的目标内核
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if req.Mode == "" {
		req.Mode = "external"
	}
	nodes, errs := link.ParseMany(req.Text)
	if len(nodes) == 0 {
		fail(w, 400, "没有可解析的链接: "+joinErrs(errs))
		return
	}

	var created, failed []string
	for _, n := range nodes {
		if req.Mode == "inbound" {
			if req.Core == "" {
				req.Core = model.CoreSingBox
			}
			in := nodeToInbound(n, req.Core)
			if err := a.Store.CreateInbound(in); err != nil {
				failed = append(failed, n.Name+": "+err.Error())
				continue
			}
			// 同步创建携带原始凭据的用户并关联，保证导入即可订阅
			c := &model.Client{
				Name:       orDefault(n.Name, "imported-"+in.Tag),
				Credential: n.Cred,
				SubToken:   token(),
				Enabled:    true,
			}
			if err := a.Store.CreateClient(c); err == nil {
				_ = a.Store.SetClientInbounds(c.ID, []int64{in.ID})
			}
			created = append(created, fmt.Sprintf("%s → 入站草稿 id=%d（未生效，需 apply）", n.Name, in.ID))
		} else {
			en := &model.ExternalNode{Name: orDefault(n.Name, n.Host), RawLink: n.Raw}
			if err := a.Store.CreateExternalNode(en); err != nil {
				failed = append(failed, n.Name+": "+err.Error())
				continue
			}
			created = append(created, fmt.Sprintf("%s → 外部节点（仅订阅聚合）", en.Name))
		}
	}
	writeJSON(w, 200, map[string]any{"created": created, "failed": failed, "parse_errors": errStrings(errs)})
	if len(created) > 0 {
		a.auditOK(r, AuditImport, req.Mode,
			fmt.Sprintf("导入 %d 条（模式 %s，失败 %d）", len(created), req.Mode, len(failed)))
	}
}

// nodeToInbound 把解析出的节点映射为本地入站草稿。
func nodeToInbound(n *link.Node, coreName string) *model.Inbound {
	settings := map[string]any{}
	for k, v := range n.Extra {
		if s, ok := v.(string); ok && s != "" {
			settings[k] = s
		}
	}
	if n.SNI != "" {
		settings["server_name"] = n.SNI
	}
	if n.Path != "" {
		settings["path"] = n.Path
	}
	settingsJSON, _ := json.Marshal(settings)
	return &model.Inbound{
		Name:      orDefault(n.Name, n.Host),
		Core:      coreName,
		Protocol:  n.Protocol,
		Port:      n.Port,
		Transport: orDefault(n.Network, "tcp"),
		TLS:       orDefault(n.Security, "none"),
		Settings:  string(settingsJSON),
		Enabled:   false, // 草稿不参与渲染，确认后手动启用
		Draft:     true,
	}
}

// ---- apply pipeline ----

// buildRenderInput 汇总某内核所有启用且非草稿的入站及用户。
func (a *Server) buildRenderInput(coreName string) ([]core.InboundWithClients, error) {
	ins, err := a.Store.ListInbounds(coreName)
	if err != nil {
		return nil, err
	}
	allClients, err := a.Store.ListClients()
	if err != nil {
		return nil, err
	}
	byID := map[int64]*model.Client{}
	for _, c := range allClients {
		byID[c.ID] = c
	}
	var out []core.InboundWithClients
	for _, in := range ins {
		if !in.Enabled || in.Draft {
			continue
		}
		iw := core.InboundWithClients{Inbound: in}
		ids, _ := a.Store.InboundClientIDs(in.ID)
		for _, id := range ids {
			if c, ok := byID[id]; ok && c.Enabled {
				iw.Clients = append(iw.Clients, c)
			}
		}
		out = append(out, iw)
	}
	return out, nil
}

func (a *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Core string `json:"core"`
		Note string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, err.Error())
		return
	}
	input, err := a.buildRenderInput(req.Core)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if req.Note == "" {
		req.Note = "apply via api"
	}
	// 先渲染落一条 pending 修订
	c, err := core.Get(req.Core)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	content, rerr := c.Render(input, a.Pipe.RenderOpts())
	rev := &model.ConfigRevision{Core: req.Core, Status: model.RevPending, Note: req.Note}
	if rerr == nil {
		rev.Content = string(content)
		rev.Hash = shortHash(content)
	} else {
		rev.Detail = rerr.Error()
	}
	_ = a.Store.CreateRevision(rev)

	res, err := a.Pipe.Apply(req.Core, input, req.Note)
	if err != nil {
		_ = a.Store.UpdateRevisionStatus(rev.ID, model.RevFailed, err.Error())
		a.auditFail(r, AuditApply, req.Core, "应用失败（运行中配置未变）："+err.Error())
		// 运行中配置未受影响 —— 这是本系统的安全保证
		writeJSON(w, 422, map[string]any{"error": err.Error(), "revision_id": rev.ID, "hint": "运行中的配置未被修改；可用 rollback 恢复历史备份"})
		return
	}
	_ = a.Store.UpdateRevisionStatus(rev.ID, model.RevApplied, "")
	res.RevisionID = rev.ID
	a.changed(r, AuditApply, req.Core, fmt.Sprintf("应用成功，修订 #%d", rev.ID))
	writeJSON(w, 200, res)
}

func (a *Server) handleDryRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Core string `json:"core"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, err.Error())
		return
	}
	input, err := a.buildRenderInput(req.Core)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	res, content, err := a.Pipe.DryRun(req.Core, input)
	if err != nil {
		fail(w, 422, err.Error())
		return
	}
	if res == nil {
		res = &core.ApplyResult{Core: req.Core}
	}
	writeJSON(w, 200, map[string]any{"result": res, "content": content})
}

func (a *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Core string `json:"core"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, err.Error())
		return
	}
	bak, err := a.Pipe.Rollback(req.Core)
	if err != nil {
		a.auditFail(r, AuditRollback, req.Core, "回滚失败："+err.Error())
		fail(w, 422, err.Error())
		return
	}
	a.changed(r, AuditRollback, req.Core, "回滚到备份 "+bak)
	writeJSON(w, 200, map[string]string{"restored_from": bak})
}

func (a *Server) listRevisions(w http.ResponseWriter, r *http.Request) {
	coreName := r.URL.Query().Get("core")
	limit := atoiDefault(r.URL.Query().Get("limit"), 30)
	revs, err := a.Store.ListRevisions(coreName, limit)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if revs == nil {
		revs = []*model.ConfigRevision{}
	}
	writeJSON(w, 200, revs)
}

func (a *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	coreName := r.URL.Query().Get("core")
	lines := atoiDefault(r.URL.Query().Get("lines"), 100)
	dir := filepath.Join(a.DataDir, "cores", coreName)
	writeJSON(w, 200, map[string]any{"logs": a.ProcMgr.Logs(coreName, dir, lines)})
}

// ---- settings ----

func (a *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	out := map[string]string{}
	for _, c := range core.All() {
		v, _ := a.Store.GetSetting("bin_" + c.Name())
		out["bin_"+c.Name()] = v
	}
	for _, k := range []string{"public_host", "stats_enabled", "stats_secret",
		"quota_warn_pct", "fw_auto",
		// Prometheus 指标令牌（空 = /metrics 关闭）
		MetricsTokenSetting,
		// 可信反向代理网段（登录来源识别用，默认空 = 不信任任何代理）
		TrustedProxiesSetting,
		"acme_enabled", "acme_domain", "acme_email",
		"gh_mirror", "gh_api_base", "panel_repo", "panel_update_index",
		// 2FA 状态展示（totp_secret 不下发前端）
		"auth_2fa_mode", "auth_email",
		"smtp_host", "smtp_port", "smtp_user", "smtp_from",
		// 变更通知：Webhook 地址可能带 token，但它是「自己填给自己看」的配置，
		// 只读角色拿不到这个接口（write 档），所以照常回显便于核对。
		NotifyEmailSetting, NotifyWebhookSetting, NotifyEventsSetting} {
		v, _ := a.Store.GetSetting(k)
		out[k] = v
	}
	writeJSON(w, 200, out)
}

func (a *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var m map[string]string
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		fail(w, 400, err.Error())
		return
	}
	// 改密单独处理：只改**当前登录账号**的密码（多管理员之后不存在
	// 「全局面板密码」这种东西），密码不走白名单直写。
	// 改完当前会话因指纹变化失效，前端收到 relogin 后回登录页。
	relogin := false
	if pass, ok := m["admin_pass"]; ok {
		delete(m, "admin_pass")
		if pass != "" {
			u := a.currentUser(r)
			if u == nil {
				fail(w, 401, "unauthorized")
				return
			}
			if err := authn.SetPasswordFor(a.Store, u.ID, pass); err != nil {
				fail(w, 400, err.Error())
				return
			}
			// 主动清理该账号会话（指纹校验是兜底，这里让「登录设备」页立刻准确）
			a.sessions.DeleteByUID(u.ID)
			relogin = true
		}
	}
	allowed := map[string]bool{
		"public_host": true, "stats_enabled": true, "stats_secret": true,
		"quota_warn_pct": true,
		// Prometheus 指标令牌：写空字符串即关闭 /metrics
		MetricsTokenSetting: true,
		// 入站端口的自动防火墙管理（fwAuto 读取，默认开启）
		"fw_auto": true,
		// 可信反向代理网段：仅当直连对端落在这些网段内才采信转发头
		TrustedProxiesSetting: true,
		"acme_enabled":        true, "acme_domain": true, "acme_email": true,
		// 组件更新相关（UI「保存更新设置」提交这些键）
		"gh_mirror": true, "gh_api_base": true,
		"panel_repo": true, "panel_update_index": true,
		// 2FA 邮箱通道（auth_2fa_mode / totp_secret 由专用端点管理，不接受直接写）
		"auth_email": true,
		"smtp_host":  true, "smtp_port": true, "smtp_user": true,
		"smtp_pass": true, "smtp_from": true,
		// 变更通知（收件邮箱 / Webhook / 订阅哪些事件）
		NotifyEmailSetting: true, NotifyWebhookSetting: true, NotifyEventsSetting: true,
	}
	for _, c := range core.All() {
		allowed["bin_"+c.Name()] = true
	}
	// 可信代理网段先整体校验再落库，避免半套配置写进去（写一半失败时
	// 用户看到的是 500，但设置里已经躺着一条错值）。
	if v, ok := m[TrustedProxiesSetting]; ok {
		if _, err := parseTrustedProxies(v); err != nil {
			fail(w, 400, "可信反向代理填写有误："+err.Error())
			return
		}
	}
	var changedKeys []string
	var diff []string
	for k, v := range m {
		if !allowed[k] {
			continue
		}
		// 先读旧值：变更通知要说清「从什么改成什么」，只报键名等于让人
		// 收到一封「你改了 public_host」却不知道改成了什么的邮件。
		old, _ := a.Store.GetSetting(k)
		if err := a.Store.SetSetting(k, v); err != nil {
			fail(w, 500, err.Error())
			return
		}
		changedKeys = append(changedKeys, k)
		if old != v {
			diff = append(diff, settingDiff(k, old, v))
		}
	}
	// 审计只记「改了哪些键」，不记值——smtp_pass / stats_secret 这类敏感项
	// 一旦进了日志就等于明文落盘，比不记更糟。
	// 通知里的 diff 走 settingDiff，敏感键自动打码。
	if len(changedKeys) > 0 {
		sort.Strings(changedKeys)
		a.auditOK(r, AuditSettings, strings.Join(changedKeys, ","), "修改设置")
		if len(diff) > 0 {
			sort.Strings(diff)
			a.notifyChange(changeEvent{
				Actor: a.actorName(r), Action: AuditSettings,
				Target: strings.Join(changedKeys, ","), Detail: "修改设置", Diff: diff,
			})
		}
	}
	if relogin {
		a.changed(r, AuditPassword, a.actorName(r), "本账号密码已修改，该账号的旧会话全部失效")
	}
	// ACME 设置变更后立即触发一次签发/续期检查，无需等下一个检查周期
	if m["acme_enabled"] == "1" && a.CertMgr != nil {
		go a.CertMgr.CheckNow()
	}
	resp := map[string]any{"ok": "1"}
	if relogin {
		resp["relogin"] = true
	}
	writeJSON(w, 200, resp)
}
