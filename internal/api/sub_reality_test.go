package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"triune/internal/link"
	"triune/internal/model"

	"gopkg.in/yaml.v3"
)

// 订阅导出必须把 REALITY 的客户端参数补齐。
//
// 现场表现：面板上建好 vless+reality 入站，用 mihomo 连不通；同一台机器上
// 3x-ui 的节点却通。差别全在导出这一侧——3x-ui 的链接长这样：
//
//	vless://uuid@ip:443?encryption=none&flow=xtls-rprx-vision&security=reality
//	  &sni=www.apple.com&fp=chrome&pbk=...&sid=ab12cd34&type=tcp
//
// 而面板以前只导出 security/pbk/flow：sni 与 sid 都是空的（因为表单存的是
// server_names / short_ids，导出却只读 server_name / sid），clash 里也没有
// client-fingerprint。三项缺一个就握不上手，且失败现象一模一样（超时/重置）。
//
// 本用例走完整 HTTP 路径：建入站（带一个用户）→ 取订阅 → 分别看 base64 链接
// 与 clash YAML；settings 里**故意不放 pbk**，验证公钥由私钥推导。
func TestRealitySubscriptionCarriesSidSniFingerprint(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	priv, pub, err := newRealityKeypair()
	if err != nil {
		t.Fatal(err)
	}
	settings, _ := json.Marshal(map[string]any{
		"handshake_server": "www.apple.com",
		"server_names":     "www.apple.com",
		"short_ids":        []string{"ab12cd34"},
		"private_key":      priv,
		"flow":             "xtls-rprx-vision",
	})
	body, _ := json.Marshal(map[string]any{
		"name": "hk-01-reality", "core": "xray", "protocol": "vless",
		"listen": "127.0.0.1", "port": 24443, "tag": "hk-01-reality",
		"transport": "tcp", "tls": "reality", "enabled": true, "draft": false,
		"settings": string(settings),
		"clients":  []map[string]any{{"name": "u1"}},
	})
	mkInbound(t, h, tok, string(body))

	clients, err := a.Store.ListClients()
	if err != nil || len(clients) != 1 {
		t.Fatalf("应有 1 个用户，实际 %d（%v）", len(clients), err)
	}
	sub := "/sub/" + clients[0].SubToken

	// 1) base64 链接列表
	w := apiReq(t, h, tok, "GET", sub, "")
	if w.Code != 200 {
		t.Fatalf("订阅应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(w.Body.String()))
	if err != nil {
		t.Fatalf("订阅不是 base64: %v", err)
	}
	linkText := string(raw)
	for _, want := range []string{
		"security=reality",
		"sni=www.apple.com", // 来自 server_names，以前是空的
		"sid=ab12cd34",      // 来自 short_ids，以前是空的
		"fp=chrome",         // REALITY 必须带指纹
		"pbk=" + pub,        // settings 里没有 pbk，应由 private_key 推导
		"encryption=none",   // vless 标准参数
		"flow=xtls-rprx-vision",
	} {
		if !strings.Contains(linkText, want) {
			t.Errorf("链接缺少 %q\n实际: %s", want, linkText)
		}
	}

	// 2) clash / mihomo YAML
	w = apiReq(t, h, tok, "GET", sub+"?target=clash", "")
	if w.Code != 200 {
		t.Fatalf("clash 订阅应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var doc struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("YAML 解析失败: %v", err)
	}
	if len(doc.Proxies) != 1 {
		t.Fatalf("应有 1 个节点，实际 %d", len(doc.Proxies))
	}
	p := doc.Proxies[0]
	if p["client-fingerprint"] != "chrome" {
		t.Errorf("缺少 client-fingerprint（mihomo 用 REALITY 必需）: %v", p["client-fingerprint"])
	}
	if p["servername"] != "www.apple.com" {
		t.Errorf("缺少 servername: %v", p["servername"])
	}
	ro, _ := p["reality-opts"].(map[string]any)
	if ro["short-id"] != "ab12cd34" {
		t.Errorf("short-id 应为 ab12cd34，实际 %v", ro["short-id"])
	}
	if ro["public-key"] != pub {
		t.Errorf("public-key 应由私钥推导出 %s，实际 %v", pub, ro["public-key"])
	}
}

// 没填短 ID 时：服务端 shortIds 是 [""]（空短 ID 合法），客户端也必须是空串，
// 不能凭空造一个。同时验证 SNI 与公钥的兜底链条。
func TestRealitySubscriptionEmptyShortIDStaysEmpty(t *testing.T) {
	a := newAuthTestServer(t)
	priv, _, err := newRealityKeypair()
	if err != nil {
		t.Fatal(err)
	}
	in := &model.Inbound{
		Name: "r-01", Protocol: "vless", Port: 443, Transport: "tcp", TLS: "reality",
		Settings: `{"handshake_server":"www.apple.com","server_names":"www.apple.com","private_key":"` + priv + `"}`,
	}
	node := a.inboundToNode(in, &model.Client{Name: "u1", Credential: "uuid-1"}, "1.2.3.4")
	if sid, ok := node.Extra["sid"].(string); ok && sid != "" {
		t.Fatalf("没有 short_ids 时 sid 应为空，实际 %q", sid)
	}
	if node.SNI != "www.apple.com" {
		t.Fatalf("SNI 应回退到 server_names，实际 %q", node.SNI)
	}
	if pbk, _ := node.Extra["pbk"].(string); pbk == "" {
		t.Fatal("公钥应由 private_key 推导出来")
	}
}

// settings 里的 pbk 可能是上一代密钥的残留（换密钥没同步、从 3x-ui 导入时带了
// 别的公钥、先填 pbk 后改私钥）。导出一律以 private_key 为准：
//
//	X25519 的公钥由私钥唯一确定，采信存下来的 pbk 只可能出错。
//
// 现场表现和「短 ID 没带上」完全一样：客户端报 REALITY authentication failed，
// 服务端日志一切正常，SNI 和短 ID 看着都对，只有公钥是错的。
func TestRealityStalePbkIsOverriddenByPrivateKey(t *testing.T) {
	a := newAuthTestServer(t)
	priv, pub, err := newRealityKeypair()
	if err != nil {
		t.Fatal(err)
	}
	// 另造一对，把它的公钥当成"旧 pbk"塞进 settings
	_, stalePub, err := newRealityKeypair()
	if err != nil {
		t.Fatal(err)
	}
	if stalePub == pub {
		t.Fatal("两对密钥的公钥不应相同，随机源有问题")
	}
	in := &model.Inbound{
		Name: "r-stale", Protocol: "vless", Port: 443, Transport: "tcp", TLS: "reality",
		Settings: `{"handshake_server":"www.apple.com","server_names":"www.apple.com",` +
			`"short_ids":["ab12cd34"],"private_key":"` + priv + `","pbk":"` + stalePub + `"}`,
	}
	node := a.inboundToNode(in, &model.Client{Name: "u1", Credential: "uuid-1"}, "1.2.3.4")
	if got, _ := node.Extra["pbk"].(string); got != pub {
		t.Fatalf("公钥应由 private_key 推导为 %s，实际 %s（旧 pbk 被采信了）", pub, got)
	}
	// 端到端再确认一次：clash 节点里的 public-key 也必须是推导出来的那个
	p, err := link.ToClash(node)
	if err != nil {
		t.Fatal(err)
	}
	ro, _ := p["reality-opts"].(map[string]any)
	if ro["public-key"] != pub {
		t.Fatalf("clash public-key = %v，期望 %s", ro["public-key"], pub)
	}
}

// subWithUA 带指定 User-Agent 拉订阅。
func subWithUA(t *testing.T, h http.Handler, path, ua string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("User-Agent", ua)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// 订阅端点必须按客户端类型给格式。
//
// 面板界面上给出的订阅地址就是不带参数的 /sub/<token>。mihomo 只吃 YAML，
// 拿到 base64 链接列表会「订阅拉取失败」——用户看到的是「节点不通」，
// 其实是格式不对。猜 UA 是各家订阅服务的通行做法。
func TestSubFormatFollowsUserAgent(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	body := `{"name":"sb-01","core":"singbox","protocol":"vless","listen":"127.0.0.1",
		"port":24444,"tag":"sb-01","transport":"tcp","tls":"none","settings":"{}",
		"enabled":true,"draft":false,"clients":[{"name":"u1"}]}`
	mkInbound(t, h, tok, body)
	clients, err := a.Store.ListClients()
	if err != nil || len(clients) != 1 {
		t.Fatalf("应有 1 个用户，实际 %d（%v）", len(clients), err)
	}
	sub := "/sub/" + clients[0].SubToken

	cases := []struct {
		ua   string
		want string // yaml / json / b64
	}{
		{"clash-verge/v2.0.3", "yaml"},
		{"mihomo/1.19.0", "yaml"},
		{"FlClash/0.8.80", "yaml"},
		{"sing-box 1.9.7", "json"},
		{"v2rayN/6.42", "b64"},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Safari/605.1.15", "b64"},
	}
	for _, c := range cases {
		w := subWithUA(t, h, sub, c.ua)
		if w.Code != 200 {
			t.Errorf("%s: 应 200，实际 %d: %s", c.ua, w.Code, w.Body.String())
			continue
		}
		got := classifySub(t, w.Body.String())
		if got != c.want {
			t.Errorf("%s: 期望 %s，实际 %s", c.ua, c.want, got)
		}
	}

	// 显式 target 优先级高于 UA
	if got := classifySub(t, subWithUA(t, h, sub+"?target=clash", "v2rayN/6.42").Body.String()); got != "yaml" {
		t.Errorf("显式 target=clash 应压过 UA，实际 %s", got)
	}
}

// classifySub 判断响应体是 YAML / JSON / base64 链接列表。
func classifySub(t *testing.T, s string) string {
	t.Helper()
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "{") {
		return "json"
	}
	if strings.Contains(s, "proxies:") {
		return "yaml"
	}
	if _, err := base64.StdEncoding.DecodeString(s); err == nil {
		return "b64"
	}
	return "unknown"
}
