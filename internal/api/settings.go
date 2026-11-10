package api

import (
	"encoding/json"
	"net"
	"net/http"
	"sort"
	"strings"

	"triune/internal/authn"
	"triune/internal/certmgr"
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
	for _, k := range []string{"public_host", "server_ip", "stats_enabled", "stats_secret",
		"quota_warn_pct", "fw_auto",
		// Prometheus 指标令牌（空 = /metrics 关闭）
		MetricsTokenSetting,
		// 可信反向代理网段（登录来源识别用，默认空 = 不信任任何代理）
		TrustedProxiesSetting,
		"acme_enabled", "acme_domain", "acme_email",
		// DNS-01：服务商 ID + 凭据（凭据不回显，见下面的打码）
		certmgr.SettingDNSProvider, certmgr.SettingDNSEnv,
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
		AutoSwitchSetting,
		// 生成客户端配置时规则集的下载源（默认 official，见 rulemirror.go）
		MirrorSetting, MirrorPrefixSetting,
		// 面板是否用 TLS 监听（"1" 开，改了要重启；见 paneltls.go）
		SettingPanelHTTPS,
		// 备份容量治理策略（见 backups.go）
		SettingBackupKeepCount, SettingBackupMaxMB, SettingBackupMaxDays, SettingBackupAutoPrune,
		// 命令工作台布局（见 workbench.go）：跨设备记住卡片顺序与折叠状态
		SettingWorkbenchLayout} {
		v, _ := a.Store.GetSetting(k)
		// DNS 凭据是**第三方账号的钥匙**（一个 Cloudflare token 能改整个域名的
		// 解析，再签一张证书冒充这个站），和面板自己的 stats_secret 不是一个
		// 量级。所以这里破一次「设置项照常回显」的惯例：只回占位符，前端把
		// 占位符原样提交回来表示「没改过」（见 putSettings）。
		if k == certmgr.SettingDNSEnv && v != "" {
			v = certmgr.MaskedSecret
		}
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
		// 服务器公网 IP：只用于「新建入站 → 节点地址」的预填与选择。
		// 弹性 IP / NAT 的机器上默认路由探测到的是内网地址，这时靠它兜底。
		"server_ip": true,
		"quota_warn_pct": true,
		// Prometheus 指标令牌：写空字符串即关闭 /metrics
		MetricsTokenSetting: true,
		// 入站端口的自动防火墙管理（fwAuto 读取，默认开启）
		"fw_auto": true,
		// 可信反向代理网段：仅当直连对端落在这些网段内才采信转发头
		TrustedProxiesSetting: true,
		"acme_enabled":        true, "acme_domain": true, "acme_email": true,
		// DNS-01 验证：服务商 ID（空 = 用 HTTP-01）与凭据
		certmgr.SettingDNSProvider: true, certmgr.SettingDNSEnv: true,
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
		// 规则集下载源（生成给客户端的配置用）。值必须是表里的 ID，
		// 允许 custom + 合法前缀；非法值在这里就拒掉，别写进库让生成页读到脏值。
		MirrorSetting: true, MirrorPrefixSetting: true,
		// 面板 HTTPS 开关：只落库，真正生效在下次启动（certmgr 不管面板自己）
		SettingPanelHTTPS: true,
		// 备份容量治理：份数 / 总容量(MB) / 天数 / 是否允许后台自动清理（默认关）
		SettingBackupKeepCount: true, SettingBackupMaxMB: true,
		SettingBackupMaxDays: true, SettingBackupAutoPrune: true,
		// 工作台布局只影响 UI，但一样要过白名单校验才落库
		SettingWorkbenchLayout: true,
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
	// 服务器公网 IP：只给「新建入站 → 节点地址 → 用 IP」做预填。
	// 允许留空（走默认路由自动探测），填了就必须是 IP——写成域名会被当成 IP
	// 预填进地址框，导出的链接连不上，而错误要到客户端才暴露。
	if v, ok := m["server_ip"]; ok && strings.TrimSpace(v) != "" {
		n, msg := parseNodeHost(v)
		if msg != "" {
			fail(w, 400, "服务器公网 IP："+msg)
			return
		}
		if net.ParseIP(n) == nil {
			fail(w, 400, "服务器公网 IP 必须填 IP（如 1.2.3.4）；域名请填上面的「节点公开地址」")
			return
		}
		m["server_ip"] = n
	}
	// 规则集下载源同学校验：库里只留合法值，否则生成页读到一个已下线的
	// 镜像 ID 时会静默退回官方，用户却以为自己在走镜像。
	if v, ok := m[MirrorSetting]; ok {
		if _, warn := resolveMirror(v, m[MirrorPrefixSetting]); warn != "" {
			fail(w, 400, "规则集下载源："+warn)
			return
		}
	}
	// HTTPS 开关只认 "0"/"1"/空：写进别的值会让下次启动既不建证书也不报错，
	// 变成一个「开了但没生效」的静默状态。
	if v, ok := m[SettingPanelHTTPS]; ok && v != "" && v != "0" && v != "1" {
		fail(w, 400, "panel_https 只能是 1 或 0")
		return
	}
	// 备份策略是「会删文件」的设置，非法值必须先拒掉：写进库再被 clamp 成 0
	// 会变成「用户以为设了上限，其实什么都没发生」的静默失效。
	if err := validateBackupSettings(m); err != nil {
		fail(w, 400, err.Error())
		return
	}
	// 工作台布局是前端拼出来的 JSON：不校验就等于开放一个任意字符串仓库，
	// 而且脏值回流到页面会直接破坏卡片顺序。
	if err := validateWorkbenchLayout(m[SettingWorkbenchLayout]); err != nil {
		fail(w, 400, err.Error())
		return
	}
	// ---- ACME / DNS-01 ----
	//
	// 这些校验全部前移到保存期。理由是签发是异步的、DNS-01 还要等记录传播，
	// 用户在结果那一端能看到的只有日志里一句英文；能在这儿挡住的就说明白。
	if v, ok := m[certmgr.SettingDNSEnv]; ok && strings.TrimSpace(v) == certmgr.MaskedSecret {
		// 原样提交占位符 = 没改过。删掉这个键，别拿占位符覆盖真凭据。
		delete(m, certmgr.SettingDNSEnv)
	}
	// 有效服务商：本次提交的优先，否则沿用库里已有的（本次只改了凭据时）。
	effProvider := ""
	if v, ok := m[certmgr.SettingDNSProvider]; ok {
		effProvider = strings.TrimSpace(v)
		m[certmgr.SettingDNSProvider] = effProvider
		if effProvider != "" {
			if _, found := certmgr.LookupDNSProvider(effProvider); !found {
				fail(w, 400, "不支持的 DNS 服务商："+effProvider+
					"（可选 "+strings.Join(certmgr.DNSProviderIDs(), " / ")+"）")
				return
			}
		}
	} else {
		effProvider, _ = a.Store.GetSetting(certmgr.SettingDNSProvider)
		effProvider = strings.TrimSpace(effProvider)
	}
	// 有效域名：同样「本次提交优先」——只切验证方式、不改域名时也要能判断
	// 「泛域名 + HTTP-01」这个组不拢的组合，否则会存进一个必然失败的配置。
	effDomains := []string(nil)
	if v, ok := m["acme_domain"]; ok {
		if strings.TrimSpace(v) == "" {
			m["acme_domain"] = ""
		} else if d, err := certmgr.ParseDomains(v); err != nil {
			fail(w, 400, "证书域名有误："+err.Error())
			return
		} else {
			m["acme_domain"] = strings.Join(d, ",")
			effDomains = d
		}
	} else if raw, _ := a.Store.GetSetting("acme_domain"); strings.TrimSpace(raw) != "" {
		if d, err := certmgr.ParseDomains(raw); err == nil {
			effDomains = d
		}
	}
	if effProvider == "" && certmgr.HasWildcard(effDomains) {
		fail(w, 400, "泛域名证书只能用 DNS-01 签发（Let's Encrypt 的硬性规定）："+
			"请先在「验证方式」里选一个 DNS 服务商并填上凭据")
		return
	}
	if v, ok := m[certmgr.SettingDNSEnv]; ok && strings.TrimSpace(v) != "" {
		if effProvider == "" {
			fail(w, 400, "填了 DNS 凭据但没有选服务商：请在「验证方式」里选一个，或清空凭据")
			return
		}
		info, _ := certmgr.LookupDNSProvider(effProvider)
		if _, err := certmgr.ParseDNSEnv(v, info); err != nil {
			fail(w, 400, info.Label+" 的凭据有误："+err.Error())
			return
		}
		m[certmgr.SettingDNSEnv] = strings.TrimSpace(v)
	}
	if effProvider != "" {
		// 选了服务商但两边都没有凭据：save 下来只会让下次检查报「没有填凭据」，
		// 不如现在就说清楚。
		stored, _ := a.Store.GetSetting(certmgr.SettingDNSEnv)
		if strings.TrimSpace(stored) == "" && strings.TrimSpace(m[certmgr.SettingDNSEnv]) == "" {
			info, _ := certmgr.LookupDNSProvider(effProvider)
			fail(w, 400, info.Label+" 需要凭据：请填 "+strings.Join(info.Env, "、")+
				"；若不想用 DNS-01，把「验证方式」改回 HTTP-01")
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
