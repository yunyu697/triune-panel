package singbox

import (
	"encoding/json"
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
func TestRenderSocksUsersNoNameField(t *testing.T) {
	s := &SingBox{}
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
