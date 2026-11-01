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

// 导入的 3x-ui 配置带多 shortIds、多 serverNames、自定义握手端口、alpn、
// spiderX、maxTimeDiff、fallbacks：这些都要如实落进 xray 的正确位置。
func TestRenderImportedRealityMultiValues(t *testing.T) {
	x := &Xray{}
	in := &model.Inbound{
		Name: "imported", Core: "xray", Protocol: "vless",
		Port: 24443, Transport: "tcp", TLS: "reality",
		Settings: `{"private_key":"PRIV","handshake_server":"www.apple.com","handshake_port":8443,
			"server_names":"www.apple.com,apple.com","short_ids":["ab12",""],"spider_x":"/x",
			"max_time_diff":60000,"flow":"xtls-rprx-vision","fallbacks":[{"dest":80,"alpn":""}]}`,
	}
	obj, err := x.renderInbound(core.InboundWithClients{Inbound: in, Clients: []*model.Client{
		{Name: "u1", Credential: "uuid-1", Enabled: true},
	}})
	if err != nil {
		t.Fatalf("renderInbound: %v", err)
	}
	rs := obj["streamSettings"].(map[string]any)["realitySettings"].(map[string]any)
	if rs["dest"] != "www.apple.com:8443" {
		t.Errorf("dest 应用自定义端口: %v", rs["dest"])
	}
	names, _ := rs["serverNames"].([]string)
	if len(names) != 2 {
		t.Errorf("serverNames 应保留两个: %v", rs["serverNames"])
	}
	sids, _ := rs["shortIds"].([]string)
	if len(sids) != 2 || sids[0] != "ab12" || sids[1] != "" {
		t.Errorf("shortIds 应含空串: %#v", rs["shortIds"])
	}
	if rs["spiderX"] != "/x" || core.FieldInt(in.Settings, "max_time_diff") != 60000 {
		t.Errorf("spiderX/maxTimeDiff 丢: %v / %v", rs["spiderX"], rs["maxTimeDiff"])
	}
	vs := obj["settings"].(map[string]any)
	if fb, ok := vs["fallbacks"].([]any); !ok || len(fb) != 1 {
		t.Errorf("fallbacks 应透传: %#v", vs["fallbacks"])
	}
}

// TLS 的 alpn 与 ws 的 Host 头（CDN 前置场景）不能丢。
func TestRenderImportedTLSExtras(t *testing.T) {
	x := &Xray{}
	in := &model.Inbound{
		Name: "ws", Core: "xray", Protocol: "vmess",
		Port: 24444, Transport: "ws", TLS: "tls",
		Settings: `{"path":"/ray","ws_host":"cdn.example.com","server_name":"cdn.example.com",
			"alpn":"h2,http/1.1","certificate_path":"/c.pem","key_path":"/k.pem"}`,
	}
	obj, err := x.renderInbound(core.InboundWithClients{Inbound: in, Clients: []*model.Client{
		{Name: "u", Credential: "uuid", Enabled: true},
	}})
	if err != nil {
		t.Fatalf("renderInbound: %v", err)
	}
	st := obj["streamSettings"].(map[string]any)
	ts := st["tlsSettings"].(map[string]any)
	alpn, _ := ts["alpn"].([]string)
	if len(alpn) != 2 || alpn[0] != "h2" {
		t.Errorf("alpn 解析错: %#v", ts["alpn"])
	}
	ws := st["wsSettings"].(map[string]any)
	hdrs, _ := ws["headers"].(map[string]any)
	if hdrs["Host"] != "cdn.example.com" {
		t.Errorf("ws Host 头丢失: %#v", ws)
	}
}

// settings 里的 short_ids 也可能是逗号串（表单填写），要能等价处理。
func TestShortIDsAcceptsBothForms(t *testing.T) {
	for _, c := range []struct {
		settings string
		want     int
	}{
		{`{"short_ids":["a","b"]}`, 2},
		{`{"short_ids":"a, b"}`, 2},
		{`{"short_id":"only"}`, 1},
		{`{}`, 1}, // 没配时给 [""]，xray 要求 shortIds 至少是数组
	} {
		if got := core.ShortIDs(c.settings); len(got) != c.want {
			t.Errorf("ShortIDs(%s) = %#v，长度应为 %d", c.settings, got, c.want)
		}
	}
}
