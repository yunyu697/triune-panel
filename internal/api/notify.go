package api

// 变更通知：把「谁在什么时候改了什么」推到面板之外。
//
// 为什么需要：审计日志只解决「事后能查」，前提是有人想起来去查。半夜有人
// 把入站端口改成 22、给陌生账号加了 100GB 配额，等下次登录才看到就太晚了。
// 这里在关键写操作之后推一条摘要（邮件 / Webhook），让异常变更在发生的
// 那一刻就有人知道。
//
// 三条设计约束：
//   - **绝不阻塞主操作**。通知走 goroutine，失败只打日志——推送服务挂了不该
//     让人连「改个设置」都失败。
//   - **敏感值打码**。diff 里出现 smtp_pass / stats_secret / metrics_token
//     这类键时只写「已变更」，否则等于把密钥抄进了邮箱和第三方系统。
//   - **默认订阅配置类事件**。登录失败之类的噪音不推；要推什么由
//     notify_events 决定，留空则用默认集合。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"triune/internal/model"
	"triune/internal/store"
)

// 通知相关的设置键。
const (
	NotifyEmailSetting   = "notify_email"   // 收件邮箱，留空 = 不发邮件
	NotifyWebhookSetting = "notify_webhook" // Webhook 地址，留空 = 不推 Webhook
	NotifyEventsSetting  = "notify_events"  // 订阅的事件前缀，逗号分隔；留空 = 默认集合
)

// defaultNotifyEvents 默认订阅的事件前缀（与审计 action 前缀一致）。
//
// 刻意不含 login：登录本来就频繁，且失败尝试有防爆破兜底；把登录也推出去，
// 收件箱很快就没人看了。
var defaultNotifyEvents = []string{"settings", "inbound", "client", "admin", "apply", "session"}

// changeEvent 一次变更的摘要。
type changeEvent struct {
	Actor  string   `json:"actor"`
	Action string   `json:"action"`
	Target string   `json:"target"`
	Detail string   `json:"detail"`
	Diff   []string `json:"diff,omitempty"` // "键: 旧 → 新"，敏感键只写「已变更」
}

// webhookPayload Webhook 请求体。
type webhookPayload struct {
	Panel   string      `json:"panel"`
	Version string      `json:"version"`
	Time    string      `json:"time"`
	Event   changeEvent `json:"event"`
}

// changed 审计 + 通知：先写审计（本地留痕），再异步推一条通知。
//
// 所有「改了配置」的地方都该走这个而不是裸 auditOK——这样不会出现
// 「审计里有、通知里没有」的漏推。
func (a *Server) changed(r *http.Request, action, target, detail string, diff ...string) {
	a.auditOK(r, action, target, detail)
	a.notifyChange(changeEvent{
		Actor: a.actorName(r), Action: action, Target: target, Detail: detail, Diff: diff,
	})
}

// notifyChange 异步推送，失败只打日志。
func (a *Server) notifyChange(ev changeEvent) {
	if a.Store == nil {
		return
	}
	go func() {
		for _, e := range a.deliverChange(ev, false) {
			log.Printf("[notify] %s", e)
		}
	}()
}

// deliverChange 同步投递，返回错误描述（空 = 全成功）。
// force=true 时忽略事件订阅（给「发送测试」用）。
func (a *Server) deliverChange(ev changeEvent, force bool) []string {
	to, _ := a.Store.GetSetting(NotifyEmailSetting)
	hook, _ := a.Store.GetSetting(NotifyWebhookSetting)
	to, hook = strings.TrimSpace(to), strings.TrimSpace(hook)
	if to == "" && hook == "" {
		return nil // 没配通道，静默跳过
	}
	if !force {
		events, _ := a.Store.GetSetting(NotifyEventsSetting)
		if !notifyWanted(events, ev.Action) {
			return nil
		}
	}

	var errs []string
	if hook != "" {
		if err := a.postWebhook(hook, ev); err != nil {
			errs = append(errs, "Webhook 推送失败："+err.Error())
		}
	}
	if to != "" {
		if err := sendMail(a.loadSMTPConfig(), to, notifySubject(ev), a.notifyBody(ev)); err != nil {
			errs = append(errs, "邮件发送失败："+err.Error())
		}
	}
	return errs
}

// notifyWanted 该动作是否在订阅范围内。
//
// 空配置 = 默认集合；条目按前缀匹配（"settings" 命中 settings.update）。
func notifyWanted(events, action string) bool {
	list := defaultNotifyEvents
	if strings.TrimSpace(events) != "" {
		list = nil
		for _, p := range strings.Split(events, ",") {
			if p = strings.TrimSpace(p); p != "" {
				list = append(list, p)
			}
		}
	}
	for _, p := range list {
		// 按段匹配：登记 "settings" 命中 settings / settings.update，
		// 但不会让 "app" 误命中 "apply"
		if p == "*" || action == p || strings.HasPrefix(action, p+".") {
			return true
		}
	}
	return false
}

// settingDiffKeys 这些键的值永远不进 diff。
//
// 判定用「键名包含」而不是白名单：新增一个 smtp_xxx_pass 之类的键时，
// 忘了登记白名单就会漏，而漏一次就是把密钥发到邮箱里。
func isSecretSetting(key string) bool {
	k := strings.ToLower(key)
	for _, s := range []string{"pass", "secret", "token", "key", "auth_email"} {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// settingDiff 生成一条设置变更的 diff 行。
func settingDiff(key, oldV, newV string) string {
	if isSecretSetting(key) {
		return key + ": 已变更（不显示值）"
	}
	return key + ": " + showVal(oldV) + " → " + showVal(newV)
}

// showVal 空值写成 (空)，超长截断——diff 是给人扫一眼的，不是全量备份。
func showVal(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "(空)"
	}
	r := []rune(v)
	if len(r) > 60 {
		return string(r[:60]) + "…"
	}
	return v
}

// postWebhook 推一条 JSON 到配置的地址。
func (a *Server) postWebhook(url string, ev changeEvent) error {
	host, _ := a.Store.GetSetting("public_host")
	p := webhookPayload{
		Panel:   orDefault(host, "triune-panel"),
		Version: Version,
		Time:    time.Now().Format(store.AuditTimeLayout),
		Event:   ev,
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Triune-Event", "change")
	req.Header.Set("User-Agent", "triune-panel/"+Version)

	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 只读一小段响应体用于报错：有的服务会返回一大页 HTML
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// notifySubject 邮件标题：谁 + 做了什么，一眼能判断要不要立刻看。
func notifySubject(ev changeEvent) string {
	actor := ev.Actor
	if actor == "" {
		actor = "匿名"
	}
	s := "[Triune] " + actor + " · " + ev.Action
	if ev.Target != "" {
		s += " · " + ev.Target
	}
	return s
}

// notifyBody 纯文本正文：结构化的几行 + diff，手机上也能读。
func (a *Server) notifyBody(ev changeEvent) string {
	host, _ := a.Store.GetSetting("public_host")
	var b strings.Builder
	fmt.Fprintf(&b, "时间：%s\n", time.Now().Format(store.AuditTimeLayout))
	fmt.Fprintf(&b, "操作人：%s\n", orDefault(ev.Actor, "匿名"))
	fmt.Fprintf(&b, "动作：%s\n", ev.Action)
	if ev.Target != "" {
		fmt.Fprintf(&b, "对象：%s\n", ev.Target)
	}
	if ev.Detail != "" {
		fmt.Fprintf(&b, "详情：%s\n", ev.Detail)
	}
	fmt.Fprintf(&b, "面板：%s\n", orDefault(host, "triune-panel"))
	if len(ev.Diff) > 0 {
		b.WriteString("\n变更：\n")
		for _, d := range ev.Diff {
			fmt.Fprintf(&b, "  %s\n", d)
		}
	}
	b.WriteString("\n---\n")
	b.WriteString("此邮件由 triune-panel 的「变更通知」发送。\n")
	b.WriteString("要改收件邮箱、Webhook 或订阅哪些事件：设置 → 变更通知。\n")
	return b.String()
}

// handleNotifyTest POST /api/notify/test
// 同步投递一条测试事件并把错误原样返回：配了 Webhook 却因为地址写错
// 一直静默失败，比没有这个功能更糟。
func (a *Server) handleNotifyTest(w http.ResponseWriter, r *http.Request) {
	ev := changeEvent{
		Actor: a.actorName(r), Action: "settings.update", Target: "notify.test",
		Detail: "测试通知：收到这条说明通道可用",
		Diff:   []string{"public_host: (空) → panel.example.com"},
	}
	errs := a.deliverChange(ev, true)
	if len(errs) > 0 {
		writeJSON(w, 200, map[string]any{"ok": false, "errors": errs})
		return
	}
	to, _ := a.Store.GetSetting(NotifyEmailSetting)
	hook, _ := a.Store.GetSetting(NotifyWebhookSetting)
	var ch []string
	if strings.TrimSpace(to) != "" {
		ch = append(ch, "邮件 → "+strings.TrimSpace(to))
	}
	if strings.TrimSpace(hook) != "" {
		ch = append(ch, "Webhook → "+strings.TrimSpace(hook))
	}
	if len(ch) == 0 {
		writeJSON(w, 200, map[string]any{"ok": false, "errors": []string{"还没配置任何通道：填收件邮箱或 Webhook 地址后先保存"}})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "channels": ch})
}

// ---- 字段级 diff（入站 / 用户）----
//
// 通知里最有用的不是「他改了入站」，而是「端口从 443 变成了 22」。
// 只比关键字段：Settings 全文太长且含凭据，整体塞进邮件没人看。

// inboundDiff 入站的关键字段变化。
func inboundDiff(old, cur *model.Inbound) []string {
	var d []string
	add := func(k, a, b string) {
		if a != b {
			d = append(d, k+": "+showVal(a)+" → "+showVal(b))
		}
	}
	add("名称", old.Name, cur.Name)
	add("内核", old.Core, cur.Core)
	add("协议", old.Protocol, cur.Protocol)
	add("端口", strconv.Itoa(old.Port), strconv.Itoa(cur.Port))
	add("传输", old.Transport, cur.Transport)
	add("TLS", old.TLS, cur.TLS)
	add("启用", boolText(old.Enabled), boolText(cur.Enabled))
	add("草稿", boolText(old.Draft), boolText(cur.Draft))
	return d
}

// clientDiff 用户的关键字段变化。凭据与订阅令牌只报「已更换」。
func clientDiff(old, cur *model.Client) []string {
	var d []string
	add := func(k, a, b string) {
		if a != b {
			d = append(d, k+": "+showVal(a)+" → "+showVal(b))
		}
	}
	add("名称", old.Name, cur.Name)
	add("配额(GB)", strconv.FormatFloat(old.QuotaGB, 'f', -1, 64), strconv.FormatFloat(cur.QuotaGB, 'f', -1, 64))
	add("到期", expireText(old.ExpiresAt), expireText(cur.ExpiresAt))
	add("启用", boolText(old.Enabled), boolText(cur.Enabled))
	if old.Credential != cur.Credential {
		d = append(d, "凭据: 已更换（不显示值）")
	}
	if old.SubToken != cur.SubToken {
		d = append(d, "订阅令牌: 已更换（旧链接失效）")
	}
	return d
}

func boolText(b bool) string {
	if b {
		return "是"
	}
	return "否"
}

func expireText(t *time.Time) string {
	if t == nil {
		return "永久"
	}
	return t.Local().Format("2006-01-02 15:04")
}

// fmtIDs 把入站 id 列表渲染成 [1,2,3]，空列表写成 (空)。
func fmtIDs(ids []int64) string {
	if len(ids) == 0 {
		return "(空)"
	}
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	return "[" + strings.Join(parts, ",") + "]"
}
