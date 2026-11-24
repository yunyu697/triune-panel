package api

// 系统规则的唯一来源：体检脚本、覆盖提示、漂移快照、以及**生成器用的清单**，
// 四件事都从这里派生。
//
// 以前规则清单（systemDirectRules / systemProxyDomains）是平铺的规则行，类别表
// 另写一份——两处靠「记得一起改」维持一致。加一条域名忘了归类，规则照样生成、
// 照样生效，只是体检与覆盖提示悄悄漏掉这一类，谁也不会发现。现在把规则挂在类上：
// 加规则必须放进某个类（没有别的地方可放），清单由各类拼出来，方向由 Expect 决定
// （direct → DIRECT、proxy → PROXY），写反了测试会红。
//
// 仍然手写的是「探针」：裸域名常常不解析（windowsupdate.com 实测 NXDOMAIN），
// 探针必须是能解析的真实子域，派生不出来，只能人工挑——但挑漏了有测试兜着
// （TestSystemClassProbesAreInRuleLists 双向断言）。
//
// 新增一条规则时只要在对应类里加一行，四处自动跟上。

import (
	"encoding/json"
	"fmt"
	"strings"
)

// systemClass 一组「会一起失效」的系统规则。
//
// Expect 决定体检时什么算通过，也决定 Rules 里每条规则的策略：
// 直连类必须解析到真实 IP（fake-ip 说明流量进了代理），代理类必须解析到 fake-ip
// （真实 IP 说明规则没生效）。
type systemClass struct {
	Key    string   `json:"key"`    // 稳定标识，前端与脚本都用它，别拿中文名做键
	Name   string   `json:"name"`   // 展示名
	Expect string   `json:"expect"` // direct / proxy / port
	Probes []string `json:"probes"` // 代表域名：第一个用于体检，全部用于断言
	URL    string   `json:"url"`    // 体检用的轻量 URL（空 = 只查解析归属）
	Hint   string   `json:"hint"`   // 体检不通过时告诉用户会坏在哪
	Sets   []string `json:"sets"`   // 能覆盖这一类的规则集 id；空 = 只有系统清单兜底
	// Rules 这一类的规则行（clash 语法 TYPE,VALUE,POLICY）。生成器直接渲染它们，
	// 顺序即规则优先级。策略必须与 Expect 一致（direct→DIRECT、proxy→PROXY），
	// 由 TestSystemClassRulesMatchExpect 把住。
	Rules []string `json:"-"`

	// 直连类的准入判据（加规则前逐条对照，缺一条就不该放进来）：
	//
	//  1. 走代理后服务**功能失效或必然失败**，而不是「只是慢一点」；
	//  2. 失效原因是出口 IP 地域判定、TLS 证书固定（pinning）、QUIC，或该服务只认本地网络；
	//  3. 只收「系统 / 更新 / 激活 / 认证」这类基础服务，**不收内容服务**——
	//     App Store 下载、Apple Music、Office 内容、OneDrive 文件同步、Windows Store
	//     应用内容都不属于「走代理必坏」，内容走不走代理是用户的选择，强制直连属越权。
	//
	// 之所以内置成硬编码而不是让用户勾规则集：上游 geosite-apple / geosite-microsoft
	// 是**大类**，会连带把内容服务一起直连；而用户什么规则集都不勾时，这些域名会落到
	// MATCH,PROXY，照样坏。
	//
	// 清单来源：v2fly/domain-list-community 的 apple-update / microsoft / onedrive
	// 三个分类，逐条核对过原文，不是凭记忆写的。
	//
	// 只允许 DOMAIN / DOMAIN-SUFFIX：DNS 与 fake-ip 的派生只认这两种类型，写别的
	// 类型会在 sing-box 侧静默失效（TestSystemListsUseSupportedRuleTypes 兜着）。
}

var systemClasses = []systemClass{
	{
		Key: "apple-update", Name: "Apple 系统更新 / 恢复", Expect: "direct",
		Probes: []string{"mesu.apple.com", "swcdn.apple.com"},
		URL:    "https://mesu.apple.com/",
		Hint:   "检查更新会一直转圈，或下到一半校验失败；软恢复不可用",
		Sets:   []string{"geosite-apple"},
		// 全量取自 apple-update 分类（见 https://support.apple.com/HT210060）。
		Rules: []string{
			"DOMAIN,mesu.apple.com,DIRECT",
			"DOMAIN,swscan.apple.com,DIRECT",
			"DOMAIN,swdist.apple.com,DIRECT",
			"DOMAIN,swdownload.apple.com,DIRECT",
			"DOMAIN,swcdn.apple.com,DIRECT",
			"DOMAIN,appldnld.apple.com,DIRECT",
			"DOMAIN,oscdn.apple.com,DIRECT",
			"DOMAIN,osrecovery.apple.com,DIRECT",
			"DOMAIN,gdmf.apple.com,DIRECT",
			"DOMAIN,gdmf-ados.apple.com,DIRECT",
			"DOMAIN,gs.apple.com,DIRECT",
			"DOMAIN,gsra.apple.com,DIRECT",
			"DOMAIN,gg.apple.com,DIRECT",
			"DOMAIN,ig.apple.com,DIRECT",
			"DOMAIN,skl.apple.com,DIRECT",
			"DOMAIN,wkms-public.apple.com,DIRECT",
			"DOMAIN,configuration.apple.com,DIRECT",
			"DOMAIN,xp.apple.com,DIRECT",
			"DOMAIN,captive.apple.com,DIRECT", // Wi-Fi 热点 / 网络接入检测
			// 更新与 Apple ID 共用的 CDN（updates / updates-http / fcs-keys-pub-prod /
			// appleid.cdn-apple 全在这条后缀下）。上游新增 CDN 主机名时无需再逐条补。
			"DOMAIN-SUFFIX,cdn-apple.com,DIRECT",
		},
	},
	{
		Key: "apple-ocsp", Name: "Apple 证书校验（OCSP）", Expect: "direct",
		Probes: []string{"ocsp.apple.com", "ocsp2.apple.com"},
		URL:    "http://ocsp.apple.com/",
		Hint:   "装 App 或更新时卡在「正在验证」，或直接报「无法验证」",
		Sets:   []string{"geosite-apple"},
		// 装 App、跑更新、首次打开签名应用时，系统都要向它问一句「这张证书还有效吗」。
		// 走代理后节点一抖或对 POST 返回非预期状态，系统就把证书当成不可信。
		// ocsp2 是新域名，老配置只收 ocsp 会漏。
		Rules: []string{
			"DOMAIN,ocsp.apple.com,DIRECT",
			"DOMAIN,ocsp2.apple.com,DIRECT",
		},
	},
	{
		Key: "apple-loc", Name: "Apple 定位与区域判定", Expect: "direct",
		Probes: []string{"gs-loc.apple.com", "gsp-ssl.ls.apple.com"},
		URL:    "https://gs-loc.apple.com/",
		Hint:   "「查找我的」定位漂到国外，时区与 App Store 区域判定跟着错",
		Sets:   []string{"geosite-apple"},
		// 这几个主机回报的是「设备大致在哪」。走代理等于告诉 Apple 你在节点所在国，
		// 于是定位漂到海外、时区自动设置错乱、区域判定跟着变。不是「慢」，是答案错了。
		Rules: []string{
			"DOMAIN,gs-loc.apple.com,DIRECT",    // 定位主入口
			"DOMAIN-SUFFIX,ls.apple.com,DIRECT", // 定位服务后端（gsp-ssl.ls 等）
			"DOMAIN-SUFFIX,ls-apple.com,DIRECT", // 地区变体
		},
	},
	{
		Key: "apple-id", Name: "Apple ID 登录与激活", Expect: "direct",
		Probes: []string{"appleid.apple.com", "idmsa.apple.com"},
		URL:    "https://appleid.apple.com/",
		Hint:   "登录被判异地，触发二次验证甚至锁账号",
		Sets:   []string{"geosite-apple"},
		// 登录走代理会被判异地 → 二次验证 / 锁账号；页面资源还做了证书固定。
		Rules: []string{
			"DOMAIN,appleid.apple.com,DIRECT",
			"DOMAIN,idmsa.apple.com,DIRECT",
			"DOMAIN,amp-api.apps.apple.com,DIRECT",
		},
	},
	{
		Key: "apple-push", Name: "Apple 推送（APNs）", Expect: "direct",
		// 裸 push.apple.com 不解析，真实主机形如 7-courier2.push.apple.com，
		// 探针必须用能解析的那个，否则体检会报「解析失败」，看起来像规则坏了。
		Probes: []string{"courier.push.apple.com"},
		// APNs 不说 HTTP：只查解析归属，不发 HTTP 请求。
		URL:  "",
		Hint: "推送延迟高、反复断连重连，iPhone / iPad 待机耗电明显加快",
		Sets: []string{"geosite-apple"},
		// 推送不是「用的时候才连」，而是设备常连的长连接（courier.push.apple.com:5223）。
		// 走代理后每次交互都要多绕一个海外 RTT，链路一抖就断线重连。它是这张表里唯一
		// 「不报错、只是变差」的条目——安卓不走这条通道，所以只有苹果设备中招。
		//
		// 只按域名放行：设备若直接拿 IP 连（没有 SNI）仍会落进 MATCH,PROXY。要彻底覆盖
		// 得加 Apple 的 17.0.0.0/8，但那会把 iCloud 等海外服务一起拉成直连，暂不默认开启。
		// 这条规则之所以真能命中，靠的是 sniffer 的 TLS 嗅探覆盖**全部端口**：直连域名
		// 被排除出 fake-ip，连接进来时只剩裸 IP，域名只能靠 SNI 补回。只嗅 443/8443 的
		// 那一版里，5223 上的这条规则一次都没生效过，表现就是待机耗电。
		Rules: []string{
			"DOMAIN-SUFFIX,push.apple.com,DIRECT",
		},
	},
	{
		Key: "ms-login", Name: "Microsoft 账号登录", Expect: "direct",
		Probes: []string{"login.live.com", "login.microsoftonline.com"},
		URL:    "https://login.live.com/",
		Hint:   "OneDrive / Office / Teams 登不上，或反复要求重新登录",
		Sets:   []string{"geosite-microsoft", "geosite-onedrive"},
		// OneDrive / Office / Windows / Teams 的登录都过这条链路；走代理出口 IP 触发风控。
		Rules: []string{
			"DOMAIN,login.microsoftonline.com,DIRECT",
			"DOMAIN,login.live.com,DIRECT",
			"DOMAIN,login.microsoft.com,DIRECT",
			"DOMAIN,login.windows.net,DIRECT",
			"DOMAIN,account.live.com,DIRECT",
			"DOMAIN,account.microsoft.com,DIRECT",
			"DOMAIN-SUFFIX,microsoftonline.com,DIRECT", // 各国后缀与 -p 变体
			"DOMAIN-SUFFIX,msftauth.net,DIRECT",        // 登录页脚本与资源 CDN
			"DOMAIN-SUFFIX,msauth.net,DIRECT",
			"DOMAIN-SUFFIX,msftauthimages.net,DIRECT", // 登录页图形验证码
		},
	},
	{
		Key: "win-update", Name: "Windows 更新 / 激活", Expect: "direct",
		// 裸 windowsupdate.com 没有 A 记录（实测 NXDOMAIN），必须用真实子域，
		// 否则体检会报「解析失败」，看起来像规则坏了。
		Probes: []string{"download.windowsupdate.com", "activation.sls.microsoft.com"},
		URL:    "https://windowsupdate.microsoft.com/",
		Hint:   "更新龟速或报 0x8024402c 一类错误；激活提示「环境异常」",
		Sets:   []string{"geosite-microsoft"},
		// 更新由 Microsoft CDN 按地域就近下发，走代理等于绕到远端节点再回源，
		// 轻则龟速、重则 0x8024402c 一类报错；激活与授权走代理会被判「异常环境」。
		// 注意：**不含** mp.microsoft.com 的应用内容分发（Store 应用下载属于内容服务，
		// 由用户勾选的规则集决定），只收操作系统更新与激活。
		Rules: []string{
			"DOMAIN-SUFFIX,windowsupdate.com,DIRECT",            // download./ctldl./au.download. 全在内
			"DOMAIN-SUFFIX,update.microsoft.com,DIRECT",         // 旧版更新端点
			"DOMAIN-SUFFIX,activation.sls.microsoft.com,DIRECT", // 激活
			"DOMAIN-SUFFIX,licensing.mp.microsoft.com,DIRECT",   // 授权
		},
	},
	{
		Key: "connectivity", Name: "系统连通性检测（NCSI）", Expect: "direct",
		// 同理：裸 msftconnecttest.com 不解析，Windows 实际访问的是 www. 前缀。
		Probes: []string{"www.msftconnecttest.com", "captive.apple.com"},
		URL:    "http://www.msftconnecttest.com/connecttest.txt",
		Hint:   "系统误判「无网络」，反复重连、Wi-Fi 标记为已连接但无法访问",
		Sets:   nil,
		// 走代理一旦节点抖一下，系统就判定「无网络」，随后反复重连、弹网络异常、
		// 甚至把 Wi-Fi 标记为「已连接但无法访问 Internet」。
		Rules: []string{
			"DOMAIN-SUFFIX,msftconnecttest.com,DIRECT",
			"DOMAIN-SUFFIX,msftncsi.com,DIRECT",
		},
	},
	{
		Key: "mediafire", Name: "MediaFire（内置直连）", Expect: "direct",
		Probes: []string{"mediafire.com"},
		URL:    "https://www.mediafire.com/",
		Hint:   "下载站与文件节点都该直连；进代理会明显变慢",
		Sets:   nil,
		// 站点与下载节点都在 mediafire.com 后缀下（download####.mediafire.com）。
		// 刻意不加 IP 规则：它全站挂在 Cloudflare 共享段上（实测
		// download.mediafire.com → 104.18.213.225 / 104.18.214.225），
		// 按 IP 放行会把同一片 CF 上的成千上万个站点一起拉成直连。
		Rules: []string{
			"DOMAIN-SUFFIX,mediafire.com,DIRECT",
		},
	},
	{
		Key: "github", Name: "GitHub 全系（内置代理）", Expect: "proxy",
		Probes: []string{"github.com", "codeload.github.com"},
		URL:    "https://github.com/",
		Hint:   "直连时握手超时：网页打不开、git push 连不上",
		Sets:   nil,
		// 2026-11-17 实测（广州联通千兆）：直连 https://github.com 握手阶段就超时
		// （5s 无任何响应），而 raw.githubusercontent.com 同刻可达——可达性是
		// 「按端点 / 按时间抖动」的，靠兜底规则碰运气不行。
		Rules: []string{
			// 主站 / gist / api / codeload / ssh.github.com 全在这条后缀下
			"DOMAIN-SUFFIX,github.com,PROXY",
			// raw / objects / avatars / camo / user-images / private-user-images
			"DOMAIN-SUFFIX,githubusercontent.com,PROXY",
			// github.githubassets.com：页面 CSS/JS，掉它就白屏
			"DOMAIN-SUFFIX,githubassets.com,PROXY",
			"DOMAIN-SUFFIX,github.io,PROXY",  // Pages
			"DOMAIN-SUFFIX,github.dev,PROXY", // 网页版 VSCode
			"DOMAIN-SUFFIX,ghcr.io,PROXY",    // 容器镜像
			"DOMAIN-SUFFIX,githubapp.com,PROXY",
		},
	},
	{
		Key: "ntp", Name: "时间同步（NTP 按端口直连）", Expect: "port",
		Hint: "时间漂移会让 TLS 证书随机报「已过期 / 无效」，且极难归因",
	},
}

// systemDirectRules / systemProxyDomains 由各类拼出来，不单独维护。
//
// 方向由 Expect 决定：direct 类的规则进直连清单，proxy 类的进代理清单。于是
// 「加一条规则」只有一处可写——放进某个类的 Rules。以前这两份清单与类别表分居
// 两个文件，加规则忘了归类，规则照样生效、体检与覆盖提示却悄悄漏掉这一类。
var systemDirectRules, systemProxyDomains = buildSystemRuleLists()

func buildSystemRuleLists() (direct, proxy []string) {
	for _, c := range systemClasses {
		switch c.Expect {
		case "direct":
			direct = append(direct, c.Rules...)
		case "proxy":
			proxy = append(proxy, c.Rules...)
		}
	}
	return direct, proxy
}

// ruleListMatches 判断某个域名是否被清单里的规则命中（只认 DOMAIN 与
// DOMAIN-SUFFIX，这正是清单允许的两种类型）。
//
// 用后缀语义而不是字符串相等：清单写 DOMAIN-SUFFIX,ls.apple.com 时，
// gsp-ssl.ls.apple.com 也必须算命中，否则「探针在清单里」这类断言会误报。
func ruleListMatches(rules []string, policy, domain string) bool {
	for _, line := range rules {
		typ, val, pol, ok := parseCustomRule(line)
		if !ok {
			continue
		}
		// 用解析出来的策略比较，而不是看行尾后缀：带 no-resolve 的规则行尾
		// 不是策略，后缀判断会把它整条漏掉。
		if policy != "" && pol != policy {
			continue
		}
		switch typ {
		case "DOMAIN":
			if strings.EqualFold(val, domain) {
				return true
			}
		case "DOMAIN-SUFFIX":
			if strings.EqualFold(val, domain) || strings.HasSuffix(strings.ToLower(domain), "."+strings.ToLower(val)) {
				return true
			}
		}
	}
	return false
}

// systemRuleSnapshot 当前二进制内置的整套系统规则，一条一行、原文保留，
// 外加一条生成器指纹（`@gen:` 前缀，见 splitRuleSnapshot）。
//
// 存进「保存的配置」里，用来事后回答「这份配置比现在少了哪几条」。
// 只放会影响分流结果的东西：端口与私网段也各有策略，所以一并收进来——
// 少了 100.64.0.0/10 这种变化，用户同样会撞上奇怪的现象。
func systemRuleSnapshot() []string {
	rules := systemRuleSnapshotRules()
	out := make([]string, 0, len(rules)+1)
	if s := generatorStamp(); s != "" {
		out = append(out, genStampPrefix+s)
	}
	return append(out, rules...)
}

// systemRuleSnapshotRules 只含规则行，不含指纹。
func systemRuleSnapshotRules() []string {
	out := make([]string, 0,
		len(blockExemptRules)+len(systemDirectRules)+len(systemProxyDomains)+len(systemProxyCIDRs)+len(privateCIDRs)+len(systemDirectPorts))
	// 拦截豁免也算「影响分流结果」：老配置少了这两条，勾着广告拦截时
	// 还是会踩到「App 首屏卡死」，所以必须让它在漂移里显示出来。
	out = append(out, blockExemptRules...)
	out = append(out, systemDirectRules...)
	out = append(out, systemProxyDomains...)
	for _, c := range systemProxyCIDRs {
		out = append(out, "IP-CIDR,"+c+",PROXY")
	}
	for _, c := range privateCIDRs {
		out = append(out, "IP-CIDR,"+c+",DIRECT")
	}
	for _, p := range systemDirectPorts {
		out = append(out, fmt.Sprintf("PORT,%d,DIRECT", p))
	}
	return out
}

// genStampPrefix 给指纹行加的前缀。取 `@` 开头是因为规则行总以类型名开头
// （DOMAIN / IP-CIDR / PORT…），自定义规则也被 parseCustomRuleFull 限制在这几种，
// 不会撞车；拆分时只认这个前缀，其余一律当规则原文。
const genStampPrefix = "@gen:"

// splitRuleSnapshot 把快照拆成「生成器指纹」与「规则行」两部分。
//
// 规则行的对比要按集合求差（用户看的是多了哪几条），指纹只做等值比较——
// 混在一起会让界面把 `@gen:xxx` 当成一条新增规则展示。
func splitRuleSnapshot(snap []string) (stamp string, rules []string) {
	rules = make([]string, 0, len(snap))
	for _, s := range snap {
		if strings.HasPrefix(s, genStampPrefix) {
			stamp = strings.TrimPrefix(s, genStampPrefix)
			continue
		}
		rules = append(rules, s)
	}
	return stamp, rules
}

// diffRuleSnapshot 按集合求差，保持各自原有顺序（配置文件是给人看的，
// 顺序乱了「新增 3 条」就不好核对）。
func diffRuleSnapshot(old, cur []string) (added, removed []string) {
	inOld := make(map[string]bool, len(old))
	for _, r := range old {
		inOld[r] = true
	}
	inCur := make(map[string]bool, len(cur))
	for _, r := range cur {
		inCur[r] = true
	}
	for _, r := range cur {
		if !inOld[r] {
			added = append(added, r)
		}
	}
	for _, r := range old {
		if !inCur[r] {
			removed = append(removed, r)
		}
	}
	return added, removed
}

// shq 把字符串包成 shell 单引号字面量。
// 探针里有域名与中文，理论上不含单引号，但一旦含了就会把脚本拆坏，
// 所以按标准写法转义，不赌输入。
func shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// healthCheckScript 生成在设备上直接跑的规则体检脚本。
//
// 为什么由后端生成而不是前端拼：探针清单与类别必须与生成器同源。
// 前端拼的话，改了 systemDirectRules 而忘了改 JS，脚本会去测一条早就
// 不在清单里的域名，通过与否都没有意义。
func healthCheckScript() string {
	var cases strings.Builder
	for _, c := range systemClasses {
		if c.Expect == "port" || len(c.Probes) == 0 {
			continue
		}
		for i, p := range c.Probes {
			url := ""
			if i == 0 {
				url = c.URL // 每个类只对第一个探针发 HTTP，避免把一个类测成 20 次请求
			}
			fmt.Fprintf(&cases, "run_case %s %s %s %s %s\n",
				shq(c.Name), shq(c.Expect), shq(p), shq(url), shq(c.Hint))
		}
	}
	out := strings.ReplaceAll(healthCheckTmpl, "@VERSION@", Version)
	return strings.ReplaceAll(out, "@CASES@", strings.TrimRight(cases.String(), "\n"))
}

// healthCheckTmpl 体检脚本主体。POSIX sh + busybox 兼容：
// OpenWrt 上不一定有 dig（bind-dig 是可选包），所以 nslookup 必须能用；
// curl 缺失时退回 wget。两者都没有就明说测不了，不猜。
const healthCheckTmpl = `#!/bin/sh
# ───────────────────────────────────────────────────────────────
# triune-panel 规则体检脚本
# 生成于面板 @VERSION@
#
# 做什么：逐个关键类别解析域名，看它被分到直连还是代理，再验一下能不能连通。
#
# 判定依据是 fake-ip：返回 198.18 / 198.19 段说明流量进了代理（面板生成的
# 配置用 198.18.0.1/16，momo 目标用 198.18.0.0/15，两者都覆盖），返回真实 IP
# 说明命中了强制直连——强制直连的域名必须被排除在 fake-ip 之外，否则拿不到
# 真实地址、也连不上。
#
# 在哪跑：**跑这份配置的那台设备上**（旁路由 / 软路由 / 本机）。在别的机器上
#         跑，测的是那台机器的 DNS，不是这份配置。
#
# 用法：
#   sh tp-check.sh              # 用 /etc/resolv.conf 里的 DNS
#   sh tp-check.sh 10.1.1.1     # 指定 DNS（旁路由地址）
#
# 退出码：0 = 全部通过，1 = 有异常项
# ───────────────────────────────────────────────────────────────

DNS=$1
PASS=0
FAIL=0
TOTAL=0

if [ -z "$DNS" ] && [ -r /etc/resolv.conf ]; then
  DNS=$(awk '/^nameserver/{print $2; exit}' /etc/resolv.conf)
fi

echo "规则体检 · 面板 @VERSION@"
if [ -n "$DNS" ]; then
  echo "DNS 服务器：$DNS"
else
  echo "DNS 服务器：系统默认"
fi
echo ""

if ! command -v dig >/dev/null 2>&1 && ! command -v nslookup >/dev/null 2>&1; then
  echo "找不到 dig 也找不到 nslookup，无法解析域名。"
  echo "OpenWrt 上装一下：opkg update && opkg install bind-dig"
  exit 2
fi

is_fake() {
  case $1 in
    198.18.*|198.19.*) return 0 ;;
  esac
  return 1
}

resolve() {
  _d=$1
  if command -v dig >/dev/null 2>&1; then
    if [ -n "$DNS" ]; then
      dig +short +time=3 +tries=2 A "$_d" @"$DNS" 2>/dev/null | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' | head -1
    else
      dig +short +time=3 +tries=2 A "$_d" 2>/dev/null | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' | head -1
    fi
    return
  fi
  # busybox nslookup：Server 段与 Name 段各有一行 Address，取最后一行才是解析结果
  if [ -n "$DNS" ]; then
    _out=$(nslookup "$_d" "$DNS" 2>/dev/null)
  else
    _out=$(nslookup "$_d" 2>/dev/null)
  fi
  echo "$_out" | awk '/^Address/{print $2}' | sed 's/:53$//' \
    | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' | tail -1
}

http_code() {
  if command -v curl >/dev/null 2>&1; then
    curl -sS -o /dev/null --connect-timeout 8 --max-time 20 -w '%{http_code}' "$1" 2>/dev/null
  elif command -v wget >/dev/null 2>&1; then
    if wget -q -T 20 -O /dev/null "$1" 2>/dev/null; then echo 200; else echo 000; fi
  else
    echo -
  fi
}

run_case() {
  _name=$1
  _expect=$2
  _dom=$3
  _url=$4
  _hint=$5
  TOTAL=$((TOTAL+1))

  _ip=$(resolve "$_dom")
  if [ -z "$_ip" ]; then
    echo "  [✗] $_name · $_dom → 解析失败"
    echo "      ↳ $_hint"
    FAIL=$((FAIL+1))
    return
  fi

  if is_fake "$_ip"; then _got=proxy; else _got=direct; fi
  _bad=0
  if [ "$_got" != "$_expect" ]; then _bad=1; fi

  if [ "$_got" = "proxy" ]; then _got_txt="进了代理"; else _got_txt="直连"; fi

  _http=""
  if [ -n "$_url" ]; then
    _code=$(http_code "$_url")
    case $_code in
      000|-) _http=" · HTTP 不通"; _bad=1 ;;
      *)     _http=" · HTTP $_code" ;;
    esac
  fi

  if [ "$_bad" = "1" ]; then
    if [ "$_got" != "$_expect" ]; then
      if [ "$_expect" = "direct" ]; then
        _why="应直连，实际进了代理"
      else
        _why="应走代理，实际直连"
      fi
      echo "  [✗] $_name · $_dom → ${_ip}（${_why}）$_http"
    else
      echo "  [✗] $_name · $_dom → ${_ip}（${_got_txt}）$_http"
    fi
    echo "      ↳ $_hint"
    FAIL=$((FAIL+1))
  else
    echo "  [✓] $_name · $_dom → ${_ip}（${_got_txt}）$_http"
    PASS=$((PASS+1))
  fi
}

echo "分流归属"
@CASES@

echo ""
echo "时间同步"
# NTP 按端口直连，没有域名可查，只能比时间。工具缺失就明说，不给假结论。
_off=""
if command -v ntpdate >/dev/null 2>&1; then
  _off=$(ntpdate -q pool.ntp.org 2>/dev/null | awk '/offset/{gsub(/,/,""); print $NF; exit}')
elif command -v sntp >/dev/null 2>&1; then
  # macOS 的 sntp 先打一行表头（sntp_exchange ...），只认形如 +0.001234 的字段
  _off=$(sntp -t 5 pool.ntp.org 2>/dev/null | awk '$1 ~ /^[+-][0-9]+\.[0-9]+$/{print $1; exit}')
fi
if [ -z "$_off" ]; then
  echo "  [–] 跳过：没装 ntpdate / sntp，无法自动比对"
  echo "      ↳ 手动看：date -u 与本机实际时间差超过 5 分钟，说明 NTP 没走直连"
else
  _abs=$(echo "$_off" | tr -d '+-')
  _int=${_abs%%.*}
  if [ -z "$_int" ]; then _int=0; fi
  if [ "$_int" -lt 5 ]; then
    echo "  [✓] 与 pool.ntp.org 偏差 ${_abs} 秒"
    PASS=$((PASS+1))
  else
    echo "  [✗] 与 pool.ntp.org 偏差 ${_abs} 秒（超过 5 秒）"
    echo "      ↳ 时间漂移会让 TLS 证书随机报「已过期 / 无效」，且极难归因"
    FAIL=$((FAIL+1))
  fi
  TOTAL=$((TOTAL+1))
fi

echo ""
echo "───────────────────────────────────────────────────────────────"
if [ "$FAIL" = "0" ]; then
  echo "全部通过：$PASS 项"
  echo ""
  echo "如果设备上刚换过配置、这里有项不通过，先清一次 DNS 缓存再跑："
  echo "  macOS：sudo dscacheutil -flushcache; sudo killall -HUP mDNSResponder"
  echo "  Windows：ipconfig /flushdns"
  echo "  路由器：/etc/init.d/dnsmasq restart"
  exit 0
fi
echo "有 $FAIL 项异常（通过 $PASS 项，共 $TOTAL 项）"
echo "把这段输出发给维护者，能直接定位到是哪一类规则没生效。"
exit 1
`

// systemRuleSnapshotJSON 快照的落库形态。编码失败只会是内存问题，
// 返回空串即「未知」，下游按「建议重新生成」处理，不阻断保存。
func systemRuleSnapshotJSON() string {
	b, err := json.Marshal(systemRuleSnapshot())
	if err != nil {
		return ""
	}
	return string(b)
}

// configDriftView 列表页要的漂移摘要：只说「落后几条」，不搬整份清单。
type configDriftView struct {
	Stale          bool   `json:"stale"`
	Unknown        bool   `json:"unknown"`
	PanelVersion   string `json:"panel_version"`
	CurrentVersion string `json:"current_version"`
	AddedCount     int    `json:"added_count"`
	RemovedCount   int    `json:"removed_count"`
	// GeneratorChanged 表示「规则一条不差，但生成器行为变了」（嗅探端口、
	// TUN MTU、规则顺序这类不在规则行里的东西）。只看 Added/Removed 会显示
	// 「一致」，而产物其实已经不一样了。
	GeneratorChanged bool `json:"generator_changed"`
}

// configDriftDetail 详情页要的漂移明细（规则行原文）。
type configDriftDetail struct {
	configDriftView
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
}

// driftOf 算出某份配置相对当前二进制的漂移。
//
// 快照为空说明是旧库里的记录（加字段之前保存的）。这种情况**不假装它是最新的**：
// Unknown=true，界面按「建议重新生成」提示，用户点一下就能拿到新配置。
//
// 规则行按集合求差；生成器指纹只做等值比较，两者分开呈现——用户要看的是
// 「多了哪几条规则」，「生成器行为变了」是另一句话。
func driftOf(snapshot string) configDriftDetail {
	d := configDriftDetail{configDriftView: configDriftView{CurrentVersion: Version}}
	d.Added, d.Removed = []string{}, []string{}
	if strings.TrimSpace(snapshot) == "" {
		d.Unknown = true
		d.Stale = true
		return d
	}
	var old []string
	if err := json.Unmarshal([]byte(snapshot), &old); err != nil {
		d.Unknown = true
		d.Stale = true
		return d
	}
	oldStamp, oldRules := splitRuleSnapshot(old)
	added, removed := diffRuleSnapshot(oldRules, systemRuleSnapshotRules())
	d.Added, d.Removed = added, removed
	d.AddedCount, d.RemovedCount = len(added), len(removed)
	// 只在两边都有指纹时比较：指纹是这次才加进快照的，把「旧记录没有指纹」
	// 当成「行为变了」会让所有历史配置一次性标红，那只是噪音。
	d.GeneratorChanged = oldStamp != "" && oldStamp != generatorStamp()
	d.Stale = len(added) > 0 || len(removed) > 0 || d.GeneratorChanged
	return d
}
