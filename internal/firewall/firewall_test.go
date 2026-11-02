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
		{"wireguard", "udp", "udp"}, // WireGuard 只有 udp
		{"wireguard", "tcp", "udp"}, // 表单里的 transport 填错也不该改变它的本质
		{"vless", "kcp", "udp"},     // xray 的 kcp 传输跑在 udp 上
		{"vless", "xhttp", "tcp"},
		{"", "", "tcp"},
	}
	for _, c := range cases {
		if got := ProtoOf(c.protocol, c.transport); got != c.want {
			t.Errorf("ProtoOf(%q,%q) = %q, want %q", c.protocol, c.transport, got, c.want)
		}
	}
}

// TestProtoList 双栈协议要两种都放行：漏放 udp 的表现是「能连上但某些应用不通」，
// 极难归因到防火墙。
func TestProtoList(t *testing.T) {
	cases := []struct {
		protocol, transport string
		want                []string
	}{
		{"vless", "tcp", []string{"tcp"}},
		{"hysteria2", "udp", []string{"udp"}},
		{"wireguard", "udp", []string{"udp"}},
		{"vless", "kcp", []string{"udp"}},
		{"socks", "tcp", []string{"tcp", "udp"}},
		{"shadowsocks", "tcp", []string{"tcp", "udp"}},
		{"direct", "tcp", []string{"tcp", "udp"}},
		{"", "", []string{"tcp"}},
	}
	for _, c := range cases {
		got := ProtoList(c.protocol, c.transport)
		if len(got) != len(c.want) {
			t.Errorf("ProtoList(%q,%q) = %v, want %v", c.protocol, c.transport, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("ProtoList(%q,%q) = %v, want %v", c.protocol, c.transport, got, c.want)
				break
			}
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
