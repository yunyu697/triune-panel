package singbox

import (
	"encoding/json"
	"strings"
	"testing"

	"triune/internal/core"
	"triune/internal/model"
)

// 托管键不得透传到内核配置顶层（sing-box 严格校验未知字段会拒配置），
// 且 short_id 应被结构化进 tls.reality.short_id。
func TestRenderInboundManagedKeysStripped(t *testing.T) {
	s := &SingBox{}
	in := &model.Inbound{
		Name: "t-reality", Core: "singbox", Protocol: "vless",
		Port: 443, Transport: "tcp", TLS: "reality",
		Settings: `{"private_key":"PRIV","handshake_server":"www.apple.com","short_id":"0123abcd","pbk":"CLIENT-ONLY","flow":"xtls-rprx-vision","server_names":"www.apple.com"}`,
	}
	iw := core.InboundWithClients{Inbound: in, Clients: []*model.Client{
		{Name: "u1", Credential: "uuid-1", Enabled: true},
	}}
	obj, err := s.renderInbound(iw)
	if err != nil {
		t.Fatalf("renderInbound: %v", err)
	}
	for _, k := range core.ManagedSettingsKeys {
		if _, ok := obj[k]; ok {
			t.Errorf("managed key %q leaked to top-level inbound object", k)
		}
	}
	tls, ok := obj["tls"].(map[string]any)
	if !ok {
		t.Fatalf("tls missing: %v", obj["tls"])
	}
	reality, ok := tls["reality"].(map[string]any)
	if !ok {
		t.Fatalf("tls.reality missing: %v", tls)
	}
	if reality["private_key"] != "PRIV" {
		t.Errorf("private_key = %v", reality["private_key"])
	}
	hs, _ := reality["handshake"].(map[string]any)
	if hs["server"] != "www.apple.com" {
		t.Errorf("handshake.server = %v", hs["server"])
	}
	sids, ok := reality["short_id"].([]string)
	if !ok || len(sids) != 1 || sids[0] != "0123abcd" {
		t.Errorf("short_id = %v", reality["short_id"])
	}
}

// 渲染产物必须是合法 JSON（冒烟）。
func TestRenderValidJSON(t *testing.T) {
	s := &SingBox{}
	in := &model.Inbound{Name: "ss", Core: "singbox", Protocol: "shadowsocks",
		Port: 8388, Transport: "tcp", TLS: "none", Settings: `{"method":"aes-256-gcm","pbk":"x"}`}
	iw := core.InboundWithClients{Inbound: in, Clients: []*model.Client{
		{Name: "u1", Credential: "pass-1", Enabled: true},
	}}
	b, err := s.Render([]core.InboundWithClients{iw}, core.RenderOpts{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("output not valid json: %v", err)
	}
}

// TUIC 的 user 必须有 uuid（只给 password 会被内核拒收），
// hysteria2/tuic 不能带 transport 字段（unknown field "transport"）。
func TestRenderQUICProtocolsCompat(t *testing.T) {
	s := &SingBox{}
	clients := []*model.Client{{Name: "u1", Credential: "cred-1", Enabled: true}}

	tuic := &model.Inbound{Name: "tuic", Core: "singbox", Protocol: "tuic",
		Port: 443, Transport: "udp", TLS: "tls",
		Settings: `{"certificate_path":"/tmp/c.pem","key_path":"/tmp/k.pem"}`}
	obj, err := s.renderInbound(core.InboundWithClients{Inbound: tuic, Clients: clients})
	if err != nil {
		t.Fatalf("tuic render: %v", err)
	}
	if _, ok := obj["transport"]; ok {
		t.Errorf("tuic 不应有 transport 字段: %v", obj["transport"])
	}
	users, _ := obj["users"].([]any)
	if len(users) != 1 {
		t.Fatalf("tuic users = %v", obj["users"])
	}
	u := users[0].(map[string]any)
	if u["uuid"] != "cred-1" {
		t.Errorf("tuic user 缺少 uuid: %v", u)
	}

	hy2 := &model.Inbound{Name: "hy2", Core: "singbox", Protocol: "hysteria2",
		Port: 8443, Transport: "udp", TLS: "tls",
		Settings: `{"certificate_path":"/tmp/c.pem","key_path":"/tmp/k.pem"}`}
	obj2, err := s.renderInbound(core.InboundWithClients{Inbound: hy2, Clients: clients})
	if err != nil {
		t.Fatalf("hysteria2 render: %v", err)
	}
	if _, ok := obj2["transport"]; ok {
		t.Errorf("hysteria2 不应有 transport 字段: %v", obj2["transport"])
	}
}

// socks/mixed 的 user 只有 username/password，多余 name 会导致严格校验失败。
func TestRenderSocksUsersNoNameField(t *testing.T) {	s := &SingBox{}
	in := &model.Inbound{Name: "socks", Core: "singbox", Protocol: "socks", Port: 1080, Transport: "tcp"}
	obj, err := s.renderInbound(core.InboundWithClients{Inbound: in,
		Clients: []*model.Client{{Name: "u1", Credential: "p1", Enabled: true}}})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	users, _ := obj["users"].([]any)
	if len(users) != 1 {
		t.Fatalf("users = %v", obj["users"])
	}
	u := users[0].(map[string]any)
	if _, ok := u["name"]; ok {
		t.Errorf("socks user 不应有 name 字段: %v", u)
	}
	if u["username"] != "u1" || u["password"] != "p1" {
		t.Errorf("socks user 字段不对: %v", u)
	}
}

// 强制要求证书的协议在缺证书时应给出可读错误，而不是内核内部报错。
func TestRenderRequiresCertMessage(t *testing.T) {
	s := &SingBox{}
	in := &model.Inbound{Name: "anytls", Core: "singbox", Protocol: "anytls", Port: 443, Transport: "tcp", TLS: "tls"}
	if _, err := s.renderInbound(core.InboundWithClients{Inbound: in}); err == nil {
		t.Fatal("anytls 缺证书时应当报错")
	}
}

// 3x-ui 的 vision 入站带 flow、reality 带多个 shortId：
// flow 要落到用户上（否则客户端退回普通 TLS），short_id 是数组。
func TestRenderImportedFlowAndShortIDs(t *testing.T) {
	sb := &SingBox{}
	in := &model.Inbound{
		Name: "imp", Core: "singbox", Protocol: "vless",
		Port: 24453, Transport: "tcp", TLS: "reality",
		Settings: `{"private_key":"PRIV","handshake_server":"www.apple.com","handshake_port":8443,
			"short_ids":["ab12",""],"flow":"xtls-rprx-vision"}`,
	}
	obj, err := sb.renderInbound(core.InboundWithClients{Inbound: in, Clients: []*model.Client{
		{Name: "alice", Credential: "uuid-1", Enabled: true},
	}})
	if err != nil {
		t.Fatalf("renderInbound: %v", err)
	}
	users := obj["users"].([]any)
	u := users[0].(map[string]any)
	if u["flow"] != "xtls-rprx-vision" {
		t.Errorf("flow 没落到用户上: %#v", u)
	}
	tls := obj["tls"].(map[string]any)
	r := tls["reality"].(map[string]any)
	sids, ok := r["short_id"].([]string)
	if !ok || len(sids) != 2 {
		t.Errorf("short_id 应是数组: %#v", r["short_id"])
	}
	hs := r["handshake"].(map[string]any)
	if hs["server"] != "www.apple.com" || hs["server_port"] != 8443 {
		t.Errorf("handshake 端口错: %#v", hs)
	}
}

// 只有空短 ID 时不能写 short_id（sing-box 会拒收 [""]）。
func TestRenderRealityEmptyShortIDOmitted(t *testing.T) {
	sb := &SingBox{}
	in := &model.Inbound{
		Name: "imp", Core: "singbox", Protocol: "vless",
		Port: 24455, Transport: "tcp", TLS: "reality",
		Settings: `{"private_key":"PRIV","handshake_server":"www.apple.com"}`,
	}
	obj, err := sb.renderInbound(core.InboundWithClients{Inbound: in, Clients: []*model.Client{
		{Name: "u", Credential: "uuid", Enabled: true},
	}})
	if err != nil {
		t.Fatalf("renderInbound: %v", err)
	}
	r := obj["tls"].(map[string]any)["reality"].(map[string]any)
	if _, ok := r["short_id"]; ok {
		t.Errorf("空短 ID 不该写进 short_id: %#v", r["short_id"])
	}
}

// http 入站：用户字段是 username/password，凭据 "user:pass" 要拆开。
func TestRenderHTTPUsers(t *testing.T) {
	sb := &SingBox{}
	in := &model.Inbound{
		Name: "h", Core: "singbox", Protocol: "http", Port: 8080,
		Transport: "tcp", TLS: "none",
	}
	obj, err := sb.renderInbound(core.InboundWithClients{Inbound: in, Clients: []*model.Client{
		{Name: "h1", Credential: "h1:hp1", Enabled: true},
	}})
	if err != nil {
		t.Fatalf("renderInbound: %v", err)
	}
	u := obj["users"].([]any)[0].(map[string]any)
	if u["username"] != "h1" || u["password"] != "hp1" {
		t.Errorf("http 用户应拆成 h1/hp1：%#v", u)
	}
	if _, ok := u["name"]; ok {
		t.Errorf("sing-box 的 http 用户不该带 name 字段：%#v", u)
	}
}

// direct（= dokodemo-door）：override_address / override_port。
func TestRenderDirectInbound(t *testing.T) {
	sb := &SingBox{}
	in := &model.Inbound{
		Name: "pf", Core: "singbox", Protocol: "direct", Port: 9000,
		Transport: "tcp", TLS: "none",
		Settings: `{"dd_address":"1.1.1.1","dd_port":80,"dd_network":"tcp"}`,
	}
	obj, err := sb.renderInbound(core.InboundWithClients{Inbound: in})
	if err != nil {
		t.Fatalf("renderInbound: %v", err)
	}
	if obj["override_address"] != "1.1.1.1" || obj["override_port"] != 80 {
		t.Errorf("direct 的 override 参数错：%#v", obj)
	}
	// dd_* 是面板托管键，不能原样留在顶层
	if _, ok := obj["dd_address"]; ok {
		t.Errorf("托管键不应透传到顶层：%#v", obj)
	}
}

// 新传输：h2 要改名成 http，httpupgrade / quic 直通；kcp/xhttp 明确报错。
func TestRenderNewTransports(t *testing.T) {
	sb := &SingBox{}
	cases := []struct {
		transport string
		settings  string
		wantType  string
	}{
		{"h2", `{"path":"/h2","ws_host":"h.example.com"}`, "http"},
		{"httpupgrade", `{"path":"/hu","ws_host":"hu.example.com"}`, "httpupgrade"},
		{"quic", `{"certificate_path":"/tmp/c.pem","key_path":"/tmp/k.pem"}`, "quic"},
		{"ws", `{"path":"/ws"}`, "ws"},
		{"grpc", `{"service_name":"g1"}`, "grpc"},
	}
	for _, c := range cases {
		in := &model.Inbound{
			Name: "v", Core: "singbox", Protocol: "vless", Port: 443,
			Transport: c.transport, TLS: "none", Settings: c.settings,
		}
		if c.transport == "quic" {
			in.TLS = "tls" // sing-box 的 quic 传输必须跑在 TLS 上
		}
		obj, err := sb.renderInbound(core.InboundWithClients{Inbound: in, Clients: []*model.Client{
			{Name: "u", Credential: "uuid", Enabled: true},
		}})
		if err != nil {
			t.Fatalf("%s renderInbound: %v", c.transport, err)
		}
		tr, _ := obj["transport"].(map[string]any)
		if tr["type"] != c.wantType {
			t.Errorf("%s transport.type = %v，期望 %s", c.transport, tr["type"], c.wantType)
		}
	}
	for _, bad := range []string{"kcp", "xhttp"} {
		in := &model.Inbound{Name: "v", Core: "singbox", Protocol: "vless", Port: 443, Transport: bad}
		if _, err := sb.renderInbound(core.InboundWithClients{Inbound: in, Clients: []*model.Client{
			{Name: "u", Credential: "uuid", Enabled: true},
		}}); err == nil {
			t.Errorf("%s 传输应报错（sing-box 不支持）", bad)
		}
	}
}

// 导入的 DNS / 路由（已由 importer 转成 sing-box 结构）要原样注入。
func TestRenderGlobalDNSRoute(t *testing.T) {
	sb := &SingBox{}
	in := &model.Inbound{Name: "v", Core: "singbox", Protocol: "vless", Port: 443, Transport: "tcp"}
	out, err := sb.Render([]core.InboundWithClients{{Inbound: in, Clients: []*model.Client{
		{Name: "u", Credential: "uuid", Enabled: true},
	}}}, core.RenderOpts{
		DNSJSON:   `{"servers":[{"type":"udp","tag":"dns-1-1-1-1","server":"1.1.1.1"}]}`,
		RouteJSON: `{"rules":[{"ip_cidr":["10.0.0.0/8"],"action":"reject"}]}`,
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := doc["dns"]; !ok {
		t.Error("DNS 段未注入")
	}
	rt, ok := doc["route"].(map[string]any)
	if !ok {
		t.Fatal("route 段未注入")
	}
	if rules, _ := rt["rules"].([]any); len(rules) != 1 {
		t.Errorf("路由规则数量错：%#v", rt["rules"])
	}
	var hasBlock bool
	for _, ob := range doc["outbounds"].([]any) {
		if ob.(map[string]any)["tag"] == "block" {
			hasBlock = true
		}
	}
	if !hasBlock {
		t.Errorf("action=reject 应带出 block 出站：%#v", doc["outbounds"])
	}
}

// TestRenderRealityHandshakeFromServerNames 守住「只填了 SNI 的 Reality 入站」
// 切到 sing-box 的情况：曾经这里只读 handshake_server / server_name，于是渲染出
// handshake.server="" ——sing-box 的 check 不拦空串，面板显示已启用，客户端却
// 永远握不上手。现在与 xray 渲染器共用 core.RealityDestParts，回退到 SNI。
func TestRenderRealityHandshakeFromServerNames(t *testing.T) {
	s := &SingBox{}
	in := &model.Inbound{
		Name: "t-reality", Core: "singbox", Protocol: "vless",
		Port: 443, Transport: "tcp", TLS: "reality",
		Settings: `{"private_key":"PRIV","server_names":["www.apple.com","apple.com"],"short_ids":["ab12"]}`,
	}
	obj, err := s.renderInbound(core.InboundWithClients{Inbound: in,
		Clients: []*model.Client{{Name: "u", Credential: "uuid-1", Enabled: true}}})
	if err != nil {
		t.Fatalf("renderInbound: %v", err)
	}
	tls := obj["tls"].(map[string]any)
	reality := tls["reality"].(map[string]any)
	hs := reality["handshake"].(map[string]any)
	if hs["server"] != "www.apple.com" {
		t.Errorf("handshake.server = %#v，期望回退到 SNI 第一个", hs["server"])
	}
	if hs["server_port"] != 443 {
		t.Errorf("handshake.server_port = %#v，期望 443", hs["server_port"])
	}
}

// TestRenderRealityNativeDest sing-box 也要认 xray 原生 dest 键。
func TestRenderRealityNativeDest(t *testing.T) {
	s := &SingBox{}
	in := &model.Inbound{
		Name: "t-dest", Core: "singbox", Protocol: "vless",
		Port: 443, Transport: "tcp", TLS: "reality",
		Settings: `{"private_key":"PRIV","dest":"www.apple.com:2053"}`,
	}
	obj, err := s.renderInbound(core.InboundWithClients{Inbound: in})
	if err != nil {
		t.Fatalf("renderInbound: %v", err)
	}
	hs := obj["tls"].(map[string]any)["reality"].(map[string]any)["handshake"].(map[string]any)
	if hs["server"] != "www.apple.com" || hs["server_port"] != 2053 {
		t.Errorf("handshake = %#v，期望 www.apple.com:2053", hs)
	}
	// dest 不能透传到入站顶层：sing-box 严格校验，会直接拒收
	if _, leaked := obj["dest"]; leaked {
		t.Error("settings 里的 dest 被透传到入站顶层")
	}
}

// TestRenderRealityMissingHandshake 握手目标完全缺失时要报可读错误。
func TestRenderRealityMissingHandshake(t *testing.T) {
	s := &SingBox{}
	in := &model.Inbound{
		Name: "t-nohs", Core: "singbox", Protocol: "vless",
		Port: 443, Transport: "tcp", TLS: "reality",
		Settings: `{"private_key":"PRIV"}`,
	}
	_, err := s.renderInbound(core.InboundWithClients{Inbound: in})
	if err == nil {
		t.Fatal("缺握手目标应报错")
	}
	if !strings.Contains(err.Error(), "握手目标") {
		t.Errorf("错误信息应说明缺握手目标，实际: %v", err)
	}
}

// TestRenderRealityWritesServerName 是 v1.1.10 的核心回归。
//
// sing-box 的 REALITY 服务端**必须**有 tls.server_name：缺了它内核照样启动、
// 端口照样监听、`sing-box check` 也过，但每个客户端都在握手阶段被拒
// （服务端 `REALITY: processed invalid connection`，客户端收到回落目标的真证书）。
//
// 实测（sing-box 1.14.4，同一对密钥 / 同一个 short_id / 同一个握手目标）：
//
//	不加 server_name → 握手失败
//	加上 server_name → 立刻 204 通
//
// xray 的 REALITY 没有这个字段（它用 serverNames 列表），照着 xray 的字段写
// 就会漏掉它——所以这条断言必须钉死。
func TestRenderRealityWritesServerName(t *testing.T) {
	sb := &SingBox{}

	// 1) server_names 优先（与 xray 的 serverNames 第一个对齐）
	in := &model.Inbound{
		Name: "r1", Core: "singbox", Protocol: "vless",
		Port: 24461, Transport: "tcp", TLS: "reality",
		Settings: `{"private_key":"PRIV","handshake_server":"www.apple.com",
			"server_name":"old.example.com","server_names":"www.apple.com,apple.com"}`,
	}
	obj, err := sb.renderInbound(core.InboundWithClients{Inbound: in, Clients: []*model.Client{
		{Name: "u", Credential: "uuid", Enabled: true},
	}})
	if err != nil {
		t.Fatalf("renderInbound: %v", err)
	}
	tls := obj["tls"].(map[string]any)
	if got, _ := tls["server_name"].(string); got != "www.apple.com" {
		t.Errorf("tls.server_name 应取 server_names 第一个 www.apple.com，实际 %q", got)
	}

	// 2) 只有 server_name 时用它
	in2 := &model.Inbound{
		Name: "r2", Core: "singbox", Protocol: "vless",
		Port: 24462, Transport: "tcp", TLS: "reality",
		Settings: `{"private_key":"PRIV","handshake_server":"www.amd.com","server_name":"www.amd.com"}`,
	}
	obj2, err := sb.renderInbound(core.InboundWithClients{Inbound: in2, Clients: []*model.Client{
		{Name: "u", Credential: "uuid", Enabled: true},
	}})
	if err != nil {
		t.Fatalf("renderInbound: %v", err)
	}
	if got, _ := obj2["tls"].(map[string]any)["server_name"].(string); got != "www.amd.com" {
		t.Errorf("tls.server_name 应为 www.amd.com，实际 %q", got)
	}

	// 3) 都没填时退回握手目标主机名——不能留空，留空等于全拒
	in3 := &model.Inbound{
		Name: "r3", Core: "singbox", Protocol: "vless",
		Port: 24463, Transport: "tcp", TLS: "reality",
		Settings: `{"private_key":"PRIV","handshake_server":"www.bing.com:8443"}`,
	}
	obj3, err := sb.renderInbound(core.InboundWithClients{Inbound: in3, Clients: []*model.Client{
		{Name: "u", Credential: "uuid", Enabled: true},
	}})
	if err != nil {
		t.Fatalf("renderInbound: %v", err)
	}
	if got, _ := obj3["tls"].(map[string]any)["server_name"].(string); got != "www.bing.com" {
		t.Errorf("没填 SNI 时应退回握手目标主机名 www.bing.com，实际 %q（空串会让服务端拒绝所有客户端）", got)
	}
}

// 从 3x-ui 导入的入站，settings 里带着 xray 的入站顶层字段 sniffing / allocate。
//
// 这两个键由 xray 渲染器原样透传（不能列进托管键，否则 xray 侧会丢掉协议嗅探），
// 但 sing-box 对未知字段是严格校验的：
//
//	FATAL decode config: inbounds[0].sniffing: json: unknown field "sniffing"
//
// 于是「3x-ui 导入 → 切到 sing-box 内核」这条最常见的路径必然失败，而报错只说
// "校验失败"，既看不出是哪个字段，也看不出它是 xray 专属的。
func TestRenderInboundDropsXrayOnlyFields(t *testing.T) {
	sb := &SingBox{}
	in := &model.Inbound{
		Name: "from-3xui", Core: "singbox", Protocol: "vless",
		Port: 24464, Transport: "tcp", TLS: "reality",
		Settings: `{"private_key":"PRIV","handshake_server":"www.apple.com",` +
			`"short_ids":["aabbccdd"],` +
			`"sniffing":{"enabled":true,"destOverride":["http","tls"]},` +
			`"allocate":{"strategy":"always"}}`,
	}
	obj, err := sb.renderInbound(core.InboundWithClients{Inbound: in, Clients: []*model.Client{
		{Name: "u", Credential: "uuid", Enabled: true},
	}})
	if err != nil {
		t.Fatalf("renderInbound: %v", err)
	}
	for _, k := range []string{"sniffing", "allocate"} {
		if _, ok := obj[k]; ok {
			t.Errorf("xray 专属字段 %q 透传到了 sing-box 入站顶层（strict 校验会拒收整份配置）", k)
		}
	}
	// 正常字段不能跟着被删
	if obj["type"] != "vless" || obj["listen_port"] != 24464 {
		t.Errorf("基础字段被误删: %v", obj)
	}
}

// hysteria2 / tuic / anytls 在 sing-box 中强制 TLS，且证书与私钥必须成对。
// 缺失时必须在渲染期给出指得到表单字段的中文错误，而不是等应用时
// 抛一句内核英文日志（`TLS required` / `missing key`）。
func TestHysteria2PreflightTLS(t *testing.T) {
	s := &SingBox{}
	clients := []*model.Client{{Name: "u1", Credential: "p1", Enabled: true}}
	cases := []struct {
		name, tls, settings, want string
	}{
		{"no-tls", "none", `{}`, "必须使用 TLS"},
		{"reality", "reality", `{"private_key":"x","handshake_server":"a.com"}`, "必须使用 TLS"},
		{"no-cert", "tls", `{}`, "必须配置 TLS 证书"},
		{"no-key", "tls", `{"certificate_path":"/tmp/c.pem"}`, "key_path"},
	}
	for _, c := range cases {
		in := &model.Inbound{Name: "hy2", Core: "singbox", Protocol: "hysteria2",
			Port: 443, Transport: "udp", TLS: c.tls, Settings: c.settings}
		_, err := s.renderInbound(core.InboundWithClients{Inbound: in, Clients: clients})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err=%v，应包含 %q", c.name, err, c.want)
		}
	}

	// 证书 + 私钥齐全时必须正常渲染出 tls 段
	ok := &model.Inbound{Name: "hy2", Core: "singbox", Protocol: "hysteria2",
		Port: 443, Transport: "udp", TLS: "tls",
		Settings: `{"certificate_path":"/tmp/c.pem","key_path":"/tmp/k.pem"}`}
	obj, err := s.renderInbound(core.InboundWithClients{Inbound: ok, Clients: clients})
	if err != nil {
		t.Fatalf("合法 hysteria2 不应报错: %v", err)
	}
	tls, _ := obj["tls"].(map[string]any)
	if tls["certificate_path"] != "/tmp/c.pem" || tls["key_path"] != "/tmp/k.pem" {
		t.Errorf("tls 段不完整: %v", tls)
	}
}
