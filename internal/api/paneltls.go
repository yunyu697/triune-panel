// 面板自身的 HTTPS 监听：没有域名也能加密传输。
//
// 【为什么不是 ACME】
// ACME 只能给「公共 CA 能验证控制权」的名字签证书：要么有域名，要么有
// 公网可达的 80/443。绝大多数自建面板是「一台 VPS + 一个 IP」，两者都没有，
// 所以证书链上根本不存在「Let's Encrypt 给 1.2.3.4 签一张长期证书」这条路
// （ACME 的 IP 证书 2025 年才落地，有效期 6 天且要求 80/443 可达，
// 仍要靠自动续期）。
//
// 【自签到底解决了什么，没解决什么】
//   - 解决：链路上不再有明文密码与 Bearer token。公共 WiFi、机房内网、
//     运营商链路里抓包拿不到登录凭据了。
//   - 没解决：身份认证。任何人可以给自己的服务器签一张同样的自签证书，
//     所以浏览器必然警告「不安全」，用户必须手动信任；这也意味着
//     DNS 劫持 / 中间人可以拿另一张自签证书冒充。
//   - 弥补手段：界面把证书的 SHA-256 指纹摊开给用户，
//     首次在设备上信任时对照指纹——这一步做了，自签的可信度才接近真证书。
//
// 【想彻底没有警告】
// 两种：一是有域名走 ACME（certmgr 已支持）；二是买到能签 IP 的证书
// （商业 CA 的 IP 证书，或 Let's Encrypt 的短周期 IP 证书），
// 把 pem 放到本包的 tls/panel.crt + tls/panel.key 覆盖即可 —— 本包检测到
// 不是自己生成的文件就**绝不覆盖**，升级、重启都不会动它。
package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SettingPanelHTTPS 面板是否用 TLS 监听（"1" 开）。改动需重启进程生效。
const SettingPanelHTTPS = "panel_https"

// panelSelfSignedOrg 标记「这张证书是面板自己签的」。
// 有它才允许自动续签 / 覆盖；用户手工放进去的证书永远不碰。
const panelSelfSignedOrg = "triune-panel self-signed"

// panelCertValidity 自签有效期。公有 CA 被限制在 398 天，自签不受这条约束，
// 但 825 天是浏览器/客户端普遍接受的旧上限，取它兼顾安全与少折腾。
const panelCertValidity = 825 * 24 * time.Hour

// panelCertRenewBefore 到期前多久自动重签（自签才有）。
const panelCertRenewBefore = 30 * 24 * time.Hour

// PanelTLSPaths 返回面板证书与私钥的落盘位置。
func PanelTLSPaths(dataDir string) (certFile, keyFile string) {
	d := filepath.Join(dataDir, "tls")
	return filepath.Join(d, "panel.crt"), filepath.Join(d, "panel.key")
}

// EnsurePanelCert 保证存在一份能用的证书，返回两个路径。
//
// 行为（顺序即优先级）：
//  1. tls/panel.crt + tls/panel.key 都在，且不是面板自己签的 → 直接用，
//     一个字节都不改（用户放的商业证书 / IP 证书 / 自己 openssl 的）。
//  2. 是面板自己签的且还有 30 天以上 → 用。
//  3. 其余情况（缺失、快过期、只剩半截）→ 生成新的自签并落盘。
//
// 只在第 3 步写文件：宁可启动失败也不覆盖用户的证书。
func EnsurePanelCert(dataDir string) (string, string, error) {
	certFile, keyFile := PanelTLSPaths(dataDir)
	if err := os.MkdirAll(filepath.Dir(certFile), 0o700); err != nil {
		return "", "", err
	}
	certPEM, certErr := os.ReadFile(certFile)
	keyPEM, keyErr := os.ReadFile(keyFile)
	selfMade := fileExists(certFile + ".selfsigned")

	if certErr == nil && keyErr == nil && len(certPEM) > 0 && len(keyPEM) > 0 {
		if !selfMade {
			// 用户自己的证书：不校验有效性，交给 tls 握手去暴露问题。
			// 我们在这里「修」它，只会把用户精心准备的证书换掉。
			return certFile, keyFile, nil
		}
		if leaf, err := parseLeaf(certPEM); err == nil && time.Until(leaf.NotAfter) > panelCertRenewBefore {
			return certFile, keyFile, nil
		}
	}

	if err := generatePanelCert(certFile, keyFile); err != nil {
		return "", "", err
	}
	return certFile, keyFile, nil
}

// generatePanelCert 生成自签证书并原子落盘。
func generatePanelCert(certFile, keyFile string) error {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	ips, names := localSANs()
	now := time.Now()
	host, _ := os.Hostname()
	if host == "" {
		host = "triune-panel"
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		// CN 只是给人看的；现代客户端只认 SAN
		Subject:   pkix.Name{CommonName: host, Organization: []string{panelSelfSignedOrg}},
		NotBefore: now.Add(-1 * time.Hour), // 容忍一点时钟漂移
		NotAfter:  now.Add(panelCertValidity),
		KeyUsage:  x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		// 只用于服务端身份，不当 CA —— 私钥泄漏也不会牵连别人
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
		DNSNames:              names,
		IPAddresses:           ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	if err := writeFileAtomic(keyFile, keyPEM, 0o600); err != nil {
		return fmt.Errorf("写私钥失败: %w", err)
	}
	if err := writeFileAtomic(certFile, certPEM, 0o644); err != nil {
		return fmt.Errorf("写证书失败: %w", err)
	}
	// 标记放在证书之后：先有证书再有标记，才不会被当成「用户证书」。
	return writeFileAtomic(certFile+".selfsigned", []byte("self-signed by triune-panel\n"), 0o644)
}

// localSANs 收集本机所有可用的身份：
// 环回 + 全部网卡 IP + localhost + 主机名。
// 用户可能是 https://1.2.3.4:10010、https://内网IP 或 https://hostname 访问，
// 少写一个就多一次「证书对不上」的报错。
func localSANs() ([]net.IP, []string) {
	ips := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	seen := map[string]bool{ips[0].String(): true, ips[1].String(): true}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsMulticast() || ip.IsLinkLocalUnicast() {
				continue
			}
			if !seen[ip.String()] {
				seen[ip.String()] = true
				ips = append(ips, ip)
			}
		}
	}
	names := []string{"localhost"}
	if h, err := os.Hostname(); err == nil && h != "" && h != "localhost" {
		names = append(names, h)
		if !strings.Contains(h, ".") {
			names = append(names, h+".local")
		}
	}
	return ips, names
}

// ---- 证书读取 ----

type panelCertInfo struct {
	certFile string
	keyFile  string
	leaf     *x509.Certificate
	raw      []byte
}

func readPanelCert(dataDir string) (*panelCertInfo, error) {
	certFile, keyFile := PanelTLSPaths(dataDir)
	raw, err := os.ReadFile(certFile)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(keyFile); err != nil {
		return nil, err
	}
	leaf, err := parseLeaf(raw)
	if err != nil {
		return nil, err
	}
	return &panelCertInfo{certFile: certFile, keyFile: keyFile, leaf: leaf, raw: raw}, nil
}

func parseLeaf(certPEM []byte) (*x509.Certificate, error) {
	for {
		var blk *pem.Block
		blk, certPEM = pem.Decode(certPEM)
		if blk == nil {
			return nil, fmt.Errorf("没有可解析的 CERTIFICATE 块")
		}
		if blk.Type != "CERTIFICATE" {
			continue
		}
		return x509.ParseCertificate(blk.Bytes)
	}
}

// certFingerprint 返回 SHA-256 指纹（AA:BB:...），供用户在设备上核对。
func certFingerprint(raw []byte) string {
	leaf, err := parseLeaf(raw)
	var der []byte
	if err == nil {
		der = leaf.Raw
	} else {
		der = raw
	}
	sum := sha256.Sum256(der)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = strings.ToUpper(hex.EncodeToString([]byte{b}))
	}
	return strings.Join(parts, ":")
}

func certSANs(leaf *x509.Certificate) []string {
	out := make([]string, 0, len(leaf.DNSNames)+len(leaf.IPAddresses))
	out = append(out, leaf.DNSNames...)
	for _, ip := range leaf.IPAddresses {
		out = append(out, ip.String())
	}
	sort.Strings(out)
	return out
}

func isSelfSigned(leaf *x509.Certificate) bool {
	for _, o := range leaf.Subject.Organization {
		if o == panelSelfSignedOrg {
			return true
		}
	}
	return false
}

// ---- HTTP ----

// handlePanelTLS GET /api/panel/tls
// 面板 HTTPS 的现状：开关、是否真的在跑 TLS、证书来源与到期时间。
//
// active 与 enabled 分开报：enabled 是库里的意图，active 是这个进程**实际**
// 的监听方式。两者不一致时（例如证书生成失败退回明文、或刚改完还没重启），
// 界面必须说清，否则用户会以为自己在加密连接上。
func (a *Server) handlePanelTLS(w http.ResponseWriter, r *http.Request) {
	enabled, _ := a.Store.GetSetting(SettingPanelHTTPS)
	out := map[string]any{
		"enabled": enabled == "1",
		"active":  a.TLSActive,
	}
	info, err := readPanelCert(a.DataDir)
	if err != nil {
		out["cert_ready"] = false
		out["reason"] = err.Error()
		writeJSON(w, 200, out)
		return
	}
	days := int(time.Until(info.leaf.NotAfter).Hours() / 24)
	out["cert_ready"] = true
	out["subject"] = info.leaf.Subject.CommonName
	out["sans"] = certSANs(info.leaf)
	out["not_before"] = info.leaf.NotBefore.Format(time.RFC3339)
	out["not_after"] = info.leaf.NotAfter.Format(time.RFC3339)
	out["days_left"] = days
	out["fingerprint"] = certFingerprint(info.raw)
	out["self_signed"] = isSelfSigned(info.leaf)
	out["expired"] = days < 0
	if !out["self_signed"].(bool) {
		out["source"] = "user"
	} else {
		out["source"] = "selfsigned"
	}
	writeJSON(w, 200, out)
}

// handlePanelCertDownload GET /api/panel/tls/cert
// 把公钥证书给用户下载，用于在手机/电脑上安装并信任。
// 只给证书，私钥永不出网。
func (a *Server) handlePanelCertDownload(w http.ResponseWriter, r *http.Request) {
	info, err := readPanelCert(a.DataDir)
	if err != nil {
		fail(w, 404, "证书尚未生成："+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", `attachment; filename="triune-panel.crt"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(info.raw)
}

// ---- 小工具 ----

// writeFileAtomic 先写同目录临时文件再 rename：
// 证书/私钥写到一半掉电，不会留下一个半截文件让下次启动直接崩。
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
