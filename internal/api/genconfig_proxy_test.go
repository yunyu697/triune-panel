package api

import (
	"strings"
	"testing"
)

// 这组测试钉住三件事：
//  1. GitHub 等境外关键服务强制走代理，且排在规则集之前——用户把 GitHub
//     规则集设成 DIRECT 时也不能把连接留在大陆网络上；
//  2. 无 SNI 的裸 TCP（git over SSH）靠 IP 段兜底，位置在规则集之后；
//  3. MediaFire 全站直连，且 DNS 侧同步走本地解析。
//
// 背景（2026-11-17 实测，广州联通）：直连 https://github.com 握手超时，
// 而 raw.githubusercontent.com 同刻可达——可达性按端点/按时间抖动。
// 另有 mediafire 全站挂在 Cloudflare 共享段上，只能按域名而不是 IP 放行。

// mihomo：GitHub 域名规则必须是 PROXY，且压在所有 RULE-SET 之前。
// 故意把 geosite-github 设成 DIRECT——现实里用户完全可能这么配，
// 此时内置代理规则仍须生效，否则「GitHub 打不开」会复现。
func TestMihomoGitHubProxyBeforeRuleSets(t *testing.T) {
	warns := new([]string)
	sets, _ := normalizeSets(genReq{Core: "mihomo", Sets: []setPolicy{
		{ID: "geosite-github", Policy: "DIRECT"},
		{ID: "geosite-cn", Policy: "DIRECT"},
	}})
	g := genMihomo("client", testNodes(), sets, nil, warns, ruleMirrorSel{}, bypassSpec{})
	rules := g.doc["rules"].([]any)
	firstSet := firstRuleSetIndex(rules)
	if firstSet < 0 {
		t.Fatal("没有生成任何规则集，测试前提不成立")
	}
	for _, line := range systemProxyDomains {
		idx := sysRuleIndex(rules, line)
		if idx < 0 {
			t.Errorf("缺少 GitHub 强制代理规则: %s", line)
			continue
		}
		if !strings.HasSuffix(line, ",PROXY") {
			t.Errorf("%s 不是 PROXY", line)
		}
		if idx > firstSet {
			t.Errorf("%s 排在规则集之后（idx=%d, firstSet=%d），会被 DIRECT 规则集抢先命中", line, idx, firstSet)
		}
	}
}

// mihomo：裸 TCP 用的 IP 段必须在规则集之后、私有网段之前，且带 no-resolve。
// 放最后是刻意的——有域名的连接早按域名命中，这里只接 SSH 这类无域名流量；
// 若放在前面，每个域名连接都要先过一次 IP 匹配，纯属浪费。
func TestMihomoGitHubIPFallbackAfterRuleSets(t *testing.T) {
	warns := new([]string)
	sets, _ := normalizeSets(genReq{Core: "mihomo", Sets: []setPolicy{
		{ID: "geosite-cn", Policy: "DIRECT"},
	}})
	g := genMihomo("client", testNodes(), sets, nil, warns, ruleMirrorSel{}, bypassSpec{})
	rules := g.doc["rules"].([]any)
	firstSet := firstRuleSetIndex(rules)
	if firstSet < 0 {
		t.Fatal("没有生成规则集")
	}
	// 私有网段直连是最后一段 IP 规则，代理兜底必须排在它之前
	privIdx := -1
	for i, r := range rules {
		if s, ok := r.(string); ok && strings.HasPrefix(s, "IP-CIDR,") && strings.HasSuffix(s, ",DIRECT,no-resolve") {
			privIdx = i
		}
	}
	if privIdx < 0 {
		t.Fatal("没有私有网段直连规则")
	}
	for _, c := range systemProxyCIDRs {
		want := "IP-CIDR," + c + ",PROXY,no-resolve"
		idx := sysRuleIndex(rules, want)
		if idx < 0 {
			t.Errorf("缺少 GitHub IP 兜底规则: %s", want)
			continue
		}
		if idx < firstSet {
			t.Errorf("%s 排在规则集之前（idx=%d < firstSet=%d），会拖慢所有域名连接", want, idx, firstSet)
		}
		if idx > privIdx {
			t.Errorf("%s 排在私有网段之后（idx=%d > privIdx=%d），私网 IP 会被误代理", want, idx, privIdx)
		}
	}
}

// sing-box：域名规则在 rule_set 之前、outbound=proxy；IP 段兜底在 rule_set 之后。
func TestSingboxGitHubProxyAndIPFallback(t *testing.T) {
	for _, verStr := range []string{"1.11", "1.14", "1.15"} {
		ver, err := parseSBVersion(verStr)
		if err != nil {
			t.Fatal(err)
		}
		sets, _ := normalizeSets(genReq{Core: "singbox", Sets: []setPolicy{
			{ID: "geosite-github", Policy: "DIRECT"},
			{ID: "geosite-cn", Policy: "DIRECT"},
		}})
		warns := []string{}
		g := genSingbox("main-router", ver, testNodes(), sets, nil, &warns, ruleMirrorSel{}, bypassSpec{})
		rules := g.doc["route"].(map[string]any)["rules"].([]any)

		firstSet := -1
		for i, r := range rules {
			if r.(map[string]any)["rule_set"] != nil {
				firstSet = i
				break
			}
		}
		if firstSet < 0 {
			t.Fatalf("%s 没有规则集规则", verStr)
		}
		// 域名：github.com / githubusercontent.com 等必须以 domain_suffix 出现且 outbound=proxy
		for _, line := range systemProxyDomains {
			typ, val, _, _ := parseCustomRule(line)
			key := "domain"
			if typ == "DOMAIN-SUFFIX" {
				key = "domain_suffix"
			}
			found := -1
			for i, r := range rules {
				m := r.(map[string]any)
				if hasInSlice(m[key], val) && m["outbound"] == "PROXY" {
					found = i
					break
				}
			}
			if found < 0 {
				t.Errorf("%s 缺少 GitHub 代理域名 %s", verStr, val)
				continue
			}
			if found > firstSet {
				t.Errorf("%s %s 排在规则集之后（%d > %d）", verStr, val, found, firstSet)
			}
		}
		// IP 兜底：一条 ip_cidr 规则装全部段，outbound=proxy，且在规则集之后
		cidrIdx := -1
		for i, r := range rules {
			m := r.(map[string]any)
			if m["outbound"] == "PROXY" && hasInSlice(m["ip_cidr"], systemProxyCIDRs[0]) {
				cidrIdx = i
				break
			}
		}
		if cidrIdx < 0 {
			t.Errorf("%s 缺少 GitHub IP 兜底规则", verStr)
			continue
		}
		if cidrIdx < firstSet {
			t.Errorf("%s IP 兜底排在规则集之前（%d < %d）", verStr, cidrIdx, firstSet)
		}
	}
}

// MediaFire：两套渲染器都要按域名后缀直连，且位置在规则集之前。
// IP 侧刻意不加（Cloudflare 共享段），所以这里只钉域名。
func TestMediafireDirect(t *testing.T) {
	const want = "DOMAIN-SUFFIX,mediafire.com,DIRECT"

	warns := new([]string)
	sets, _ := normalizeSets(genReq{Core: "mihomo", Sets: []setPolicy{
		// 海外 CDN 默认 PROXY：不前置就会被它抢先，mediafire 直连失效
		{ID: "geosite-category-cdn-!cn", Policy: "PROXY"},
	}})
	g := genMihomo("client", testNodes(), sets, nil, warns, ruleMirrorSel{}, bypassSpec{})
	rules := g.doc["rules"].([]any)
	idx := sysRuleIndex(rules, want)
	if idx < 0 {
		t.Fatalf("mihomo 缺少 mediafire 直连规则 %s", want)
	}
	if fs := firstRuleSetIndex(rules); fs >= 0 && idx > fs {
		t.Errorf("mediafire 直连排在规则集之后（idx=%d > %d），会被海外 CDN 的 PROXY 抢先", idx, fs)
	}
	// DNS 侧必须同源派生，否则 fake-ip 下的直连拿不到真实 IP
	filter := g.doc["dns"].(map[string]any)["fake-ip-filter"].([]any)
	hit := false
	for _, v := range filter {
		if v == "+.mediafire.com" {
			hit = true
		}
	}
	if !hit {
		t.Error("mihomo fake-ip-filter 缺少 +.mediafire.com")
	}

	// sing-box / momo 走同一份清单
	ver, _ := parseSBVersion("1.14")
	warns2 := []string{}
	g2 := genSingbox("main-router", ver, testNodes(), nil, nil, &warns2, ruleMirrorSel{}, bypassSpec{})
	rs := g2.doc["route"].(map[string]any)["rules"].([]any)
	found := false
	for _, r := range rs {
		m := r.(map[string]any)
		if m["outbound"] == "direct" && hasInSlice(m["domain_suffix"], "mediafire.com") {
			found = true
		}
	}
	if !found {
		t.Error("sing-box 缺少 mediafire 直连（domain_suffix → direct）")
	}
	// DNS：mediafire 必须本地解析，否则 remote DoH 返回的 IP 与直连出口对不上
	dnsRules := g2.doc["dns"].(map[string]any)["rules"].([]any)
	dnsHit := false
	for _, r := range dnsRules {
		m := r.(map[string]any)
		if m["server"] == "local" && hasInSlice(m["domain_suffix"], "mediafire.com") {
			dnsHit = true
		}
	}
	if !dnsHit {
		t.Error("sing-box DNS 规则未把 mediafire 交给 local")
	}
}

// TUN 的 MTU 必须显式压到真实链路大小。
// 不写的默认值 9000 会让 MSS 远超路径 MTU，大包被丢弃又收不到分片通知，
// 现象正是「网页能开、git push / 大文件下载卡死」。这是本次 GitHub
// 「推 git 有时连不上」的核心修复之一，必须钉住，否则以后重构容易丢。
func TestTunMTUExplicit(t *testing.T) {
	warns := new([]string)
	// mihomo（router 模式才有 tun）
	g := genMihomo("main-router", testNodes(), nil, nil, warns, ruleMirrorSel{}, bypassSpec{})
	tun, ok := g.doc["tun"].(map[string]any)
	if !ok {
		t.Fatal("mihomo 配置没有 tun 段")
	}
	if got := tun["mtu"]; got != 1500 {
		t.Errorf("mihomo tun.mtu=%v，期望 1500", got)
	}

	// sing-box
	ver, _ := parseSBVersion("1.14")
	warns2 := []string{}
	g2 := genSingbox("main-router", ver, testNodes(), nil, nil, &warns2, ruleMirrorSel{}, bypassSpec{})
	mtu := tunMTU(g2.doc["inbounds"].([]any))
	if mtu != 1500 {
		t.Errorf("sing-box tun.mtu=%v，期望 1500", mtu)
	}

	// openwrt-momo：PPPoE 常见，取 1420 给封装留余量
	warns3 := []string{}
	g3 := genMomo(ver, testNodes(), nil, nil, &warns3, ruleMirrorSel{}, bypassSpec{})
	if m := tunMTU(g3.doc["inbounds"].([]any)); m != 1420 {
		t.Errorf("momo tun-in mtu=%v，期望 1420", m)
	}
}

// tunMTU 从 inbounds 里挖出 tun 入站的 mtu，找不到返回 nil。
func tunMTU(inbounds []any) any {
	for _, in := range inbounds {
		m, ok := in.(map[string]any)
		if !ok {
			continue
		}
		if m["type"] == "tun" {
			return m["mtu"]
		}
	}
	return nil
}

// momo 复用 genSingbox 的 route，必须自动继承 GitHub 强制代理。
func TestMomoInheritsGitHubProxy(t *testing.T) {
	ver, _ := parseSBVersion("1.14")
	warns := []string{}
	g := genMomo(ver, testNodes(), nil, nil, &warns, ruleMirrorSel{}, bypassSpec{})
	rules := g.doc["route"].(map[string]any)["rules"].([]any)
	hit := false
	for _, r := range rules {
		m := r.(map[string]any)
		if m["outbound"] == "PROXY" && hasInSlice(m["domain_suffix"], "github.com") {
			hit = true
		}
	}
	if !hit {
		t.Error("openwrt-momo 没有继承 GitHub 强制代理规则")
	}
}
