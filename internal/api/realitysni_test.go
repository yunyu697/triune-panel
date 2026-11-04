package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// REALITY 同站 SNI 收集。
//
// 锁住的是「凭什么叫合法」：
//   - 只有证书里写明的域名才算候选，通配符本身不能当 SNI（客户端发的必须是具体名字）；
//   - 候选必须真握手验过才返回——按 SNI 分流的目标会给不存在的子域另一张证书；
//   - 只谈成 TLS 1.2 的目标不给任何 SNI 建议（它连 dest 都当不了）。

// startSNIRoutedServer 起一个按 SNI 分流的本地 TLS 服务：名字在 goodNames
// 里的给 good 证书，其余给 other 证书——模拟「不存在的子域拿到另一张证书」。
func startSNIRoutedServer(t *testing.T, good tls.Certificate, goodNames []string,
	other tls.Certificate, maxVer uint16) (string, int) {
	t.Helper()
	cfg := &tls.Config{
		MaxVersion: maxVer,
		GetCertificate: func(hi *tls.ClientHelloInfo) (*tls.Certificate, error) {
			for _, n := range goodNames {
				if strings.EqualFold(hi.ServerName, n) {
					return &good, nil
				}
			}
			return &other, nil
		},
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
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

// TestCertInfoOfSplitsWildcards 证书里的域名要分成「具体」与「通配符」两堆。
func TestCertInfoOfSplitsWildcards(t *testing.T) {
	leaf := &x509.Certificate{
		DNSNames: []string{"www.example.test", "*.example.test", "www.example.test"},
		Subject:  pkix.Name{CommonName: "example.test"},
		Issuer:   pkix.Name{Organization: []string{"Test CA"}},
		NotAfter: time.Now().Add(48 * time.Hour),
	}
	info := certInfoOf(leaf)

	// 重复项去重；CN 里那个（example.test）没在 SAN 里，要补进来
	want := []string{"www.example.test", "example.test"}
	if strings.Join(info.Names, ",") != strings.Join(want, ",") {
		t.Errorf("具体域名 = %v，期望 %v", info.Names, want)
	}
	if len(info.Wildcard) != 1 || info.Wildcard[0] != "*.example.test" {
		t.Errorf("通配符 = %v，期望 [*.example.test]", info.Wildcard)
	}
	if info.Issuer != "Test CA" {
		t.Errorf("签发者 = %q", info.Issuer)
	}
	if info.DaysLeft < 1 || info.DaysLeft > 2 {
		t.Errorf("48 小时应算成 2 天以内，实际 %d", info.DaysLeft)
	}
}

// TestSNISeeds 候选顺序与来源：目标本身 → 证书 SAN → 通配符展开，且去重。
func TestSNISeeds(t *testing.T) {
	cert := realityCertInfo{
		Names:    []string{"www.example.test", "mail.example.test"},
		Wildcard: []string{"*.example.test"},
	}
	seeds, total := sniSeeds("www.example.test", cert)

	wantNames := []string{"www.example.test", "mail.example.test", "api.example.test", "cdn.example.test"}
	wantSrc := []string{"目标本身", "证书 SAN", "通配符展开", "通配符展开"}
	if len(seeds) != len(wantNames) {
		t.Fatalf("候选 = %+v，期望 %d 个", seeds, len(wantNames))
	}
	if total != len(wantNames) {
		t.Errorf("总数 = %d，期望 %d", total, len(wantNames))
	}
	for i := range wantNames {
		if seeds[i].Name != wantNames[i] {
			t.Errorf("第 %d 个候选 = %q，期望 %q", i+1, seeds[i].Name, wantNames[i])
		}
		if seeds[i].Source != wantSrc[i] {
			t.Errorf("%s 的来源 = %q，期望 %q", seeds[i].Name, seeds[i].Source, wantSrc[i])
		}
	}

	// 通配符本身绝不能当候选：客户端发的 SNI 必须是具体名字
	for _, s := range seeds {
		if strings.Contains(s.Name, "*") {
			t.Errorf("候选里出现了通配符 %q", s.Name)
		}
	}

	// 上限：一堆 SAN 时不能无限往外连，但要让人知道被截了
	many := realityCertInfo{}
	for i := 0; i < sniCollectMaxCandidates+8; i++ {
		many.Names = append(many.Names, "h"+strconv.Itoa(i)+".example.test")
	}
	got, tot := sniSeeds("h0.example.test", many)
	if len(got) != sniCollectMaxCandidates {
		t.Errorf("候选应截到 %d 个，实际 %d", sniCollectMaxCandidates, len(got))
	}
	if tot != sniCollectMaxCandidates+8 {
		t.Errorf("总数应是截断前的 %d，实际 %d", sniCollectMaxCandidates+8, tot)
	}
}

// TestCollectRealitySNI 端到端：只返回真握手验过的 SNI。
func TestCollectRealitySNI(t *testing.T) {
	good, pool := destTestCert(t, "www.example.test", "*.example.test", "mail.example.test")
	other, _ := destTestCert(t, "unrelated.test")
	// 把「另一张证书」也加进根池：这样 api.example.test 失败的原因是
	// 「证书与 SNI 对不上」，而不是链验不过——前者才是真实场景。
	if oc, err := x509.ParseCertificate(other.Certificate[0]); err == nil {
		pool.AddCert(oc)
	}
	// 服务端只认这三个名字，api.example.test 会拿到 unrelated.test 的证书
	host, port := startSNIRoutedServer(t, good,
		[]string{"www.example.test", "mail.example.test", "cdn.example.test"}, other, tls.VersionTLS13)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, msg := collectRealitySNI(ctx, destCandidate{Host: host, Port: port, SNI: "www.example.test"}, pool)
	if msg != "" {
		t.Fatalf("不该失败：%s", msg)
	}

	got := map[string]bool{}
	for _, s := range res.SNIs {
		got[s.SNI] = true
		if !s.OK {
			t.Errorf("%s 出现在可用列表里却标着 ok=false", s.SNI)
		}
		if s.Source == "" {
			t.Errorf("%s 没有来源说明", s.SNI)
		}
	}
	for _, want := range []string{"www.example.test", "mail.example.test", "cdn.example.test"} {
		if !got[want] {
			t.Errorf("验过的 %s 没出现在结果里：%+v", want, res.SNIs)
		}
	}
	if got["api.example.test"] {
		t.Error("api.example.test 拿到的是别的证书，不该被推荐")
	}

	var rejected []string
	for _, r := range res.Rejected {
		rejected = append(rejected, r.SNI)
		if r.Detail == "" {
			t.Errorf("%s 被拒却没写原因", r.SNI)
		}
	}
	if strings.Join(rejected, ",") != "api.example.test" {
		t.Errorf("被拒的应只有 api.example.test，实际 %v", rejected)
	}
	if !strings.Contains(res.Rejected[0].Detail, "证书") {
		t.Errorf("被拒原因应指向证书，实际 %q", res.Rejected[0].Detail)
	}

	if len(res.Cert.Wildcard) != 1 || res.Cert.Wildcard[0] != "*.example.test" {
		t.Errorf("证书通配符 = %v", res.Cert.Wildcard)
	}
	if res.Tried != 4 {
		t.Errorf("共试了 4 个名字（www + mail + api + cdn），实际 %d", res.Tried)
	}
	if res.Note != "" {
		t.Errorf("验出多个可用域名时不该有 note，实际 %q", res.Note)
	}
}

// TestCollectRealitySNITLS12 只支持 TLS 1.2 的目标：不给任何 SNI 建议。
func TestCollectRealitySNITLS12(t *testing.T) {
	good, pool := destTestCert(t, "www.example.test")
	other, _ := destTestCert(t, "unrelated.test")
	host, port := startSNIRoutedServer(t, good, []string{"www.example.test"}, other, tls.VersionTLS12)

	res, msg := collectRealitySNI(context.Background(),
		destCandidate{Host: host, Port: port, SNI: "www.example.test"}, pool)
	if msg == "" {
		t.Fatalf("TLS 1.2 的目标应直接失败，实际返回 %+v", res)
	}
	if !strings.Contains(msg, "TLS 1.3") {
		t.Errorf("失败原因应说清 REALITY 需要 TLS 1.3，实际 %q", msg)
	}
}

// TestRealitySNIHandlerBadRequests 接口层：畸形与内网输入不该发出任何外网连接。
func TestRealitySNIHandlerBadRequests(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	w := apiReq(t, h, tok, "POST", "/api/tools/reality-sni", `{"host":"10.0.0.5","port":443}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("内网地址应 400，实际 %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "内网") {
		t.Errorf("错误里应说清是内网地址：%s", w.Body.String())
	}

	if w := apiReq(t, h, tok, "POST", "/api/tools/reality-sni", `{"host":""}`); w.Code != http.StatusBadRequest {
		t.Errorf("空目标应 400，实际 %d", w.Code)
	}
}
