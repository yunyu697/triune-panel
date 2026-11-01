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
