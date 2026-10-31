package api

// 登录二次验证（2FA）：管理端点 + 邮箱验证码状态机。
// 支持两种验证方式（二选一，由 auth_2fa_mode 控制）：
//   - totp：验证器 App 动态口令（密钥存在 admin_users.totp_secret，**每账号一份**）
//   - email：登录时向后配置的邮箱发送 6 位验证码（5 分钟有效，面板级单槽位）
// 2FA 校验失败同样计入登录防爆破计数。
//
// 多管理员之后的分工：
//   - auth_2fa_mode 仍是面板级开关（开/关一次，全体生效）；
//   - TOTP 密钥是账号级的：没绑验证器的人登录时**跳过**这一步，
//     不会因为同事绑了 TOTP 就把自己锁在外面；
//   - 邮箱验证码是面板级单收件人，多账号共用——这适合「小团队共用一个
//     运维邮箱」的场景；要按人收信得给每个账号配邮箱，暂未做。

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	emailCodeTTL      = 5 * time.Minute
	emailCodeMaxTries = 5
)

// emailCodeMax 验证码取值范围 [0, 1000000)。
var emailCodeMax = big.NewInt(1000000)

// emailCodeState 登录邮箱验证码（单管理员，单槽位即可）。
type emailCodeState struct {
	mu    sync.Mutex
	code  string
	exp   time.Time
	tries int
}

// genEmailCode 生成 6 位数字验证码。
func genEmailCode() string {
	n, err := rand.Int(rand.Reader, emailCodeMax)
	if err != nil { // crypto/rand 失败极罕见；退化为时间熵
		return fmt.Sprintf("%06d", time.Now().UnixNano()%1000000)
	}
	return fmt.Sprintf("%06d", n.Int64())
}

// ---- 端点 ----

// handleAuth2FAStatus GET /api/auth2fa
func (a *Server) handleAuth2FAStatus(w http.ResponseWriter, r *http.Request) {
	mode, _ := a.Store.GetSetting("auth_2fa_mode")
	email, _ := a.Store.GetSetting("auth_email")
	smtp := a.loadSMTPConfig()
	u := a.currentUser(r)
	writeJSON(w, 200, map[string]any{
		"mode":       orDefault(mode, "off"),
		"totp_ready": u != nil && u.TotpSecret != "",
		"email":      email,
		"smtp_ready": smtp.ready(),
	})
}

// handleAuth2FATotpSetup POST /api/auth2fa/totp/setup
// 生成新密钥暂存（staged），用户在验证器中绑定后输入当前口令调用 enable 才会生效。
//
// 暂存放内存而不是 settings：它是**每个账号**的临时状态，落库既没必要
// （重启后重扫一次二维码就行）也会和「谁在绑定」耦合出一堆脏数据。
func (a *Server) handleAuth2FATotpSetup(w http.ResponseWriter, r *http.Request) {
	u := a.currentUser(r)
	if u == nil {
		fail(w, 401, "unauthorized")
		return
	}
	secret, err := generateTOTPSecret()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	a.totpStaged.Store(u.ID, secret)
	writeJSON(w, 200, map[string]string{"secret": secret, "uri": otpauthURI(secret, u.Username)})
}

// handleAuth2FATotpEnable POST /api/auth2fa/totp/enable  body: {"code":"123456"}
func (a *Server) handleAuth2FATotpEnable(w http.ResponseWriter, r *http.Request) {
	u := a.currentUser(r)
	if u == nil {
		fail(w, 401, "unauthorized")
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, err.Error())
		return
	}
	staged, _ := a.totpStaged.Load(u.ID)
	secret, _ := staged.(string)
	if secret == "" {
		fail(w, 400, "请先生成密钥并在验证器 App 中添加")
		return
	}
	if !verifyTOTP(secret, req.Code) {
		fail(w, 401, "动态口令不正确，请确认验证器已绑定且时间同步")
		return
	}
	u.TotpSecret = secret
	if err := a.Store.UpdateAdmin(u); err != nil {
		fail(w, 500, err.Error())
		return
	}
	a.totpStaged.Delete(u.ID)
	if err := a.Store.SetSetting("auth_2fa_mode", "totp"); err != nil {
		fail(w, 500, err.Error())
		return
	}
	log.Printf("[auth] %s 已启用 TOTP 二次验证", u.Username)
	a.auditOK(r, AuditAuth2FA, "totp", "启用 TOTP 二次验证")
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

// handleAuth2FAEmailEnable POST /api/auth2fa/email/enable
// 保存 SMTP 配置并立即发送一封验证邮件，发送成功才启用。
func (a *Server) handleAuth2FAEmailEnable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
		Host  string `json:"smtp_host"`
		Port  string `json:"smtp_port"`
		User  string `json:"smtp_user"`
		Pass  string `json:"smtp_pass"`
		From  string `json:"smtp_from"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, err.Error())
		return
	}
	req.Email = strings.TrimSpace(req.Email)
	if !strings.Contains(req.Email, "@") {
		fail(w, 400, "收件邮箱格式无效")
		return
	}
	cfg := smtpConfig{
		Host: strings.TrimSpace(req.Host), Port: strings.TrimSpace(req.Port),
		User: strings.TrimSpace(req.User), Pass: req.Pass,
		From: strings.TrimSpace(req.From),
	}
	if cfg.Port == "" {
		cfg.Port = "587"
	}
	if cfg.From == "" {
		cfg.From = cfg.User
	}
	if cfg.Pass == "" { // 密码留空 = 沿用已保存的授权码
		if old, _ := a.Store.GetSetting("smtp_pass"); old != "" {
			cfg.Pass = old
		}
	}
	if err := sendMail(cfg, req.Email, "Triune Panel 二次验证已启用",
		"这是一封测试邮件：您的邮箱验证码登录方式已启用。\n\n若非本人操作，请立即登录面板关闭二次验证。"); err != nil {
		fail(w, 422, "测试邮件发送失败："+err.Error())
		return
	}
	for k, v := range map[string]string{
		"auth_email": req.Email,
		"smtp_host":  cfg.Host, "smtp_port": cfg.Port,
		"smtp_user": cfg.User, "smtp_pass": cfg.Pass, "smtp_from": cfg.From,
	} {
		if err := a.Store.SetSetting(k, v); err != nil {
			fail(w, 500, err.Error())
			return
		}
	}
	if err := a.Store.SetSetting("auth_2fa_mode", "email"); err != nil {
		fail(w, 500, err.Error())
		return
	}
	log.Printf("[auth] 已启用邮箱验证码二次验证（%s）", maskEmail(req.Email))
	a.auditOK(r, AuditAuth2FA, "email", "启用邮箱验证码二次验证（"+maskEmail(req.Email)+"）")
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

// handleAuth2FAEmailTest POST /api/auth2fa/email/test
// 用「已保存」的 SMTP 配置发一封测试邮件（不改动配置）。
func (a *Server) handleAuth2FAEmailTest(w http.ResponseWriter, r *http.Request) {
	cfg := a.loadSMTPConfig()
	email, _ := a.Store.GetSetting("auth_email")
	if email == "" {
		fail(w, 400, "请先填写并保存收件邮箱")
		return
	}
	if err := sendMail(cfg, email, "Triune Panel 测试邮件",
		"SMTP 配置有效，验证码登录可以正常收信。"); err != nil {
		fail(w, 422, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

// handleAuth2FADisable POST /api/auth2fa/disable
//
// 关闭的是**面板级**的二次验证开关（auth_2fa_mode），同时清掉自己的 TOTP
// 密钥；其他管理员的密钥保留，重新开启后他们无需重新绑定。
func (a *Server) handleAuth2FADisable(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.SetSetting("auth_2fa_mode", "off"); err != nil {
		fail(w, 500, err.Error())
		return
	}
	if u := a.currentUser(r); u != nil {
		u.TotpSecret = ""
		if err := a.Store.UpdateAdmin(u); err != nil {
			log.Printf("[auth] 清空 %s 的 TOTP 密钥失败: %v", u.Username, err)
		}
		a.totpStaged.Delete(u.ID)
	}
	log.Printf("[auth] 已关闭二次验证")
	a.auditOK(r, AuditAuth2FA, "off", "关闭二次验证")
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

// ---- 登录流程集成 ----

// sendLoginCode 生成并发送登录验证码，返回打码后的收件地址。
func (a *Server) sendLoginCode(to string) (string, error) {
	cfg := a.loadSMTPConfig()
	code := genEmailCode()
	body := fmt.Sprintf("您的 Triune Panel 登录验证码：%s\n\n%d 分钟内有效。若非本人操作，请忽略本邮件并尽快修改面板密码。",
		code, int(emailCodeTTL.Minutes()))
	if err := sendMail(cfg, to, "Triune Panel 登录验证码", body); err != nil {
		return "", err
	}
	a.emailCode.mu.Lock()
	a.emailCode.code = code
	a.emailCode.exp = time.Now().Add(emailCodeTTL)
	a.emailCode.tries = 0
	a.emailCode.mu.Unlock()
	return maskEmail(to), nil
}

// checkEmailCode 校验验证码（一次性，限尝试次数）。
func (a *Server) checkEmailCode(code string) error {
	a.emailCode.mu.Lock()
	defer a.emailCode.mu.Unlock()
	if a.emailCode.code == "" || time.Now().After(a.emailCode.exp) {
		return fmt.Errorf("验证码已过期，请重新发送")
	}
	a.emailCode.tries++
	if a.emailCode.tries > emailCodeMaxTries {
		a.emailCode.code = ""
		return fmt.Errorf("验证码尝试次数过多，请重新发送")
	}
	if a.emailCode.code != strings.TrimSpace(code) {
		return fmt.Errorf("验证码不正确")
	}
	a.emailCode.code = "" // 消费掉：一次性使用
	return nil
}
