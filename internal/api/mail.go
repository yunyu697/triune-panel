package api

// SMTP 邮件发送：用于登录邮箱验证码。
// 支持两种端口模式：
//   - 465：隐式 TLS（tls.Dial 直连）
//   - 其他（常见 587/25）：STARTTLS（服务器支持时自动升级加密）
// 使用 net/smtp 标准库，无外部依赖。

import (
	"crypto/tls"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

type smtpConfig struct {
	Host string // 如 smtp.qq.com
	Port string // 465 / 587 / 25
	User string
	Pass string // 授权码或登录密码
	From string // 发件人，留空则用 User
}

func (a *Server) loadSMTPConfig() smtpConfig {
	get := func(k string) string { v, _ := a.Store.GetSetting(k); return strings.TrimSpace(v) }
	c := smtpConfig{
		Host: get("smtp_host"),
		Port: get("smtp_port"),
		User: get("smtp_user"),
		Pass: get("smtp_pass"),
		From: get("smtp_from"),
	}
	if c.Port == "" {
		c.Port = "587"
	}
	if c.From == "" {
		c.From = c.User
	}
	return c
}

func (c smtpConfig) ready() bool { return c.Host != "" && c.From != "" }

// sendMail 发送一封纯文本邮件。subject/body 支持 UTF-8 中文。
func sendMail(c smtpConfig, to, subject, body string) error {
	if !c.ready() {
		return fmt.Errorf("SMTP 未配置（需要 smtp_host 与发件人）")
	}
	if to == "" {
		return fmt.Errorf("收件邮箱未配置")
	}

	from := c.From
	host, _, _ := net.SplitHostPort(c.Host + ":" + c.Port)
	msg := strings.Join([]string{
		"From: " + from,
		"To: " + to,
		"Subject: " + mime.QEncoding.Encode("utf-8", subject),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"Date: " + time.Now().Format(time.RFC1123Z),
		"",
		body,
	}, "\r\n")

	var auth smtp.Auth
	if c.User != "" && c.Pass != "" {
		auth = smtp.PlainAuth("", c.User, c.Pass, host)
	}

	port, _ := strconv.Atoi(c.Port)
	if port == 465 {
		return sendMailImplicitTLS(c, host, auth, from, to, []byte(msg))
	}
	addr := net.JoinHostPort(host, c.Port)
	// net/smtp 的 SendMail 在服务器支持 STARTTLS 时自动升级；
	// PlainAuth 只在加密连接上发送凭据，明文 25 端口无 TLS 时会拒绝认证。
	return smtp.SendMail(addr, auth, from, []string{to}, []byte(msg))
}

func sendMailImplicitTLS(c smtpConfig, host string, auth smtp.Auth, from, to string, msg []byte) error {
	d := net.Dialer{Timeout: 15 * time.Second}
	conn, err := d.Dial("tcp", net.JoinHostPort(host, c.Port))
	if err != nil {
		return fmt.Errorf("连接 SMTP 服务器失败: %w", err)
	}
	tlsConn := tls.Client(conn, &tls.Config{ServerName: host})
	cl, err := smtp.NewClient(tlsConn, net.JoinHostPort(host, c.Port))
	if err != nil {
		conn.Close()
		return fmt.Errorf("SMTP 握手失败: %w", err)
	}
	defer cl.Close()
	if err := cl.Hello(host); err != nil {
		return fmt.Errorf("SMTP HELLO 失败: %w", err)
	}
	if auth != nil {
		if ok, _ := cl.Extension("AUTH"); ok {
			if err := cl.Auth(auth); err != nil {
				return fmt.Errorf("SMTP 认证失败: %w", err)
			}
		}
	}
	if err := cl.Mail(from); err != nil {
		return fmt.Errorf("MAIL FROM 失败: %w", err)
	}
	if err := cl.Rcpt(to); err != nil {
		return fmt.Errorf("收件人被拒绝: %w", err)
	}
	w, err := cl.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return cl.Quit()
}

// maskEmail 打码邮箱：a***b@example.com。
func maskEmail(addr string) string {
	at := strings.LastIndex(addr, "@")
	if at <= 0 {
		return addr
	}
	local := addr[:at]
	domain := addr[at:]
	r := []rune(local)
	keep := 1
	if len(r) > 3 {
		keep = 2
	}
	return string(r[:keep]) + "***" + domain
}
