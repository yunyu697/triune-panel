// Package certmgr 基于 lego 的 ACME 证书自动签发与续期。
//
// 流程：读取设置 acme_enabled / acme_domain / acme_email / acme_dns_provider →
// 按需选择验证方式 →
//
//	HTTP-01（默认，不需要凭据）：lego 内置 standalone 服务临时占用 :80；
//	DNS-01（配置了 DNS 服务商时）：调服务商 API 写 _acme-challenge 记录，
//	         不占用任何入站端口，**泛域名只能用这一种**（Let's Encrypt 的规定）。
//
// → 证书落盘 data/certs/<slug>/{fullchain.pem,privkey.pem} →
// 每小时检查一次，剩余 <30 天自动续期。
//
// 用户把这**两个文件的路径**填进入站的 TLS 设置即可（证书 + 私钥），
// 也可以直接从面板的证书卡片里复制路径。
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
	"strings"
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
	// Domain 主域名（列表里的第一个），也是演示给用户的那一行。
	Domain string `json:"domain"`
	// Domains 这张证书里的全部名字（SAN）。泛域名场景通常是
	// example.com + *.example.com 两个。
	Domains []string `json:"domains,omitempty"`
	// Challenge http-01 / dns-01，用来解释「为什么这次失败了」。
	Challenge string `json:"challenge,omitempty"`
	// Provider DNS-01 的服务商 ID（http-01 时为空）。
	Provider  string    `json:"provider,omitempty"`
	Issuer    string    `json:"issuer,omitempty"`
	NotBefore time.Time `json:"not_before,omitempty"`
	NotAfter  time.Time `json:"not_after,omitempty"`
	Error     string    `json:"error,omitempty"`
	// File 证书所在目录（用户要复制的是它下面的 fullchain.pem / privkey.pem）。
	File string `json:"file"`
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

// check 一次完整检查：读设置 → 校验 → 需要就签发/续期 → 更新状态。
//
// 这里刻意把**所有失败都写进状态**而不是只打日志：ACME 的报错是英文、
// 而且经常是「域名解析没生效」「token 权限不够」这类外部原因，用户唯一
// 能看到它的地方就是证书卡片。日志在 systemd 里，多数人不会去看。
func (m *Manager) check() {
	en, _ := m.Store.Get("acme_enabled")
	if en != "1" {
		return
	}
	email, _ := m.Store.Get("acme_email")
	rawDomain, _ := m.Store.Get("acme_domain")
	providerID, _ := m.Store.Get(SettingDNSProvider)
	providerID = strings.TrimSpace(providerID)
	envRaw, _ := m.Store.Get(SettingDNSEnv)

	challenge := "http-01"
	var info DNSProviderInfo
	if providerID != "" {
		var ok bool
		info, ok = LookupDNSProvider(providerID)
		if !ok {
			m.setStatus(domainState{
				Domain: strings.TrimSpace(rawDomain), Challenge: "dns-01", Provider: providerID,
				Error: fmt.Sprintf("验证方式填错了：不认识的 DNS 服务商 %q（可选：%s）",
					providerID, strings.Join(DNSProviderIDs(), " / ")),
			})
			return
		}
		challenge = "dns-01"
	}

	domains, err := ParseDomains(rawDomain)
	if err != nil {
		m.setStatus(domainState{
			Domain: strings.TrimSpace(rawDomain), Challenge: challenge, Provider: providerID,
			Error: "域名填写有误：" + err.Error(),
		})
		return
	}
	// 泛域名 + HTTP-01 是协议层面不可能成功的组合（Let's Encrypt 硬性要求
	// DNS-01），提前说明比让用户等一分钟看英文报错强。
	if challenge == "http-01" && HasWildcard(domains) {
		m.setStatus(domainState{
			Domain: domains[0], Domains: domains, Challenge: challenge,
			Error: "泛域名证书只能用 DNS-01 签发（Let's Encrypt 的硬性规定）：" +
				"请在「验证方式」里选一个 DNS 服务商并填上凭据",
		})
		return
	}
	if email == "" {
		m.setStatus(domainState{
			Domain: domains[0], Domains: domains, Challenge: challenge, Provider: providerID,
			Error: "没有填邮箱（Let's Encrypt 用于证书到期通知与账户恢复）",
		})
		return
	}

	// DNS-01 的凭据先解析校验，别等到绑好客户端再报错。
	var env map[string]string
	if challenge == "dns-01" {
		env, err = ParseDNSEnv(envRaw, info)
		if err != nil {
			m.setStatus(domainState{
				Domain: domains[0], Domains: domains, Challenge: challenge, Provider: providerID,
				Error: info.Label + " 的凭据有误：" + err.Error(),
			})
			return
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	dir := CertDirFor(m.DataDir, domains)
	fullchain := filepath.Join(dir, "fullchain.pem")

	leaf, err := readLeafCert(fullchain)
	if err == nil && !needsRenew(leaf) {
		// 证书还新鲜：只刷新状态（否则面板重启后卡片是空的）。
		m.setStatusReady(domains, challenge, providerID, leaf, dir)
		return
	}
	if err == nil {
		log.Printf("[certmgr] 续期 %s（%s）...", strings.Join(domains, ","), challenge)
	}

	if err := m.obtain(domains, email, providerID, env); err != nil {
		log.Printf("[certmgr] %s 签发失败（%s）: %v%s", strings.Join(domains, ","), challenge, err, failHint(challenge))
		m.setStatus(domainState{
			Domain: domains[0], Domains: domains, Challenge: challenge, Provider: providerID,
			Error: err.Error(), File: dir,
		})
		return
	}
	if leaf, err := readLeafCert(fullchain); err == nil {
		m.setStatusReady(domains, challenge, providerID, leaf, dir)
	}
}

// failHint 失败时追加一句「最可能的原因」，因为 lego 的原始报错对外行不友好。
func failHint(challenge string) string {
	if challenge == "dns-01" {
		return "（DNS-01 常见原因：凭据权限不足、域名不在该服务商名下、" +
			"或 DNS 记录传播超过 2 分钟还没生效）"
	}
	return "（HTTP-01 需要面板能绑定 :80 端口，且域名此刻解析到本机）"
}

func (m *Manager) setStatusReady(domains []string, challenge, providerID string, leaf *x509.Certificate, dir string) {
	st := domainState{
		Domain:    domains[0],
		Domains:   domains,
		Challenge: challenge,
		Provider:  providerID,
		Issuer:    leaf.Issuer.CommonName,
		NotBefore: leaf.NotBefore,
		NotAfter:  leaf.NotAfter,
		File:      dir,
	}
	m.setStatus(st)
}

// needsRenew 剩余有效期不足 30 天就该续了。
//
// 30 天不是拍出来的：Let's Encrypt 证书 90 天有效期，官方建议在剩 30 天时
// 续期——留出足够多次重试机会，又不至于频繁触碰签发速率限制。
func needsRenew(leaf *x509.Certificate) bool {
	return time.Until(leaf.NotAfter) < 30*24*time.Hour
}

// readLeafCert 读 fullchain 的第一张证书。
func readLeafCert(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			return nil, fmt.Errorf("没有可解析的 CERTIFICATE 块")
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		return x509.ParseCertificate(block.Bytes)
	}
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
		row := map[string]any{
			"domain": s.Domain, "issuer": s.Issuer,
			"not_before": s.NotBefore, "not_after": s.NotAfter,
			"error": s.Error, "file": s.File,
			"challenge": s.Challenge,
		}
		if len(s.Domains) > 0 {
			row["domains"] = s.Domains
		}
		if s.Provider != "" {
			row["provider"] = s.Provider
		}
		// 证书文件路径直接给出：用户要把它填进入站的 TLS 设置，
		// 让他自己拼 data/certs/<domain>/fullchain.pem 很容易漏一层目录。
		if s.File != "" {
			row["cert_path"] = filepath.Join(s.File, "fullchain.pem")
			row["key_path"] = filepath.Join(s.File, "privkey.pem")
		}
		out = append(out, row)
	}
	return out
}

// obtain 真正去签发。
//
// 顺序上先绑 challenge 再注册账户：注册失败时至少能看到「凭据配错了」
// 而不是一句 registration failed。
func (m *Manager) obtain(domains []string, email, providerID string, env map[string]string) error {
	cfg := lego.NewConfig(m.accountFor(email))
	cfg.CADirURL = lego.LEDirectoryProduction
	cfg.Certificate.KeyType = certcrypto.EC256
	client, err := lego.NewClient(cfg)
	if err != nil {
		return err
	}

	if providerID == "" {
		// HTTP-01：lego 在验证期间自行绑定 :80（standalone）
		if err := client.Challenge.SetHTTP01Provider(http01.NewProviderServer("", "80")); err != nil {
			return fmt.Errorf("配置 HTTP-01 验证失败: %w", err)
		}
	} else {
		// 凭据进环境变量，用完立刻还原：换服务商之后旧 token 不该继续留在
		// 进程环境里，否则下次签发可能拿着上一个账号的钥匙去改这个域名。
		restore := applyDNSEnv(env)
		defer restore()
		p, err := newDNSProvider(providerID)
		if err != nil {
			return err
		}
		if err := client.Challenge.SetDNS01Provider(p); err != nil {
			return fmt.Errorf("配置 DNS-01 验证失败: %w", err)
		}
	}

	if m.account.Registration == nil {
		reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
		if err != nil {
			return fmt.Errorf("注册 ACME 账户: %w", err)
		}
		m.account.Registration = reg
	}

	res, err := client.Certificate.Obtain(certificate.ObtainRequest{
		Domains: domains, Bundle: true,
	})
	if err != nil {
		return err
	}

	dir := CertDirFor(m.DataDir, domains)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "fullchain.pem"), res.Certificate, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "privkey.pem"), res.PrivateKey, 0o600); err != nil {
		return err
	}
	log.Printf("[certmgr] %s 证书已签发（%s）: %s", strings.Join(domains, ","), providerIDOr(providerID), dir)
	return nil
}

func providerIDOr(id string) string {
	if id == "" {
		return "http-01"
	}
	return "dns-01/" + id
}

// accountFor 取（或首次创建）ACME 账户。
//
// 账户密钥必须持久化：每次注册新账户会消耗 Let's Encrypt 的账户额度，
// 而且旧证书会挂在一个已经不存在的账户下，续期必须用同一个账户。
func (m *Manager) accountFor(email string) *acmeUser {
	keyPath := filepath.Join(m.DataDir, "acme", "account.key")
	if m.account == nil {
		user := &acmeUser{Email: email}
		if data, err := os.ReadFile(keyPath); err == nil {
			if key, err := certcrypto.ParsePEMPrivateKey(data); err == nil {
				user.key = key
			}
		}
		if user.key == nil {
			key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				return user // 交给后续步骤报错
			}
			user.key = key
			if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err == nil {
				_ = os.WriteFile(keyPath, certcrypto.PEMEncode(key), 0o600)
			}
		}
		m.account = user
	}
	return m.account
}
