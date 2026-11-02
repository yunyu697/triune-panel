package api

// 出站视图与「外部节点进订阅」开关的测试。
//
// 这两件事的共同点：出问题都不会报错，只会"看起来正常"——
// 出站页显示 0 条让人以为配置坏了，外部节点默默混进每个人的订阅
// 让人以为是自己配的。所以断言都落在「具体有几条、来源是什么」。

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"triune/internal/core"
	"triune/internal/model"
)

// withPipeline 给测试服务装上流水线：newAuthTestServer 不建 Pipe，
// 而出站视图要经 RenderOptsFor 读导入的 DNS / 路由。
func withPipeline(t *testing.T, a *Server) {
	t.Helper()
	if a.Pipe == nil {
		a.Pipe = &core.PipelineManager{DataDir: a.DataDir, Settings: a.Store}
	}
}

func mkXrayInbound(t *testing.T, a *Server, proto string, port int) *model.Inbound {
	t.Helper()
	in := &model.Inbound{
		Name: proto + "-test", Core: model.CoreXray, Protocol: proto,
		Listen: "::", Port: port, Transport: "tcp", TLS: "none",
		Settings: "{}", Enabled: true,
	}
	if err := a.Store.CreateInbound(in); err != nil {
		t.Fatalf("建入站: %v", err)
	}
	return in
}

func getOutbounds(t *testing.T, h http.Handler, tok, coreName string) outboundView {
	t.Helper()
	w := apiReq(t, h, tok, "GET", "/api/outbounds?core="+coreName, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/outbounds?core=%s → %d: %s", coreName, w.Code, w.Body.String())
	}
	var v outboundView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	return v
}

func hasNote(notes []string, want string) bool {
	for _, n := range notes {
		if strings.Contains(n, want) {
			return true
		}
	}
	return false
}

// TestOutboundsViewXrayBuiltinDirect 空白配置下出站只有一条 direct，
// 没有路由规则也没有 DNS——这是「面板不配出站」的基线。
func TestOutboundsViewXrayBuiltinDirect(t *testing.T) {
	a := newAuthTestServer(t)
	withPipeline(t, a)
	h := a.Handler()
	tok := loginForToken(t, a)
	mkXrayInbound(t, a, "vless", 24443)

	v := getOutbounds(t, h, tok, "xray")
	if v.RenderError != "" {
		t.Fatalf("渲染不该失败: %s", v.RenderError)
	}
	if v.InboundCount != 1 {
		t.Errorf("入站数 = %d，期望 1", v.InboundCount)
	}
	if len(v.Outbounds) != 1 {
		t.Fatalf("出站应只有 1 条，实际 %d: %+v", len(v.Outbounds), v.Outbounds)
	}
	o := v.Outbounds[0]
	if o.Tag != "direct" || o.Type != "freedom" || o.Source != "builtin" {
		t.Errorf("出站应是内置的 direct/freedom，实际 %+v", o)
	}
	if len(v.RouteRules) != 0 {
		t.Errorf("没开统计、没导入路由时不该有规则，实际 %+v", v.RouteRules)
	}
	if v.DNS != nil || v.DNSImported {
		t.Errorf("没导入 DNS 时不该有 dns 段：%v / imported=%v", v.DNS, v.DNSImported)
	}
	if !hasNote(v.Notes, "direct") {
		t.Errorf("说明里应点明出站固定 direct，实际 %v", v.Notes)
	}
}

// TestOutboundsViewXrayImportedGlobal 导入 DNS 与路由后：
// 统计规则在前、导入规则在后，出现 block 时补 blackhole 出站。
func TestOutboundsViewXrayImportedGlobal(t *testing.T) {
	a := newAuthTestServer(t)
	withPipeline(t, a)
	h := a.Handler()
	tok := loginForToken(t, a)
	mkXrayInbound(t, a, "vless", 24443)

	if err := a.Store.SetSetting("stats_enabled", "1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.SetSetting("xray_dns_json", `{"servers":["1.1.1.1","8.8.8.8"]}`); err != nil {
		t.Fatal(err)
	}
	route := `{"domainStrategy":"IPIfNonMatch","rules":[` +
		`{"type":"field","domain":["ads.example.com","tracker.example.net"],"outboundTag":"block"}]}`
	if err := a.Store.SetSetting("xray_route_json", route); err != nil {
		t.Fatal(err)
	}

	v := getOutbounds(t, h, tok, "xray")
	if v.RenderError != "" {
		t.Fatalf("渲染不该失败: %s", v.RenderError)
	}
	// direct + block（blackhole）
	if len(v.Outbounds) != 2 {
		t.Fatalf("应补出 block 出站，实际 %+v", v.Outbounds)
	}
	if v.Outbounds[1].Tag != "block" || v.Outbounds[1].Type != "blackhole" {
		t.Errorf("第二条应是 blackhole/block，实际 %+v", v.Outbounds[1])
	}
	// 规则顺序：统计 API 必须最前，否则读不到流量计数
	if len(v.RouteRules) != 2 {
		t.Fatalf("应有 2 条规则（panel + imported），实际 %+v", v.RouteRules)
	}
	if v.RouteRules[0].Source != "panel" || v.RouteRules[0].Outbound != "api" {
		t.Errorf("第一条应是面板统计规则，实际 %+v", v.RouteRules[0])
	}
	if v.RouteRules[1].Source != "imported" || v.RouteRules[1].Outbound != "block" {
		t.Errorf("第二条应是导入的 block 规则，实际 %+v", v.RouteRules[1])
	}
	if !strings.Contains(v.RouteRules[1].Summary, "ads.example.com") {
		t.Errorf("摘要应带上匹配的域名，实际 %q", v.RouteRules[1].Summary)
	}
	if !v.DNSImported || !v.RouteImported {
		t.Errorf("应标记为导入：dns=%v route=%v", v.DNSImported, v.RouteImported)
	}
	if v.DNS == nil {
		t.Error("应回显导入的 dns 段")
	}
}

// TestOutboundsViewSingbox 换内核看同一套逻辑：出站类型字段是 type 不是 protocol。
func TestOutboundsViewSingbox(t *testing.T) {
	a := newAuthTestServer(t)
	withPipeline(t, a)
	h := a.Handler()
	tok := loginForToken(t, a)

	in := &model.Inbound{
		Name: "sb-vless", Core: model.CoreSingBox, Protocol: "vless",
		Listen: "::", Port: 24444, Transport: "tcp", TLS: "none",
		Settings: "{}", Enabled: true,
	}
	if err := a.Store.CreateInbound(in); err != nil {
		t.Fatal(err)
	}

	v := getOutbounds(t, h, tok, "singbox")
	if v.RenderError != "" {
		t.Fatalf("渲染不该失败: %s", v.RenderError)
	}
	if len(v.Outbounds) != 1 || v.Outbounds[0].Tag != "direct" || v.Outbounds[0].Type != "direct" {
		t.Fatalf("sing-box 出站应是 direct/direct，实际 %+v", v.Outbounds)
	}
}

// TestOutboundsViewMihomoImplicitDirect mihomo 服务端不写 proxies，
// 页面必须补一条"隐式 DIRECT"，否则会显示「出站 0 条」让人以为坏配置。
func TestOutboundsViewMihomoImplicitDirect(t *testing.T) {
	a := newAuthTestServer(t)
	withPipeline(t, a)
	h := a.Handler()
	tok := loginForToken(t, a)

	v := getOutbounds(t, h, tok, "mihomo")
	if v.RenderError != "" {
		t.Fatalf("渲染不该失败: %s", v.RenderError)
	}
	if len(v.Outbounds) != 1 {
		t.Fatalf("应有一条隐式 DIRECT，实际 %+v", v.Outbounds)
	}
	if v.Outbounds[0].Tag != "DIRECT" || !strings.Contains(v.Outbounds[0].Note, "proxies") {
		t.Errorf("隐式出站应带说明，实际 %+v", v.Outbounds[0])
	}
}

// TestExternalNodesGatedBySetting 外部节点默认**不**混进订阅：
// 它是全局的，进了订阅等于给每个用户都塞上别人的节点。
func TestExternalNodesGatedBySetting(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()

	in := mkXrayInbound(t, a, "vless", 24445)
	c := &model.Client{Name: "alice", Credential: "11111111-2222-3333-4444-555555555555",
		SubToken: "tok-ext-test", Enabled: true}
	if err := a.Store.CreateClient(c); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.SetClientInbounds(c.ID, []int64{in.ID}); err != nil {
		t.Fatal(err)
	}
	ext := &model.ExternalNode{
		Name: "别人的机场节点",
		RawLink: "vless://99999999-8888-7777-6666-555555555555@198.51.100.7:443" +
			"?encryption=none&security=none&type=tcp#other-airport",
	}
	if err := a.Store.CreateExternalNode(ext); err != nil {
		t.Fatal(err)
	}

	subBody := func() string {
		req := httptest.NewRequest("GET", "/sub/tok-ext-test", nil)
		req.Host = "panel.example.com"
		req.RemoteAddr = "203.0.113.9:1234"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("订阅接口 → %d: %s", w.Code, w.Body.String())
		}
		raw := w.Body.String()
		if dec, err := base64.StdEncoding.DecodeString(raw); err == nil {
			raw = string(dec)
		}
		return raw
	}

	if body := subBody(); strings.Contains(body, "other-airport") {
		t.Fatalf("默认不该把外部节点混进订阅，实际:\n%s", body)
	}

	// 打开开关后才混进来
	tok := loginForToken(t, a)
	w := apiReq(t, h, tok, "PUT", "/api/settings", `{"external_in_sub":"1"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("打开开关失败: %d %s", w.Code, w.Body.String())
	}
	body := subBody()
	if !strings.Contains(body, "other-airport") {
		t.Fatalf("打开开关后订阅应包含外部节点，实际:\n%s", body)
	}
	// 自己的入站始终在，不受开关影响
	if !strings.Contains(body, "vless-test") {
		t.Errorf("自己的入站不该被开关影响，实际:\n%s", body)
	}
}

// TestSubOnlyEffectiveSSClient shadowsocks 的服务端密码是入站级单值，
// 渲染时只取首个启用用户。其余用户拿到的节点密码必然对不上——发出去等于
// 发一个永远连不上的配置，而面板上看起来一切正常（有节点、有流量统计位）。
func TestSubOnlyEffectiveSSClient(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()

	in := &model.Inbound{Name: "ss1", Core: "xray", Protocol: "shadowsocks",
		Port: 24455, Transport: "tcp", TLS: "none", Enabled: true,
		Settings: `{"method":"aes-256-gcm"}`}
	if err := a.Store.CreateInbound(in); err != nil {
		t.Fatal(err)
	}
	u1 := &model.Client{Name: "winner", Credential: "first-pass",
		SubToken: "tok-ss-1", Enabled: true}
	u2 := &model.Client{Name: "loser", Credential: "second-pass",
		SubToken: "tok-ss-2", Enabled: true}
	for _, c := range []*model.Client{u1, u2} {
		if err := a.Store.CreateClient(c); err != nil {
			t.Fatal(err)
		}
		if err := a.Store.SetClientInbounds(c.ID, []int64{in.ID}); err != nil {
			t.Fatal(err)
		}
	}

	sub := func(tok string) string {
		req := httptest.NewRequest("GET", "/sub/"+tok, nil)
		req.Host = "panel.example.com"
		req.RemoteAddr = "203.0.113.9:1234"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("订阅 %s → %d: %s", tok, w.Code, w.Body.String())
		}
		raw := w.Body.String()
		if dec, err := base64.StdEncoding.DecodeString(raw); err == nil {
			raw = string(dec)
		}
		return raw
	}

	if body := sub("tok-ss-1"); !strings.Contains(body, "ss1") {
		t.Errorf("首个启用用户应拿到节点，实际:\n%s", body)
	}
	if body := sub("tok-ss-2"); strings.Contains(body, "ss1") {
		t.Errorf("非生效用户不该拿到这个 shadowsocks 节点（密码对不上），实际:\n%s", body)
	}

	// 首个用户被停用后，生效者顺延到下一个：两边都按 client_id 顺序取，
	// 渲染器与订阅必须同时换人，否则又变成一个人拿着错密码。
	u1.Enabled = false
	if err := a.Store.UpdateClient(u1); err != nil {
		t.Fatal(err)
	}
	if body := sub("tok-ss-2"); !strings.Contains(body, "ss1") {
		t.Errorf("首个用户停用后应轮到下一个，实际:\n%s", body)
	}
}
