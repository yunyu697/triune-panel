package xray

import (
	"encoding/json"
	"strings"
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

// socks / http 入站：xray 用 accounts（不是 clients），auth 必须显式给。
func TestRenderSocksHTTPAccounts(t *testing.T) {
	x := &Xray{}
	cases := []struct {
		proto    string
		settings string
		wantAuth string
	}{
		{"socks", `{"account_auth":"password","udp":true}`, "password"},
		{"http", `{}`, "password"},
		{"socks", `{"account_auth":"noauth"}`, "noauth"},
	}
	for _, c := range cases {
		in := &model.Inbound{
			Name: "p", Core: "xray", Protocol: c.proto, Port: 1080,
			Transport: "tcp", TLS: "none", Settings: c.settings,
		}
		obj, err := x.renderInbound(core.InboundWithClients{Inbound: in, Clients: []*model.Client{
			{Name: "u1", Credential: "u1:p1", Enabled: true},
		}})
		if err != nil {
			t.Fatalf("%s renderInbound: %v", c.proto, err)
		}
		s := obj["settings"].(map[string]any)
		if s["auth"] != c.wantAuth {
			t.Errorf("%s auth = %v，期望 %s", c.proto, s["auth"], c.wantAuth)
		}
		if c.wantAuth == "password" {
			accs, _ := s["accounts"].([]any)
			if len(accs) != 1 {
				t.Fatalf("%s accounts 数量错：%#v", c.proto, s["accounts"])
			}
			a := accs[0].(map[string]any)
			if a["user"] != "u1" || a["pass"] != "p1" {
				t.Errorf("%s 账号应拆成 u1/p1：%#v", c.proto, a)
			}
		}
		if c.proto == "socks" && c.wantAuth == "password" && s["udp"] != true {
			t.Errorf("socks 应保留 udp 开关：%#v", s)
		}
	}
}

// dokodemo-door：端口转发参数落到 settings，缺地址时给出可读错误。
func TestRenderDokodemoDoor(t *testing.T) {
	x := &Xray{}
	in := &model.Inbound{
		Name: "pf", Core: "xray", Protocol: "dokodemo-door", Port: 9000,
		Transport: "tcp", TLS: "none",
		Settings: `{"dd_address":"1.1.1.1","dd_port":80,"dd_network":"tcp,udp","dd_follow_redirect":true}`,
	}
	obj, err := x.renderInbound(core.InboundWithClients{Inbound: in})
	if err != nil {
		t.Fatalf("renderInbound: %v", err)
	}
	s := obj["settings"].(map[string]any)
	if s["address"] != "1.1.1.1" || s["port"] != 80 || s["followRedirect"] != true {
		t.Errorf("端口转发参数错：%#v", s)
	}
	bad := &model.Inbound{Name: "pf", Core: "xray", Protocol: "dokodemo-door", Port: 9000, Settings: `{}`}
	if _, err := x.renderInbound(core.InboundWithClients{Inbound: bad}); err == nil {
		t.Error("缺目标地址应报错")
	}
}

// 新增传输：h2 / httpupgrade / xhttp / kcp / quic 的字段位置必须正确。
func TestRenderNewTransports(t *testing.T) {
	x := &Xray{}
	cases := []struct {
		name      string
		transport string
		settings  string
		network   string
		key       string
		check     func(t *testing.T, m map[string]any)
	}{
		{"httpupgrade", "httpupgrade", `{"path":"/hu","ws_host":"hu.example.com"}`, "httpupgrade", "httpupgradeSettings", func(t *testing.T, m map[string]any) {
			if m["path"] != "/hu" || m["host"] != "hu.example.com" {
				t.Errorf("httpupgrade 字段错：%#v", m)
			}
		}},
		{"xhttp", "xhttp", `{"path":"/xh","ws_host":"x.example.com","xhttp_mode":"packet-up"}`, "xhttp", "xhttpSettings", func(t *testing.T, m map[string]any) {
			if m["mode"] != "packet-up" {
				t.Errorf("xhttp mode 错：%#v", m)
			}
		}},
		{"kcp", "kcp", `{"kcp_seed":"s3","kcp_header":"srtp"}`, "kcp", "kcpSettings", func(t *testing.T, m map[string]any) {
			if m["seed"] != "s3" {
				t.Errorf("kcp seed 错：%#v", m)
			}
			if h, _ := m["header"].(map[string]any); h["type"] != "srtp" {
				t.Errorf("kcp header 错：%#v", m["header"])
			}
		}},
	}
	for _, c := range cases {
		in := &model.Inbound{
			Name: c.name, Core: "xray", Protocol: "vmess", Port: 443,
			Transport: c.transport, TLS: "none", Settings: c.settings,
		}
		obj, err := x.renderInbound(core.InboundWithClients{Inbound: in, Clients: []*model.Client{
			{Name: "u", Credential: "uuid", Enabled: true},
		}})
		if err != nil {
			t.Fatalf("%s renderInbound: %v", c.name, err)
		}
		st := obj["streamSettings"].(map[string]any)
		if st["network"] != c.network {
			t.Errorf("%s network = %v，期望 %s", c.name, st["network"], c.network)
		}
		m, ok := st[c.key].(map[string]any)
		if !ok {
			t.Fatalf("%s 缺 %s：%#v", c.name, c.key, st)
		}
		c.check(t, m)
	}
}

// 未知传输要直接报错，而不是悄悄渲染成 tcp（客户端会连不上）。
func TestRenderUnknownTransportErrors(t *testing.T) {
	x := &Xray{}
	in := &model.Inbound{Name: "x", Core: "xray", Protocol: "vmess", Port: 443, Transport: "weird"}
	if _, err := x.renderInbound(core.InboundWithClients{Inbound: in}); err == nil {
		t.Error("未知传输应报错")
	}
}

// Xray 26 已移除 h2 / 裸 quic 传输：必须给出「改成 xhttp」的可执行提示，
// 而不是渲染出一份启动就 FATAL 的配置。
func TestRenderRemovedTransportsExplain(t *testing.T) {
	x := &Xray{}
	for _, tr := range []string{"h2", "quic"} {
		in := &model.Inbound{Name: "x", Core: "xray", Protocol: "vmess", Port: 443, Transport: tr}
		_, err := x.renderInbound(core.InboundWithClients{Inbound: in})
		if err == nil {
			t.Fatalf("%s 应报错（Xray 26 已移除）", tr)
		}
		if !strings.Contains(err.Error(), "xhttp") {
			t.Errorf("%s 的报错应指出改用 xhttp：%v", tr, err)
		}
	}
}

// 导入的 DNS / 路由：DNS 原样带走，路由追加在统计 API 规则之后，
// 出现 block 规则时自动补 blackhole 出站（否则校验不过）。
func TestRenderGlobalDNSRouting(t *testing.T) {
	x := &Xray{}
	in := &model.Inbound{Name: "v", Core: "xray", Protocol: "vmess", Port: 443, Transport: "tcp"}
	out, err := x.Render([]core.InboundWithClients{{Inbound: in, Clients: []*model.Client{
		{Name: "u", Credential: "uuid", Enabled: true},
	}}}, core.RenderOpts{
		StatsEnabled: true,
		DNSJSON:      `{"servers":["1.1.1.1"],"queryStrategy":"UseIP"}`,
		RouteJSON:    `{"domainStrategy":"IPIfNonMatch","rules":[{"type":"field","ip":["geoip:private"],"outboundTag":"block"}]}`,
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if dns, _ := doc["dns"].(map[string]any); dns["queryStrategy"] != "UseIP" {
		t.Errorf("DNS 段未注入：%#v", doc["dns"])
	}
	rt := doc["routing"].(map[string]any)
	if rt["domainStrategy"] != "IPIfNonMatch" {
		t.Errorf("路由顶层字段未合并：%#v", rt)
	}
	rules, _ := rt["rules"].([]any)
	if len(rules) != 2 {
		t.Fatalf("应是 API 规则 + 导入规则两条：%#v", rules)
	}
	if rules[0].(map[string]any)["outboundTag"] != "api" {
		t.Errorf("API 规则必须在最前：%#v", rules[0])
	}
	var hasBlock bool
	for _, ob := range doc["outbounds"].([]any) {
		if ob.(map[string]any)["tag"] == "block" {
			hasBlock = true
		}
	}
	if !hasBlock {
		t.Errorf("block 规则应带出 blackhole 出站：%#v", doc["outbounds"])
	}
}
