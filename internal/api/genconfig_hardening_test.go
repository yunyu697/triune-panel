package api

// 分流硬化：三个「静默失效」口子的回归测试。
//
// 它们的共同点是没有报错——配置合法、日志干净、界面正常，只是流量悄悄走了
// 错的一侧。这类缺陷人工验收发现不了，只能靠断言钉住。
//
// 三个口子：
//   1. QUIC 不嗅探 → 真实 IP + UDP:443 的连接上没有任何域名信息；
//   2. sing-box 完全没有 sniff → 纯 IP 的 TLS 连接同理；
//   3. geoip-cn 只有远程规则集一层 → 首次启动下载失败即整条规则消失。

import (
	"strings"
	"testing"
)

// hasPort 判断 mihomo 的 ports 列表是否包含某个数字端口。
// 列表里可能混着 "8080-8880" 这样的区间字符串，只认精确的数字项。
func hasPort(v any, want int) bool {
	arr, ok := v.([]any)
	if !ok {
		return false
	}
	for _, x := range arr {
		if n, ok := x.(int); ok && n == want {
			return true
		}
	}
	return false
}

// mihomo：sniffer 必须嗅 QUIC。
//
// HTTP/3 走 UDP:443，只嗅 TLS 时这类连接拿不到 SNI；而 App 内置 HTTPDNS 的
// 场景本来就连真实 IP、绕过了域名规则——两条叠加之后，这条连接上一个域名
// 信息都不剩，域名类规则（含用户自定义的直连）全部失效，只剩 IP 归属可判。
func TestMihomoSnifferCoversQUIC(t *testing.T) {
	warns := new([]string)
	g := genMihomo("main-router", testNodes(), nil, nil, warns, ruleMirrorSel{}, bypassSpec{})
	sn, ok := g.doc["sniffer"].(map[string]any)
	if !ok {
		t.Fatal("router 模式必须生成 sniffer 段")
	}
	if sn["enable"] != true || sn["parse-pure-ip"] != true {
		t.Errorf("sniffer 必须同时开 enable 与 parse-pure-ip（否则纯 IP 连接不会被嗅）: %v", sn)
	}
	sniff, ok := sn["sniff"].(map[string]any)
	if !ok {
		t.Fatal("sniffer.sniff 缺失")
	}
	for _, c := range []struct {
		key  string
		port int
	}{{"HTTP", 80}, {"TLS", 443}, {"QUIC", 443}} {
		m, ok := sniff[c.key].(map[string]any)
		if !ok {
			t.Errorf("sniffer.sniff 缺少 %s", c.key)
			continue
		}
		if !hasPort(m["ports"], c.port) {
			t.Errorf("sniffer.sniff.%s 未覆盖 %d 端口: %v", c.key, c.port, m["ports"])
		}
	}
}

// 客户端模式没有 TUN，写 sniffer 是无意义字段，还可能被旧版内核拒收。
func TestMihomoClientHasNoSniffer(t *testing.T) {
	warns := new([]string)
	g := genMihomo("client", testNodes(), nil, nil, warns, ruleMirrorSel{}, bypassSpec{})
	if _, ok := g.doc["sniffer"]; ok {
		t.Error("client 模式不应生成 sniffer（没有 TUN 可嗅）")
	}
}

// sing-box：sniff 必须早于任何带域名的规则。
//
// 顺序错了不会报错，但嗅探结果还没产生，域名规则就已经判完——等于白写。
// 1.11 与 1.14 都要验：route action 是 1.11 引入的，两端都得能起。
func TestSingboxSniffsBeforeDomainRules(t *testing.T) {
	for _, v := range []string{"1.11", "1.14"} {
		ver, err := parseSBVersion(v)
		if err != nil {
			t.Fatalf("解析版本 %s: %v", v, err)
		}
		warns := []string{}
		g := genSingbox("main-router", ver, testNodes(), nil, nil, &warns, ruleMirrorSel{}, bypassSpec{})
		rules, ok := g.doc["route"].(map[string]any)["rules"].([]any)
		if !ok {
			t.Fatalf("sing-box %s 没有 route.rules", v)
		}
		sniffIdx, domainIdx := -1, -1
		for i, r := range rules {
			m, ok := r.(map[string]any)
			if !ok {
				continue
			}
			if m["action"] == "sniff" && sniffIdx < 0 {
				sniffIdx = i
			}
			if domainIdx < 0 {
				for _, k := range []string{"domain", "domain_suffix", "domain_keyword", "rule_set"} {
					if m[k] != nil {
						domainIdx = i
						break
					}
				}
			}
		}
		if sniffIdx < 0 {
			t.Fatalf("sing-box %s 没有 sniff action：纯 IP 的 TLS/QUIC 连接无法还原域名", v)
		}
		if domainIdx >= 0 && sniffIdx > domainIdx {
			t.Errorf("sing-box %s 的 sniff 排在第 %d 条，晚于第 %d 条的域名规则——"+
				"嗅探还没发生，域名规则就已经判完了", v, sniffIdx, domainIdx)
		}
	}
}

// mihomo：禁止用裸 GEOIP 规则做兜底。
//
// GEOIP 规则在**解析配置阶段**就要加载 MMDB，拿不到就 fatal、整个内核起不来。
// 实测（v1.19.34，home 目录不可写）：
//
//	Parse config error: rules[0] [GEOIP,CN,DIRECT] error: can't download MMDB
//
// 所以「用内建 GEOIP 给规则集兜底」是把静默降级换成硬失败，方向反了。
// 自定义的 GEOIP 规则走 RULE-SET（geoip-<val>），不产生裸规则，不在此列。
func TestMihomoRulesAvoidBareGeoip(t *testing.T) {
	warns := new([]string)
	// 连勾选了 geoip-cn 的场景一起验：正是这个场景最容易让人想到「补一条兜底」。
	sets, err := normalizeSets(genReq{Core: "mihomo", Sets: []setPolicy{
		{ID: "geoip-cn", Policy: "DIRECT"},
	}})
	if err != nil {
		t.Fatalf("构造规则集: %v", err)
	}
	for _, target := range []string{"client", "main-router"} {
		g := genMihomo(target, testNodes(), sets, nil, warns, ruleMirrorSel{}, bypassSpec{})
		for _, r := range g.doc["rules"].([]any) {
			s, ok := r.(string)
			if !ok {
				continue
			}
			if strings.HasPrefix(s, "GEOIP,") {
				t.Errorf("%s 出现裸 GEOIP 规则 %q：MMDB 不可用时内核会直接起不来，"+
					"这不是「兜底」而是把静默降级换成硬失败", target, s)
			}
		}
	}
}

// 产物必须自己声明规则集格式的版本下限。
//
// mrs 是二进制格式，旧内核不会退回按文本解析，而是直接报 format 不支持。
// 把下限只写在更新日志里等于没写：拿到文件的人看不到更新日志，只会看到
// 「导入成功、分流失效」，然后开始怀疑规则顺序和 DNS。
func TestMihomoHeaderDeclaresMrsFloor(t *testing.T) {
	sets, err := normalizeSets(genReq{Core: "mihomo", Sets: []setPolicy{
		{ID: "geosite-cn", Policy: "DIRECT"},
	}})
	if err != nil {
		t.Fatalf("构造规则集: %v", err)
	}
	warns := new([]string)
	g := genMihomo("client", testNodes(), sets, nil, warns, ruleMirrorSel{}, bypassSpec{})
	if !strings.Contains(g.header, "mrs") {
		t.Errorf("用了 mrs 规则集却没在文件头说明格式与版本要求：\n%s", g.header)
	}
	if !strings.Contains(g.header, "1.18") {
		t.Errorf("文件头缺版本下限：\n%s", g.header)
	}
	// 没有规则集时不该出现这条提示，否则会误导只做直连分流的人去升内核。
	g2 := genMihomo("client", testNodes(), nil, nil, warns, ruleMirrorSel{}, bypassSpec{})
	if strings.Contains(g2.header, "mrs") {
		t.Errorf("没选规则集却提示 mrs：\n%s", g2.header)
	}
}
