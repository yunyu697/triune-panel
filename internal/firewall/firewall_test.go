package firewall

import "testing"

func TestProtoOf(t *testing.T) {
	cases := []struct {
		protocol, transport, want string
	}{
		{"vless", "tcp", "tcp"},
		{"vmess", "ws", "tcp"},
		{"trojan", "grpc", "tcp"},
		{"shadowsocks", "tcp", "tcp"},
		{"hysteria2", "udp", "udp"},
		{"tuic", "udp", "udp"},
		{"vless", "quic", "udp"},    // quic 传输 → udp
		{"hysteria2", "tcp", "udp"}, // 协议本身即 udp
		{"tuic", "quic", "udp"},
		{"", "", "tcp"},
	}
	for _, c := range cases {
		if got := ProtoOf(c.protocol, c.transport); got != c.want {
			t.Errorf("ProtoOf(%q,%q) = %q, want %q", c.protocol, c.transport, got, c.want)
		}
	}
}

// TestOpenCloseNoneBackend 验证在无任何防火墙后端的开发机上，Open/Close 直接返回 nil，
// 不产生副作用（CI/开发环境多为 mac，无 ufw/firewalld/iptables，后端即为 none）。
func TestOpenCloseNoneBackend(t *testing.T) {
	if DetectBackend() != BackendNone {
		t.Skip("当前环境存在防火墙后端，跳过无副作用断言（保留给真实环境手动验证）")
	}
	for _, port := range []int{10086, 443, 8443} {
		for _, proto := range []string{"tcp", "udp"} {
			if err := OpenPort(port, proto); err != nil {
				t.Errorf("OpenPort(%d,%s) on none backend = %v, want nil", port, proto, err)
			}
			if err := ClosePort(port, proto); err != nil {
				t.Errorf("ClosePort(%d,%s) on none backend = %v, want nil", port, proto, err)
			}
		}
	}
}
