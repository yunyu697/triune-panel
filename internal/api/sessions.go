package api

// 会话表与「登录设备」管理。
//
// 为什么单独一个文件：v0.9.6 把账号改成多人之后，「谁还挂在线上」成了必须回答的
// 问题——能查清「谁改了什么」（审计），却查不清「谁还连着」，发现异常会话时
// 只能改密（霰弹枪，把该账号全部设备一起踢掉）。这里补上精准止血。
//
// 安全边界（重要）：
//   - 会话凭据（token）**永不外发**。对外只暴露 sha256(token) 的前 16 位作为
//     sid，吊销时服务端自己算哈希比对。把 token 发给前端等于给任何能看一眼
//     面板的人一把万能钥匙。
//   - 吊销权限按角色等级：owner 可踢所有人；admin 可踢 admin / viewer，但**不能
//     踢 owner**（否则一个被钓鱼的管理员就能把超级管理员锁在门外）；任何账号
//     都可以踢自己的会话。
//   - 会话存在内存，面板重启即全部失效；账号被停用/删除/改密时指纹校验兜底，
//     同时主动清理内存，让列表立刻准确。

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"triune/internal/authn"
	"triune/internal/store"
)

// sessionTTL 会话有效期。
const sessionTTL = 24 * time.Hour

// sessionTouchInterval 最后活跃时间的写入节流。
//
// 每个请求都写一次时间戳没必要（列表只显示到秒），但完全记「登录时间」又看不出
// 「这个会话还在动」——被遗忘的登录最危险。60 秒一次足够分辨活跃与僵尸。
const sessionTouchInterval = 60 * time.Second

// session 一次登录签发的会话。
//
// FP 是签发时该账号密码哈希的指纹（见 authn.Fingerprint），请求时比对：
// 只有「这个人的密码变了 / 账号被删 / 被停用」才会失效。
// v0.9.6 之前用的是全局 session_epoch，多管理员下那是错的——
// A 改密码不该把 B 踢下线。
type session struct {
	Token    string    // 会话凭据，绝不外发
	SID      string    // 对外标识（token 的哈希前缀）
	UID      int64     // 账号 id
	FP       string    // 密码哈希指纹
	IP       string    // 登录来源
	UA       string    // User-Agent 原文
	Created  time.Time // 登录时间
	LastSeen time.Time // 最后活跃（节流更新）
	Exp      time.Time // 过期时间
}

// sessionSID 由 token 派生对外标识。取 8 字节（16 位十六进制）足够区分
// 同时在线的那几十个会话，且不泄漏 token 本身。
func sessionSID(token string) string {
	sum := sha256.Sum256([]byte("triune-sid-v1|" + token))
	return hex.EncodeToString(sum[:8])
}

// sessionStore 并发安全的会话表。
//
// 刻意不用 sync.Map：这里需要「遍历」「按账号批量删」「按 sid 找」，
// 这些在 sync.Map 上要么靠 Range 加锁要么干脆做不到。面板规模下
// 一把 RWMutex 的争用可以忽略。
//
// 所有方法都返回**值拷贝**，杜绝调用方拿到指针后与 Touch 抢同一块内存
// （`go test -race` 下这类竞争会直接报错）。
type sessionStore struct {
	mu sync.RWMutex
	m  map[string]*session // token -> session
}

func newSessionStore() *sessionStore {
	return &sessionStore{m: make(map[string]*session)}
}

// Add 登记一个新会话。
func (s *sessionStore) Add(sess *session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *sess
	s.m[sess.Token] = &cp
}

// Get 按 token 取会话快照。
func (s *sessionStore) Get(token string) (session, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.m[token]
	if !ok {
		return session{}, false
	}
	return *sess, true
}

// Delete 删除一个会话，返回是否真的删掉了。
func (s *sessionStore) Delete(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[token]; !ok {
		return false
	}
	delete(s.m, token)
	return true
}

// Touch 更新最后活跃时间（节流）。
func (s *sessionStore) Touch(token string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[token]
	if !ok {
		return
	}
	if now.Sub(sess.LastSeen) < sessionTouchInterval {
		return
	}
	sess.LastSeen = now
}

// List 返回全部会话快照（顺序不保证，调用方自行排序）。
func (s *sessionStore) List() []session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]session, 0, len(s.m))
	for _, sess := range s.m {
		out = append(out, *sess)
	}
	return out
}

// Count 当前会话数。
func (s *sessionStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.m)
}

// DeleteByUID 踢掉某个账号的全部会话，返回删除条数。
// 改密、停用、删除账号后调用：指纹校验已经是兜底，这里让内存立刻干净。
func (s *sessionStore) DeleteByUID(uid int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for tok, sess := range s.m {
		if sess.UID == uid {
			delete(s.m, tok)
			n++
		}
	}
	return n
}

// DeleteOthers 踢掉某账号除 keep 之外的全部会话（「退出其他设备」）。
func (s *sessionStore) DeleteOthers(uid int64, keep string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for tok, sess := range s.m {
		if sess.UID == uid && tok != keep {
			delete(s.m, tok)
			n++
		}
	}
	return n
}

// DeleteBySID 按对外标识吊销会话，返回被删的会话与是否找到。
func (s *sessionStore) DeleteBySID(sid string) (session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for tok, sess := range s.m {
		if sess.SID == sid {
			cp := *sess
			delete(s.m, tok)
			return cp, true
		}
	}
	return session{}, false
}

// ---- 设备描述 ----

// describeUA 把 User-Agent 压成「浏览器 · 系统」这种一眼能看懂的形式。
//
// 不引第三方 UA 库：面板只需要区分「这是谁家的浏览器、什么系统、还是脚本」，
// 完整解析（版本号、设备型号）对「判断是不是我自己的设备」没有增量价值。
func describeUA(ua string) string {
	ua = strings.TrimSpace(ua)
	if ua == "" {
		return "未知设备"
	}
	low := strings.ToLower(ua)

	// 脚本/客户端优先判断：它们的 UA 里常常也带 "Mozilla" 兼容串
	switch {
	case strings.HasPrefix(low, "curl/"):
		return "命令行 · curl"
	case strings.HasPrefix(low, "wget/"):
		return "命令行 · wget"
	case strings.Contains(low, "python-requests"), strings.Contains(low, "python-urllib"):
		return "脚本 · Python"
	case strings.HasPrefix(low, "go-http-client"):
		return "脚本 · Go"
	case strings.Contains(low, "postmanruntime"):
		return "Postman"
	}

	osName := ""
	switch {
	case strings.Contains(low, "windows nt"):
		osName = "Windows"
	case strings.Contains(low, "iphone"), strings.Contains(low, "ipad"):
		osName = "iOS"
	case strings.Contains(low, "android"):
		osName = "Android"
	case strings.Contains(low, "mac os x"), strings.Contains(low, "macintosh"):
		osName = "macOS"
	case strings.Contains(low, "linux"):
		osName = "Linux"
	}

	browser := ""
	switch {
	case strings.Contains(low, "edg/"):
		browser = "Edge"
	case strings.Contains(low, "opr/"), strings.Contains(low, "opera"):
		browser = "Opera"
	case strings.Contains(low, "firefox/"):
		browser = "Firefox"
	case strings.Contains(low, "chrome/"), strings.Contains(low, "crios/"):
		browser = "Chrome"
	case strings.Contains(low, "safari/"):
		browser = "Safari"
	}

	switch {
	case browser != "" && osName != "":
		return browser + " · " + osName
	case browser != "":
		return browser
	case osName != "":
		return osName
	}
	return "未知设备"
}

// ---- 端点 ----

// sessionView 对外视图：没有 token，只有派生 sid。
type sessionView struct {
	ID        string `json:"id"`
	User      string `json:"user"`
	Role      string `json:"role"`
	IP        string `json:"ip"`
	Device    string `json:"device"`
	UA        string `json:"ua"`
	Created   string `json:"created"`
	LastSeen  string `json:"last_seen"`
	Expires   string `json:"expires"`
	Current   bool   `json:"current"`
	Revocable bool   `json:"revocable"`
}

const sessTimeLayout = "2006-01-02 15:04:05"

// canRevoke 当前账号能否吊销 targetRole 的会话。
//
// 规则：自己的一定能踢；比自己权限低的能踢；owner 例外——只有 owner 能踢 owner。
// 这样既能让 admin 处理被钓鱼的同事，又不会让一个被钓鱼的 admin 把超级管理员
// 锁在门外（那等于面板失去管理能力）。
func canRevoke(me *store.AdminUser, targetRole string, isSelf bool) bool {
	if me == nil {
		return false
	}
	if isSelf {
		return true
	}
	if me.Role == authn.RoleOwner {
		return true
	}
	if targetRole == authn.RoleOwner {
		return false
	}
	return authn.RoleRank(me.Role) >= authn.RoleRank(targetRole)
}

// handleSessionsList GET /api/sessions
//
// owner / admin 看到全部会话；viewer 只看到自己的（角色不足的账号不该知道
// 别人什么时候登录过，这本身就是情报）。
func (a *Server) handleSessionsList(w http.ResponseWriter, r *http.Request) {
	me := a.currentUser(r)
	if me == nil {
		fail(w, 401, "unauthorized")
		return
	}
	curSID := sessionSID(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	// 一次把账号读全，避免每个会话一次查询
	admins, err := a.Store.ListAdmins()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	byID := make(map[int64]*store.AdminUser, len(admins))
	for _, u := range admins {
		byID[u.ID] = u
	}

	list := a.sessions.List()
	out := make([]sessionView, 0, len(list))
	for _, s := range list {
		u := byID[s.UID]
		if u == nil {
			continue // 账号已被删除，会话即将因指纹校验失效
		}
		isSelf := u.ID == me.ID
		if me.Role == authn.RoleViewer && !isSelf {
			continue
		}
		out = append(out, sessionView{
			ID: s.SID, User: u.Username, Role: u.Role,
			IP: s.IP, Device: describeUA(s.UA), UA: s.UA,
			Created:   s.Created.Local().Format(sessTimeLayout),
			LastSeen:  s.LastSeen.Local().Format(sessTimeLayout),
			Expires:   s.Exp.Local().Format(sessTimeLayout),
			Current:   s.SID == curSID,
			Revocable: canRevoke(me, u.Role, isSelf),
		})
	}
	// 当前会话排最前，其余按最后活跃倒序（最可能被遗忘的排后面，先看活跃的）
	sortSessions(out)
	writeJSON(w, 200, out)
}

// handleSessionRevoke DELETE /api/sessions/{id}
func (a *Server) handleSessionRevoke(w http.ResponseWriter, r *http.Request) {
	me := a.currentUser(r)
	if me == nil {
		fail(w, 401, "unauthorized")
		return
	}
	sid := r.PathValue("id")
	if sid == "" {
		fail(w, 400, "缺少会话标识")
		return
	}
	// 先定位再判权限：直接删会删掉不该删的
	target, ok := a.findSessionBySID(sid)
	if !ok {
		fail(w, 404, "会话不存在或已失效")
		return
	}
	u, err := a.Store.GetAdmin(target.UID)
	if err != nil {
		fail(w, 404, "账号已不存在")
		return
	}
	if !canRevoke(me, u.Role, u.ID == me.ID) {
		a.auditFail(r, AuditSession, u.Username, "越权吊销会话（角色不足）")
		fail(w, 403, "不能吊销同级或更高权限账号的会话")
		return
	}
	a.sessions.Delete(target.Token)
	a.changed(r, AuditSession, u.Username,
		"吊销登录会话 "+target.SID[:8]+"（IP "+orDefault(target.IP, "?")+"，"+describeUA(target.UA)+"）")
	writeJSON(w, 200, map[string]any{"ok": 1})
}

// handleSessionsRevokeOthers DELETE /api/sessions
//
// 踢掉自己除当前之外的全部会话——「我在外面登录过，全退掉」的一键操作。
func (a *Server) handleSessionsRevokeOthers(w http.ResponseWriter, r *http.Request) {
	me := a.currentUser(r)
	if me == nil {
		fail(w, 401, "unauthorized")
		return
	}
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	n := a.sessions.DeleteOthers(me.ID, tok)
	a.changed(r, AuditSession, me.Username, "退出其他设备，吊销 "+strconv.Itoa(n)+" 个会话")
	writeJSON(w, 200, map[string]any{"ok": 1, "revoked": n})
}

// findSessionBySID 按 sid 找会话（只读，不删）。
func (a *Server) findSessionBySID(sid string) (session, bool) {
	for _, s := range a.sessions.List() {
		if s.SID == sid {
			return s, true
		}
	}
	return session{}, false
}

// sortSessions 当前会话置顶，其余按最后活跃倒序。
func sortSessions(list []sessionView) {
	// 小切片，插入排序足够，也避免为了一个字段引入 sort.Slice 的闭包噪音
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && lessSession(list[j], list[j-1]); j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}

func lessSession(a, b sessionView) bool {
	if a.Current != b.Current {
		return a.Current
	}
	return a.LastSeen > b.LastSeen
}
