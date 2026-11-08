// Package api 提供 HTTP API 与订阅服务。
//
// 这个包按领域分文件，本文件只放「装配」——Server 结构、路由表、鉴权中间件
// 以及几个通用小工具（writeJSON / fail / token）。具体处理逻辑在：
//
//	login.go     登录、防爆破（含锁定）、登出
//	status.go    /api/status
//	inbounds.go  入站增删改查、保存期端口冲突校验、防火墙端口同步
//	clients.go   用户（客户端）增删改查、入站授权、限速校验
//	external.go  外部节点（分享链接导入的、不进本机内核的那种）
//	apply.go     配置导入、渲染→校验→切换流水线、回滚、日志
//	settings.go  设置读写（含 mihomo 兼容开关、外部节点进订阅开关）
//
// 其余横切能力各自成文件：security.go（响应头/CSP）、authz.go（角色判定）、
// sessions.go（登录会话）、audit.go（审计）、subscription.go（订阅输出）等。
package api

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"triune/internal/authn"
	"triune/internal/core"
	"triune/internal/ops"
	"triune/internal/proc"
	"triune/internal/store"
)

type Server struct {
	Store   *store.Store
	Pipe    *core.PipelineManager
	ProcMgr *proc.Manager
	DataDir string
	CertMgr CertManager    // 可为 nil（未启用 ACME）
	Coll    *ops.Collector // 流量采样器（实时速率查询用），可为 nil
	// TLSActive 表示当前进程**实际**用 TLS 监听。由 cmd/server 在启动时设置：
	// 库里开启了 HTTPS 但证书生成失败会退回明文，界面必须能看出这个差别。
	TLSActive  bool
	sessions   *sessionStore  // 登录会话表（见 sessions.go）
	loginFails sync.Map       // ip -> *loginFail（防爆破）
	totpStaged sync.Map       // uid -> 待绑定的 TOTP 密钥（每账号一份，重启即丢）
	emailCode  emailCodeState // 登录邮箱验证码（2FA）

	// 磁盘上那个二进制自报的版本，10 秒缓存（见 diskBinaryVersion）
	diskVerMu sync.Mutex
	diskVer   string
	diskVerAt time.Time
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

	// ---- PWA 静态资源（免认证，必须挂在站点根上）----
	// 作用域规则见 pwa.go 顶部说明：sw.js / manifest 一旦放进子目录就失效。
	mux.HandleFunc("GET /manifest.webmanifest", a.handlePWA("manifest.webmanifest"))
	mux.HandleFunc("GET /sw.js", a.handlePWA("sw.js"))
	mux.HandleFunc("GET /icons/{file}", a.handlePWA(""))

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
	// 出站 / 路由 / DNS 的只读视图：渲染当前入站后抽三段出来，不落盘不重启。
	// 只读角色也能看——里面不含任何凭据，而它本来就是给排障用的。
	mux.HandleFunc("GET /api/outbounds", a.auth(a.handleOutbounds))
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
	// JSON 配置导入（3x-ui / xray 的入站定义）：dry_run=true 时只预览不写库
	mux.HandleFunc("POST /api/inbounds/import-json", a.write(a.handleImportJSON))
	// JSON → 入站表单：只解析单条入站，返回逐字段的回填数据（不写库）。
	// 与上面那个批量导入的分工见 parsejson.go 顶部注释。
	mux.HandleFunc("POST /api/inbounds/parse-json", a.write(a.handleParseJSON))
	mux.HandleFunc("PUT /api/inbounds/{id}", a.write(a.updateInbound))
	mux.HandleFunc("DELETE /api/inbounds/{id}", a.write(a.deleteInbound))
	// 导出接口会把节点凭据（UUID / 密码 / 私钥）整段吐出来，只读角色不能碰
	mux.HandleFunc("GET /api/inbounds/{id}/export", a.write(a.handleInboundExport))
	mux.HandleFunc("GET /api/export/catalog", a.write(a.handleExportCatalog))
	mux.HandleFunc("POST /api/export/config", a.write(a.handleExportConfig))
	// 下载源自检会让面板主动发一次外网请求，与自检同一档（write）：
	// 只读角色不需要它，而它确实消耗面板的出口。
	mux.HandleFunc("POST /api/export/check-mirror", a.write(a.handleCheckMirror))
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
	mux.HandleFunc("PUT /api/external-nodes/{id}", a.write(a.updateExternal))
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
	// REALITY 握手目标实测：从这台服务器真握手几次，给出延迟 / TLS 版本 /
	// 证书结论，供用户挑 dest（见 reality.go）。会替用户连外网，按 write 档。
	mux.HandleFunc("POST /api/tools/reality-dest", a.write(a.handleRealityDestProbe))
	// 同站 SNI 收集：读 dest 的证书，把里面每个域名逐个真握手验证，
	// 只返回能填进 serverNames 的那些（见 reality.go）。同样会连外网。
	mux.HandleFunc("POST /api/tools/reality-sni", a.write(a.handleRealitySNICollect))
	mux.HandleFunc("POST /api/self-update", a.write(a.handleSelfUpdate))
	// 变更通知：发一条测试事件，把错误原样返回（配错了要能立刻发现）
	mux.HandleFunc("POST /api/notify/test", a.write(a.handleNotifyTest))
	mux.HandleFunc("POST /api/cores/{name}/update", a.write(a.handleCoreUpdate))
	mux.HandleFunc("POST /api/cores/{name}/rollback", a.write(a.handleCoreRollback))
	// 下载镜像：候选列表 + 测速（装/更新内核时选，默认官方地址）
	mux.HandleFunc("GET /api/mirrors", a.write(a.handleMirrors))
	mux.HandleFunc("POST /api/mirrors/test", a.write(a.handleMirrorTest))
	mux.HandleFunc("GET /api/panel/backups", a.write(a.handlePanelBackups))
	// 备份容量治理：盘点只读，预览/删除按 write 档（见 backups.go）。
	// 四个 .bak 目录 + 两处数据归档统一成一个视图，删除只认重新扫出的白名单。
	mux.HandleFunc("GET /api/backups/inventory", a.auth(a.handleBackupInventory))
	mux.HandleFunc("POST /api/backups/prune", a.write(a.handleBackupPrune))
	// 主机网络优化体检（BBR）：报告只读，管理员可见；应用/撤销见下面 ownerOnly。
	mux.HandleFunc("GET /api/host-opt", a.write(a.handleHostOpt))
	// 面板自身 HTTPS：没有域名时用自签证书（见 paneltls.go 的诚实边界）
	mux.HandleFunc("GET /api/panel/tls", a.write(a.handlePanelTLS))
	mux.HandleFunc("GET /api/panel/tls/cert", a.write(a.handlePanelCertDownload))
	mux.HandleFunc("POST /api/certs/issue", a.write(a.handleCertsIssue))

	// ---- 仅超级管理员：账号管理、销毁审计、整面板回滚 ----
	mux.HandleFunc("GET /api/admins", a.ownerOnly(a.handleAdminsList))
	mux.HandleFunc("POST /api/admins", a.ownerOnly(a.handleAdminCreate))
	mux.HandleFunc("PUT /api/admins/{id}", a.ownerOnly(a.handleAdminUpdate))
	mux.HandleFunc("DELETE /api/admins/{id}", a.ownerOnly(a.handleAdminDelete))
	mux.HandleFunc("DELETE /api/audit", a.ownerOnly(a.handleAuditClear))
	mux.HandleFunc("POST /api/panel/rollback", a.ownerOnly(a.handlePanelRollback))
	// 写 /etc/sysctl.d 属于系统级改动：只有超级管理员、且带回传的确认字面量才会执行
	mux.HandleFunc("POST /api/host-opt/apply", a.ownerOnly(a.handleHostOptApply))
	mux.HandleFunc("POST /api/host-opt/revert", a.ownerOnly(a.handleHostOptRevert))

	// 前端脚本从内嵌副本走（见 webui.AssetFile）：内容与二进制严格对应，
	// 且 CSP 收紧到 script-src 'self' 之后必须能同源加载。
	mux.HandleFunc("GET /assets/app.js", a.handleAsset("app.js"))
	mux.HandleFunc("GET /assets/qr.js", a.handleAsset("qr.js"))
	mux.HandleFunc("GET /assets/boot.js", a.handleAsset("boot.js"))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// 禁止中间层/浏览器长期缓存页面：升级面板后旧 UI 常驻缓存会导致
		// "明明升级了，界面还是旧的"。API 走 JSON 不受影响。
		path := filepath.Join(a.DataDir, "web", "index.html")
		data, err := os.ReadFile(path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		// 自定义 UI（data/web/index.html 被换成自己的构建产物）可能仍在用内联
		// 脚本或内联事件属性。对这类页面单独放宽 CSP，否则收紧的那一下会把
		// 人家整套界面打死；内置 UI 不带这些形态，始终走严格策略。
		if htmlNeedsInlineScript(data) {
			w.Header().Set("Content-Security-Policy", cspPolicyLegacyInline)
		}
		http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(data))
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
