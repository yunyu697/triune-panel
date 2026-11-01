// Package livecheck 用**真实内核二进制**校验渲染结果。
//
// 单元测试只能证明「字段放对了位置」，证明不了「内核认这份配置」——
// 版本升级改名（h2→http、shortIds 类型、xhttp 字段）时，只有真机校验能发现。
//
// 二进制通过环境变量指定，未设置时整组跳过（CI 里没有内核也能跑）：
//
//	TRINE_XRAY_BIN=/path/to/xray TRINE_SINGBOX_BIN=/path/to/sing-box \
//	  go test ./internal/livecheck/ -v
package livecheck

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"triune/internal/core"
	"triune/internal/core/singbox"
	"triune/internal/core/xray"
	"triune/internal/importer"
	"triune/internal/model"
)

const (
	testCert = "/tmp/test-cert.pem"
	testKey  = "/tmp/test-key.pem"
)

type liveCase struct {
	name    string
	inbound *model.Inbound
	clients []*model.Client
	opts    core.RenderOpts
}

func client(name, cred string) *model.Client {
	return &model.Client{Name: name, Credential: cred, Enabled: true}
}

func TestXrayRendersValidConfig(t *testing.T) {
	bin := os.Getenv("TRINE_XRAY_BIN")
	if bin == "" {
		t.Skip("未设置 TRINE_XRAY_BIN，跳过真机校验")
	}
	if _, err := os.Stat(testCert); err != nil {
		t.Skip("缺少测试证书 /tmp/test-cert.pem")
	}
	wgKey, wgPub := wgKeypair(t)
	tls := `{"server_name":"example.com","certificate_path":"` + testCert + `","key_path":"` + testKey + `","alpn":"h2,http/1.1"}`

	cases := []liveCase{
		{
			name:    "vless-reality",
			inbound: &model.Inbound{Name: "r", Core: "xray", Protocol: "vless", Port: 24443, Transport: "tcp", TLS: "reality", Settings: `{"private_key":"` + realityPriv(t) + `","handshake_server":"www.apple.com","handshake_port":443,"server_names":"www.apple.com","short_ids":["ab12",""],"flow":"xtls-rprx-vision"}`},
			clients: []*model.Client{client("alice", "11111111-2222-3333-4444-555555555555")},
		},
		{
			name:    "vmess-ws-tls",
			inbound: &model.Inbound{Name: "w", Core: "xray", Protocol: "vmess", Port: 24444, Transport: "ws", TLS: "tls", Settings: `{"path":"/ray","ws_host":"example.com",` + strings.TrimPrefix(tls, "{")},
			clients: []*model.Client{client("bob", "99999999-8888-7777-6666-555555555555")},
		},
		{
			name:    "socks-password",
			inbound: &model.Inbound{Name: "s", Core: "xray", Protocol: "socks", Port: 24445, Transport: "tcp", TLS: "none", Settings: `{"account_auth":"password","udp":true}`},
			clients: []*model.Client{client("u1", "u1:p1")},
		},
		{
			name:    "http-proxy",
			inbound: &model.Inbound{Name: "h", Core: "xray", Protocol: "http", Port: 24446, Transport: "tcp", TLS: "none", Settings: `{"account_auth":"password"}`},
			clients: []*model.Client{client("h1", "h1:hp1")},
		},
		{
			name:    "dokodemo-door",
			inbound: &model.Inbound{Name: "d", Core: "xray", Protocol: "dokodemo-door", Port: 24447, Transport: "tcp", TLS: "none", Settings: `{"dd_address":"1.1.1.1","dd_port":80,"dd_network":"tcp"}`},
		},
		{
			name:    "wireguard",
			inbound: &model.Inbound{Name: "g", Core: "xray", Protocol: "wireguard", Port: 24448, Transport: "tcp", TLS: "none", Settings: `{"xray_raw_settings":{"secretKey":"` + wgKey + `","mtu":1420,"peers":[{"publicKey":"` + wgPub + `","allowedIPs":["10.66.0.2/32"]}]}}`},
		},
		{
			name:    "xhttp-stream-one-tls",
			inbound: &model.Inbound{Name: "h2", Core: "xray", Protocol: "vless", Port: 24449, Transport: "xhttp", TLS: "tls", Settings: `{"path":"/h2","ws_host":"example.com","xhttp_mode":"stream-one",` + strings.TrimPrefix(tls, "{")},
			clients: []*model.Client{client("c", "aaaaaaaa-bbbb-cccc-dddd-0000000000f3")},
		},
		{
			name:    "httpupgrade",
			inbound: &model.Inbound{Name: "hu", Core: "xray", Protocol: "vless", Port: 24450, Transport: "httpupgrade", TLS: "none", Settings: `{"path":"/hu","ws_host":"example.com"}`},
			clients: []*model.Client{client("c", "aaaaaaaa-bbbb-cccc-dddd-0000000000f2")},
		},
		{
			name:    "xhttp",
			inbound: &model.Inbound{Name: "xh", Core: "xray", Protocol: "vless", Port: 24451, Transport: "xhttp", TLS: "none", Settings: `{"path":"/xh","xhttp_mode":"packet-up"}`},
			clients: []*model.Client{client("c", "aaaaaaaa-bbbb-cccc-dddd-0000000000f1")},
		},
		{
			name:    "kcp",
			inbound: &model.Inbound{Name: "k", Core: "xray", Protocol: "vmess", Port: 24452, Transport: "kcp", TLS: "none", Settings: `{"kcp_seed":"s3","kcp_header":"srtp"}`},
			clients: []*model.Client{client("c", "99999999-8888-7777-6666-555555555556")},
		},
		{
			name:    "global-dns-routing",
			inbound: &model.Inbound{Name: "v", Core: "xray", Protocol: "vmess", Port: 24454, Transport: "tcp", TLS: "none"},
			clients: []*model.Client{client("c", "99999999-8888-7777-6666-555555555558")},
			opts: core.RenderOpts{
				StatsEnabled: true,
				DNSJSON:      `{"servers":["1.1.1.1",{"address":"8.8.8.8","domains":["geosite:cn"]}],"queryStrategy":"UseIP"}`,
				RouteJSON:    `{"domainStrategy":"IPIfNonMatch","rules":[{"type":"field","ip":["geoip:private"],"outboundTag":"block"}]}`,
			},
		},
	}

	x := &xray.Xray{}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := x.Render([]core.InboundWithClients{{Inbound: c.inbound, Clients: c.clients}}, c.opts)
			if err != nil {
				t.Fatalf("渲染失败: %v", err)
			}
			runValidate(t, bin, "run", "-test", "-config", writeCfg(t, "xray", out))
		})
	}
}

func TestSingboxRendersValidConfig(t *testing.T) {
	bin := os.Getenv("TRINE_SINGBOX_BIN")
	if bin == "" {
		t.Skip("未设置 TRINE_SINGBOX_BIN，跳过真机校验")
	}
	if _, err := os.Stat(testCert); err != nil {
		t.Skip("缺少测试证书 /tmp/test-cert.pem")
	}
	tlsSettings := `{"server_name":"example.com","certificate_path":"` + testCert + `","key_path":"` + testKey + `"`

	cases := []liveCase{
		{
			name:    "vless-vision",
			inbound: &model.Inbound{Name: "v", Core: "singbox", Protocol: "vless", Port: 24543, Transport: "tcp", TLS: "tls", Settings: tlsSettings + `,"flow":"xtls-rprx-vision"}`},
			clients: []*model.Client{client("alice", "11111111-2222-3333-4444-555555555555")},
		},
		{
			name:    "http-users",
			inbound: &model.Inbound{Name: "h", Core: "singbox", Protocol: "http", Port: 24544, Transport: "tcp", TLS: "none"},
			clients: []*model.Client{client("h1", "h1:hp1")},
		},
		{
			name:    "direct-portforward",
			inbound: &model.Inbound{Name: "d", Core: "singbox", Protocol: "direct", Port: 24545, Transport: "tcp", TLS: "none", Settings: `{"dd_address":"1.1.1.1","dd_port":80}`},
		},
		{
			name:    "h2-tls",
			inbound: &model.Inbound{Name: "h2", Core: "singbox", Protocol: "vless", Port: 24546, Transport: "h2", TLS: "tls", Settings: `{"path":"/h2","ws_host":"example.com",` + strings.TrimPrefix(tlsSettings, "{") + `}`},
			clients: []*model.Client{client("c", "aaaaaaaa-bbbb-cccc-dddd-0000000000f3")},
		},
		{
			name:    "httpupgrade",
			inbound: &model.Inbound{Name: "hu", Core: "singbox", Protocol: "vless", Port: 24547, Transport: "httpupgrade", TLS: "none", Settings: `{"path":"/hu","ws_host":"example.com"}`},
			clients: []*model.Client{client("c", "aaaaaaaa-bbbb-cccc-dddd-0000000000f2")},
		},
		{
			name:    "quic",
			inbound: &model.Inbound{Name: "q", Core: "singbox", Protocol: "vless", Port: 24548, Transport: "quic", TLS: "tls", Settings: tlsSettings + `}`},
			clients: []*model.Client{client("c", "aaaaaaaa-bbbb-cccc-dddd-0000000000f1")},
		},
		{
			// importer 的转换结果也要能过内核校验：xray 格式 → sing-box 格式
			name:    "converted-global-from-xray",
			inbound: &model.Inbound{Name: "v3", Core: "singbox", Protocol: "vless", Port: 24550, Transport: "tcp", TLS: "none"},
			clients: []*model.Client{client("c", "aaaaaaaa-bbbb-cccc-dddd-0000000000f5")},
			opts:    convertedGlobal(t),
		},
		{
			name:    "global-dns-route",
			inbound: &model.Inbound{Name: "v2", Core: "singbox", Protocol: "vless", Port: 24549, Transport: "tcp", TLS: "none"},
			clients: []*model.Client{client("c", "aaaaaaaa-bbbb-cccc-dddd-0000000000f4")},
			opts: core.RenderOpts{
				DNSJSON:   `{"servers":[{"type":"udp","tag":"dns-1-1-1-1","server":"1.1.1.1"}],"rules":[{"domain_suffix":["cn"],"server":"dns-1-1-1-1"}]}`,
				RouteJSON: `{"rules":[{"ip_cidr":["10.0.0.0/8"],"action":"reject"}]}`,
			},
		},
	}

	sb := &singbox.SingBox{}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := sb.Render([]core.InboundWithClients{{Inbound: c.inbound, Clients: c.clients}}, c.opts)
			if err != nil {
				t.Fatalf("渲染失败: %v", err)
			}
			runValidate(t, bin, "check", "-c", writeCfg(t, "singbox", out))
		})
	}
}

func writeCfg(t *testing.T, name string, content []byte) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name+".json")
	if err := os.WriteFile(p, content, 0o600); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}
	return p
}

func runValidate(t *testing.T, bin string, args ...string) {
	t.Helper()
	out, err := exec.Command(bin, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("内核校验失败: %v\n%s", err, out)
	}
}

// realityPriv 用 crypto/ecdh 生成一对 X25519 私钥（base64url），
// 与面板 /api/tools/reality-keypair 的实现一致。
func realityPriv(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// wgKeypair 生成 WireGuard 用的 base64 密钥（32 字节，标准 base64）。
func wgKeypair(t *testing.T) (string, string) {
	t.Helper()
	priv := make([]byte, 32)
	pub := make([]byte, 32)
	if _, err := rand.Read(priv); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(pub); err != nil {
		t.Fatal(err)
	}
	// xray 的 wireguard 用 curve25519 点乘；这里只是喂一份结构合法的配置，
	// 校验阶段不会做握手，密钥数值本身不重要。
	return base64.StdEncoding.EncodeToString(priv), base64.StdEncoding.EncodeToString(pub)
}

// convertedGlobal 走一遍 importer 的转换，产出 RenderOpts。
func convertedGlobal(t *testing.T) core.RenderOpts {
	t.Helper()
	dns, w1 := importer.ToSingboxDNS(map[string]any{
		"servers": []any{
			"1.1.1.1",
			map[string]any{"address": "https://dns.google/dns-query", "domains": []any{"domain:example.com"}},
		},
	})
	route, w2 := importer.ToSingboxRoute(map[string]any{
		"rules": []any{
			map[string]any{"type": "field", "ip": []any{"geoip:private"}, "outboundTag": "block"},
			map[string]any{"type": "field", "domain": []any{"geosite:cn"}, "outboundTag": "direct"},
			map[string]any{"type": "field", "port": "443", "inboundTag": []any{"in-1"}, "outboundTag": "direct"},
		},
	})
	if len(dns) == 0 || len(route) == 0 {
		t.Fatalf("转换结果为空：dns=%v route=%v warns=%v/%v", dns, route, w1, w2)
	}
	dj, _ := json.Marshal(dns)
	rj, _ := json.Marshal(route)
	return core.RenderOpts{DNSJSON: string(dj), RouteJSON: string(rj)}
}
