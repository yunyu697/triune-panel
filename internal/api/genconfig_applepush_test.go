package api

// Apple 推送（APNs）必须直连：把修复钉在生成器里，别指望用户自己知道。
//
// 真实症状（2026-11-24）：开着代理时 iPhone / iPad 待机耗电明显加快，安卓设备
// 没有这个现象。实测那台旁路由上，`7-courier2.push.apple.com:5223` 的链是 PROXY，
// `17.0.0.0/8` 上的活跃连接有 147 条。推送是**常连**的长连接，不是「用的时候才连」，
// 每次交互都要多绕一个海外 RTT，链路一抖就断线重连——只有苹果设备走这条通道，
// 所以症状只出现在苹果设备上。
//
// 这里钉的是「写了还得真生效」：两个内核都表达得出、排在规则集之前、DNS 不吃
// fake-ip。给 fake-ip 的话流量仍会绕一圈回到 mihomo，体检也会把「已直连」
// 误判成「进了代理」——这类「配了但没生效」的缺陷，只看清单是看不出来的。

import (
	"testing"
)

const applePushRule = "DOMAIN-SUFFIX,push.apple.com,DIRECT"

func TestApplePushRuleInSystemDirectList(t *testing.T) {
	for _, line := range systemDirectRules {
		if line == applePushRule {
			return
		}
	}
	t.Fatalf("内置直连清单里没有 %s——APNs 会落进 MATCH,PROXY，苹果设备待机耗电加快", applePushRule)
}

func TestApplePushRenderedBeforeRuleSets(t *testing.T) {
	g := genMihomo("main-router", testNodes(), adSets(), nil, new([]string), ruleMirrorSel{}, bypassSpec{})
	rules := g.doc["rules"].([]any)

	idx := sysRuleIndex(rules, applePushRule)
	if idx < 0 {
		t.Fatalf("mihomo 产物里没有 push.apple.com 直连规则：%v", rules)
	}
	if set := firstRuleSetIndex(rules); set >= 0 && idx > set {
		t.Errorf("push.apple.com 排在规则集之后（规则 #%d、规则集 #%d）：先命中 PROXY 规则集等于没写",
			idx, set)
	}

	// DNS 侧必须把域名排除在 fake-ip 之外，否则拿到 198.18.x，
	// 既多绕一圈，也会让体检脚本判成「进了代理」。
	filter := g.doc["dns"].(map[string]any)["fake-ip-filter"].([]any)
	for _, v := range filter {
		if s, _ := v.(string); s == "+.push.apple.com" {
			return
		}
	}
	t.Error("fake-ip-filter 里没有 +.push.apple.com：DNS 会给假 IP，体检会把「已直连」误判成「进了代理」")
}

func TestApplePushRenderedInSingbox(t *testing.T) {
	ver, err := parseSBVersion("1.14")
	if err != nil {
		t.Fatal(err)
	}
	g := genSingbox("main-router", ver, testNodes(), adSets(), nil, new([]string), ruleMirrorSel{}, bypassSpec{})
	rules := g.doc["route"].(map[string]any)["rules"].([]any)

	firstSet := -1
	for i, r := range rules {
		if m, ok := r.(map[string]any); ok && m["rule_set"] != nil {
			firstSet = i
			break
		}
	}

	for i, r := range rules {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		if !hasInSlice(m["domain_suffix"], "push.apple.com") {
			continue
		}
		if m["outbound"] != "direct" {
			t.Errorf("sing-box 里 push.apple.com 的出口是 %v，应为 direct", m["outbound"])
		}
		if firstSet >= 0 && i > firstSet {
			t.Errorf("sing-box 里 push.apple.com 排在 rule_set 之后（#%d > #%d）：顺序匹配，等于没写", i, firstSet)
		}
		return
	}
	t.Fatalf("sing-box 产物里没有 push.apple.com——同一份清单必须在两个内核上都生效：%v", rules)
}

// 快照要收进去，否则老配置看不出「自己缺了这条修复」。
func TestApplePushInRuleSnapshot(t *testing.T) {
	for _, s := range systemRuleSnapshot() {
		if s == applePushRule {
			return
		}
	}
	t.Errorf("快照里没有 %s，已保存的老配置不会显示这条漂移", applePushRule)
}

// 这条规则能生效，靠的是「DNS 给真实 IP + TLS 嗅探补回域名」。
//
// 这里钉的不是「5223 在不在列表里」，而是**TLS 嗅探不允许退回枚举端口**。
// 原因：mihomo 的嗅探有硬门槛 `SupportPort(DstPort)`，不在列表就直接不嗅
// （component/sniffer/dispatcher.go:134），而 `ports` 缺省是空集＝不嗅。
// 一旦写成枚举清单，「规则是否生效」就取决于「这个服务碰巧用了我猜的端口没有」，
// 而端口由第三方决定。2026-11-24 的 APNs（5223）就是这么静默失效的——
// 配置合法、内核不报错、日志干净，只有翻连接列表才看得出推送还挂在海外节点上。
//
// 所以断言写成「任意端口都必须被覆盖」，并特意验 5223/2197/993 这些真实存在
// 的非标端口：将来谁把 "1-65535" 改回清单，这条会立刻红。
func TestTLSSniffCoversEveryPort(t *testing.T) {
	g := genMihomo("main-router", testNodes(), nil, nil, new([]string), ruleMirrorSel{}, bypassSpec{})
	// 旁路由目标才发 sniffer 段；客户端模式由客户端自己的设置负责。
	sn, ok := g.doc["sniffer"].(map[string]any)
	if !ok {
		t.Fatal("旁路由产物里没有 sniffer 段：直连清单里的域名拿不到 SNI，规则全部失效")
	}
	sniff, ok := sn["sniff"].(map[string]any)
	if !ok {
		t.Fatal("sniffer 里没有 sniff 规则")
	}
	tls, ok := sniff["TLS"].(map[string]any)
	if !ok {
		t.Fatal("sniffer 里没有 TLS 规则")
	}
	ports := tls["ports"]
	// APNs 5223、APNs 备用 2197、IMAPS 993——每一个都是真实在用的非 443 端口，
	// 也都是「逐个补清单」补漏过的形状。
	for _, p := range []int{443, 5223, 2197, 993, 65535} {
		if !portRangeCovers(ports, p) {
			t.Errorf("TLS 嗅探没覆盖 %d 端口（实际 %v）：走这个端口的直连域名规则会静默失效，"+
				"配置合法、内核不报错，只是流量悄悄走了代理", p, ports)
		}
	}
}
