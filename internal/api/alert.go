package api

// 流量告警邮件：collector（ops 包）发现用户用量越过阈值时回调，
// 通过面板已配置的 SMTP 通道发送到 auth_email。
// ops 包不能反向依赖 api（会循环引用），因此以函数注入的方式接线（见 main.go）。

import (
	"log"
	"strings"

	"triune/internal/store"
)

// SendPanelAlert 供 ops.Collector 注入使用：读取 SMTP 与收件邮箱设置并发送告警。
func SendPanelAlert(st *store.Store, subject, body string) {
	to, _ := st.GetSetting("auth_email")
	if strings.TrimSpace(to) == "" {
		return // 未配置收件邮箱则静默跳过
	}
	cfg := smtpConfigFromStore(st)
	if err := sendMail(cfg, to, subject, body); err != nil {
		log.Printf("[alert] 发送流量告警失败: %v", err)
	}
}

// smtpConfigFromStore 与 Server.loadSMTPConfig 同逻辑（独立函数供包外注入使用）。
func smtpConfigFromStore(st *store.Store) smtpConfig {
	get := func(k string) string { v, _ := st.GetSetting(k); return strings.TrimSpace(v) }
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
