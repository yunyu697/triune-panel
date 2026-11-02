package api

// 连通性自检里「TLS 握手」这一项的测试。
//
// 这一项误报过一次，代价是：真机跑着完全正常的 Reality 入站，面板却报
// 「TLS 握手失败: remote error: tls: handshake failure」。原因是探测用的 SNI
// 取错了字段（读 server_name，表单存的是 server_names），而 Reality 对不在
// serverNames 里的 SNI 是按设计拒绝的。
//
// 所以这里用「真的起一个 TLS 服务 / 真的起一个会断开的端口」来断言，
// 而不是断言字符串拼接——探测本身要能连上、能失败，结论才有意义。

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"triune/internal/model"
)

// selfSignedTLS 生成一张自签证书，用于本机 TLS 监听。
func selfSignedTLS(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成密钥: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "t.example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		DNSNames:     []string{"t.example.com"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("签发证书: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// startTLSListener 起一个只做 TLS 握手的监听，返回端口。
func startTLSListener(t *testing.T) int {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{selfSignedTLS(t)}})
	if err != nil {
		t.Fatalf("监听: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.(*tls.Conn).Handshake() // 握手完就断，模拟「只验证握手」
			}(c)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// startDroppingListener 起一个 accept 后立刻关闭的端口，
// 用来模拟「端口活着但握手谈不成」（Reality 的 dest 不可达就是这样）。
func startDroppingListener(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestProbeTLSRealityUsesRealSNI(t *testing.T) {
	port := startTLSListener(t)
	in := &model.Inbound{
		Name: "r", Protocol: "vless", TLS: "reality",
		Listen: "127.0.0.1", Port: port,
		// 关键：SNI 只在 server_names 里，server_name 是空的。
		// 探测要是仍读 server_name，就会退化成 probe.local —— 那正是曾经的 bug。
		Settings: `{"server_names":"www.apple.com,apple.com","handshake_server":"www.apple.com"}`,
	}
	c := probeTLS(in)
	if !c.OK {
		t.Fatalf("应握手成功，实际失败: %s", c.Detail)
	}
	if !strings.Contains(c.Detail, "SNI=www.apple.com") {
		t.Errorf("探测必须用 serverNames 的第一个，详情: %s", c.Detail)
	}
	if strings.Contains(c.Detail, "probe.local") {
		t.Errorf("不该再用占位 SNI: %s", c.Detail)
	}
}

func TestProbeTLSRealityWithoutSNISkips(t *testing.T) {
	port := startTLSListener(t)
	in := &model.Inbound{
		Name: "r", Protocol: "vless", TLS: "reality",
		Listen: "127.0.0.1", Port: port, Settings: `{}`,
	}
	c := probeTLS(in)
	if c.OK {
		t.Fatal("没有 SNI 时不该报握手成功")
	}
	if !c.Warn {
		t.Error("缺配置是提示性结论，不该算硬失败")
	}
	if !strings.Contains(c.Detail, "未配置 SNI") {
		t.Errorf("要直接说缺什么，实际: %s", c.Detail)
	}
}

func TestProbeTLSRealityFailurePointsAtDest(t *testing.T) {
	port := startDroppingListener(t)
	in := &model.Inbound{
		Name: "r", Protocol: "vless", TLS: "reality",
		Listen: "127.0.0.1", Port: port,
		Settings: `{"server_names":"www.apple.com","handshake_server":"1.2.3.4","handshake_port":443}`,
	}
	c := probeTLS(in)
	if c.OK {
		t.Fatal("端口立刻断开时不该报成功")
	}
	if !c.Warn {
		t.Error("握手失败不足以判定入站坏了，应为提示")
	}
	// 文案要把人指向 dest，而不是含糊地说「握手失败」
	if !strings.Contains(c.Detail, "伪装目标 1.2.3.4:443") {
		t.Errorf("Reality 失败应点明 dest，实际: %s", c.Detail)
	}
	if !strings.Contains(c.Detail, "shortId") {
		t.Errorf("应说明客户端真实握手可能仍正常，实际: %s", c.Detail)
	}
}

func TestProbeTLSNormalFailureMentionsCert(t *testing.T) {
	port := startDroppingListener(t)
	in := &model.Inbound{
		Name: "t", Protocol: "vless", TLS: "tls",
		Listen: "127.0.0.1", Port: port,
		Settings: `{"server_name":"t.example.com"}`,
	}
	c := probeTLS(in)
	if c.OK {
		t.Fatal("端口立刻断开时不该报成功")
	}
	if !strings.Contains(c.Detail, "证书") {
		t.Errorf("普通 TLS 失败应提示查证书，实际: %s", c.Detail)
	}
}

// TestProbeTLSUsesListenAddr 入站只监听某个具体地址时，
// 探测不能写死 127.0.0.1——否则会被误判成「连不上」。
func TestProbeTLSUsesListenAddr(t *testing.T) {
	port := startTLSListener(t)
	in := &model.Inbound{
		Name: "t", Protocol: "vless", TLS: "tls",
		Listen: "127.0.0.1", Port: port,
		Settings: `{"server_name":"t.example.com"}`,
	}
	if c := probeTLS(in); !c.OK {
		t.Fatalf("监听 127.0.0.1 时应能探测到，实际: %s", c.Detail)
	}
	// 换成一个必然连不上的地址（本机不持有的 IP），应报失败而不是 panic
	in.Listen = "192.0.2.1" // TEST-NET-1，保证不可达
	c := probeTLS(in)
	if c.OK {
		t.Error("连不上却报成功")
	}
	if !strings.Contains(c.Detail, "192.0.2.1:"+strconv.Itoa(port)) {
		t.Errorf("失败信息应带上真实探测地址，实际: %s", c.Detail)
	}
}
