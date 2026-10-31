// Package api 提供 HTTP API 与订阅服务。
package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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
	Store    *store.Store
	Pipe     *core.PipelineManager
	ProcMgr  *proc.Manager
	DataDir  string
	CertMgr  CertManager   // 可为 nil（未启用 ACME）
	Coll     *ops.Collector // 流量采样器（实时速率查询用），可为 nil
	sessions   sync.Map     // token -> session
	loginFails sync.Map     // ip -> *loginFail（防爆破）
	emailCode  emailCodeState // 登录邮箱验证码（2FA）
}

// session 一次登录签发的会话。
// epoch 记录签发时的会话纪元，改密/重置密码后纪元变化 → 旧会话全部失效
// （CLI 重置密码在另一个进程里，只有存 DB 的纪元能让 daemon 立刻感知）。
type session struct {
	exp   time.Time
	epoch string
}

// CertManager ACME 证书管理器接口（internal/certmgr 实现）。
type CertManager interface {
	Status() []map[string]any
	CheckNow() // 手动触发一次签发/续期检查
}

func New(s *store.Store, pipe *core.PipelineManager, pm *proc.Manager, dataDir string) *Server {
	return &Server{Store: s, Pipe: pipe, ProcMgr: pm, DataDir: dataDir}
}

func (a *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/login", a.handleLogin)
	mux.HandleFunc("GET /healthz", a.handleHealthz)           // 免认证探活，供 systemd / 监控使用
	mux.HandleFunc("GET /api/public/info", a.handlePublicInfo) // 免认证，仅版本号
	mux.HandleFunc("GET /api/auth2fa", a.auth(a.handleAuth2FAStatus))
	mux.HandleFunc("POST /api/auth2fa/totp/setup", a.auth(a.handleAuth2FATotpSetup))
	mux.HandleFunc("POST /api/auth2fa/totp/enable", a.auth(a.handleAuth2FATotpEnable))
	mux.HandleFunc("POST /api/auth2fa/email/enable", a.auth(a.handleAuth2FAEmailEnable))
	mux.HandleFunc("POST /api/auth2fa/email/test", a.auth(a.handleAuth2FAEmailTest))
	mux.HandleFunc("POST /api/auth2fa/disable", a.auth(a.handleAuth2FADisable))
	mux.HandleFunc("GET /api/status", a.auth(a.handleStatus))
	mux.HandleFunc("GET /api/inbounds", a.auth(a.listInbounds))
	mux.HandleFunc("POST /api/inbounds", a.auth(a.createInbound))
	mux.HandleFunc("PUT /api/inbounds/{id}", a.auth(a.updateInbound))
	mux.HandleFunc("DELETE /api/inbounds/{id}", a.auth(a.deleteInbound))
	mux.HandleFunc("GET /api/inbounds/{id}/export", a.auth(a.handleInboundExport))
	mux.HandleFunc("GET /api/export/catalog", a.auth(a.handleExportCatalog))
	mux.HandleFunc("POST /api/export/config", a.auth(a.handleExportConfig))
	mux.HandleFunc("GET /api/clients", a.auth(a.listClients))
	mux.HandleFunc("POST /api/clients", a.auth(a.createClient))
	mux.HandleFunc("PUT /api/clients/{id}", a.auth(a.updateClient))
	mux.HandleFunc("DELETE /api/clients/{id}", a.auth(a.deleteClient))
	mux.HandleFunc("PUT /api/clients/{id}/inbounds", a.auth(a.setClientInbounds))
	mux.HandleFunc("POST /api/clients/{id}/reset-token", a.auth(a.resetSubToken))
	mux.HandleFunc("GET /api/inbounds/{id}/check", a.auth(a.checkInbound))
	mux.HandleFunc("POST /api/config/diff", a.auth(a.handleConfigDiff))
	mux.HandleFunc("POST /api/external-nodes/import-url", a.auth(a.importSubURL))
	mux.HandleFunc("GET /api/external-nodes", a.auth(a.listExternal))
	mux.HandleFunc("POST /api/external-nodes", a.auth(a.addExternal))
	mux.HandleFunc("DELETE /api/external-nodes/{id}", a.auth(a.deleteExternal))
	mux.HandleFunc("POST /api/import", a.auth(a.handleImport))
	mux.HandleFunc("POST /api/apply", a.auth(a.handleApply))
	mux.HandleFunc("POST /api/apply/dry-run", a.auth(a.handleDryRun))
	mux.HandleFunc("POST /api/rollback", a.auth(a.handleRollback))
	mux.HandleFunc("POST /api/switch-core", a.auth(a.handleSwitchCore))
	mux.HandleFunc("GET /api/revisions", a.auth(a.listRevisions))
	mux.HandleFunc("GET /api/logs", a.auth(a.handleLogs))
	mux.HandleFunc("GET /api/settings", a.auth(a.getSettings))
	mux.HandleFunc("PUT /api/settings", a.auth(a.putSettings))
	mux.HandleFunc("GET /api/audit", a.auth(a.handleAuditList))
	mux.HandleFunc("DELETE /api/audit", a.auth(a.handleAuditClear))
	mux.HandleFunc("GET /api/traffic", a.auth(a.handleTraffic))
	mux.HandleFunc("GET /api/traffic/daily", a.auth(a.handleTrafficDaily))
	mux.HandleFunc("GET /api/traffic/live", a.auth(a.handleTrafficLive))
	mux.HandleFunc("POST /api/tools/reality-keypair", a.auth(a.handleRealityKeypair))
	mux.HandleFunc("GET /api/tools/uuid", a.auth(a.handleUUID))
	mux.HandleFunc("GET /api/version", a.auth(a.handleVersion))
	mux.HandleFunc("POST /api/self-update", a.auth(a.handleSelfUpdate))
	mux.HandleFunc("GET /api/components", a.auth(a.handleComponents))
	mux.HandleFunc("GET /api/updates/versions", a.auth(a.handleUpdateVersions))
	mux.HandleFunc("POST /api/cores/{name}/update", a.auth(a.handleCoreUpdate))
	mux.HandleFunc("POST /api/cores/{name}/rollback", a.auth(a.handleCoreRollback))
	mux.HandleFunc("GET /api/panel/backups", a.auth(a.handlePanelBackups))
	mux.HandleFunc("POST /api/panel/rollback", a.auth(a.handlePanelRollback))
	mux.HandleFunc("GET /api/certs", a.auth(a.handleCerts))
	mux.HandleFunc("POST /api/certs/issue", a.auth(a.handleCertsIssue))
	mux.HandleFunc("GET /sub/{token}", a.handleSub)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// 禁止中间层/浏览器长期缓存页面：升级面板后旧 UI 常驻缓存会导致
		// "明明升级了，界面还是旧的"。API 走 JSON 不受影响。
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, filepath.Join(a.DataDir, "web", "index.html"))
	})
	return mux
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
		v, ok := a.sessions.Load(t)
		if !ok {
			fail(w, 401, "unauthorized")
			return
		}
		s := v.(session)
		// 过期，或改密后纪元已变（旧会话立即失效）
		if s.exp.Before(time.Now()) || s.epoch != authn.Epoch(a.Store) {
			fail(w, 401, "session expired")
			return
		}
		next(w, r)
	}
}

// ---- 登录防爆破 ----

const (
	maxLoginFails   = 5               // 连续失败次数阈值
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
	ok, legacy, err := authn.Verify(a.Store, req.Pass)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if req.User != authn.User(a.Store) || !ok {
		a.recordLoginFail(key)
		a.auditFail(r, AuditLogin, req.User, "用户名或密码错误")
		fail(w, 401, "用户名或密码错误")
		return
	}
	if legacy {
		if err := authn.MigrateLegacy(a.Store); err != nil {
			log.Printf("迁移遗留明文密码失败: %v", err)
		} else {
			log.Printf("已把遗留明文密码升级为 bcrypt 哈希")
		}
	}

	// 二次验证：账密正确后按 auth_2fa_mode 进入第二步。
	mode, _ := a.Store.GetSetting("auth_2fa_mode")
	switch mode {
	case "totp":
		secret, _ := a.Store.GetSetting("totp_secret")
		if secret != "" {
			if strings.TrimSpace(req.Code) == "" {
				writeJSON(w, 200, map[string]any{"need_2fa": "totp"})
				return
			}
			if !verifyTOTP(secret, req.Code) {
				a.recordLoginFail(key)
				a.auditFail(r, AuditLogin, req.User, "动态口令不正确")
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
				a.auditFail(r, AuditLogin, req.User, "邮箱验证码校验失败")
				fail(w, 401, err.Error())
				return
			}
		}
	}

	a.loginFails.Delete(key) // 登录成功，清零失败计数

	// 最近一次成功登录的来源与时间：先读旧值（随本次响应返回给用户，
	// 便于发现异常访问），再写入本次的（概览页常驻展示）。
	prevIP, _ := a.Store.GetSetting("login_last_ip")
	prevTime, _ := a.Store.GetSetting("login_last_time")
	_ = a.Store.SetSetting("login_last_ip", key)
	_ = a.Store.SetSetting("login_last_time", time.Now().Format("2006-01-02 15:04:05"))

	t := token()
	a.sessions.Store(t, session{exp: time.Now().Add(24 * time.Hour), epoch: authn.Epoch(a.Store)})
	// 管理密码仍是出厂默认 → 要求首次登录后强制改密
	mustChange := authn.IsFactoryDefault(a.Store)
	detail := "登录成功"
	if mode != "" {
		detail += "（已通过 " + mode + " 二次验证）"
	}
	a.auditOK(r, AuditLogin, req.User, detail)
	writeJSON(w, 200, map[string]any{
		"token": t, "must_change": mustChange,
		"last_ip": prevIP, "last_time": prevTime,
	})
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
	a.auditOK(r, AuditInboundCreate, in.Name, detail)
	writeJSON(w, 200, in)
}

func (a *Server) updateInbound(w http.ResponseWriter, r *http.Request) {	var in model.Inbound
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	in.ID = pathID(r)
	if in.Settings == "" {
		in.Settings = "{}"
	}
	// 运行中的入站不允许直接改内核 —— 必须走 /api/switch-core 安全切换流程
	// （预检 → 停旧 → 启新 → 健康检查 → 失败自动回滚），否则可能出现
	// 新内核配置从未校验过就上线的风险。
	var old *model.Inbound
	if o, err := a.Store.GetInbound(in.ID); err == nil && o != nil {
		old = o
		if o.Core != in.Core && o.Enabled && !o.Draft {
			fail(w, 400, "运行中的入站切换内核请使用 POST /api/switch-core（含预检与健康检查）")
			return
		}
	}
	if err := a.Store.UpdateInbound(&in); err != nil {
		fail(w, 500, err.Error())
		return
	}
	// 防火墙端口对账：草稿转正 / 端口或协议变化
	if fwAuto(a) && old != nil {
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
	if old != nil && old.Draft && !in.Draft {
		detail += "（草稿转正）"
	}
	a.auditOK(r, AuditInboundUpdate, in.Name, detail)
	writeJSON(w, 200, in)
}

func (a *Server) deleteInbound(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	in, _ := a.Store.GetInbound(id)
	if err := a.Store.DeleteInbound(id); err != nil {
		fail(w, 500, err.Error())
		return
	}	// 回收防火墙端口：仅当该端口/协议不再被其它启用入站使用时
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
	a.auditOK(r, AuditInboundDelete, name, fmt.Sprintf("删除入站 #%d", id))
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

func (a *Server) listClients(w http.ResponseWriter, r *http.Request) {
	cs, err := a.Store.ListClients()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if cs == nil {
		cs = []*model.Client{}
	}
	out := make([]map[string]any, 0, len(cs))
	for _, c := range cs {
		ids, _ := a.Store.ClientInboundIDs(c.ID)
		out = append(out, map[string]any{"client": c, "inbound_ids": ids})
	}
	writeJSON(w, 200, out)
}

func (a *Server) createClient(w http.ResponseWriter, r *http.Request) {
	var req struct {
		model.Client
		InboundIDs []int64 `json:"inbound_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, err.Error())
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
	a.auditOK(r, AuditClientCreate, req.Client.Name,
		fmt.Sprintf("新建用户 #%d，绑定 %d 个入站", req.Client.ID, len(req.InboundIDs)))
	writeJSON(w, 200, req.Client)
}

func (a *Server) updateClient(w http.ResponseWriter, r *http.Request) {
	var c model.Client
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		fail(w, 400, err.Error())
		return
	}
	c.ID = pathID(r)
	if err := a.Store.UpdateClient(&c); err != nil {
		fail(w, 500, err.Error())
		return
	}
	a.auditOK(r, AuditClientUpdate, c.Name, fmt.Sprintf("修改用户 #%d", c.ID))
	writeJSON(w, 200, c)
}

func (a *Server) deleteClient(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	if err := a.Store.DeleteClient(id); err != nil {
		fail(w, 500, err.Error())
		return
	}
	a.auditOK(r, AuditClientDelete, fmt.Sprintf("#%d", id), fmt.Sprintf("删除用户 #%d", id))
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
	if err := a.Store.SetClientInbounds(id, req.InboundIDs); err != nil {
		fail(w, 500, err.Error())
		return
	}
	a.auditOK(r, AuditClientUpdate, fmt.Sprintf("#%d", id),
		fmt.Sprintf("调整用户 #%d 的入站绑定，共 %d 个", id, len(req.InboundIDs)))
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
	a.auditOK(r, AuditApply, req.Core, fmt.Sprintf("应用成功，修订 #%d", rev.ID))
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
	a.auditOK(r, AuditRollback, req.Core, "回滚到备份 "+bak)
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
		// 可信反向代理网段（登录来源识别用，默认空 = 不信任任何代理）
		TrustedProxiesSetting,
		"acme_enabled", "acme_domain", "acme_email",
		"gh_mirror", "gh_api_base", "panel_repo", "panel_update_index",
		// 2FA 状态展示（totp_secret 不下发前端）
		"auth_2fa_mode", "auth_email",
		"smtp_host", "smtp_port", "smtp_user", "smtp_from",
		// 最近一次成功登录（只读展示，PUT 白名单不含）
		"login_last_ip", "login_last_time"} {
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
	// 改密单独处理：写 bcrypt 哈希 + 递增会话纪元，旧会话（含当前这个）
	// 立即失效，前端收到 relogin 后清 token 回登录页。密码不走白名单直写。
	relogin := false
	if pass, ok := m["admin_pass"]; ok {
		delete(m, "admin_pass")
		if pass != "" {
			if err := authn.SetPassword(a.Store, pass); err != nil {
				fail(w, 400, err.Error())
				return
			}
			relogin = true
		}
	}
	allowed := map[string]bool{
		"public_host": true, "stats_enabled": true, "stats_secret": true,
		"quota_warn_pct": true,
		// 入站端口的自动防火墙管理（fwAuto 读取，默认开启）
		"fw_auto": true,
		// 可信反向代理网段：仅当直连对端落在这些网段内才采信转发头
		TrustedProxiesSetting: true,
		"acme_enabled": true, "acme_domain": true, "acme_email": true,
		"admin_user": true,
		// 组件更新相关（UI「保存更新设置」提交这些键）
		"gh_mirror": true, "gh_api_base": true,
		"panel_repo": true, "panel_update_index": true,
		// 2FA 邮箱通道（auth_2fa_mode / totp_secret 由专用端点管理，不接受直接写）
		"auth_email": true,
		"smtp_host": true, "smtp_port": true, "smtp_user": true,
		"smtp_pass": true, "smtp_from": true,
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
	var changed []string
	for k, v := range m {
		if allowed[k] {
			if err := a.Store.SetSetting(k, v); err != nil {
				fail(w, 500, err.Error())
				return
			}
			changed = append(changed, k)
		}
	}
	// 审计只记「改了哪些键」，不记值——smtp_pass / stats_secret 这类敏感项
	// 一旦进了日志就等于明文落盘，比不记更糟。
	if len(changed) > 0 {
		sort.Strings(changed)
		a.auditOK(r, AuditSettings, strings.Join(changed, ","), "修改设置")
	}
	if relogin {
		a.auditOK(r, AuditPassword, authn.User(a.Store), "管理密码已修改，全部会话失效")
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
