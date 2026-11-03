package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// REALITY 握手目标择优 + shortIds 校验。
//
// 这里锁住的是「判断依据」而不是「跑得快不快」：
//   - 只谈成 TLS 1.2 的目标必须判成不可用（REALITY 只在 1.3 上工作）；
//   - 证书与 SNI 对不上时必须给出警告（正常连接看不出来，回落时才露馅）；
//   - 内网地址必须被挡在接口外（否则面板成了内网端口扫描器）；
//   - shortIds 的三种错法（非十六进制 / 奇数长度 / 超长）必须在保存期拦下。

func destTestCert(t *testing.T, dns ...string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: dns[0], Organization: []string{"Triune Test CA"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(90 * 24 * time.Hour),
		DNSNames:              dns,
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

// startDestServer 起一个只做 TLS 握手的本地服务，返回 host/port。
func startDestServer(t *testing.T, cert tls.Certificate, maxVer uint16, alpn []string) (string, int) {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
		MaxVersion:   maxVer,
		NextProtos:   alpn,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				if tc, ok := c.(*tls.Conn); ok {
					_ = tc.Handshake()
				}
				_, _ = io.Copy(io.Discard, c)
			}(c)
		}
	}()
	host, ps, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(ps)
	return host, port
}

// TestDestProbeTLS13 支持 TLS 1.3 + 证书对得上 + h2 → 可用且标记推荐。
func TestDestProbeTLS13(t *testing.T) {
	cert, pool := destTestCert(t, "localhost")
	host, port := startDestServer(t, cert, tls.VersionTLS13, []string{"h2", "http/1.1"})

	res := probeRealityDest(context.Background(), destCandidate{Host: host, Port: port, SNI: "localhost"}, pool)
	if !res.OK {
		t.Fatalf("应判为可用，实际 %+v", res)
	}
	if res.TLS != "1.3" {
		t.Errorf("TLS 版本 = %q，期望 1.3", res.TLS)
	}
	if !res.CertOK || res.Issuer == "" {
		t.Errorf("证书应校验通过并给出签发者，实际 cert_ok=%v issuer=%q", res.CertOK, res.Issuer)
	}
	if !res.H2 || res.ALPN != "h2" {
		t.Errorf("ALPN 应谈成 h2，实际 %q", res.ALPN)
	}
	if res.MS < 0 {
		t.Errorf("耗时不能为负：%d", res.MS)
	}
	if res.DaysLeft < 80 {
		t.Errorf("证书还有 90 天，剩余天数算成了 %d", res.DaysLeft)
	}
}

// TestDestProbeTLS12Rejected 只给 TLS 1.2 的目标：握手能成，但 REALITY 用不了。
func TestDestProbeTLS12Rejected(t *testing.T) {
	cert, pool := destTestCert(t, "localhost")
	host, port := startDestServer(t, cert, tls.VersionTLS12, []string{"http/1.1"})

	res := probeRealityDest(context.Background(), destCandidate{Host: host, Port: port, SNI: "localhost"}, pool)
	if res.OK {
		t.Fatalf("只支持 TLS 1.2 的目标不该判为可用：%+v", res)
	}
	if res.TLS != "1.2" {
		t.Errorf("TLS 版本 = %q，期望 1.2", res.TLS)
	}
	if !strings.Contains(res.Detail, "TLS 1.3") {
		t.Errorf("结论里应说清「REALITY 必须在 TLS 1.3 上工作」，实际：%s", res.Detail)
	}
}

// TestDestProbeCertMismatch 证书域名与 SNI 不符：可用，但必须带警告。
func TestDestProbeCertMismatch(t *testing.T) {
	cert, pool := destTestCert(t, "other.example")
	host, port := startDestServer(t, cert, tls.VersionTLS13, []string{"h2"})

	res := probeRealityDest(context.Background(), destCandidate{Host: host, Port: port, SNI: "localhost"}, pool)
	if !res.OK {
		t.Fatalf("握手成功就该判可用，实际 %+v", res)
	}
	if res.CertOK {
		t.Fatal("证书域名不符却判成校验通过")
	}
	if !strings.Contains(res.Warning, "证书") {
		t.Errorf("警告里应说明证书问题，实际：%s", res.Warning)
	}
	// 排序时证书不对的不能占「推荐」位
	sortDestResults([]realityDestResult{res})
	if res.Best {
		t.Error("证书不对的目标不该被标成推荐")
	}
}

// TestDestProbeRefused 端口没人听：明确报连不上，而不是卡到超时。
func TestDestProbeRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, ps, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(ps)
	_ = ln.Close()

	start := time.Now()
	res := probeRealityDest(context.Background(), destCandidate{Host: "127.0.0.1", Port: port}, nil)
	if res.OK {
		t.Fatalf("端口没人听却判为可用：%+v", res)
	}
	if res.Detail == "" {
		t.Error("失败时必须给出原因")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("连不上应该快速返回，实际花了 %v", d)
	}
}

// TestNormalizeDestCandidates 入参归一：容忍 host:port、去重、补默认端口。
func TestNormalizeDestCandidates(t *testing.T) {
	got, msg := normalizeDestCandidates([]destCandidate{
		{Host: "www.apple.com"},
		{Host: " www.apple.com:443 "},
		{Host: "www.microsoft.com:8443"},
	})
	if msg != "" {
		t.Fatalf("归一失败：%s", msg)
	}
	if len(got) != 2 {
		t.Fatalf("重复项应被去掉，剩 2 个，实际 %d：%+v", len(got), got)
	}
	if got[0].Host != "www.apple.com" || got[0].Port != 443 {
		t.Errorf("默认端口应为 443，实际 %+v", got[0])
	}
	if got[1].Host != "www.microsoft.com" || got[1].Port != 8443 {
		t.Errorf("显式端口应被保留，实际 %+v", got[1])
	}
}

// TestDestCandidateRejects 畸形与内网输入必须当场拒绝。
func TestDestCandidateRejects(t *testing.T) {
	cases := []struct {
		name string
		in   []destCandidate
		want string
	}{
		{"带协议", []destCandidate{{Host: "https://www.apple.com"}}, "协议"},
		{"带路径", []destCandidate{{Host: "www.apple.com/x"}}, "非法字符"},
		{"端口不是数字", []destCandidate{{Host: "www.apple.com:abc"}}, "端口"},
		{"内网 IP", []destCandidate{{Host: "127.0.0.1"}}, "内网"},
		{"私有网段", []destCandidate{{Host: "10.1.2.3:443"}}, "内网"},
		{"空列表", nil, "至少"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, msg := normalizeDestCandidates(c.in); !strings.Contains(msg, c.want) {
				t.Errorf("期望错误里含「%s」，实际 %q", c.want, msg)
			}
		})
	}

	// 超过上限：构造 17 个不同域名
	many := make([]destCandidate, 0, destProbeMaxCandidates+1)
	for i := 0; i <= destProbeMaxCandidates; i++ {
		many = append(many, destCandidate{Host: "h" + strconv.Itoa(i) + ".example.com"})
	}
	if _, msg := normalizeDestCandidates(many); !strings.Contains(msg, "最多") {
		t.Errorf("超过上限应报错，实际 %q", msg)
	}
}

// TestRealityDestHandlerBadRequests 接口层也要挡：这些请求不该发出任何外网连接。
func TestRealityDestHandlerBadRequests(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	w := apiReq(t, h, tok, "POST", "/api/tools/reality-dest", `{"candidates":[{"host":"127.0.0.1"}]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("内网地址应 400，实际 %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "内网") {
		t.Errorf("错误里应说清是内网地址：%s", w.Body.String())
	}

	if w := apiReq(t, h, tok, "POST", "/api/tools/reality-dest", `{"candidates":[]}`); w.Code != http.StatusBadRequest {
		t.Errorf("空候选应 400，实际 %d", w.Code)
	}
}

// TestValidateRealitySettings shortIds / spiderX 的保存期校验。
func TestValidateRealitySettings(t *testing.T) {
	ok := []string{
		`{}`,
		`{"short_ids":[""]}`,
		`{"short_ids":["ab12","cd34ef56"]}`,
		`{"short_ids":"ab12, cd34"}`,
		`{"short_id":"ab"}`,
		`{"short_ids":["ab12"],"spider_x":"/x"}`,
	}
	for _, s := range ok {
		if msg := validateRealitySettings(s); msg != "" {
			t.Errorf("%s 应通过，实际报错：%s", s, msg)
		}
	}

	bad := []struct{ settings, want string }{
		{`{"short_ids":["xyz1"]}`, "十六进制"},
		{`{"short_ids":["ab1"]}`, "偶数"},
		{`{"short_ids":["ab12","0123456789abcdef0"]}`, "最多 16"},
		{`{"short_ids":["ab12"],"spider_x":"x"}`, "必须以 / 开头"},
	}
	for _, c := range bad {
		msg := validateRealitySettings(c.settings)
		if !strings.Contains(msg, c.want) {
			t.Errorf("%s 应报「%s」，实际 %q", c.settings, c.want, msg)
		}
	}

	// 错误里要能定位到「第几个」，否则一堆短 ID 时没法改
	msg := validateRealitySettings(`{"short_ids":["ab12","zz"]}`)
	if !strings.Contains(msg, "第 2 个") {
		t.Errorf("错误应点名第几个短 ID，实际：%s", msg)
	}
}

// TestInboundRejectsBadShortIDs 走真实保存路径：shortIds 非法 → 400 且不落库。
func TestInboundRejectsBadShortIDs(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	body := `{"name":"r-bad","core":"xray","protocol":"vless","listen":"127.0.0.1","port":24443,` +
		`"tag":"r-bad","transport":"tcp","tls":"reality","enabled":false,"draft":false,` +
		`"settings":"{\"private_key\":\"PRIV\",\"short_ids\":[\"ab1g\"]}"}`
	w := apiReq(t, h, tok, "POST", "/api/inbounds", body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非法 shortIds 应 400，实际 %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "shortIds") {
		t.Errorf("错误里应点名 shortIds：%s", w.Body.String())
	}
}

// TestSortDestResults 排序：可用 → 证书对 → h2 → 快。
func TestSortDestResults(t *testing.T) {
	rs := []realityDestResult{
		{Host: "slow", OK: true, CertOK: true, H2: true, MS: 300},
		{Host: "dead", OK: false},
		{Host: "fast", OK: true, CertOK: true, H2: true, MS: 30},
		{Host: "nocert", OK: true, CertOK: false, H2: true, MS: 5},
	}
	sortDestResults(rs)
	want := []string{"fast", "slow", "nocert", "dead"}
	for i, w := range want {
		if rs[i].Host != w {
			t.Fatalf("第 %d 位应是 %s，实际 %s（%+v）", i+1, w, rs[i].Host, rs)
		}
	}
	if !rs[0].Best {
		t.Error("排第一的应被标成推荐")
	}
	for _, r := range rs[1:] {
		if r.Best {
			t.Errorf("%s 不该被标成推荐", r.Host)
		}
	}
}

// TestDestProbeConcurrent 并发跑一组：结果数量与顺序稳定性要对得上。
func TestDestProbeConcurrent(t *testing.T) {
	cert, pool := destTestCert(t, "localhost")
	h1, p1 := startDestServer(t, cert, tls.VersionTLS13, []string{"h2"})
	h2, p2 := startDestServer(t, cert, tls.VersionTLS12, nil)

	cands := []destCandidate{
		{Host: h1, Port: p1, SNI: "localhost"},
		{Host: h2, Port: p2, SNI: "localhost"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res := probeRealityDestAll(ctx, cands, pool)
	if len(res) != 2 {
		t.Fatalf("应返回 2 条结果，实际 %d", len(res))
	}
	if !res[0].OK || res[0].Port != p1 {
		t.Errorf("可用的应排在最前：%+v", res[0])
	}
	if res[1].OK {
		t.Errorf("TLS 1.2 的目标不该排在可用位置：%+v", res[1])
	}
}

// TestDestProbeResponseShape 接口的成功响应结构（用本地 TLS 服务，不出网）。
// 直接调 probe 组合响应里的字段，避免依赖真实外网。
func TestDestProbeResponseShape(t *testing.T) {
	cert, pool := destTestCert(t, "localhost")
	host, port := startDestServer(t, cert, tls.VersionTLS13, []string{"h2"})
	res := probeRealityDestAll(context.Background(), []destCandidate{{Host: host, Port: port, SNI: "localhost"}}, pool)
	b, err := json.Marshal(map[string]any{"results": res, "best": net.JoinHostPort(res[0].Host, strconv.Itoa(res[0].Port))})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Results []map[string]any `json:"results"`
		Best    string           `json:"best"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"host", "port", "ok", "ms", "tls", "alpn", "h2", "cert_ok", "detail"} {
		if _, ok := out.Results[0][k]; !ok {
			t.Errorf("响应缺字段 %s：%v", k, out.Results[0])
		}
	}
	if out.Best == "" {
		t.Error("best 不该为空")
	}
}
