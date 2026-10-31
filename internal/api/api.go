// Package api 提供 HTTP API 与订阅服务。
package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"triune/internal/core"
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
	sessions sync.Map       // token -> expiry
	loginFails sync.Map     // ip -> *loginFail（防爆破）
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
		if v, ok := a.sessions.Load(t); !ok || v.(time.Time).Before(time.Now()) {
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

func loginKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return host
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

func (a *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	key := loginKey(r)
	if remain := a.loginLocked(key); remain > 0 {
		fail(w, http.StatusTooManyRequests,
			fmt.Sprintf("登录失败次数过多，已临时锁定，请约 %d 分钟后再试", remain/60+1))
		return
	}
	var req struct {
		User string `json:"user"`
		Pass string `json:"pass"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, "bad request")
		return
	}
	u, _ := a.Store.GetSetting("admin_user")
	p, _ := a.Store.GetSetting("admin_pass")
	if u == "" {
		u = "admin"
	}
	if p == "" {
		p = "admin" // 首次登录后请立即修改
	}
	if req.User != u || req.Pass != p {
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
		fail(w, 401, "用户名或密码错误")
		return
	}
	a.loginFails.Delete(key) // 登录成功，清零失败计数
	t := token()
	a.sessions.Store(t, time.Now().Add(24*time.Hour))
	writeJSON(w, 200, map[string]string{"token": t})
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
	writeJSON(w, 200, in)
}

func (a *Server) updateInbound(w http.ResponseWriter, r *http.Request) {
	var in model.Inbound
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
	if old, err := a.Store.GetInbound(in.ID); err == nil && old != nil &&
		old.Core != in.Core && old.Enabled && !old.Draft {
		fail(w, 400, "运行中的入站切换内核请使用 POST /api/switch-core（含预检与健康检查）")
		return
	}
	if err := a.Store.UpdateInbound(&in); err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, in)
}

func (a *Server) deleteInbound(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.DeleteInbound(pathID(r)); err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
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
	writeJSON(w, 200, c)
}

func (a *Server) deleteClient(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.DeleteClient(pathID(r)); err != nil {
		fail(w, 500, err.Error())
		return
	}
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
	if err := a.Store.SetClientInbounds(pathID(r), req.InboundIDs); err != nil {
		fail(w, 500, err.Error())
		return
	}
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
	writeJSON(w, 200, n)
}

func (a *Server) deleteExternal(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.DeleteExternalNode(pathID(r)); err != nil {
		fail(w, 500, err.Error())
		return
	}
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
		// 运行中配置未受影响 —— 这是本系统的安全保证
		writeJSON(w, 422, map[string]any{"error": err.Error(), "revision_id": rev.ID, "hint": "运行中的配置未被修改；可用 rollback 恢复历史备份"})
		return
	}
	_ = a.Store.UpdateRevisionStatus(rev.ID, model.RevApplied, "")
	res.RevisionID = rev.ID
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
		fail(w, 422, err.Error())
		return
	}
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
		"acme_enabled", "acme_domain", "acme_email",
		"gh_mirror", "gh_api_base", "panel_repo", "panel_update_index"} {
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
	allowed := map[string]bool{
		"public_host": true, "stats_enabled": true, "stats_secret": true,
		"acme_enabled": true, "acme_domain": true, "acme_email": true,
		"admin_user": true, "admin_pass": true,
		// 组件更新相关（UI「保存更新设置」提交这些键）
		"gh_mirror": true, "gh_api_base": true,
		"panel_repo": true, "panel_update_index": true,
	}
	for _, c := range core.All() {
		allowed["bin_"+c.Name()] = true
	}
	for k, v := range m {
		if allowed[k] {
			if err := a.Store.SetSetting(k, v); err != nil {
				fail(w, 500, err.Error())
				return
			}
		}
	}
	// ACME 设置变更后立即触发一次签发/续期检查，无需等下一个检查周期
	if m["acme_enabled"] == "1" && a.CertMgr != nil {
		go a.CertMgr.CheckNow()
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}
