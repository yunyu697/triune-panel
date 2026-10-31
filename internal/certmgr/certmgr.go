// Package certmgr 基于 lego 的 ACME 证书自动签发与续期。
//
// 流程：读取设置 acme_enabled/acme_domain/acme_email →
// HTTP-01 验证（lego 内置 standalone 服务临时占用 :80）→
// 证书落盘 data/certs/<domain>/{fullchain.pem,privkey.pem} →
// 每日检查，剩余 <30 天自动续期。
// 用户在入站 Settings 里引用这两个文件路径即可。
package certmgr

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge/http01"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/registration"
)

// SettingsReader 设置读取接口。
type SettingsReader interface {
	Get(key string) (string, error)
}

type acmeUser struct {
	Email        string
	Registration *registration.Resource
	key          crypto.PrivateKey
}

func (u *acmeUser) GetEmail() string                        { return u.Email }
func (u *acmeUser) GetRegistration() *registration.Resource { return u.Registration }
func (u *acmeUser) GetPrivateKey() crypto.PrivateKey        { return u.key }

type domainState struct {
	Domain    string    `json:"domain"`
	Issuer    string    `json:"issuer,omitempty"`
	NotBefore time.Time `json:"not_before,omitempty"`
	NotAfter  time.Time `json:"not_after,omitempty"`
	Error     string    `json:"error,omitempty"`
	File      string    `json:"file"`
}

type Manager struct {
	DataDir string
	Store   SettingsReader

	mu      sync.Mutex
	status  []domainState
	account *acmeUser
}

func New(dataDir string, st SettingsReader) *Manager {
	return &Manager{DataDir: dataDir, Store: st}
}

func (m *Manager) certDir(domain string) string {
	return filepath.Join(m.DataDir, "certs", domain)
}

// Run 阻塞运行：每小时检查一次设置与证书状态。
func (m *Manager) Run() {
	m.check()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		m.check()
	}
}

// CheckNow 手动触发一次签发/续期检查（供 API 调用，可并发）。
func (m *Manager) CheckNow() { m.check() }

func (m *Manager) check() {
	en, _ := m.Store.Get("acme_enabled")
	if en != "1" {
		return
	}
	domain, _ := m.Store.Get("acme_domain")
	email, _ := m.Store.Get("acme_email")
	if domain == "" || email == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	fullchain := filepath.Join(m.certDir(domain), "fullchain.pem")
	if _, err := os.Stat(fullchain); err == nil {
		// 已有证书：检查剩余有效期
		if !m.needsRenew(fullchain) {
			return
		}
		log.Printf("[certmgr] 续期 %s ...", domain)
	}
	if err := m.obtain(domain, email); err != nil {
		log.Printf("[certmgr] %s 签发失败: %v（注意 HTTP-01 需要面板可绑定 :80 端口且域名解析到本机）", domain, err)
		st := domainState{Domain: domain, Error: err.Error(),
			File: filepath.Join(m.certDir(domain))}
		m.setStatus(st)
	}
}

func (m *Manager) needsRenew(fullchain string) bool {
	data, err := os.ReadFile(fullchain)
	if err != nil {
		return true
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return true
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return true
	}
	m.updateStatus(domainOf(cert), cert)
	return time.Until(cert.NotAfter) < 30*24*time.Hour
}

func domainOf(cert *x509.Certificate) string {
	if len(cert.DNSNames) > 0 {
		return cert.DNSNames[0]
	}
	return cert.Subject.CommonName
}

func (m *Manager) updateStatus(domain string, cert *x509.Certificate) {
	st := domainState{
		Domain:    domain,
		NotBefore: cert.NotBefore,
		NotAfter:  cert.NotAfter,
		Issuer:    cert.Issuer.CommonName,
		File:      m.certDir(domain),
	}
	m.setStatus(st)
}

func (m *Manager) setStatus(st domainState) {
	for i, s := range m.status {
		if s.Domain == st.Domain {
			m.status[i] = st
			return
		}
	}
	m.status = append(m.status, st)
}

// Status 供 API 查询。
func (m *Manager) Status() []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []map[string]any{}
	for _, s := range m.status {
		out = append(out, map[string]any{
			"domain": s.Domain, "issuer": s.Issuer,
			"not_before": s.NotBefore, "not_after": s.NotAfter,
			"error": s.Error, "file": s.File,
		})
	}
	return out
}

func (m *Manager) obtain(domain, email string) error {
	// ACME 账户密钥持久化，避免重复注册
	keyPath := filepath.Join(m.DataDir, "acme", "account.key")
	if m.account == nil {
		user := &acmeUser{Email: email}
		if data, err := os.ReadFile(keyPath); err == nil {
			key, err := certcrypto.ParsePEMPrivateKey(data)
			if err == nil {
				user.key = key
			}
		}
		if user.key == nil {
			key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				return err
			}
			user.key = key
			_ = os.MkdirAll(filepath.Dir(keyPath), 0o700)
			_ = os.WriteFile(keyPath, certcrypto.PEMEncode(key), 0o600)
		}
		m.account = user
	}

	cfg := lego.NewConfig(m.account)
	cfg.CADirURL = lego.LEDirectoryProduction
	cfg.Certificate.KeyType = certcrypto.EC256
	client, err := lego.NewClient(cfg)
	if err != nil {
		return err
	}
	// HTTP-01：lego 在验证期间自行绑定 :80（standalone）
	if err := client.Challenge.SetHTTP01Provider(http01.NewProviderServer("", "80")); err != nil {
		return fmt.Errorf("配置 HTTP-01 验证失败: %w", err)
	}
	if m.account.Registration == nil {
		reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
		if err != nil {
			return fmt.Errorf("注册 ACME 账户: %w", err)
		}
		m.account.Registration = reg
	}

	res, err := client.Certificate.Obtain(certificate.ObtainRequest{
		Domains: []string{domain}, Bundle: true,
	})
	if err != nil {
		return err
	}

	dir := m.certDir(domain)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "fullchain.pem"), res.Certificate, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "privkey.pem"), res.PrivateKey, 0o600); err != nil {
		return err
	}
	if block, _ := pem.Decode(res.Certificate); block != nil {
		if cert, err := x509.ParseCertificate(block.Bytes); err == nil {
			m.updateStatus(domain, cert)
		}
	}
	log.Printf("[certmgr] %s 证书已签发: %s", domain, dir)
	return nil
}
