package api

// 自定义规则的「写对了但永远不生效」回归测试。
//
// 这一类缺陷的共同形态：语法合法、内核不报错、日志干净，只有结果不对。
// 用户在界面上看到「规则已保存」，实际一次都没匹配上，于是把时间花在怀疑
// 分流顺序、内核版本、DNS 上。
//
// 真实事故：`DOMAIN-SUFFIX,applog.fqnovel.com,DIRECT` 匹配不到
// `log5-applog.fqnovel.com`（`log5-applog` 是一个 DNS 标签，连字符不是点），
// 因此那条放行规则从未生效。同类的还有前置 `*.`、DOMAIN 带通配符、IP-CIDR
// 缺掩码、以及写在策略后面的 `no-resolve` 被当成策略名整条丢弃。

import (
	"strings"
	"testing"
)

func mihomoRuleList(t *testing.T, g *generator) []string {
	t.Helper()
	raw, ok := g.doc["rules"].([]any)
	if !ok {
		t.Fatal("mihomo 配置缺少 rules")
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// 四段写法是 clash 官方语法，必须原样保留到 mihomo 产物里。
// 之前的实现把最后一段当策略，`no-resolve` 变成策略名 → 整条规则被丢弃。
func TestCustomRuleKeepsNoResolve(t *testing.T) {
	line := "IP-CIDR,1.2.3.0/24,DIRECT,no-resolve"
	c, ok := parseCustomRuleFull(line)
	if !ok {
		t.Fatalf("标准四段写法必须可解析: %s", line)
	}
	if !c.NoResolve {
		t.Error("no-resolve 没有被识别")
	}
	if c.Val != "1.2.3.0/24" || c.Pol != "DIRECT" {
		t.Errorf("值与策略解析错位: val=%q pol=%q", c.Val, c.Pol)
	}

	warns := new([]string)
	g := genMihomo("main-router", testNodes(), nil, []string{line}, warns, genOpts{})
	found := false
	for _, r := range mihomoRuleList(t, g) {
		if r == line {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("生成结果里没有 %q\n实际规则: %v", line, mihomoRuleList(t, g))
	}
	if len(*warns) != 0 {
		t.Errorf("标准写法不该产生警告: %v", *warns)
	}
}

// DOMAIN-SUFFIX 的前置 `*.` 是字面量，必须去掉，否则规则永远匹配不上。
func TestCustomRuleNormalizesWildcardSuffix(t *testing.T) {
	for _, in := range []string{
		"DOMAIN-SUFFIX,*.foo.com,DIRECT",
		"DOMAIN-SUFFIX,.foo.com,DIRECT",
	} {
		c, ok := parseCustomRuleFull(in)
		if !ok {
			t.Fatalf("%q 应当被纠正而不是丢弃", in)
		}
		if c.Val != "foo.com" {
			t.Errorf("%q 规范化后应为 foo.com，实际 %q", in, c.Val)
		}
		if len(c.Notes) == 0 {
			t.Errorf("%q 纠正了值却没有说明，用户不会知道发生了什么", in)
		}
	}

	warns := new([]string)
	g := genMihomo("main-router", testNodes(), nil,
		[]string{"DOMAIN-SUFFIX,*.foo.com,DIRECT"}, warns, genOpts{})
	for _, r := range mihomoRuleList(t, g) {
		if strings.Contains(r, "*.") {
			t.Errorf("产物里仍带通配符，这条规则永远不会命中: %q", r)
		}
	}
	if len(*warns) == 0 {
		t.Error("值被纠正时必须提示用户")
	}
}

// DOMAIN 是完整相等匹配，带通配符等于空操作；能确定意图时改成后缀。
func TestCustomRuleWildcardDomainBecomesSuffix(t *testing.T) {
	c, ok := parseCustomRuleFull("DOMAIN,*.foo.com,DIRECT")
	if !ok {
		t.Fatal("应纠正为 DOMAIN-SUFFIX 而不是丢弃")
	}
	if c.Typ != "DOMAIN-SUFFIX" || c.Val != "foo.com" {
		t.Errorf("期望 DOMAIN-SUFFIX,foo.com，实际 %s,%s", c.Typ, c.Val)
	}
	// 通配符在中间时无法用后缀表达，必须拒收并说明原因。
	if _, ok := parseCustomRuleFull("DOMAIN,foo.*.com,DIRECT"); ok {
		t.Error("中间带通配符的 DOMAIN 无法表达，应当拒收")
	}
}

// 关键字里的 `*` 同样是字面量，去掉即可。
func TestCustomRuleKeywordStripsWildcard(t *testing.T) {
	c, ok := parseCustomRuleFull("DOMAIN-KEYWORD,*applog*,DIRECT")
	if !ok {
		t.Fatal("应纠正而不是丢弃")
	}
	if c.Val != "applog" {
		t.Errorf("期望 applog，实际 %q", c.Val)
	}
}

// 缺掩码的 IP 在两种产物里必须得到同一个值。
// 之前只有 sing-box 侧补 /32，mihomo 侧原样输出，同一行规则一边能用一边起不来。
func TestCustomRuleCidrGetsMaskBothCores(t *testing.T) {
	cases := []struct {
		line string
		typ  string
		val  string
	}{
		{"IP-CIDR,1.2.3.4,DIRECT", "IP-CIDR", "1.2.3.4/32"},
		{"IP-CIDR6,2001:db8::1,DIRECT", "IP-CIDR6", "2001:db8::1/128"},
		{"IP-CIDR,2001:db8::1,DIRECT", "IP-CIDR6", "2001:db8::1/128"},
		{"IP-CIDR6,1.2.3.4,DIRECT", "IP-CIDR", "1.2.3.4/32"},
	}
	for _, tc := range cases {
		c, ok := parseCustomRuleFull(tc.line)
		if !ok {
			t.Errorf("%q 应当可解析", tc.line)
			continue
		}
		if c.Typ != tc.typ || c.Val != tc.val {
			t.Errorf("%q → 期望 %s,%s，实际 %s,%s", tc.line, tc.typ, tc.val, c.Typ, c.Val)
		}
	}

	warns := new([]string)
	m := genMihomo("main-router", testNodes(), nil,
		[]string{"IP-CIDR,1.2.3.4,DIRECT"}, warns, genOpts{})
	mv := ""
	for _, r := range mihomoRuleList(t, m) {
		if strings.Contains(r, "1.2.3.4") {
			mv = r
		}
	}
	if mv != "IP-CIDR,1.2.3.4/32,DIRECT" {
		t.Errorf("mihomo 侧缺掩码未补全: %q", mv)
	}

	ver, err := parseSBVersion("1.14")
	if err != nil {
		t.Fatal(err)
	}
	sw := new([]string)
	sb := genSingbox("main-router", ver, testNodes(), nil,
		[]string{"IP-CIDR,1.2.3.4,DIRECT"}, sw, genOpts{})
	sv := ""
	for _, r := range sb.doc["route"].(map[string]any)["rules"].([]any) {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		ip, ok := m["ip_cidr"].([]any)
		if !ok {
			continue
		}
		for _, x := range ip {
			if s, ok := x.(string); ok && strings.Contains(s, "1.2.3.4") {
				sv = s
			}
		}
	}
	if sv != "1.2.3.4/32" {
		t.Errorf("sing-box 侧与 mihomo 侧不一致: %q", sv)
	}
}

// 写错的地址要在生成前就被拦下，并且告诉用户错在哪——否则用户看到的是
// 「保存成功、规则没出现」，只能靠猜。
func TestCustomRuleRejectsBadCidrWithReason(t *testing.T) {
	for _, line := range []string{
		"IP-CIDR,1.2.3.0/33,DIRECT",
		"IP-CIDR,999.1.1.1,DIRECT",
		"IP-CIDR,not-an-ip,DIRECT",
	} {
		c, ok := parseCustomRuleFull(line)
		if ok {
			t.Errorf("%q 应当被拒收", line)
			continue
		}
		if c.Reason == "" {
			t.Errorf("%q 被拒收但没给原因", line)
		}
	}

	warns := new([]string)
	genMihomo("main-router", testNodes(), nil,
		[]string{"IP-CIDR,1.2.3.0/33,DIRECT"}, warns, genOpts{})
	if len(*warns) == 0 {
		t.Fatal("非法规则必须产生警告")
	}
	if !strings.Contains((*warns)[0], "无法解析") || !strings.Contains((*warns)[0], "CIDR") {
		t.Errorf("警告要说明原因，实际: %q", (*warns)[0])
	}
}

// no-resolve 写在域名规则上：摘掉并说明，不能让它变成「整份配置解析失败」。
func TestCustomRuleNoResolveIgnoredOnDomain(t *testing.T) {
	c, ok := parseCustomRuleFull("DOMAIN,foo.com,DIRECT,no-resolve")
	if !ok {
		t.Fatal("域名规则带 no-resolve 不应整条丢弃")
	}
	if c.NoResolve {
		t.Error("域名规则的 no-resolve 必须摘掉")
	}
	if len(c.Notes) == 0 {
		t.Error("摘掉 no-resolve 时要告诉用户")
	}

	warns := new([]string)
	g := genMihomo("main-router", testNodes(), nil,
		[]string{"DOMAIN,foo.com,DIRECT,no-resolve"}, warns, genOpts{})
	for _, r := range mihomoRuleList(t, g) {
		if strings.HasPrefix(r, "DOMAIN,foo.com") && strings.Contains(r, "no-resolve") {
			t.Errorf("域名规则上不应保留 no-resolve: %q", r)
		}
	}
}

// 后缀规则的连字符陷阱没法自动纠正（那需要知道用户想匹配什么），但至少要
// 保证「写成什么样，产物里就是什么样」——不许在解析阶段偷偷改写后缀。
func TestCustomRuleSuffixIsNotRewritten(t *testing.T) {
	c, ok := parseCustomRuleFull("DOMAIN-SUFFIX,applog.fqnovel.com,DIRECT")
	if !ok {
		t.Fatal("普通后缀规则必须可解析")
	}
	if c.Val != "applog.fqnovel.com" || len(c.Notes) != 0 {
		t.Errorf("普通后缀规则不该被改写: val=%q notes=%v", c.Val, c.Notes)
	}
}
