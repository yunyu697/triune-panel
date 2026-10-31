package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"triune/internal/model"
)

// apiReq 带登录 token 发一个 JSON 请求（走完整 mux，含鉴权与安全头中间件）。
func apiReq(t *testing.T, h http.Handler, tok, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.30:1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// TestCreateClientRejectsRateLimit 传了非零 rate_limit_mbps 必须 400。
//
// 三个内核都没有每用户带宽限制能力，静默接受一个永远不生效的数字，
// 比没有这个功能更糟——用户填了「100」会以为限速生效了。
func TestCreateClientRejectsRateLimit(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	w := apiReq(t, h, tok, "POST", "/api/clients", `{"name":"bob","rate_limit_mbps":100}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("应 400，实际 %d: %s", w.Code, w.Body.String())
	}
	msg := w.Body.String()
	// 拒绝时要说清「为什么不行」和「该用什么」，否则调用方只会反复重试
	for _, want := range []string{"不支持每用户带宽限速", "quota_gb"} {
		if !strings.Contains(msg, want) {
			t.Errorf("错误说明应包含 %q，实际: %s", want, msg)
		}
	}
	if cs, err := a.Store.ListClients(); err != nil || len(cs) != 0 {
		t.Fatalf("被拒绝的请求不应创建用户（err=%v, 实际 %d 个）", err, len(cs))
	}
}

// TestCreateClientAcceptsZeroOrAbsentRateLimit 不传或显式传 0 都放行，
// 且响应里不能再出现这个字段——留着就是在暗示它有效。
func TestCreateClientAcceptsZeroOrAbsentRateLimit(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	for _, body := range []string{
		`{"name":"no-field"}`,
		`{"name":"zero","rate_limit_mbps":0}`,
	} {
		w := apiReq(t, h, tok, "POST", "/api/clients", body)
		if w.Code != http.StatusOK {
			t.Fatalf("%s 应 200，实际 %d: %s", body, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "rate_limit_mbps") {
			t.Errorf("%s 的响应不该出现 rate_limit_mbps: %s", body, w.Body.String())
		}
	}
}

// TestUpdateClientRejectsRateLimit 更新路径同样拒绝，且原记录保持不变。
func TestUpdateClientRejectsRateLimit(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	w := apiReq(t, h, tok, "POST", "/api/clients", `{"name":"carol","quota_gb":10}`)
	if w.Code != http.StatusOK {
		t.Fatalf("建用户应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var created model.Client
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	path := "/api/clients/" + strconv.FormatInt(created.ID, 10)

	w = apiReq(t, h, tok, "PUT", path, `{"name":"carol","quota_gb":10,"rate_limit_mbps":50}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("带限速的更新应 400，实际 %d: %s", w.Code, w.Body.String())
	}

	// 被拒绝的更新不能留下任何副作用
	cs, err := a.Store.ListClients()
	if err != nil || len(cs) != 1 {
		t.Fatalf("用户数异常: err=%v len=%d", err, len(cs))
	}
	if cs[0].Name != "carol" || cs[0].QuotaGB != 10 {
		t.Fatalf("被拒绝的更新改动了数据: %+v", cs[0])
	}
}

// TestListClientsHasNoRateLimitField 列表响应也不能出现这个字段。
func TestListClientsHasNoRateLimitField(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	if w := apiReq(t, h, tok, "POST", "/api/clients", `{"name":"dave","quota_gb":5}`); w.Code != http.StatusOK {
		t.Fatalf("建用户应 200，实际 %d: %s", w.Code, w.Body.String())
	}

	w := apiReq(t, h, tok, "GET", "/api/clients", "")
	if w.Code != http.StatusOK {
		t.Fatalf("列表应 200，实际 %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "rate_limit_mbps") {
		t.Fatalf("列表响应不该出现 rate_limit_mbps: %s", w.Body.String())
	}
	// 顺带确认结构没被改坏：仍应包成 {client, inbound_ids}
	var out []struct {
		Client     model.Client `json:"client"`
		InboundIDs []int64      `json:"inbound_ids"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("列表结构变了: %v", err)
	}
	if len(out) != 1 || out[0].Client.Name != "dave" {
		t.Fatalf("列表内容异常: %+v", out)
	}
}

// TestUpdateClientPartialKeepsCredential 部分更新不能动没传的字段。
//
// 旧实现把请求体解成完整 Client 再整行 UPDATE，`PUT {"name":"x","quota_gb":5}`
// 会把 credential、sub_token 清成空串、enabled 置 false，还返回 200——
// 用户的订阅链接和凭据静默失效，面板上只表现为「凭据变空」。
func TestUpdateClientPartialKeepsCredential(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	w := apiReq(t, h, tok, "POST", "/api/clients", `{"name":"erin","quota_gb":10}`)
	if w.Code != http.StatusOK {
		t.Fatalf("建用户应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var created model.Client
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Credential == "" || created.SubToken == "" {
		t.Fatalf("建用户应生成凭据与订阅令牌: %+v", created)
	}
	path := "/api/clients/" + strconv.FormatInt(created.ID, 10)

	// 只改配额，其余字段一个都不传
	if w := apiReq(t, h, tok, "PUT", path, `{"quota_gb":42}`); w.Code != http.StatusOK {
		t.Fatalf("部分更新应 200，实际 %d: %s", w.Code, w.Body.String())
	}

	got, err := a.Store.GetClient(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Credential != created.Credential {
		t.Errorf("credential 被改动: %q → %q", created.Credential, got.Credential)
	}
	if got.SubToken != created.SubToken {
		t.Errorf("sub_token 被改动: %q → %q", created.SubToken, got.SubToken)
	}
	if !got.Enabled {
		t.Error("enabled 被误置为 false")
	}
	if got.Name != "erin" {
		t.Errorf("name 被改动: %q", got.Name)
	}
	if got.QuotaGB != 42 {
		t.Errorf("quota_gb 未更新: %v", got.QuotaGB)
	}
}

// TestUpdateClientExpiresAtSemantics 显式 null 清除到期时间，不传则保持原值。
func TestUpdateClientExpiresAtSemantics(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	w := apiReq(t, h, tok, "POST", "/api/clients",
		`{"name":"frank","expires_at":"2027-01-02T03:04:05Z"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("建用户应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var created model.Client
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ExpiresAt == nil {
		t.Fatal("expires_at 未生效")
	}
	path := "/api/clients/" + strconv.FormatInt(created.ID, 10)

	// 不传 expires_at → 保持
	if w := apiReq(t, h, tok, "PUT", path, `{"name":"frank2"}`); w.Code != http.StatusOK {
		t.Fatalf("更新应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	if got, _ := a.Store.GetClient(created.ID); got.ExpiresAt == nil {
		t.Fatal("没传 expires_at 不该清掉到期时间")
	}

	// 显式 null → 清除
	if w := apiReq(t, h, tok, "PUT", path, `{"expires_at":null}`); w.Code != http.StatusOK {
		t.Fatalf("更新应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	if got, _ := a.Store.GetClient(created.ID); got.ExpiresAt != nil {
		t.Fatalf("显式 null 应清除到期时间，实际 %v", got.ExpiresAt)
	}
}

// TestUpdateClientNotFound 改不存在的用户要 404，不能假装成功。
func TestUpdateClientNotFound(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	if w := apiReq(t, h, tok, "PUT", "/api/clients/999", `{"name":"ghost"}`); w.Code != http.StatusNotFound {
		t.Fatalf("应 404，实际 %d: %s", w.Code, w.Body.String())
	}
}

// TestViewerSeesMaskedClients 只读账号能看用户列表，但拿不到凭据与订阅令牌。
//
// v0.9.7 及以前整条 GET /api/clients 对 viewer 是 403：连「谁用了多少流量」
// 都看不到，代价太大。真正敏感的只有 credential / sub_token 两列，
// 所以改成脱敏返回，并带 masked 标记让前端能说清「是你看不到，不是没配」。
func TestViewerSeesMaskedClients(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")
	guest := loginAs(t, a, "guest", "guest-password-1")

	if w := do(a, owner, "POST", "/api/clients", map[string]any{"name": "bob"}); w.Code != 200 {
		t.Fatalf("建用户应 200，实际 %d: %s", w.Code, w.Body.String())
	}

	// owner 拿到完整凭据
	w := do(a, owner, "GET", "/api/clients", nil)
	if w.Code != 200 {
		t.Fatalf("owner 拉列表应 200，实际 %d", w.Code)
	}
	var full []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &full); err != nil {
		t.Fatal(err)
	}
	if len(full) != 1 {
		t.Fatalf("应有 1 个用户，实际 %d", len(full))
	}
	fc, _ := full[0]["client"].(map[string]any)
	if fc["credential"] == "" || fc["sub_token"] == "" {
		t.Fatalf("owner 应看到凭据与订阅令牌，实际 %v", fc)
	}
	if full[0]["masked"] != false {
		t.Fatalf("owner 的响应不应标记 masked，实际 %v", full[0]["masked"])
	}

	// viewer 能看列表，但两列都空，且带 masked 标记
	w = do(a, guest, "GET", "/api/clients", nil)
	if w.Code != 200 {
		t.Fatalf("viewer 拉列表应 200（脱敏），实际 %d: %s", w.Code, w.Body.String())
	}
	var masked []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &masked); err != nil {
		t.Fatal(err)
	}
	mc, _ := masked[0]["client"].(map[string]any)
	if mc["credential"] != "" || mc["sub_token"] != "" {
		t.Fatalf("viewer 看到的凭据与订阅令牌必须为空，实际 %v", mc)
	}
	if masked[0]["masked"] != true {
		t.Fatalf("viewer 的响应应标记 masked=true，实际 %v", masked[0]["masked"])
	}
	// 但配额、启用状态这些「看得懂但不危险」的字段要保留
	if mc["name"] != "bob" || mc["enabled"] != true {
		t.Fatalf("脱敏不应抹掉非敏感字段，实际 %v", mc)
	}

	// viewer 仍然不能建用户
	if w := do(a, guest, "POST", "/api/clients", map[string]any{"name": "x"}); w.Code != http.StatusForbidden {
		t.Fatalf("viewer 建用户应 403，实际 %d", w.Code)
	}
}
