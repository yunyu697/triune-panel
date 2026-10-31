package link

import "testing"

func TestFromClashVlessReality(t *testing.T) {
	m := map[string]any{
		"name": "hk-01", "type": "vless", "server": "1.2.3.4", "port": "443",
		"uuid": "8f2b1c3d-1111-2222-3333-444455556666", "flow": "xtls-rprx-vision",
		"tls": true, "servername": "www.apple.com", "network": "tcp",
		"reality-opts": map[string]any{"public-key": "PBK123", "short-id": "ab12"},
		"client-fingerprint": "chrome",
	}
	n, err := FromClash(m)
	if err != nil {
		t.Fatalf("FromClash: %v", err)
	}
	if n.Protocol != "vless" || n.Security != "reality" || n.SNI != "www.apple.com" {
		t.Fatalf("unexpected node: %+v", n)
	}
	if n.Extra["pbk"] != "PBK123" || n.Extra["sid"] != "ab12" || n.Extra["flow"] != "xtls-rprx-vision" {
		t.Fatalf("extra mismatch: %+v", n.Extra)
	}
	if l := GenLink(n); l == "" || !contains(l, "security=reality") || !contains(l, "pbk=PBK123") {
		t.Fatalf("bad link: %q", l)
	}
}

func TestFromClashSS(t *testing.T) {
	n, err := FromClash(map[string]any{
		"name": "ss-01", "type": "ss", "server": "5.6.7.8", "port": 8388,
		"cipher": "aes-256-gcm", "password": "secret",
	})
	if err != nil {
		t.Fatalf("FromClash: %v", err)
	}
	if n.Protocol != "shadowsocks" || n.Extra["method"] != "aes-256-gcm" || n.Cred != "secret" {
		t.Fatalf("unexpected node: %+v", n)
	}
	if l := GenLink(n); !contains(l, "ss://") {
		t.Fatalf("bad link: %q", l)
	}
}

func TestFromClashTrojanDefaultsTLS(t *testing.T) {
	n, err := FromClash(map[string]any{
		"name": "tr-01", "type": "trojan", "server": "example.com", "port": "443",
		"password": "pw", "sni": "example.com",
	})
	if err != nil {
		t.Fatalf("FromClash: %v", err)
	}
	if n.Security != "tls" {
		t.Fatalf("trojan should default tls, got %q", n.Security)
	}
}

func TestFromClashRejectsUnknown(t *testing.T) {
	if _, err := FromClash(map[string]any{"name": "x", "type": "wireguard", "server": "h", "port": "1"}); err == nil {
		t.Fatal("expected error for unsupported type")
	}
	if _, err := FromClash(map[string]any{"name": "x", "type": "ss"}); err == nil {
		t.Fatal("expected error for missing server")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && index(s, sub) >= 0)
}

func index(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
