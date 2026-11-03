package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"triune/internal/authn"
	"triune/internal/core"
	"triune/internal/model"
)

// ---- settings ----

// ShowCompatCoreSetting 控制兼容内核（mihomo）是否出现在「新建入站」的内核
// 下拉里，默认关闭。
//
// mihomo 支持的协议是 sing-box 的**真子集**，且缺 vless / reality —— 也就是说
// 选它不是「另一种选择」，而是纯降级。它在面板里的唯一真实价值是存量接管：
// 机器上已经在跑 mihomo 的人可以原地纳管，不必推翻重配。
//
// 所以默认把它从新建入口里摘掉（避免用户选到一个能力更弱的选项还不知道），
// 但**不删**：已有 mihomo 入站照常渲染与运行，老用户升级后配置不受影响。
const ShowCompatCoreSetting = "show_compat_core"

// externalInSubSetting 外部节点是否混入订阅输出（默认关）。
const externalInSubSetting = "external_in_sub"

// SettingRestartFailed 自更新「二进制换了但重启失败」的说明，空值表示没发生过。
//
// 只在 exec 失败时写入，进程下次成功启动时清掉（见 cmd/server/main.go）——
// 新进程起来了就说明重启这条路是通的，旧记录只会误导。
const SettingRestartFailed = "update_restart_failed"

// compatCoreAllowed 判断当前设置下能否新建指定内核的入站。
// 非兼容内核恒为 true；mihomo 只有显式打开开关才允许。
func (a *Server) compatCoreAllowed(core string) bool {
	if core != model.CoreMihomo {
		return true
	}
	v, _ := a.Store.GetSetting(ShowCompatCoreSetting)
	return v == "1"
}

func (a *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	out := map[string]string{}
	for _, c := range core.All() {
		v, _ := a.Store.GetSetting("bin_" + c.Name())
		out["bin_"+c.Name()] = v
	}
	for _, k := range []string{"public_host", "stats_enabled", "stats_secret",
		"quota_warn_pct", "fw_auto",
		// Prometheus 指标令牌（空 = /metrics 关闭）
		MetricsTokenSetting,
		// 可信反向代理网段（登录来源识别用，默认空 = 不信任任何代理）
		TrustedProxiesSetting,
		"acme_enabled", "acme_domain", "acme_email",
		"gh_mirror", "gh_api_base", "panel_repo", "panel_update_index",
		// 2FA 状态展示（totp_secret 不下发前端）
		"auth_2fa_mode", "auth_email",
		"smtp_host", "smtp_port", "smtp_user", "smtp_from",
		// 变更通知：Webhook 地址可能带 token，但它是「自己填给自己看」的配置，
		// 只读角色拿不到这个接口（write 档），所以照常回显便于核对。
		NotifyEmailSetting, NotifyWebhookSetting, NotifyEventsSetting,
		// 外部节点是否混进所有用户的订阅（默认关：见 subscription.go 的说明）
		externalInSubSetting,
		// 兼容内核（mihomo）是否出现在「新建入站」的内核下拉里（默认关）
		ShowCompatCoreSetting,
		// 磁盘上的二进制变了是否自动 exec 过去（默认开，见 watch.go）
		AutoSwitchSetting} {
		v, _ := a.Store.GetSetting(k)
		out[k] = v
	}
	writeJSON(w, 200, out)
}

func (a *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var m map[string]string
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		fail(w, 400, err.Error())
		return
	}
	// 改密单独处理：只改**当前登录账号**的密码（多管理员之后不存在
	// 「全局面板密码」这种东西），密码不走白名单直写。
	// 改完当前会话因指纹变化失效，前端收到 relogin 后回登录页。
	relogin := false
	if pass, ok := m["admin_pass"]; ok {
		delete(m, "admin_pass")
		if pass != "" {
			u := a.currentUser(r)
			if u == nil {
				fail(w, 401, "unauthorized")
				return
			}
			if err := authn.SetPasswordFor(a.Store, u.ID, pass); err != nil {
				fail(w, 400, err.Error())
				return
			}
			// 主动清理该账号会话（指纹校验是兜底，这里让「登录设备」页立刻准确）
			a.sessions.DeleteByUID(u.ID)
			relogin = true
		}
	}
	allowed := map[string]bool{
		"public_host": true, "stats_enabled": true, "stats_secret": true,
		"quota_warn_pct": true,
		// Prometheus 指标令牌：写空字符串即关闭 /metrics
		MetricsTokenSetting: true,
		// 入站端口的自动防火墙管理（fwAuto 读取，默认开启）
		"fw_auto": true,
		// 可信反向代理网段：仅当直连对端落在这些网段内才采信转发头
		TrustedProxiesSetting: true,
		"acme_enabled":        true, "acme_domain": true, "acme_email": true,
		// 组件更新相关（UI「保存更新设置」提交这些键）
		"gh_mirror": true, "gh_api_base": true,
		"panel_repo": true, "panel_update_index": true,
		// 2FA 邮箱通道（auth_2fa_mode / totp_secret 由专用端点管理，不接受直接写）
		"auth_email": true,
		"smtp_host":  true, "smtp_port": true, "smtp_user": true,
		"smtp_pass": true, "smtp_from": true,
		// 变更通知（收件邮箱 / Webhook / 订阅哪些事件）
		NotifyEmailSetting: true, NotifyWebhookSetting: true, NotifyEventsSetting: true,
		// 外部节点是否混进所有用户的订阅（默认关）
		externalInSubSetting: true,
		// 兼容内核（mihomo）是否出现在「新建入站」的内核下拉里（默认关）
		ShowCompatCoreSetting: true,
		// 磁盘上的二进制变了是否自动 exec 过去（默认开）
		AutoSwitchSetting: true,
	}
	for _, c := range core.All() {
		allowed["bin_"+c.Name()] = true
	}
	// 可信代理网段先整体校验再落库，避免半套配置写进去（写一半失败时
	// 用户看到的是 500，但设置里已经躺着一条错值）。
	if v, ok := m[TrustedProxiesSetting]; ok {
		if _, err := parseTrustedProxies(v); err != nil {
			fail(w, 400, "可信反向代理填写有误："+err.Error())
			return
		}
	}
	var changedKeys []string
	var diff []string
	for k, v := range m {
		if !allowed[k] {
			continue
		}
		// 先读旧值：变更通知要说清「从什么改成什么」，只报键名等于让人
		// 收到一封「你改了 public_host」却不知道改成了什么的邮件。
		old, _ := a.Store.GetSetting(k)
		if err := a.Store.SetSetting(k, v); err != nil {
			fail(w, 500, err.Error())
			return
		}
		changedKeys = append(changedKeys, k)
		if old != v {
			diff = append(diff, settingDiff(k, old, v))
		}
	}
	// 审计只记「改了哪些键」，不记值——smtp_pass / stats_secret 这类敏感项
	// 一旦进了日志就等于明文落盘，比不记更糟。
	// 通知里的 diff 走 settingDiff，敏感键自动打码。
	if len(changedKeys) > 0 {
		sort.Strings(changedKeys)
		a.auditOK(r, AuditSettings, strings.Join(changedKeys, ","), "修改设置")
		if len(diff) > 0 {
			sort.Strings(diff)
			a.notifyChange(changeEvent{
				Actor: a.actorName(r), Action: AuditSettings,
				Target: strings.Join(changedKeys, ","), Detail: "修改设置", Diff: diff,
			})
		}
	}
	if relogin {
		a.changed(r, AuditPassword, a.actorName(r), "本账号密码已修改，该账号的旧会话全部失效")
	}
	// ACME 设置变更后立即触发一次签发/续期检查，无需等下一个检查周期
	if m["acme_enabled"] == "1" && a.CertMgr != nil {
		go a.CertMgr.CheckNow()
	}
	resp := map[string]any{"ok": "1"}
	if relogin {
		resp["relogin"] = true
	}
	writeJSON(w, 200, resp)
}
