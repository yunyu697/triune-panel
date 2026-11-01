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

// xray 的 realitySettings.dest 必须是 host:port；
// 只写裸域名会被判为非法 target（切换内核预检失败的根因）。
func TestRenderRealityDestHasPort(t *testing.T) {
	x := &Xray{}
	in := &model.Inbound{Name: "r", Core: "xray", Protocol: "vless", Port: 443,
		Transport: "tcp", TLS: "reality",
		Settings: `{"private_key":"PRIV","handshake_server":"www.apple.com","server_names":"www.apple.com"}`}
	obj, err := x.renderInbound(core.InboundWithClients{Inbound: in})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	stream := obj["streamSettings"].(map[string]any)
	rs := stream["realitySettings"].(map[string]any)
	if rs["dest"] != "www.apple.com:443" {
		t.Errorf("dest 应补全端口，得到 %v", rs["dest"])
	}
	if _, ok := rs["shortIds"]; !ok {
		t.Error("shortIds 必须存在（空数组元素也合法）")
	}
}

// 只给 handshake_server 时 serverNames 应回退为主机名，不能是空数组。
func TestRenderRealityServerNamesFallback(t *testing.T) {
	x := &Xray{}
	in := &model.Inbound{Name: "r", Core: "xray", Protocol: "vless", Port: 443,
		Transport: "tcp", TLS: "reality",
		Settings: `{"private_key":"PRIV","handshake_server":"www.cloudflare.com:8443"}`}
	obj, _ := x.renderInbound(core.InboundWithClients{Inbound: in})
	rs := obj["streamSettings"].(map[string]any)["realitySettings"].(map[string]any)
	names, _ := rs["serverNames"].([]string)
	if len(names) != 1 || names[0] != "www.cloudflare.com" {
		t.Errorf("serverNames 回退失败: %v", rs["serverNames"])
	}
	if rs["dest"] != "www.cloudflare.com:8443" {
		t.Errorf("已带端口的 dest 不应被改写: %v", rs["dest"])
	}
}

// xray 的 shadowsocks 服务端：密码在 settings 内（入站级单值）。
func TestRenderShadowsocks(t *testing.T) {
	x := &Xray{}
	in := &model.Inbound{Name: "ss", Core: "xray", Protocol: "shadowsocks", Port: 8388,
		Transport: "tcp", Settings: `{"method":"aes-256-gcm"}`}
	obj, err := x.renderInbound(core.InboundWithClients{Inbound: in,
		Clients: []*model.Client{{Name: "u1", Credential: "p1", Enabled: true}}})
	if err != nil {
		t.Fatalf("shadowsocks render: %v", err)
	}
	if obj["protocol"] != "shadowsocks" {
		t.Errorf("protocol = %v", obj["protocol"])
	}
	st := obj["settings"].(map[string]any)
	if st["method"] != "aes-256-gcm" || st["password"] != "p1" {
		t.Errorf("settings = %v", st)
	}
	if _, ok := obj["method"]; ok {
		t.Error("method 不应残留在入站顶层")
	}
}
