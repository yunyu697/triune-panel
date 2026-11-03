package link

import (
	"errors"
	"testing"
)

// 传输在客户端配置里没有对应类型时**必须报错**，不能什么都不写。
//
// 不写 network 等于 tcp，而服务端明明在用 xhttp / httpupgrade：客户端按 tcp
// 连过去必然失败，表现是「节点能加载、一连就不通」，两边日志都不指向真正原因。
// 这类静默降级只能靠上层「跳过并说明」来处理，所以错误要能被认出来
// （errors.Is(err, ErrTransport)），否则上层只能把原因说成「协议不支持」。
func TestClashRejectsUnrepresentableTransport(t *testing.T) {
	for _, net := range []string{"httpupgrade", "xhttp", "kcp", "quic"} {
		n := &Node{Protocol: "vless", Host: "1.2.3.4", Port: 443, Cred: "uuid-1",
			Network: net, Security: "none", Path: "/x", Extra: map[string]any{}}
		if _, err := ToClash(n); !errors.Is(err, ErrTransport) {
			t.Errorf("%s: 期望 ErrTransport，实际 %v", net, err)
		}
		if _, err := ToSingboxOutbound(n); net == "quic" && err != nil {
			t.Errorf("quic 在 sing-box 里是支持的，不该报错: %v", err)
		}
	}
	// sing-box 认 httpupgrade，不该被拒
	n := &Node{Protocol: "vless", Host: "1.2.3.4", Port: 443, Cred: "uuid-1",
		Network: "httpupgrade", Security: "none", Path: "/up", Extra: map[string]any{"host": "cdn.example.com"}}
	o, err := ToSingboxOutbound(n)
	if err != nil {
		t.Fatalf("sing-box 应支持 httpupgrade: %v", err)
	}
	tr, _ := o["transport"].(map[string]any)
	if tr["type"] != "httpupgrade" || tr["path"] != "/up" || tr["host"] != "cdn.example.com" {
		t.Errorf("httpupgrade transport 不对: %#v", tr)
	}
}

// h2 / http 传输要落到 mihomo 的 network: h2 + h2-opts（host 是数组）。
// 以前 applyNet 只认 ws / grpc，h2 被当成 tcp 导出。
func TestClashH2Transport(t *testing.T) {
	n := &Node{Protocol: "vless", Host: "1.2.3.4", Port: 443, Cred: "uuid-1",
		Network: "h2", Security: "tls", SNI: "cdn.example.com",
		Path: "/h2", Extra: map[string]any{"host": "cdn.example.com"}}
	p, err := ToClash(n)
	if err != nil {
		t.Fatal(err)
	}
	if p["network"] != "h2" {
		t.Fatalf("network = %v，期望 h2", p["network"])
	}
	h2, _ := p["h2-opts"].(map[string]any)
	if h2["path"] != "/h2" {
		t.Errorf("h2-opts.path = %v，期望 /h2", h2["path"])
	}
	hosts, ok := h2["host"].([]any)
	if !ok || len(hosts) != 1 || hosts[0] != "cdn.example.com" {
		t.Errorf("h2-opts.host 必须是数组且带 Host: %#v", h2["host"])
	}
}

// 空传输与 tcp 等价，不能因此报错（历史数据里 transport 可能是空串）。
func TestTcpAndEmptyTransportStillExport(t *testing.T) {
	for _, net := range []string{"", "tcp", "raw"} {
		n := &Node{Protocol: "vless", Host: "1.2.3.4", Port: 443, Cred: "uuid-1",
			Network: net, Security: "none", Extra: map[string]any{}}
		p, err := ToClash(n)
		if err != nil {
			t.Errorf("network=%q 应能导出: %v", net, err)
			continue
		}
		if _, ok := p["network"]; ok {
			t.Errorf("network=%q 不该写 network 字段，实际 %v", net, p["network"])
		}
		if _, err := ToSingboxOutbound(n); err != nil {
			t.Errorf("network=%q 的 sing-box 导出失败: %v", net, err)
		}
	}
}
