package api

// 广告拦截误杀应用日志：把修复钉在生成器里，而不是钉在某个人的设备上。
//
// 真实事故（2026-11-23）：勾了「广告域名（通用全量）」= REJECT 之后，红果短剧
// 首屏永远停在加载。原因是 geosite-category-ads-all 把 log5-applog.fqnovel.com
// 这类**应用日志上报**也收进了分类，被 RST 后埋点 SDK 在冷启动同步等首屏事件，
// 失败就退避重试，把首屏堵死。同刻功能域名（API / 封面 CDN / snssdk 系列）全部
// geosite-cn 直连且有大流量，所以顺着分流查永远查不出问题。
//
// 修复放在生成器里而不是「让用户自己加规则」：每一个下载配置的人都会踩同一个
// 坑，知道原理的人只有出过事的那一个。
//
// 这里断言的是**顺序**，不是「规则存在」——插在规则集之后等于没插：REJECT 先
// 命中就 RST 了。这类「写了但没生效」的缺陷，只看规则列表是看不出来的。

import (
	"strings"
	"testing"
)

// adSets 模拟「均衡日常」预设：全量广告拦截 + 国内直连。
func adSets() []normSet {
	return []normSet{
		{Tag: "geosite-category-ads-all", URL: "http://mirror/geosite/category-ads-all.mrs",
			Behavior: "domain", Policy: "REJECT", Format: "mrs", Interval: 86400},
		{Tag: "geosite-cn", URL: "http://mirror/geosite/cn.mrs",
			Behavior: "domain", Policy: "DIRECT", Format: "mrs", Interval: 86400},
	}
}

func TestBlockExemptPrecedesAdRuleSet(t *testing.T) {
	warns := new([]string)
	g := genMihomo("main-router", testNodes(), adSets(), nil, warns, ruleMirrorSel{}, bypassSpec{})
	rules := mihomoRuleList(t, g)

	exemptIdx, adIdx := -1, -1
	for i, r := range rules {
		if r == "DOMAIN-KEYWORD,applog,DIRECT" && exemptIdx < 0 {
			exemptIdx = i
		}
		if strings.HasPrefix(r, "RULE-SET,geosite-category-ads-all,") && adIdx < 0 {
			adIdx = i
		}
	}
	if adIdx < 0 {
		t.Fatalf("测试前提不成立：产物里没有广告规则集\n实际规则: %v", rules)
	}
	if exemptIdx < 0 {
		t.Fatalf("没有插入 applog 豁免——勾着广告拦截时应用首屏会被拦死\n实际规则: %v", rules)
	}
	if exemptIdx > adIdx {
		t.Errorf("豁免规则排在广告规则集之后（豁免 #%d、规则集 #%d），等于没插：\n%v",
			exemptIdx, adIdx, rules)
	}

	// 用户的显式自定义规则优先级高于豁免：他真想拦，得能拦得住。
	custom := []string{"DOMAIN-KEYWORD,applog,REJECT"}
	g2 := genMihomo("main-router", testNodes(), adSets(), custom, new([]string), ruleMirrorSel{}, bypassSpec{})
	rules2 := mihomoRuleList(t, g2)
	customIdx := -1
	for i, r := range rules2 {
		if r == "DOMAIN-KEYWORD,applog,REJECT" {
			customIdx = i
			break
		}
	}
	if customIdx < 0 {
		t.Fatalf("自定义规则丢了: %v", rules2)
	}
	for i, r := range rules2 {
		if r == "DOMAIN-KEYWORD,applog,DIRECT" && i < customIdx {
			t.Errorf("豁免规则排在用户自定义 REJECT 之前，用户的显式选择被覆盖了：\n%v", rules2)
		}
	}
}

func TestBlockExemptInSingboxBeforeAdRuleSet(t *testing.T) {
	ver, err := parseSBVersion("1.14")
	if err != nil {
		t.Fatal(err)
	}
	sb := genSingbox("main-router", ver, testNodes(), adSets(), nil,
		new([]string), ruleMirrorSel{}, bypassSpec{})
	rules := sb.doc["route"].(map[string]any)["rules"].([]any)

	exemptIdx, adIdx := -1, -1
	for i, r := range rules {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		if kw, ok := m["domain_keyword"].([]any); ok {
			for _, x := range kw {
				if s, _ := x.(string); s == "applog" && exemptIdx < 0 {
					exemptIdx = i
				}
			}
		}
		if rs, ok := m["rule_set"].([]any); ok {
			for _, x := range rs {
				if s, _ := x.(string); s == "geosite-category-ads-all" && adIdx < 0 {
					adIdx = i
				}
			}
		}
	}
	if adIdx < 0 {
		t.Fatalf("测试前提不成立：产物里没有广告 rule_set")
	}
	if exemptIdx < 0 {
		t.Fatalf("sing-box 侧没有插入 applog 豁免——同一份清单必须在两个内核上都生效")
	}
	if exemptIdx > adIdx {
		t.Errorf("sing-box 豁免排在 rule_set 之后（豁免 #%d、规则集 #%d）：sing-box 顺序匹配，等于没写",
			exemptIdx, adIdx)
	}
}

// 没开拦截就不该插入：多出的规则虽无害，但会让「我明明没勾广告拦截，为什么有
// 一条 applog 直连」变成新的疑问，也让漂移对比失去意义。
func TestBlockExemptSkippedWithoutRejectSet(t *testing.T) {
	sets := []normSet{
		{Tag: "geosite-cn", Policy: "DIRECT", Behavior: "domain"},
		{Tag: "geosite-google", Policy: "PROXY", Behavior: "domain"},
	}
	warns := new([]string)
	g := genMihomo("main-router", testNodes(), sets, nil, warns, ruleMirrorSel{}, bypassSpec{})
	for _, r := range mihomoRuleList(t, g) {
		if strings.Contains(r, "applog") {
			t.Errorf("没勾广告拦截却插了豁免规则: %s", r)
		}
	}
	for _, w := range *warns {
		if strings.Contains(w, "应用日志") {
			t.Errorf("没插入豁免却给了豁免提示: %s", w)
		}
	}
}

// 「隐私优先」「纯净拦截」预设显式勾了遥测 REJECT：那边放行埋点等于违背用户的
// 选择。但**不插不等于不说**——那种配置下 App 首屏真的会卡，用户得知道是自己勾的。
func TestBlockExemptSkippedWhenTelemetryExplicitlyBlocked(t *testing.T) {
	sets := append(adSets(), normSet{
		Tag: "geosite-category-telemetry", Policy: "REJECT", Behavior: "domain",
	})
	warns := new([]string)
	g := genMihomo("main-router", testNodes(), sets, nil, warns, ruleMirrorSel{}, bypassSpec{})
	for _, r := range mihomoRuleList(t, g) {
		if strings.Contains(r, "applog") {
			t.Errorf("用户显式拦遥测，不该再插豁免: %s", r)
		}
	}
	if !hasWarn(*warns, "首屏") {
		t.Errorf("显式拦遥测时不插豁免，但没有一句提示——用户只会看到 App 卡住却查不出原因。实际提示: %v", *warns)
	}
	if !blockExemptNeeded(adSets()) {
		t.Error("只勾广告拦截时必须插入豁免")
	}
	if blockExemptNeeded(sets) {
		t.Error("遥测被显式 REJECT 时不应插入豁免")
	}

	// sing-box 侧同样要提示：两个内核的产物由同一套判据生成，
	// 只有一边提示的话，用 sing-box 的人就看不到。
	ver, err := parseSBVersion("1.14")
	if err != nil {
		t.Fatal(err)
	}
	sbWarns := new([]string)
	genSingbox("main-router", ver, testNodes(), sets, nil, sbWarns, ruleMirrorSel{}, bypassSpec{})
	if !hasWarn(*sbWarns, "首屏") {
		t.Errorf("sing-box 侧显式拦遥测时没有提示: %v", *sbWarns)
	}
}

// 精简广告集与全量集是同一类，不是「干净版」。
//
// 上游 v2fly/domain-list-community 里 category-ads 通过 include:bytedance @ads
// 带着 log.zijieapi.com——正是当初实测被拒的那个域名。此前判据只认
// geosite-category-ads-all 这一个 ID，选了精简集就一条豁免都不插：配置合法、
// 内核不报错，App 首屏照样卡。换一个勾选框，同一个坑。
func TestBlockExemptAppliesToTrimmedAdSet(t *testing.T) {
	trimmed := []normSet{
		{Tag: "geosite-category-ads", Policy: "REJECT", Behavior: "domain"},
		{Tag: "geosite-cn", Policy: "DIRECT", Behavior: "domain"},
	}
	if !blockExemptNeeded(trimmed) {
		t.Fatal("勾「广告域名（精简）」没有触发豁免：它同样会 REJECT log.zijieapi.com")
	}
	warns := new([]string)
	g := genMihomo("main-router", testNodes(), trimmed, nil, warns, ruleMirrorSel{}, bypassSpec{})
	found := false
	for _, r := range mihomoRuleList(t, g) {
		if r == "DOMAIN-SUFFIX,zijieapi.com,DIRECT" {
			found = true
		}
	}
	if !found {
		t.Errorf("精简集下没有 zijieapi 豁免规则: %v", mihomoRuleList(t, g))
	}
	if !hasWarn(*warns, "应用日志") {
		t.Errorf("精简集下插了豁免却没说明: %v", *warns)
	}
}

// 目录里每个「拦截类」规则集都必须声明 AppLog。
//
// 这条守卫针对的是判据本身：以前要记得「哪些 tag 该插豁免」，现在只需在目录里
// 填一行。填漏了就红——而不是等某个用户勾了那个集合、发现 App 卡在首屏才暴露。
func TestCatalogBlockSetsDeclareAppLog(t *testing.T) {
	allowed := map[string]bool{appLogNone: true, appLogMixed: true, appLogExplicit: true}
	for _, d := range ruleCatalog {
		if d.Policy != "REJECT" || d.Group != "广告与隐私" {
			continue
		}
		if !allowed[d.AppLog] {
			t.Errorf("规则集 %s（%s）设为 REJECT 但没声明 AppLog：拦掉它会不会连带拦掉应用日志，必须显式回答（none / mixed / explicit）",
				d.ID, d.Name)
		}
	}
	// 反向：声明了 mixed/explicit 的集合必须真的存在于目录里，否则判据永远不生效。
	for _, d := range ruleCatalog {
		if d.AppLog == appLogMixed || d.AppLog == appLogExplicit {
			if d.Policy != "REJECT" {
				t.Errorf("规则集 %s 声明了 AppLog=%s 但默认策略不是 REJECT——豁免只对拒绝生效，这条声明是死的",
					d.ID, d.AppLog)
			}
		}
	}
}

func hasWarn(warns []string, sub string) bool {
	for _, w := range warns {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}

// 清单要能被两个渲染器都表达出来。曾经往内置清单加 DOMAIN-KEYWORD 时
// mihomo 生效、sing-box 静默不生效——配置合法、内核不报错，最难查的一类。
func TestBlockExemptRulesExpressibleInBothCores(t *testing.T) {
	if len(blockExemptRules) == 0 {
		t.Fatal("豁免清单为空：那这个修复等于没做")
	}
	for _, line := range blockExemptRules {
		typ, val, pol, ok := parseCustomRule(line)
		if !ok {
			t.Errorf("无法解析: %q", line)
			continue
		}
		if pol != "DIRECT" {
			t.Errorf("%q 的策略必须是 DIRECT，实际 %s", line, pol)
		}
		if typ == "" || val == "" {
			t.Errorf("%q 解析结果不完整", line)
		}
		if sbRuleBody(typ, val) == nil {
			t.Errorf("%q 无法在 sing-box 表达——会在那边静默失效", line)
		}
	}
}

// 豁免规则要进快照，否则老配置看不出「自己缺了这条修复」。
func TestBlockExemptInRuleSnapshot(t *testing.T) {
	snap := systemRuleSnapshot()
	for _, line := range blockExemptRules {
		found := false
		for _, s := range snap {
			if s == line {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("快照里没有 %q，老配置不会显示这条漂移", line)
		}
	}
}
