package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// hookSink 收 Webhook 请求的假服务器。
//
// 同时留一份原始报文：只断言结构化的 payload 会漏掉「密码被序列化进
// 某个字段」这类问题，必须能在原文里搜一遍。
type hookSink struct {
	srv  *httptest.Server
	ch   chan webhookPayload
	mu   sync.Mutex
	raw  []string
	code int
}

func newHookSink(t *testing.T) *hookSink {
	t.Helper()
	h := &hookSink{ch: make(chan webhookPayload, 16), code: 200}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		h.raw = append(h.raw, string(body))
		code := h.code
		h.mu.Unlock()
		var p webhookPayload
		if err := json.Unmarshal(body, &p); err == nil {
			h.ch <- p
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(h.srv.Close)
	return h
}

// raws 返回已收到的全部原始报文。
func (h *hookSink) raws() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.raw))
	copy(out, h.raw)
	return out
}

// waitFor 等一条满足条件的 payload（期间可能有别的通知先到）。
func (h *hookSink) waitFor(t *testing.T, why string, pred func(webhookPayload) bool) webhookPayload {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case p := <-h.ch:
			if pred(p) {
				return p
			}
		case <-deadline:
			t.Fatalf("3 秒内没等到通知：%s", why)
		}
	}
}

// TestSettingDiffMasksSecrets 敏感键的值绝不进通知。
//
// 判定是「键名包含 pass/secret/token/key」而不是白名单：将来加个
// smtp_xxx_pass 之类的新键，忘了登记白名单就会把密钥发到邮箱里。
func TestSettingDiffMasksSecrets(t *testing.T) {
	cases := []struct{ key, old, nw, want string }{
		{"smtp_pass", "a", "b", "smtp_pass: 已变更（不显示值）"},
		{"stats_secret", "a", "b", "stats_secret: 已变更（不显示值）"},
		{"metrics_token", "a", "b", "metrics_token: 已变更（不显示值）"},
		{"public_host", "a.com", "b.com", "public_host: a.com → b.com"},
		{"public_host", "", "x.com", "public_host: (空) → x.com"},
	}
	for _, c := range cases {
		if got := settingDiff(c.key, c.old, c.nw); got != c.want {
			t.Errorf("settingDiff(%q) = %q, want %q", c.key, got, c.want)
		}
	}
	// 超长值截断，避免把整段证书/密钥文本塞进邮件
	long := strings.Repeat("x", 200)
	if got := settingDiff("public_host", "", long); len([]rune(got)) > 100 {
		t.Errorf("超长值应截断，实际长度 %d", len([]rune(got)))
	}
}

// TestNotifyWanted 事件订阅按段匹配。
func TestNotifyWanted(t *testing.T) {
	cases := []struct {
		events, action string
		want           bool
	}{
		{"", "settings.update", true}, // 留空 = 默认集合
		{"", "inbound.create", true},  // 默认含 inbound
		{"", "login", false},          // 登录默认不推（太吵）
		{"inbound", "inbound.delete", true},
		{"inbound", "settings.update", false},
		{"settings,client", "client.update", true},
		{"*", "login", true},
		{"app", "apply", false}, // 按段匹配，不做模糊包含
		{"apply", "apply", true},
	}
	for _, c := range cases {
		if got := notifyWanted(c.events, c.action); got != c.want {
			t.Errorf("notifyWanted(%q, %q) = %v, want %v", c.events, c.action, got, c.want)
		}
	}
}

// TestNotifyWebhookDeliversSettingsChange 改设置会推一条带 diff 的 Webhook，
// 且敏感值不会出现在报文里。
func TestNotifyWebhookDeliversSettingsChange(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")
	sink := newHookSink(t)

	if w := do(a, owner, "PUT", "/api/settings", map[string]string{
		NotifyWebhookSetting: sink.srv.URL,
		NotifyEventsSetting:  "settings",
	}); w.Code != 200 {
		t.Fatalf("保存通知设置应 200，实际 %d: %s", w.Code, w.Body.String())
	}

	// 这次才是要观察的变更：一个普通键 + 一个敏感键
	if w := do(a, owner, "PUT", "/api/settings", map[string]string{
		"public_host": "panel.example.com",
		"smtp_pass":   "super-secret-pass",
	}); w.Code != 200 {
		t.Fatalf("改设置应 200，实际 %d: %s", w.Code, w.Body.String())
	}

	p := sink.waitFor(t, "设置变更", func(p webhookPayload) bool {
		return strings.Contains(strings.Join(p.Event.Diff, "\n"), "public_host")
	})
	if p.Event.Actor != "admin" {
		t.Fatalf("操作人应为 admin，实际 %q", p.Event.Actor)
	}
	if p.Event.Action != AuditSettings {
		t.Fatalf("动作应为 %s，实际 %q", AuditSettings, p.Event.Action)
	}
	diff := strings.Join(p.Event.Diff, "\n")
	if !strings.Contains(diff, "public_host: (空) → panel.example.com") {
		t.Fatalf("diff 应包含旧值与新值，实际：\n%s", diff)
	}
	if !strings.Contains(diff, "smtp_pass: 已变更（不显示值）") {
		t.Fatalf("敏感键应打码，实际：\n%s", diff)
	}
	for _, raw := range sink.raws() {
		if strings.Contains(raw, "super-secret-pass") {
			t.Fatalf("Webhook 报文里出现了明文密码：%s", raw)
		}
	}
}

// TestNotifySkipsUnsubscribed 没订阅的事件不推。
func TestNotifySkipsUnsubscribed(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")
	sink := newHookSink(t)

	do(a, owner, "PUT", "/api/settings", map[string]string{
		NotifyWebhookSetting: sink.srv.URL,
		NotifyEventsSetting:  "inbound",
	})
	do(a, owner, "PUT", "/api/settings", map[string]string{"public_host": "x.example.com"})

	select {
	case p := <-sink.ch:
		t.Fatalf("只订阅了 inbound，不该收到设置变更：%+v", p.Event)
	case <-time.After(600 * time.Millisecond):
	}
}

// TestNotifyNoChannelSilent 没配通道时静默跳过，主操作照常成功。
func TestNotifyNoChannelSilent(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")
	if errs := a.deliverChange(changeEvent{Action: AuditSettings}, false); len(errs) != 0 {
		t.Fatalf("没配通道时应静默跳过，实际 %v", errs)
	}
	if w := do(a, owner, "PUT", "/api/settings", map[string]string{"public_host": "y.example.com"}); w.Code != 200 {
		t.Fatalf("改设置应 200，实际 %d", w.Code)
	}
}

// TestNotifyTestEndpoint 测试按钮要如实报告成功与失败。
func TestNotifyTestEndpoint(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")
	sink := newHookSink(t)

	// 没配通道：明确告诉用户「先去保存」
	w := do(a, owner, "POST", "/api/notify/test", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "还没配置任何通道") {
		t.Fatalf("未配置时应提示先配置，实际 %d: %s", w.Code, w.Body.String())
	}

	do(a, owner, "PUT", "/api/settings", map[string]string{NotifyWebhookSetting: sink.srv.URL})
	w = do(a, owner, "POST", "/api/notify/test", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("配置后测试应成功，实际 %d: %s", w.Code, w.Body.String())
	}
	sink.waitFor(t, "测试事件", func(p webhookPayload) bool { return p.Event.Target == "notify.test" })

	// Webhook 返回 500 时，错误必须回传给界面而不是假装成功
	sink.mu.Lock()
	sink.code = 500
	sink.mu.Unlock()
	w = do(a, owner, "POST", "/api/notify/test", nil)
	if !strings.Contains(w.Body.String(), `"ok":false`) || !strings.Contains(w.Body.String(), "HTTP 500") {
		t.Fatalf("上游 500 时应如实报错，实际 %s", w.Body.String())
	}
}

// TestNotifyTestRequiresAdmin 只读账号不能拿它当探针打外部地址。
func TestNotifyTestRequiresAdmin(t *testing.T) {
	a := newRoleTestServer(t)
	guest := loginAs(t, a, "guest", "guest-password-1")
	if w := do(a, guest, "POST", "/api/notify/test", nil); w.Code != http.StatusForbidden {
		t.Fatalf("viewer 应 403，实际 %d", w.Code)
	}
}

// TestInboundUpdateNotifiesDiff 改端口这种「一眼能看出风险」的变更要进 diff。
func TestInboundUpdateNotifiesDiff(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")
	sink := newHookSink(t)
	do(a, owner, "PUT", "/api/settings", map[string]string{
		NotifyWebhookSetting: sink.srv.URL,
		NotifyEventsSetting:  "inbound",
	})

	w := do(a, owner, "POST", "/api/inbounds", map[string]any{
		"name": "hk-01", "core": "singbox", "protocol": "vless",
		"port": 443, "transport": "tcp", "tls": "tls",
	})
	if w.Code != 200 {
		t.Fatalf("建入站应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var in map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &in)
	id := int64(in["id"].(float64))
	sink.waitFor(t, "创建入站", func(p webhookPayload) bool {
		return p.Event.Action == AuditInboundCreate
	})

	if w := do(a, owner, "PUT", "/api/inbounds/"+strconv.FormatInt(id, 10),
		map[string]any{"port": 22}); w.Code != 200 {
		t.Fatalf("改端口应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	p := sink.waitFor(t, "改端口", func(p webhookPayload) bool {
		return p.Event.Action == AuditInboundUpdate
	})
	if diff := strings.Join(p.Event.Diff, "\n"); !strings.Contains(diff, "端口: 443 → 22") {
		t.Fatalf("diff 应包含端口变化，实际：\n%s", diff)
	}
}
