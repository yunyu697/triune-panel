package api

import (
	"net"
	"strings"
	"testing"
)

// 这组测试不针对某个具体域名，而是针对**缺陷类别**。
//
// 背景：过去的问题是「发现一个域名补一个」，补完苹果漏微软、补完微软漏更新，
// 而且每次都要重新判断位置（必须在规则集之前）、DNS（必须走本地）、fake-ip
// （必须排除）三处是否都改了。这里的断言把这三件事变成对**每一条**内置规则
// 都成立的硬约束——以后往清单里加东西，只要漏改任何一处就会红。
//
// 换句话说：这些用例守住的是「机制」，不是「清单内容」。

// 每类关键服务至少要有代表条目。
//
// 上面那些用例遍历的是「清单里现有什么」，因此整类被删掉时它们照样绿。
// 这条用例按**类别**守住下限：谁把一整类删了，立刻红。
func TestSystemDirectCoversEachCriticalClass(t *testing.T) {
	has := func(vals ...string) bool {
		for _, line := range systemDirectRules {
			_, val, _, ok := parseCustomRule(line)
			if !ok {
				continue
			}
			for _, v := range vals {
				if val == v {
					return true
				}
			}
		}
		return false
	}
	classes := []struct {
		name string
		vals []string
	}{
		{"Apple 系统更新", []string{"mesu.apple.com", "swscan.apple.com", "swcdn.apple.com"}},
		{"Apple ID 登录", []string{"appleid.apple.com", "idmsa.apple.com"}},
		// 装 App / 跑更新都要过 OCSP；走代理时节点一抖就报「无法验证」。
		{"OCSP 证书校验", []string{"ocsp.apple.com", "ocsp2.apple.com"}},
		// 定位走代理会漂到节点所在国，连带时区与区域判定一起错。
		{"定位与区域判定", []string{"gs-loc.apple.com", "ls.apple.com"}},
		{"Microsoft 账号登录", []string{"login.microsoftonline.com", "login.live.com"}},
		{"Microsoft 认证资源", []string{"msftauth.net", "msauth.net"}},
		{"Windows 更新", []string{"windowsupdate.com", "update.microsoft.com"}},
		{"Windows 激活/授权", []string{"activation.sls.microsoft.com", "licensing.mp.microsoft.com"}},
		{"系统连通性检测", []string{"msftconnecttest.com", "msftncsi.com", "captive.apple.com"}},
		{"用户指定直连（MediaFire）", []string{"mediafire.com"}},
	}
	for _, c := range classes {
		if !has(c.vals...) {
			t.Errorf("「%s」整类缺失：期望至少包含 %v 中的一条", c.name, c.vals)
		}
	}
}

// 清单里的规则必须能被两套渲染器都表达出来。
//
// 曾经 sing-box 的注入 switch 只认 DOMAIN / DOMAIN-SUFFIX，往清单里加一条
// DOMAIN-KEYWORD 时 mihomo 生效、sing-box 静默不生效——配置合法、内核不报错，
// 是最难查的一类缺陷。这里从源头限制类型。
func TestSystemListsUseSupportedRuleTypes(t *testing.T) {
	type list struct {
		name       string
		lines      []string
		wantPolicy string
	}
	lists := []list{
		{"systemDirectRules", systemDirectRules, "DIRECT"},
		{"systemProxyDomains", systemProxyDomains, "PROXY"},
	}
	for _, l := range lists {
		if len(l.lines) == 0 {
			t.Fatalf("%s 为空", l.name)
		}
		for _, line := range l.lines {
			typ, val, pol, ok := parseCustomRule(line)
			if !ok {
				t.Errorf("%s 里有无法解析的规则: %q", l.name, line)
				continue
			}
			if typ != "DOMAIN" && typ != "DOMAIN-SUFFIX" {
				t.Errorf("%s 里出现 %s：DNS 与 fake-ip 的派生只认 DOMAIN/DOMAIN-SUFFIX，"+
					"这种类型会在一侧静默失效: %q", l.name, typ, line)
			}
			if pol != l.wantPolicy {
				t.Errorf("%s 里 %q 的策略是 %s，期望 %s", l.name, line, pol, l.wantPolicy)
			}
			// 值必须是合法域名形态：不带协议、不带端口、不带通配符。
			// 这些写错了内核不报错，只是永远匹配不上。
			if strings.ContainsAny(val, ":/ ") || strings.HasPrefix(val, "*") {
				t.Errorf("%s 里 %q 的值不是裸域名（不要带协议/端口/通配符）", l.name, line)
			}
		}
	}
}

// 同一条规则不许写两遍——重复项多半是「补丁叠补丁」的痕迹，
// 也说明有人没看到已有的那一条。
func TestSystemListsNoDuplicates(t *testing.T) {
	for name, lines := range map[string][]string{
		"systemDirectRules":  systemDirectRules,
		"systemProxyDomains": systemProxyDomains,
	} {
		seen := map[string]int{}
		for _, line := range lines {
			typ, val, pol, ok := parseCustomRule(line)
			if !ok {
				continue
			}
			key := typ + "|" + val + "|" + pol
			seen[key]++
			if seen[key] == 2 {
				t.Errorf("%s 里重复: %q", name, line)
			}
		}
	}
}

// 同一个域名不能既「强制直连」又「强制代理」。
// 真出现这种交集时，实际结果由书写顺序决定——那是运气，不是设计。
func TestSystemDirectAndProxyDisjoint(t *testing.T) {
	direct := map[string]string{} // 域名 -> 类型
	for _, line := range systemDirectRules {
		if typ, val, _, ok := parseCustomRule(line); ok {
			direct[val] = typ
		}
	}
	for _, line := range systemProxyDomains {
		typ, val, _, ok := parseCustomRule(line)
		if !ok {
			continue
		}
		if dtyp, hit := direct[val]; hit {
			t.Errorf("域名 %s 同时在直连(%s)与代理(%s)清单里", val, dtyp, typ)
		}
		// 后缀重叠也算冲突：+.github.com 与 github.com 这类组合会让结果取决于顺序。
		for dv, dtyp := range direct {
			if typ == "DOMAIN-SUFFIX" && strings.HasSuffix(dv, "."+val) {
				t.Errorf("%s（代理后缀）覆盖了直连域名 %s（%s）", val, dv, dtyp)
			}
			if dtyp == "DOMAIN-SUFFIX" && strings.HasSuffix(val, "."+dv) {
				t.Errorf("%s（直连后缀）覆盖了代理域名 %s（%s）", dv, val, typ)
			}
		}
	}
}

// 【核心不变量】每一条强制直连规则，必须同时落到三个地方：
//  1. mihomo rules —— 且在第一条 RULE-SET 之前（否则被 PROXY 规则集抢先）
//  2. mihomo dns.fake-ip-filter —— 否则 TUN 下拿到假 IP，证书固定的客户端连不上
//  3. sing-box route.rules + dns.rules(local) —— 否则 DNS 走代理、流量直连，形成死锁
//
// 过去每次都是「只改了一处」，所以这条断言遍历全清单而不是抽查几条。
func TestEveryDirectRuleReachesAllThreeSinks(t *testing.T) {
	warns := new([]string)
	sets, _ := normalizeSets(genReq{Core: "mihomo", Sets: []setPolicy{
		{ID: "geosite-cn", Policy: "DIRECT"},
		{ID: "geosite-microsoft", Policy: "PROXY"},
	}})
	mihomo := genMihomo("main-router", testNodes(), sets, nil, warns, ruleMirrorSel{}, bypassSpec{})
	rules := mihomo.doc["rules"].([]any)
	firstSet := firstRuleSetIndex(rules)
	if firstSet < 0 {
		t.Fatal("mihomo 没有生成规则集，测试前提不成立")
	}
	filter := mihomo.doc["dns"].(map[string]any)["fake-ip-filter"].([]any)
	inFilter := func(want string) bool {
		for _, v := range filter {
			if v == want {
				return true
			}
		}
		return false
	}

	ver, err := parseSBVersion("1.14")
	if err != nil {
		t.Fatal(err)
	}
	sw := []string{}
	sbSets, _ := normalizeSets(genReq{Core: "singbox", Sets: []setPolicy{
		{ID: "geosite-microsoft", Policy: "PROXY"},
	}})
	sb := genSingbox("main-router", ver, testNodes(), sbSets, nil, &sw, ruleMirrorSel{}, bypassSpec{})
	sbRules := sb.doc["route"].(map[string]any)["rules"].([]any)
	sbDNS := sb.doc["dns"].(map[string]any)["rules"].([]any)

	for _, line := range systemDirectRules {
		typ, val, _, ok := parseCustomRule(line)
		if !ok {
			continue
		}
		// 1. mihomo 路由
		idx := sysRuleIndex(rules, line)
		if idx < 0 {
			t.Errorf("mihomo 缺少直连规则: %s", line)
		} else if idx > firstSet {
			t.Errorf("mihomo 的 %s 排在规则集之后（%d > %d）", line, idx, firstSet)
		}
		// 2. mihomo fake-ip-filter
		want := val
		if typ == "DOMAIN-SUFFIX" {
			want = "+." + val
		}
		if !inFilter(want) {
			t.Errorf("mihomo fake-ip-filter 缺少 %s（对应 %s）", want, line)
		}
		// 3. sing-box 路由 + DNS
		key := "domain"
		if typ == "DOMAIN-SUFFIX" {
			key = "domain_suffix"
		}
		if !sbHasRule(sbRules, key, val, "direct") {
			t.Errorf("sing-box route 缺少直连规则 %s=%s", key, val)
		}
		if !sbHasRule(sbDNS, key, val, "") || !sbRuleServerIs(sbDNS, key, val, "local") {
			t.Errorf("sing-box dns 未把 %s=%s 交给 local（会落 final:remote，形成 DNS 靠代理的死锁）", key, val)
		}
	}
}

// 强制代理的域名必须在规则集之前——用户的规则集把它设成 DIRECT 时也要压得住。
func TestEveryProxyDomainBeforeRuleSets(t *testing.T) {
	warns := new([]string)
	sets, _ := normalizeSets(genReq{Core: "mihomo", Sets: []setPolicy{
		{ID: "geosite-cn", Policy: "DIRECT"},
		{ID: "geosite-github", Policy: "DIRECT"},
	}})
	mihomo := genMihomo("main-router", testNodes(), sets, nil, warns, ruleMirrorSel{}, bypassSpec{})
	rules := mihomo.doc["rules"].([]any)
	firstSet := firstRuleSetIndex(rules)
	if firstSet < 0 {
		t.Fatal("mihomo 没有生成规则集")
	}
	for _, line := range systemProxyDomains {
		idx := sysRuleIndex(rules, line)
		if idx < 0 {
			t.Errorf("mihomo 缺少强制代理规则: %s", line)
			continue
		}
		if idx > firstSet {
			t.Errorf("mihomo 的 %s 排在规则集之后（%d > %d）", line, idx, firstSet)
		}
	}

	ver, _ := parseSBVersion("1.14")
	sw := []string{}
	sbSets, _ := normalizeSets(genReq{Core: "singbox", Sets: []setPolicy{
		{ID: "geosite-github", Policy: "DIRECT"},
	}})
	sb := genSingbox("main-router", ver, testNodes(), sbSets, nil, &sw, ruleMirrorSel{}, bypassSpec{})
	sbRules := sb.doc["route"].(map[string]any)["rules"].([]any)
	sbFirst := -1
	for i, r := range sbRules {
		if r.(map[string]any)["rule_set"] != nil {
			sbFirst = i
			break
		}
	}
	if sbFirst < 0 {
		t.Fatal("sing-box 没有生成规则集规则")
	}
	for _, line := range systemProxyDomains {
		typ, val, _, ok := parseCustomRule(line)
		if !ok {
			continue
		}
		key := "domain"
		if typ == "DOMAIN-SUFFIX" {
			key = "domain_suffix"
		}
		idx := sbFirstIndex(sbRules, key, val, "PROXY")
		if idx < 0 {
			t.Errorf("sing-box route 缺少强制代理规则 %s=%s", key, val)
			continue
		}
		if idx > sbFirst {
			t.Errorf("sing-box 的 %s=%s 排在规则集之后（%d > %d）", key, val, idx, sbFirst)
		}
	}
}

// NTP 这类「按端口识别、拿不到域名」的流量必须有独立兜底。
// 漏了它的症状是系统时间漂移 → TLS 证书随机报错，没人会想到是代理。
func TestNTPDirectByPortInBothCores(t *testing.T) {
	warns := new([]string)
	mihomo := genMihomo("main-router", testNodes(), nil, nil, warns, ruleMirrorSel{}, bypassSpec{})
	rules := mihomo.doc["rules"].([]any)
	firstSet := firstRuleSetIndex(rules)
	found := -1
	for i, r := range rules {
		if s, _ := r.(string); s == "DST-PORT,123,DIRECT" {
			found = i
			break
		}
	}
	if found < 0 {
		t.Error("mihomo 缺少 DST-PORT,123,DIRECT（NTP 会落进 MATCH,PROXY）")
	} else if firstSet >= 0 && found > firstSet {
		t.Errorf("mihomo 的 NTP 规则排在规则集之后（%d > %d）", found, firstSet)
	}

	ver, _ := parseSBVersion("1.14")
	sw := []string{}
	sb := genSingbox("main-router", ver, testNodes(), nil, nil, &sw, ruleMirrorSel{}, bypassSpec{})
	sbFound := false
	for _, r := range sb.doc["route"].(map[string]any)["rules"].([]any) {
		m := r.(map[string]any)
		if m["outbound"] != "direct" {
			continue
		}
		if ports, ok := m["port"].([]any); ok && len(ports) == 1 && ports[0] == 123 {
			sbFound = true
		}
	}
	if !sbFound {
		t.Error("sing-box 缺少 port=123 → direct 的规则")
	}
}

// 局域网 / 特殊用途网段必须覆盖全：漏一个就是一类「设备发现失败 /
// 内网服务连不上」的问题。这里按类别断言，不依赖具体清单顺序。
func TestPrivateCIDRsCoverSpecialRanges(t *testing.T) {
	must := map[string]string{
		"192.168.0.0/16":     "RFC1918 C 类",
		"10.0.0.0/8":         "RFC1918 A 类",
		"172.16.0.0/12":      "RFC1918 B 类",
		"127.0.0.0/8":        "本机回环",
		"169.254.0.0/16":     "链路本地",
		"100.64.0.0/10":      "CGNAT（Tailscale/ZeroTier 打洞）",
		"224.0.0.0/4":        "IPv4 组播（mDNS/SSDP/投屏/打印机）",
		"255.255.255.255/32": "有限广播",
		"fc00::/7":           "IPv6 唯一本地",
		"fe80::/10":          "IPv6 链路本地",
		"ff00::/8":           "IPv6 组播",
	}
	have := map[string]bool{}
	for _, c := range privateCIDRs {
		if _, _, err := net.ParseCIDR(c); err != nil {
			t.Errorf("privateCIDRs 里的 %s 不是合法 CIDR: %v", c, err)
		}
		have[c] = true
	}
	for c, why := range must {
		if !have[c] {
			t.Errorf("privateCIDRs 缺少 %s（%s）", c, why)
		}
	}
}

// IP 段兜底是「最后一道网」，前缀写宽了会把整片 CDN 拉走。
// 这里限制单条网段不得宽于 /20（4096 个地址）——GitHub 这类自有段都满足，
// 而一片 Cloudflare / Akamai 共享段一定超限，能在加进来时就拦住。
func TestSystemProxyCIDRsNotTooWide(t *testing.T) {
	if len(systemProxyCIDRs) == 0 {
		t.Fatal("systemProxyCIDRs 为空")
	}
	for _, c := range systemProxyCIDRs {
		_, ipnet, err := net.ParseCIDR(c)
		if err != nil {
			t.Errorf("systemProxyCIDRs 里的 %s 不是合法 CIDR: %v", c, err)
			continue
		}
		ones, bits := ipnet.Mask.Size()
		maxOnes := 20
		if bits == 128 {
			maxOnes = 48 // IPv6 同理：不允许比 /48 更宽
		}
		if ones < maxOnes {
			t.Errorf("%s 的前缀太宽（/%d < /%d）：这种段几乎都是共享 CDN，"+
				"按它强制代理会连带把大量无关站点送进隧道", c, ones, maxOnes)
		}
	}
}

// sbRuleBody 是「类型 → 表达式」的唯一出口。它必须覆盖清单允许的全部类型，
// 并对确实无法表达的类型返回 nil（调用方据此告警，而不是静默丢弃）。
func TestSBRuleBodyCoversSupportedTypes(t *testing.T) {
	supported := []string{"DOMAIN", "DOMAIN-SUFFIX", "DOMAIN-KEYWORD", "IP-CIDR", "IP-CIDR6"}
	for _, typ := range supported {
		val := "example.com"
		if typ == "IP-CIDR" {
			val = "1.2.3.0/24"
		}
		if typ == "IP-CIDR6" {
			val = "2001:db8::/32"
		}
		if sbRuleBody(typ, val) == nil {
			t.Errorf("sbRuleBody 未覆盖 %s", typ)
		}
	}
	// 裸 IP 必须自动补前缀长度，否则 sing-box 的 ip_cidr 不认。
	m := sbRuleBody("IP-CIDR", "1.2.3.4")
	if m == nil || !hasInSlice(m["ip_cidr"], "1.2.3.4/32") {
		t.Errorf("裸 IPv4 未补 /32: %v", m)
	}
	m = sbRuleBody("IP-CIDR6", "2001:db8::1")
	if m == nil || !hasInSlice(m["ip_cidr"], "2001:db8::1/128") {
		t.Errorf("裸 IPv6 未补 /128: %v", m)
	}
	// 规则集类型由 provider 机制单独处理，这里必须返回 nil 让调用方走另一条路。
	if sbRuleBody("GEOSITE", "cn") != nil {
		t.Error("GEOSITE 应返回 nil，由 geoProviderFor 单独处理")
	}
}

// 自定义 GEOIP 规则在 sing-box 里必须引用 geoip-* 的 tag。
// 曾经这里固定拼 geosite-<val>：规则集里恰好有同名 geosite 时静默用错集合，
// 没有时 sing-box 启动 FATAL。
func TestSingboxCustomGeoipUsesGeoipTag(t *testing.T) {
	ver, _ := parseSBVersion("1.14")
	warns := []string{}
	g := genSingbox("main-router", ver, testNodes(), nil,
		[]string{"GEOIP,cn,DIRECT"}, &warns, ruleMirrorSel{}, bypassSpec{})
	route := g.doc["route"].(map[string]any)
	rules := route["rules"].([]any)

	ref := ""
	for _, r := range rules {
		m := r.(map[string]any)
		if rs, ok := m["rule_set"].([]any); ok && len(rs) == 1 {
			if s, _ := rs[0].(string); s == "geoip-cn" {
				ref = s
			}
		}
	}
	if ref != "geoip-cn" {
		t.Errorf("自定义 GEOIP 规则未引用 geoip-cn（实际 %q）", ref)
	}
	// 引用的 tag 必须在 rule_set 定义里真的存在，否则启动直接失败。
	defined := map[string]bool{}
	for _, rs := range route["rule_set"].([]any) {
		if tag, _ := rs.(map[string]any)["tag"].(string); tag != "" {
			defined[tag] = true
		}
	}
	if !defined["geoip-cn"] {
		t.Errorf("route.rule_set 里没有 geoip-cn 定义（有 %v）", defined)
	}
}

// ---- 小工具 ----

// sbHasRule sing-box 规则里是否存在某条匹配项（outbound 为空表示不校验出站）。
func sbHasRule(rules []any, key, val, outbound string) bool {
	for _, r := range rules {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		if !hasInSlice(m[key], val) {
			continue
		}
		if outbound != "" && m["outbound"] != outbound {
			continue
		}
		return true
	}
	return false
}

// sbRuleServerIs sing-box DNS 规则里该匹配项是否指向指定 DNS 服务器。
func sbRuleServerIs(rules []any, key, val, server string) bool {
	for _, r := range rules {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		if hasInSlice(m[key], val) && m["server"] == server {
			return true
		}
	}
	return false
}

// sbFirstIndex 返回第一条匹配的 sing-box 规则下标。
func sbFirstIndex(rules []any, key, val, outbound string) int {
	for i, r := range rules {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		if hasInSlice(m[key], val) && m["outbound"] == outbound {
			return i
		}
	}
	return -1
}
