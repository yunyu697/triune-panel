package api

// 自更新接力凭证的行为锁。
//
// 这是**唯一一个免认证就能换到会话**的端点，所以每条边界都要有用例盯着：
// 一次性、过期、改密后失效、坏凭证不影响好凭证、并发只换一张。
// 只测「能换成功」是不够的——那样把 TTL 写成 24 小时、把一次性删掉、
// 或者在校验失败时顺手把票作废，测试都还是绿的。

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"triune/internal/authn"
	"triune/internal/store"
)

// ticketFor 为某个已有会话签发一张接力凭证（模拟 handleSelfUpdate 的那一步）。
func ticketFor(t *testing.T, a *Server, tok string) string {
	t.Helper()
	sess, ok := a.sessions.Get(tok)
	if !ok {
		t.Fatalf("会话 %q 不在内存表里", tok)
	}
	u, err := a.Store.GetAdmin(sess.UID)
	if err != nil || u == nil {
		t.Fatalf("取账号失败: %v", err)
	}
	ticket, err := a.issueUpdateResume(u)
	if err != nil || ticket == "" {
		t.Fatalf("签发票据失败: %v", err)
	}
	return ticket
}

// resume 用给定凭证换会话（匿名，和真实前端一样）。
func resume(a *Server, ticket string) (code int, body map[string]any) {
	w := do(a, "", "POST", "/api/session/resume", map[string]string{"token": ticket})
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w.Code, body
}

// TestResumeTicketRoundTrip 正常路径：换到的新 token 必须真能用。
//
// 「返回 200」不等于「会话可用」——忘记 Add 进 sessionStore 也会 200，
// 前端拿着新 token 立刻又 401。所以这里用新 token 打一次受保护端点。
func TestResumeTicketRoundTrip(t *testing.T) {
	a := newRoleTestServer(t)
	old := loginAs(t, a, "admin", "admin")
	ticket := ticketFor(t, a, old)

	code, body := resume(a, ticket)
	if code != 200 {
		t.Fatalf("换票应 200，实际 %d: %v", code, body)
	}
	newTok, _ := body["token"].(string)
	if newTok == "" || newTok == old {
		t.Fatalf("应拿到一张全新 token，实际 %q（旧 %q）", newTok, old)
	}
	if body["user"] != "admin" || body["role"] != "owner" {
		t.Fatalf("身份不对: %v", body)
	}
	if w := do(a, newTok, "GET", "/api/me", nil); w.Code != 200 {
		t.Fatalf("换到的 token 不可用: %d %s", w.Code, w.Body.String())
	}
	// 恢复会话必须留痕：这是「谁在什么时候没输密码就进来了」，审计里不能断档。
	if !auditContains(a, "面板自更新后恢复会话") {
		t.Fatal("审计里没有恢复会话的记录")
	}
}

// TestResumeTicketTTLIsShort 有效期必须真是「几分钟」，不是「反正会过期」。
//
// 过期用例是手改 Exp 造的，所以它抓不到「把 TTL 改成 24 小时」——
// 那样一张泄漏的票在整整一天里都能换到会话。这里直接量签发出来的时间窗。
func TestResumeTicketTTLIsShort(t *testing.T) {
	a := newRoleTestServer(t)
	ticketFor(t, a, loginAs(t, a, "admin", "admin"))

	raw, _ := a.Store.GetSetting(SettingUpdateResume)
	var tk resumeTicket
	if err := json.Unmarshal([]byte(raw), &tk); err != nil {
		t.Fatal(err)
	}
	left := time.Until(time.Unix(tk.Exp, 0))
	if left > updateResumeTTL+time.Minute || left < updateResumeTTL-time.Minute {
		t.Fatalf("票据剩余有效期 %v，与声明的 %v 不符（窗口越短越好，别悄悄放宽）", left, updateResumeTTL)
	}
	if updateResumeTTL > 10*time.Minute {
		t.Fatalf("TTL 被改成 %v：重启只需几秒，窗口越长被盗用的机会越大", updateResumeTTL)
	}
}

// TestResumeTicketIsSingleUse 同一张票第二次必须失败。
func TestResumeTicketIsSingleUse(t *testing.T) {
	a := newRoleTestServer(t)
	ticket := ticketFor(t, a, loginAs(t, a, "admin", "admin"))

	if code, _ := resume(a, ticket); code != 200 {
		t.Fatalf("第一次应成功，实际 %d", code)
	}
	if code, _ := resume(a, ticket); code != 401 {
		t.Fatalf("第二次必须 401（一次性），实际 %d", code)
	}
}

// TestResumeTicketExpires 过期即失效，且库里那条记录要被清掉。
func TestResumeTicketExpires(t *testing.T) {
	a := newRoleTestServer(t)
	ticket := ticketFor(t, a, loginAs(t, a, "admin", "admin"))

	// 直接把过期时间改到过去：比睡 5 分钟可靠
	raw, _ := a.Store.GetSetting(SettingUpdateResume)
	var tk resumeTicket
	if err := json.Unmarshal([]byte(raw), &tk); err != nil {
		t.Fatal(err)
	}
	tk.Exp = time.Now().Add(-time.Second).Unix()
	b, _ := json.Marshal(tk)
	if err := a.Store.SetSetting(SettingUpdateResume, string(b)); err != nil {
		t.Fatal(err)
	}

	if code, _ := resume(a, ticket); code != 401 {
		t.Fatalf("过期票据必须 401，实际 %d", code)
	}
	if left, _ := a.Store.GetSetting(SettingUpdateResume); left != "" {
		t.Fatalf("过期票据应被清掉，库里还有 %q", left)
	}
}

// TestResumeBadTokenDoesNotBurnRealTicket 这是本文件最重要的一条。
//
// 端点免认证，谁都能打。如果「校验失败也作废」，任何人发一个垃圾 token
// 就能把合法用户手里那张真票打掉 —— 免重登变成必重登，而且攻击成本为零。
// 所以：坏凭证碰不到好凭证。
func TestResumeBadTokenDoesNotBurnRealTicket(t *testing.T) {
	a := newRoleTestServer(t)
	ticket := ticketFor(t, a, loginAs(t, a, "admin", "admin"))

	for _, bad := range []string{"", "deadbeef", ticket + "x", strings.Repeat("0", 64)} {
		if code, _ := resume(a, bad); code == 200 {
			t.Fatalf("坏凭证 %q 不该换到会话", bad)
		}
	}
	// 真票必须仍然可用
	if code, body := resume(a, ticket); code != 200 {
		t.Fatalf("好票被坏请求打掉了: %d %v", code, body)
	}
}

// TestResumeRejectedAfterPasswordChange 票据绑定的是「密码指纹」，改密即失效。
//
// 不加这一条的话，「更新前把票偷走」就能在改密后依然换到会话——
// 等于把「改密踢掉所有会话」这条止血手段打个洞。
func TestResumeRejectedAfterPasswordChange(t *testing.T) {
	a := newRoleTestServer(t)
	tok := loginAs(t, a, "admin", "admin")
	ticket := ticketFor(t, a, tok)

	sess, _ := a.sessions.Get(tok)
	if err := authn.SetPasswordFor(a.Store, sess.UID, "brand-new-password-9"); err != nil {
		t.Fatal(err)
	}
	if code, _ := resume(a, ticket); code != 401 {
		t.Fatalf("改密后票据必须失效，实际 %d", code)
	}
}

// TestResumeWithoutTicket 面板没在更新流程里时，端点不能给出任何会话。
func TestResumeWithoutTicket(t *testing.T) {
	a := newRoleTestServer(t)
	if code, _ := resume(a, "whatever"); code != 401 {
		t.Fatalf("没有票据时必须 401，实际 %d", code)
	}
}

// TestResumeConcurrentOnlyOneWins 同一张票并发换两次，只能有一张会话。
//
// 读-校验-删不是原子的，缺锁时两个请求会同时通过校验、各换一张会话，
// 「一次性」名存实亡。这条用例就是那把锁的可执行说明。
func TestResumeConcurrentOnlyOneWins(t *testing.T) {
	a := newRoleTestServer(t)
	ticket := ticketFor(t, a, loginAs(t, a, "admin", "admin"))

	var wg sync.WaitGroup
	results := make([]int, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			code, _ := resume(a, ticket)
			results[i] = code
		}(i)
	}
	wg.Wait()

	ok := 0
	for _, c := range results {
		if c == 200 {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("并发换票应只有 1 次成功，实际 %d 次（结果 %v）", ok, results)
	}
}

// TestResumeTicketStoredAsHash 库里绝不能出现凭证明文。
//
// 与「会话 token 永不外发」同一条理由：settings 表会进备份、会被导出，
// 明文票躺在里面等于给拿到备份的人一张免密登录券。
func TestResumeTicketStoredAsHash(t *testing.T) {
	a := newRoleTestServer(t)
	ticket := ticketFor(t, a, loginAs(t, a, "admin", "admin"))

	raw, _ := a.Store.GetSetting(SettingUpdateResume)
	if raw == "" {
		t.Fatal("库里应有票据记录")
	}
	if strings.Contains(raw, ticket) {
		t.Fatalf("库里出现了凭证明文: %s", raw)
	}
	if !strings.Contains(raw, resumeHash(ticket)) {
		t.Fatalf("库里应是 sha256 哈希，实际 %s", raw)
	}
}

// auditContains 审计里是否有包含给定片段的记录。
func auditContains(a *Server, frag string) bool {
	rows, err := a.Store.ListAudit(store.AuditFilter{Limit: 200})
	if err != nil {
		return false
	}
	for _, r := range rows {
		if strings.Contains(r.Detail, frag) {
			return true
		}
	}
	return false
}
