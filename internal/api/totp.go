package api

// TOTP 动态口令（RFC 6238）：SHA1 / 6 位 / 30 秒步长，兼容
// Google Authenticator、Microsoft Authenticator、1Password、FreeOTP 等
// 常用验证器 App（手动输入密钥或扫描 otpauth:// 二维码均可）。
// 无外部依赖，密钥为 base32（无填充）编码。

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	totpPeriod = 30 * time.Second
	totpDigits = 6
	totpWindow = 1 // 允许前后各 1 个周期（应对设备时钟偏差）
)

// generateTOTPSecret 生成 160 位随机密钥，返回 base32（无填充、大写）。
func generateTOTPSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b), nil
}

// normalizeSecret 去空格、去连字符并统一大写，兼容用户手动输入时的格式差异。
func normalizeSecret(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "-", "")
	return s
}

func decodeSecret(secret string) ([]byte, error) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(normalizeSecret(secret))
	if err != nil || len(key) == 0 {
		return nil, errors.New("密钥格式无效（应为 base32）")
	}
	return key, nil
}

// totpAt 计算指定时刻的 6 位动态口令。
func totpAt(key []byte, t time.Time) string {
	counter := uint64(t.Unix()) / 30 //nolint:gosec // 2106 年前不会溢出
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(buf[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	code := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, code%1000000)
}

// verifyTOTP 校验动态口令，允许 ±1 个周期的时钟偏差。
func verifyTOTP(secret, code string) bool {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return false
		}
	}
	key, err := decodeSecret(secret)
	if err != nil {
		return false
	}
	now := time.Now()
	for i := -totpWindow; i <= totpWindow; i++ {
		want := totpAt(key, now.Add(time.Duration(i)*totpPeriod))
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

// otpauthURI 生成验证器 App 可识别的绑定 URI。
func otpauthURI(secret, account string) string {
	if account == "" {
		account = "admin"
	}
	u := url.URL{
		Scheme: "otpauth",
		Host:   "totp",
		Path:   "/TriunePanel:" + account,
	}
	q := url.Values{}
	q.Set("secret", normalizeSecret(secret))
	q.Set("issuer", "TriunePanel")
	q.Set("algorithm", "SHA1")
	q.Set("digits", "6")
	q.Set("period", "30")
	u.RawQuery = q.Encode()
	return u.String()
}
