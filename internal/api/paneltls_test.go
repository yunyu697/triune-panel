package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnsurePanelCertGenerates(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, err := EnsurePanelCert(dir)
	if err != nil {
		t.Fatalf("生成失败: %v", err)
	}
	if certFile == "" || keyFile == "" {
		t.Fatal("返回了空路径")
	}
	keySt, err := os.Stat(keyFile)
	if err != nil {
		t.Fatalf("私钥没落盘: %v", err)
	}
	// 私钥必须只有属主可读：其它用户能读等于证书体系失效
	if perm := keySt.Mode().Perm(); perm != 0o600 {
		t.Errorf("私钥权限 = %o, want 600", perm)
	}
	raw, _ := os.ReadFile(certFile)
	leaf, err := parseLeaf(raw)
	if err != nil {
		t.Fatalf("证书解析失败: %v", err)
	}
	// 用户多半用 https://<IP>:10010 访问，SAN 缺 IP 就直接握手失败
	var hasLoopback bool
	for _, ip := range leaf.IPAddresses {
		if ip.Equal(net.ParseIP("127.0.0.1")) {
			hasLoopback = true
		}
	}
	if !hasLoopback {
		t.Errorf("SAN 不含 127.0.0.1: %v", certSANs(leaf))
	}
	if !isSelfSigned(leaf) {
		t.Error("生成的证书没有自签标记，下次启动会被当作用户证书而不再续签")
	}
	if time.Until(leaf.NotAfter) < panelCertValidity-24*time.Hour {
		t.Errorf("有效期异常: %v", leaf.NotAfter)
	}
}

// 已有可用证书时必须原样复用：每次启动重签会让所有设备上的信任全部失效。
func TestEnsurePanelCertReuses(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, err := EnsurePanelCert(dir)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(certFile)

	certFile2, keyFile2, err := EnsurePanelCert(dir)
	if err != nil {
		t.Fatal(err)
	}
	if certFile2 != certFile || keyFile2 != keyFile {
		t.Fatal("路径变了")
	}
	after, _ := os.ReadFile(certFile)
	if string(before) != string(after) {
		t.Error("证书被重签了，所有已信任的设备都会失效")
	}
}

// 用户自己放的证书（商业证书 / IP 证书 / openssl 生成的）永远不能覆盖。
func TestEnsurePanelCertKeepsUserCert(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := PanelTLSPaths(dir)
	if err := os.MkdirAll(filepath.Dir(certFile), 0o700); err != nil {
		t.Fatal(err)
	}
	userCert, userKey := makeCert(t, "user.example.com", 200*24*time.Hour)
	if err := os.WriteFile(certFile, userCert, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, userKey, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := EnsurePanelCert(dir); err != nil {
		t.Fatalf("用户证书不该报错: %v", err)
	}
	after, _ := os.ReadFile(certFile)
	if string(after) != string(userCert) {
		t.Error("用户证书被覆盖了 —— 升级一次就把商业证书换成自签，属于最恶劣的破坏")
	}
}

// 自签证书临近到期（<30 天）时才重签，且重签结果仍带自签标记。
func TestEnsurePanelCertRenewsSoonExpiring(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := PanelTLSPaths(dir)
	_ = os.MkdirAll(filepath.Dir(certFile), 0o700)
	shortCert, shortKey := makeSelfSigned(t, 10*24*time.Hour)
	_ = os.WriteFile(certFile, shortCert, 0o644)
	_ = os.WriteFile(keyFile, shortKey, 0o600)
	_ = os.WriteFile(certFile+".selfsigned", []byte("self\n"), 0o644)

	if _, _, err := EnsurePanelCert(dir); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(certFile)
	leaf, err := parseLeaf(raw)
	if err != nil {
		t.Fatal(err)
	}
	if time.Until(leaf.NotAfter) < 300*24*time.Hour {
		t.Errorf("快过期的自签没有被重签，NotAfter=%v", leaf.NotAfter)
	}
	if !isSelfSigned(leaf) {
		t.Error("重签后丢了自签标记")
	}
}

func TestCertFingerprint(t *testing.T) {
	certPEM, _ := makeCert(t, "fp.example.com", time.Hour)
	fp := certFingerprint(certPEM)
	parts := strings.Split(fp, ":")
	if len(parts) != 32 {
		t.Fatalf("SHA-256 指纹应有 32 段，得到 %d: %s", len(parts), fp)
	}
	for _, p := range parts {
		if len(p) != 2 || strings.ToUpper(p) != p {
			t.Errorf("指纹格式不对: %q in %s", p, fp)
		}
	}
	if fp != certFingerprint(certPEM) {
		t.Error("同一证书两次计算的指纹不一致")
	}
}

// ---- 测试用证书 ----

func makeCert(t *testing.T, cn string, valid time.Duration) (certPEM, keyPEM []byte) {
	t.Helper()
	return buildCert(t, cn, nil, valid)
}

func makeSelfSigned(t *testing.T, valid time.Duration) (certPEM, keyPEM []byte) {
	t.Helper()
	return buildCert(t, "self", []string{panelSelfSignedOrg}, valid)
}

func buildCert(t *testing.T, cn string, org []string, valid time.Duration) (certPEM, keyPEM []byte) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn, Organization: org},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(valid),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{cn},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	kd, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd})
}
