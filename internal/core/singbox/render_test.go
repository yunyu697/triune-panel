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
