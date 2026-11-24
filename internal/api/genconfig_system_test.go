package api

import (
	"strings"
	"testing"
)

// 苹果更新 / OneDrive 登录这类系统关键服务走代理就会坏：苹果更新转圈、
// 账号登录触发风控或证书固定校验失败。用户反馈「生成的配置苹果和微软
// 服务经常出问题」即源于此。这组测试把「无条件直连、且排在规则集之前」
// 钉住——只断言规则写进去了没用，位置错了照样被 PROXY 规则集抢先命中。

// Apple 生态规则集必须可选：用户想代理 App Store / Apple Music 内容服务时
// 有正规入口，而不是手敲 GEOSITE 自定义规则。系统关键域名另有内置直连保护。
func TestAppleRuleCatalogExistsAndDirect(t *testing.T) {
	d, ok := ruleSetIndex["geosite-apple"]
	if !ok {
		t.Fatal("规则目录缺少 geosite-apple")
	}
	if d.Policy != "DIRECT" {
		t.Errorf("geosite-apple 默认应为 DIRECT，实际 %s", d.Policy)
	}
	if d.Cores != "both" {
		t.Errorf("上游 apple 规则集应两种内核都有，实际 %s", d.Cores)
	}
}

func sysRuleIndex(rules []any, want string) int {
	for i, r := range rules {
		if s, ok := r.(string); ok && s == want {
			return i
		}
	}
	return -1
}

func firstRuleSetIndex(rules []any) int {
	for i, r := range rules {
		if s, ok := r.(string); ok && strings.HasPrefix(s, "RULE-SET,") {
			return i
		}
	}
	return -1
}

// mihomo：system 直连规则必须存在、走 DIRECT、且排在所有 RULE-SET 之前。
func TestMihomoSystemDirectRulesBeforeRuleSets(t *testing.T) {
	warns := new([]string)
	// 故意构造一个把 microsoft/onedrive 设成 PROXY 的场景——这正是出问题时
	// 用户用的办公配置。直连规则必须压在这些 PROXY 规则集前面。
	sets, _ := normalizeSets(genReq{Core: "mihomo", Sets: []setPolicy{
		{ID: "geosite-microsoft", Policy: "PROXY"},
		{ID: "geosite-onedrive", Policy: "PROXY"},
		{ID: "geosite-cn", Policy: "DIRECT"},
	}})
	g := genMihomo("client", testNodes(), sets, nil, warns, ruleMirrorSel{}, bypassSpec{})
	rules := g.doc["rules"].([]any)

	firstSet := firstRuleSetIndex(rules)
	if firstSet < 0 {
		t.Fatal("没有生成任何规则集，测试前提不成立")
	}
	for _, line := range systemDirectRules {
		idx := sysRuleIndex(rules, line)
		if idx < 0 {
			t.Errorf("缺少系统直连规则: %s", line)
			continue
		}
		if !strings.HasSuffix(line, ",DIRECT") {
			t.Errorf("%s 不是 DIRECT", line)
		}
		if idx > firstSet {
			t.Errorf("%s 排在规则集之后（idx=%d, firstSet=%d），会被 PROXY 规则集抢先命中", line, idx, firstSet)
		}
	}
}

// 用户自定义规则仍应排在系统直连之前——保留用户显式覆盖（含 REJECT）的能力。
func TestMihomoCustomRulesBeforeSystemDirect(t *testing.T) {
	warns := new([]string)
	g := genMihomo("client", testNodes(), nil,
		[]string{"DOMAIN,login.microsoftonline.com,REJECT"},
		warns, ruleMirrorSel{}, bypassSpec{})
	rules := g.doc["rules"].([]any)
	custom := sysRuleIndex(rules, "DOMAIN,login.microsoftonline.com,REJECT")
	sys := sysRuleIndex(rules, "DOMAIN,login.microsoftonline.com,DIRECT")
	if custom < 0 || sys < 0 {
		t.Fatalf("自定义或系统规则缺失: custom=%d sys=%d", custom, sys)
	}
	if custom > sys {
		t.Errorf("自定义规则应排在系统直连之前（custom=%d, sys=%d）", custom, sys)
	}
}

// sing-box：route.rules 里同样要前置 DIRECT，且 DNS 侧这些域名走 local。
func TestSingboxSystemDirectRulesAndDNS(t *testing.T) {
	for _, verStr := range []string{"1.11", "1.14", "1.15"} {
		ver, err := parseSBVersion(verStr)
		if err != nil {
			t.Fatal(err)
		}
		sets, _ := normalizeSets(genReq{Core: "singbox", Sets: []setPolicy{
			{ID: "geosite-microsoft", Policy: "PROXY"},
			{ID: "geosite-onedrive", Policy: "PROXY"},
			{ID: "geosite-cn", Policy: "DIRECT"},
		}})
		warns := []string{}
		g := genSingbox("main-router", ver, testNodes(), sets, nil, &warns, ruleMirrorSel{}, bypassSpec{})
		rules := g.doc["route"].(map[string]any)["rules"].([]any)

		// 找到第一条规则集规则的位置
		firstSet := -1
		for i, r := range rules {
			m := r.(map[string]any)
			if m["rule_set"] != nil {
				firstSet = i
				break
			}
		}
		if firstSet < 0 {
			t.Fatalf("%s 没有规则集规则", verStr)
		}
		// 逐条核对 systemDirectRules：域名出现在 domain/domain_suffix，outbound=direct，
		// 且位置在规则集之前
		for _, line := range systemDirectRules {
			typ, val, _, _ := parseCustomRule(line)
			found := -1
			for i, r := range rules {
				m := r.(map[string]any)
				key := "domain"
				if typ == "DOMAIN-SUFFIX" {
					key = "domain_suffix"
				} else if m["domain_suffix"] != nil {
					continue
				}
				if hasInSlice(m[key], val) && m["outbound"] == "direct" {
					found = i
					break
				}
			}
			if found < 0 {
				t.Errorf("%s 缺少系统直连域名 %s（%s）", verStr, val, typ)
				continue
			}
			if found > firstSet {
				t.Errorf("%s %s 排在规则集之后（%d > %d）", verStr, val, found, firstSet)
			}
		}

		// DNS：这些域名必须走 local，否则落 final:remote 形成「DNS 靠代理、流量直连」死锁
		dnsRules := g.doc["dns"].(map[string]any)["rules"].([]any)
		loginLocal := false
		for _, r := range dnsRules {
			m := r.(map[string]any)
			if m["server"] == "local" && hasInSlice(m["domain"], "login.microsoftonline.com") {
				loginLocal = true
			}
		}
		if !loginLocal {
			t.Errorf("%s DNS 规则未把登录域名交给 local: %v", verStr, dnsRules)
		}
	}
}

// mock 目标（openwrt-momo）复用 genSingbox，必须自动继承系统直连规则。
func TestMomoInheritsSystemDirect(t *testing.T) {
	v, _ := parseSBVersion("1.14")
	warns := []string{}
	g := genMomo(v, testNodes(), nil, nil, &warns, ruleMirrorSel{}, bypassSpec{})
	rules := g.doc["route"].(map[string]any)["rules"].([]any)
	found := false
	for _, r := range rules {
		m := r.(map[string]any)
		if m["outbound"] == "direct" && hasInSlice(m["domain"], "login.microsoftonline.com") {
			found = true
		}
	}
	if !found {
		t.Error("openwrt-momo 目标没有继承系统关键服务直连规则")
	}
}

// fake-ip-filter 与直连规则必须同源派生，避免两处清单漂移导致「规则对但连不上」。
func TestSystemDirectFakeIPFilterDerived(t *testing.T) {
	filter := systemDirectFakeIPFilter()
	// 按集合比，不按位置：fake-ip-filter 是集合语义，派生顺序（精确域名在前、
	// 后缀在后）不该被当成契约。
	want := map[string]bool{}
	exact, suffix := systemDirectDNSForms()
	for _, d := range exact {
		want[d] = true
	}
	for _, s := range suffix {
		want["+."+s] = true
	}
	for _, s := range localOnlySuffixes {
		want["+."+s] = true
	}
	if len(filter) != len(want) || len(want) != len(systemDirectRules)+len(localOnlySuffixes) {
		t.Fatalf("filter %d 条、派生 %d 条、规则 %d 条 + 局域网后缀 %d 条，应相等",
			len(filter), len(want), len(systemDirectRules), len(localOnlySuffixes))
	}
	for _, got := range filter {
		if !want[got] {
			t.Errorf("filter 里多出 %q", got)
			continue
		}
		delete(want, got)
	}
	for miss := range want {
		t.Errorf("filter 少了 %q", miss)
	}
	// 关键域名必须在 mihomo 的 fake-ip-filter 里出现。每个「类」各取一条，
	// 避免下次只改一类就以为全好了。
	warns := new([]string)
	g := genMihomo("main-router", testNodes(), nil, nil, warns, ruleMirrorSel{}, bypassSpec{})
	got := g.doc["dns"].(map[string]any)["fake-ip-filter"].([]any)
	for _, want := range []string{
		"mesu.apple.com",            // 类 1 Apple 更新
		"swcdn.apple.com",           // 类 1 Apple 更新（此前漏收）
		"+.cdn-apple.com",           // 类 1 Apple 更新 CDN 后缀
		"+.push.apple.com",          // 类 8 Apple 推送（APNs）
		"login.microsoftonline.com", // 类 3 微软登录
		"+.windowsupdate.com",       // 类 4 Windows 更新
		"+.msftauth.net",            // 类 3 登录页资源
		"+.msftconnecttest.com",     // 类 5 连通性检测
	} {
		ok := false
		for _, v := range got {
			if v == want {
				ok = true
			}
		}
		if !ok {
			t.Errorf("fake-ip-filter 缺少 %s", want)
		}
	}
}

// 「这些名字必须由本地解析」在两套产物里都得成立。
//
// 两套内核的机制不同：mihomo 是 fake-ip 模式下把域名排除出 fake-ip（否则拿到
// 198.18.x.x），sing-box 是 dns.rules → local（否则落 final: remote，经代理问
// DoH）。同一条需求两种写法，各自写一遍时最容易漏一边——sing-box 侧的
// .lan / .local 就这样漏了整整几版：局域网主机名解析不了，两边都不报错。
//
// 这里断言的是「共享契约」：直连清单里的域名 + 局域网专用后缀。
// 不进契约的：STUN / Xbox / NTP 那几个 fake-ip 专属条目（sing-box 没有 fake-ip，
// 本来就不会把它们的域名映射成假地址，无需对应规则）。
func TestBothCoresResolveDirectNamesLocally(t *testing.T) {
	warns := []string{}
	strs := func(v any) []string {
		out := []string{}
		if arr, ok := v.([]any); ok {
			for _, x := range arr {
				if s, ok := x.(string); ok {
					out = append(out, s)
				}
			}
		}
		return out
	}

	want := map[string]bool{}
	exact, suffix := systemDirectDNSForms()
	for _, d := range exact {
		want[d] = true
	}
	for _, s := range suffix {
		want["+."+s] = true
	}
	for _, s := range localOnlySuffixes {
		want["+."+s] = true
	}
	if len(want) == 0 {
		t.Fatal("共享契约是空的，这份用例会变成空断言")
	}

	mg := genMihomo("main-router", testNodes(), nil, nil, &warns, ruleMirrorSel{}, bypassSpec{})
	mihomoSet := map[string]bool{}
	for _, d := range strs(mg.doc["dns"].(map[string]any)["fake-ip-filter"]) {
		mihomoSet[d] = true
	}

	v, _ := parseSBVersion("1.14")
	sg := genSingbox("main-router", v, testNodes(), nil, nil, &warns, ruleMirrorSel{}, bypassSpec{})
	sbSet := map[string]bool{}
	for _, r := range sg.doc["dns"].(map[string]any)["rules"].([]any) {
		m, ok := r.(map[string]any)
		if !ok || m["server"] != "local" {
			continue
		}
		for _, d := range strs(m["domain"]) {
			sbSet[d] = true
		}
		for _, d := range strs(m["domain_suffix"]) {
			sbSet["+."+d] = true
		}
	}

	for d := range want {
		if !mihomoSet[d] {
			t.Errorf("mihomo 的 fake-ip-filter 少了 %q：该域名会拿到假 IP", d)
		}
		if !sbSet[d] {
			t.Errorf("sing-box 的 dns.rules 没把 %q 交给本地解析：会经代理问 DoH", d)
		}
	}
}
