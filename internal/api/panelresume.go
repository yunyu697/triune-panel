package api

// 面板自更新时的「会话接力」。
//
// 问题：自更新的重启是 syscall.Exec，**进程映像整个被换掉**，内存里的
// session 表随之清空（见 sessions.go 的安全边界）。于是用户点完更新、页面
// 刷新回来必然 401，被踢到登录页——哪怕他十秒前刚登录过。
//
// 为什么不是「把 session 表落库」：那等于让所有旧 token 跨重启继续有效，
// 「重启」就从一次全员止血降级成普通事件；DB 文件被读走即等于拿到全部
// 在线会话。这与既有设计正相反，不能为了升级这一个场景放开。
//
// 改为只给**用户主动点的更新**开一个窄口子：服务端在 exec 之前签发一张
// 一次性接力凭证，前端重启后拿它换一张新会话。窄口子由三件事压住：
//   - 5 分钟过期（重启通常 1~3 秒，够用且窗口极短）
//   - 一次性（校验通过即删除，重放拿不到第二张会话）
//   - 绑定密码指纹与账号状态（改过密码、账号被停用/删除，凭证立刻作废）
//
// 「窄口子」在这里不是枚举个例：手动自更新是**唯一能预知重启**的时刻，
// 服务端才有可能提前签发凭证。崩溃重启、systemd 重启、watch 自动切二进制
// 都没有这个时机，这几条路径仍然会踢人（已登记进 docs/known-limits.md）。

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"triune/internal/authn"
	"triune/internal/store"
)

// SettingUpdateResume 待使用的接力凭证（JSON）。空值表示没有。
//
// 复用 settings 表而不是新建表：这本来就是一个「一次性运行时状态」，
// 与 SettingRestartFailed 同类，不值得为它加 schema 迁移。
// **不要**把这个键加进 getSettings 的下发白名单——它含凭证明文的哈希。
const SettingUpdateResume = "panel_update_resume"

// updateResumeTTL 凭证有效期。
//
// 从签发到前端换票要跨一次进程重启：停内核、exec、新进程绑定端口、
// 轮询拿到新版本号，正常 1~3 秒。5 分钟是给「机器很慢 + 内核起得慢」留的
// 余量，再长就只是扩大窗口，没有收益。
const updateResumeTTL = 5 * time.Minute

// resumeTicket 落库的凭证记录。只存哈希，明文只在签发响应里出现一次。
type resumeTicket struct {
	Hash string `json:"h"` // sha256(token)
	UID  int64  `json:"u"` // 绑定账号
	FP   string `json:"f"` // 签发时该账号的密码哈希指纹（authn.Fingerprint）
	Exp  int64  `json:"e"` // unix 秒
}

// resumeHash 与 sessionSID 一样用带域前缀的 sha256，避免同一个随机串在
// 别处被复用时能相互推导。
func resumeHash(token string) string {
	sum := sha256.Sum256([]byte("triune-update-resume-v1|" + token))
	return hex.EncodeToString(sum[:])
}

// issueUpdateResume 为当前账号签发一张接力凭证，返回明文。
//
// 只保留最近一张：同一时刻只可能有一次更新真正成功（要重启的进程只有一个），
// 多张并存除了让「哪张还有效」变得难判断之外没有用处。
func (a *Server) issueUpdateResume(u *store.AdminUser) (string, error) {
	if u == nil {
		return "", nil
	}
	tok := token() + token() // 32 字节熵，不靠 TTL 也爆破不了
	tk := resumeTicket{
		Hash: resumeHash(tok),
		UID:  u.ID,
		FP:   authn.Fingerprint(u.PassHash),
		Exp:  time.Now().Add(updateResumeTTL).Unix(),
	}
	b, err := json.Marshal(tk)
	if err != nil {
		return "", err
	}
	if err := a.Store.SetSetting(SettingUpdateResume, string(b)); err != nil {
		return "", err
	}
	return tok, nil
}

// clearUpdateResume 作废当前凭证（消费成功、或数据损坏时用）。
func (a *Server) clearUpdateResume() {
	_ = a.Store.SetSetting(SettingUpdateResume, "")
}

// handleSessionResume POST /api/session/resume {"token":"..."}
//
// 免认证：它要解决的正是「重启后没有任何有效会话」这个状态。
// 安全性由凭证本身承担——32 字节随机、一次性、5 分钟、绑密码指纹。
//
// 校验失败时**不**作废凭证。否则任何人（这个端点免认证）发一个垃圾 token
// 就能把合法用户手里那张真凭证打掉，把「免重登」变成「必重登」——
// 拿不到凭证的人不该有能力影响凭证的寿命。重放保护靠「成功即删」，
// 而错误尝试对 32 字节随机串毫无威胁。
func (a *Server) handleSessionResume(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Token) == "" {
		fail(w, 400, "需要 token 字段")
		return
	}

	// 读-校验-删必须串行：两个并发请求同时通过校验的话，一次性就失效了。
	a.resumeMu.Lock()
	defer a.resumeMu.Unlock()

	raw, _ := a.Store.GetSetting(SettingUpdateResume)
	if strings.TrimSpace(raw) == "" {
		fail(w, 401, "没有待恢复的会话：面板不在自更新流程中，或凭证已被使用")
		return
	}
	var tk resumeTicket
	if err := json.Unmarshal([]byte(raw), &tk); err != nil {
		// 库里的结构坏了就不是「再等等会好」，直接清掉，让用户走正常登录
		a.clearUpdateResume()
		fail(w, 401, "接力凭证已损坏，请重新登录")
		return
	}
	if subtle.ConstantTimeCompare([]byte(resumeHash(req.Token)), []byte(tk.Hash)) != 1 {
		fail(w, 401, "接力凭证不匹配，请重新登录")
		return
	}
	if time.Now().Unix() > tk.Exp {
		a.clearUpdateResume()
		fail(w, 401, "接力凭证已过期（有效期 5 分钟），请重新登录")
		return
	}
	u, err := a.Store.GetAdmin(tk.UID)
	if err != nil || u == nil {
		a.clearUpdateResume()
		fail(w, 401, "账号已不存在，请重新登录")
		return
	}
	if !u.Enabled {
		a.clearUpdateResume()
		fail(w, 401, "账号已停用，请重新登录")
		return
	}
	if authn.Fingerprint(u.PassHash) != tk.FP {
		// 更新前后改过密码：旧凭证不该再能换会话（这正是 Fingerprint 的用途）
		a.clearUpdateResume()
		fail(w, 401, "密码已变更，请用新密码登录")
		return
	}

	a.clearUpdateResume() // 一次性：换票成功即作废

	t := token()
	now := time.Now()
	a.sessions.Add(&session{
		Token: t, SID: sessionSID(t), UID: u.ID,
		FP:      authn.Fingerprint(u.PassHash),
		IP:      a.loginKey(r),
		UA:      r.UserAgent(),
		Created: now, LastSeen: now,
		Exp: now.Add(sessionTTL),
	})
	a.auditAs(u.Username, r, AuditLogin, u.Username, store.AuditOK,
		"面板自更新后恢复会话（未重新输入密码，凭证在 "+updateResumeTTL.String()+"内一次性使用）")
	writeJSON(w, 200, map[string]any{"token": t, "user": u.Username, "role": u.Role})
}
