package xray

import (
	"testing"

	"triune/internal/core"
	"triune/internal/model"
)

// 托管键不透传；reality 的 short_id 结构化为 shortIds 数组。
func TestRenderInboundRealityAndManagedKeys(t *testing.T) {
	x := &Xray{}
	in := &model.Inbound{
		Name: "t-reality", Core: "xray", Protocol: "vless",
		Port: 443, Transport: "tcp", TLS: "reality",
		Settings: `{"private_key":"PRIV","handshake_server":"www.apple.com","server_names":"www.apple.com","short_id":"ab","pbk":"CLIENT-ONLY","flow":"xtls-rprx-vision"}`,
	}
	iw := core.InboundWithClients{Inbound: in, Clients: []*model.Client{
		{Name: "u1", Credential: "uuid-1", Enabled: true},
	}}
	obj, err := x.renderInbound(iw)
	if err != nil {
		t.Fatalf("renderInbound: %v", err)
	}
	for _, k := range core.ManagedSettingsKeys {
		if _, ok := obj[k]; ok {
			t.Errorf("managed key %q leaked to top-level inbound object", k)
		}
	}
	stream, _ := obj["streamSettings"].(map[string]any)
	if stream == nil {
		t.Fatalf("streamSettings missing")
	}
	rs, _ := stream["realitySettings"].(map[string]any)
	if rs == nil {
		t.Fatalf("realitySettings missing: %v", stream)
	}
	if rs["privateKey"] != "PRIV" {
		t.Errorf("privateKey = %v", rs["privateKey"])
	}
	sids, ok := rs["shortIds"].([]string)
	if !ok || len(sids) != 1 || sids[0] != "ab" {
		t.Errorf("shortIds = %v", rs["shortIds"])
	}
	// flow 应写入 client
	settings, _ := obj["settings"].(map[string]any)
	clients, _ := settings["clients"].([]any)
	c0, _ := clients[0].(map[string]any)
	if c0["flow"] != "xtls-rprx-vision" {
		t.Errorf("client flow = %v", c0["flow"])
	}
}
