package link

import "testing"

// REALITY 节点的导出必须带齐四件套：security=reality / sni / pbk / sid，
// 外加 fp（uTLS 指纹）。
//
// 这几项缺任何一个，客户端和服务端就握不上手，而失败现象完全一样
// （超时或连接重置），在客户端侧根本看不出是哪一项的问题。曾经的实现
// 只输出 security/pbk/flow，于是「面板上填了短 ID 和 SNI，导出的节点里
// 两者都是空的」——服务端 shortIds=["ab12"] 只认 ab12，客户端发空 short-id，
// REALITY 必然失败。
func TestGenLinkRealityCarriesClientParams(t *testing.T) {
	n := &Node{
		Protocol: "vless", Name: "hk-01", Host: "1.2.3.4", Port: 443,
		Cred: "8f2b1c3d-1111-2222-3333-444455556666", Security: "reality",
		SNI: "www.apple.com", Network: "tcp",
		Extra: map[string]any{"pbk": "PBK123", "sid": "ab12cd34", "flow": "xtls-rprx-vision"},
	}
	l := GenLink(n)
	for _, want := range []string{
		"security=reality", "sni=www.apple.com", "pbk=PBK123",
		"sid=ab12cd34", "fp=chrome", "encryption=none", "flow=xtls-rprx-vision",
	} {
		if !contains(l, want) {
			t.Errorf("链接缺少 %q\n实际: %s", want, l)
		}
	}
}

// 显式指定了指纹（如 firefox）时不能被默认的 chrome 覆盖。
func TestGenLinkRealityKeepsExplicitFingerprint(t *testing.T) {
	n := &Node{
		Protocol: "vless", Host: "1.2.3.4", Port: 443, Cred: "u",
		Security: "reality", SNI: "a.example.com", Network: "tcp",
		Extra: map[string]any{"pbk": "PBK", "sid": "ab", "fp": "firefox"},
	}
	if l := GenLink(n); !contains(l, "fp=firefox") {
		t.Fatalf("应保留显式指纹，实际: %s", l)
	}
}

// mihomo 侧：REALITY 必须写 client-fingerprint，否则它用普通 Go TLS 发
// ClientHello，服务端判定不是浏览器、直接拒绝。3x-ui 生成的客户端配置里
// 永远带着这一项，这就是「同一条节点 3x-ui 通、面板不通」的直接原因。
func TestToClashRealityCarriesClientFingerprint(t *testing.T) {
	n := &Node{
		Protocol: "vless", Name: "hk-01", Host: "1.2.3.4", Port: 443,
		Cred: "uuid-1", Security: "reality", SNI: "www.apple.com", Network: "tcp",
		Extra: map[string]any{"pbk": "PBK123", "sid": "ab12cd34", "flow": "xtls-rprx-vision"},
	}
	p, err := ToClash(n)
	if err != nil {
		t.Fatal(err)
	}
	if p["client-fingerprint"] != "chrome" {
		t.Errorf("缺少 client-fingerprint: %v", p["client-fingerprint"])
	}
	if p["servername"] != "www.apple.com" {
		t.Errorf("缺少 servername: %v", p["servername"])
	}
	if p["flow"] != "xtls-rprx-vision" {
		t.Errorf("缺少 flow: %v", p["flow"])
	}
	ro, ok := p["reality-opts"].(map[string]any)
	if !ok {
		t.Fatalf("缺少 reality-opts: %v", p)
	}
	if ro["public-key"] != "PBK123" || ro["short-id"] != "ab12cd34" {
		t.Errorf("reality-opts 不完整: %v", ro)
	}
}

// ws 前置 CDN 时，Host 头必须原样带给客户端，否则握手带的是 IP，
// CDN 找不到对应站点、直接回 404。
func TestToClashWSCarriesHostHeader(t *testing.T) {
	n := &Node{
		Protocol: "vless", Name: "ws-01", Host: "1.2.3.4", Port: 80, Cred: "uuid-1",
		Security: "none", Network: "ws", Path: "/ray",
		Extra: map[string]any{"host": "cdn.example.com"},
	}
	p, err := ToClash(n)
	if err != nil {
		t.Fatal(err)
	}
	ws, ok := p["ws-opts"].(map[string]any)
	if !ok {
		t.Fatalf("缺少 ws-opts: %v", p)
	}
	if ws["path"] != "/ray" {
		t.Errorf("path 丢了: %v", ws["path"])
	}
	hs, _ := ws["headers"].(map[string]any)
	if hs["Host"] != "cdn.example.com" {
		t.Errorf("ws Host 头丢了: %v", ws)
	}
}
