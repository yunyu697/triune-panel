package api

import (
	"encoding/json"
	"strings"
	"testing"

	"triune/internal/link"
)

// 「绕过节点服务器」的判据不是「规则写进去了」，而是「写在了所有可能把它
// 吸进隧道的规则之前」。顺序错了配置照样能加载、内核照样不报错，只是 TUN
// 模式下永远连不上节点——所以每条断言都查位置，不只用 Contains。

func TestNormalizeNodeHost(t *testing.T) {
	cases := map[string]string{
		"1.2.3.4":             "1.2.3.4",
		"1.2.3.4:443":         "1.2.3.4",
		"[2001:db8::1]:443":   "2001:db8::1",
		"[2001:db8::1]":       "2001:db8::1",
		"2001:db8::1":         "2001:db8::1", // 裸 IPv6 的冒号不是端口
		"Node.Example.COM.":   "node.example.com",
		"  example.com  ":     "example.com",
		"":                    "",
		"   ":                 "",
	}
	for in, want := range cases {
		if got := normalizeNodeHost(in); got != want {
			t.Errorf("normalizeNodeHost(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestCollectBypassHosts(t *testing.T) {
	nodes := []*link.Node{
		{Host: "1.2.3.4"}, {Host: "1.2.3.4"}, // 同地址多个用户，必须去重
		{Host: "2001:db8::1"},
		{Host: "node.example.com."}, {Host: "NODE.EXAMPLE.COM"}, // 归一后重复
		{Host: "127.0.0.1"}, // 回环也是合法地址，规则照给（私网段已覆盖）
		{Host: ""}, {Host: "   "},
	}
	b := collectBypassHosts(nodes)
	if got := strings.Join(b.Domains, ","); got != "node.example.com" {
		t.Errorf("域名 = %q", got)
	}
	wantC := "1.2.3.4/32,2001:db8::1/128,127.0.0.1/32"
	if got := strings.Join(b.CIDRs, ","); got != wantC {
		t.Errorf("CIDR = %q，期望 %q", got, wantC)
	}
	if got := strings.Join(b.hosts(), ","); got != "node.example.com,"+wantC {
		t.Errorf("hosts() 顺序/内容不对: %q", got)
	}
	if !collectBypassHosts(nil).empty() {
		t.Error("空节点列表应返回空 spec")
	}
}

func TestMihomoBypassRulesComeFirst(t *testing.T) {
	b := bypassSpec{Domains: []string{"node.example.com"}, CIDRs: []string{"1.2.3.4/32"}}
	warns := new([]string)
	filterLen := len(fakeIPFilter)
	g := genMihomo("main-router", testNodes(), nil,
		[]string{"DOMAIN-SUFFIX,ads.example,REJECT", "DOMAIN,node.example.com,PROXY"},
		warns, ruleMirrorSel{}, b)

	rules := g.doc["rules"].([]any)
	if len(rules) < 4 {
		t.Fatalf("规则太少: %#v", rules)
	}
	// 这两条必须排在用户自定义规则之前：第二条自定义规则故意把同一域名
	// 指向 PROXY，用来证明「后写的规则抢不走节点直连」。
	if rules[0] != "DOMAIN,node.example.com,DIRECT" {
		t.Errorf("rules[0] = %v，节点域名直连不是第一条", rules[0])
	}
	if rules[1] != "IP-CIDR,1.2.3.4/32,DIRECT,no-resolve" {
		t.Errorf("rules[1] = %v，节点 IP 直连不是第二条", rules[1])
	}
	if rules[2] != "DOMAIN-SUFFIX,ads.example,REJECT" {
		t.Errorf("rules[2] = %v，自定义规则被挤掉了", rules[2])
	}
	if rules[len(rules)-1] != "MATCH,PROXY" {
		t.Errorf("最后一条应是 MATCH,PROXY，实际 %v", rules[len(rules)-1])
	}

	// TUN + fake-ip：节点域名要排除在 fake-ip 之外，且不能污染全局切片
	dns := g.doc["dns"].(map[string]any)
	filter := dns["fake-ip-filter"].([]any)
	found := false
	for _, v := range filter {
		if v == "node.example.com" {
			found = true
		}
	}
	if !found {
		t.Error("fake-ip-filter 里没有节点域名")
	}
	if len(fakeIPFilter) != filterLen {
		t.Errorf("全局 fakeIPFilter 被污染：%d → %d", filterLen, len(fakeIPFilter))
	}
	if !strings.Contains(g.header, "节点服务器直连") {
		t.Error("YAML 头注释没写明直连了哪些地址")
	}
}

func TestMihomoWithoutBypassKeepsOldRules(t *testing.T) {
	warns := new([]string)
	g := genMihomo("client", testNodes(), nil, nil, warns, ruleMirrorSel{}, bypassSpec{})
	rules := g.doc["rules"].([]any)
	for _, r := range rules {
		s, _ := r.(string)
		if strings.HasPrefix(s, "DOMAIN,") || strings.HasPrefix(s, "IP-CIDR,") && strings.Contains(s, "/32") {
			t.Errorf("未开启开关却生成了节点直连规则: %s", s)
		}
	}
	if strings.Contains(g.header, "节点服务器直连") {
		t.Error("未开启开关却写了头注释")
	}
}

func TestSingboxBypassRulesFollowDNSHijack(t *testing.T) {
	b := bypassSpec{Domains: []string{"node.example.com"}, CIDRs: []string{"1.2.3.4/32"}}
	for _, verStr := range []string{"1.11", "1.14", "1.15"} {
		ver, err := parseSBVersion(verStr)
		if err != nil {
			t.Fatal(err)
		}
		warns := []string{}
		g := genSingbox("main-router", ver, testNodes(), nil,
			[]string{"DOMAIN-SUFFIX,ads.example,REJECT"}, &warns, ruleMirrorSel{}, b)
		route := g.doc["route"].(map[string]any)
		rules := route["rules"].([]any)
		if len(rules) < 4 {
			t.Fatalf("%s 规则太少: %#v", verStr, rules)
		}
		first := rules[0].(map[string]any)
		if first["action"] != "hijack-dns" {
			t.Errorf("%s rules[0] 应是 DNS 劫持，实际 %v", verStr, first)
		}
		second := rules[1].(map[string]any)
		if second["outbound"] != "direct" || !hasInSlice(second["domain"], "node.example.com") {
			t.Errorf("%s rules[1] 应是节点域名直连，实际 %v", verStr, second)
		}
		third := rules[2].(map[string]any)
		if third["outbound"] != "direct" || !hasInSlice(third["ip_cidr"], "1.2.3.4/32") {
			t.Errorf("%s rules[2] 应是节点 IP 直连，实际 %v", verStr, third)
		}

		// DNS 侧同样要绕开：节点域名落到 final:remote（经代理的 DoH）就成了
		// 「先连上代理才能解析代理地址」，1.11 没有 default_domain_resolver 时更明显。
		dnsRules := g.doc["dns"].(map[string]any)["rules"].([]any)
		d0 := dnsRules[0].(map[string]any)
		if d0["server"] != "local" || !hasInSlice(d0["domain"], "node.example.com") {
			t.Errorf("%s dns.rules[0] 应把节点域名交给 local，实际 %v", verStr, d0)
		}
	}
}

func hasInSlice(v any, want string) bool {
	s, ok := v.([]any)
	if !ok {
		return false
	}
	for _, x := range s {
		if x == want {
			return true
		}
	}
	return false
}

func TestSingboxWithoutBypassKeepsOldRules(t *testing.T) {
	ver, _ := parseSBVersion("1.14")
	warns := []string{}
	g := genSingbox("client", ver, testNodes(), nil, nil, &warns, ruleMirrorSel{}, bypassSpec{})
	rules := g.doc["route"].(map[string]any)["rules"].([]any)
	for _, r := range rules {
		m := r.(map[string]any)
		if m["outbound"] == "direct" && (m["domain"] != nil || m["ip_cidr"] != nil) {
			// 末尾本来就有一条私网 ip_cidr 直连，按 CIDR 内容区分
			if hasInSlice(m["ip_cidr"], "1.2.3.4/32") || hasInSlice(m["domain"], "node.example.com") {
				t.Errorf("未开启开关却生成了节点直连规则: %v", m)
			}
		}
	}
	dnsRules := g.doc["dns"].(map[string]any)["rules"].([]any)
	if hasInSlice(dnsRules[0].(map[string]any)["domain"], "node.example.com") {
		t.Error("未开启开关却改了 DNS 规则")
	}
}

// 默认关闭是硬口径：老版本 UI / 直接调 API 的脚本不带这个字段时，
// 不能因为发了新功能就悄悄改写别人已经跑通的分流规则。
func TestBypassServerIsOptIn(t *testing.T) {
	var q genReq
	if err := json.Unmarshal([]byte(`{"core":"mihomo","target":"client","inbound_ids":[1]}`), &q); err != nil {
		t.Fatal(err)
	}
	if q.BypassServer {
		t.Error("不带 bypass_server 时必须默认 false")
	}
	if err := json.Unmarshal([]byte(`{"bypass_server":true}`), &q); err != nil {
		t.Fatal(err)
	}
	if !q.BypassServer {
		t.Error("显式开启没生效")
	}
}

// HTTP 层：开关打开后响应要回显实际直连的地址，且配置正文里真的有那条规则。
func TestExportConfigBypassServerResponse(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")
	if w := do(a, owner, "POST", "/api/inbounds", map[string]any{
		"name": "hk", "core": "singbox", "protocol": "vless", "listen": "::",
		"port": 443, "transport": "tcp", "tls": "tls", "enabled": true,
		"settings": `{"server_name":"www.example.com"}`,
	}); w.Code != 200 {
		t.Fatalf("建入站失败 %d: %s", w.Code, w.Body.String())
	}
	if w := do(a, owner, "POST", "/api/clients", map[string]any{"name": "alice"}); w.Code != 200 {
		t.Fatalf("建用户失败 %d: %s", w.Code, w.Body.String())
	}
	if w := do(a, owner, "PUT", "/api/clients/1/inbounds", map[string]any{"inbound_ids": []int64{1}}); w.Code != 200 {
		t.Fatalf("绑定入站失败 %d: %s", w.Code, w.Body.String())
	}

	base := map[string]any{
		"core": "mihomo", "target": "main-router", "inbound_ids": []int64{1},
		"scenario": "balanced",
	}
	// 关：响应里不该有 bypass_servers，配置里也不该有节点直连规则
	w := do(a, owner, "POST", "/api/export/config", base)
	if w.Code != 200 {
		t.Fatalf("生成失败 %d: %s", w.Code, w.Body.String())
	}
	var off map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &off); err != nil {
		t.Fatal(err)
	}
	if _, ok := off["bypass_servers"]; ok {
		t.Error("未开启开关却回传了 bypass_servers")
	}
	offContent, _ := off["content"].(string)
	if strings.Contains(offContent, "节点服务器直连") {
		t.Error("未开启开关却写了节点直连头注释")
	}

	// 开：回显地址 + 配置里出现该地址的 DIRECT 规则（排在所有分流规则之前）
	on := map[string]any{}
	for k, v := range base {
		on[k] = v
	}
	on["bypass_server"] = true
	w = do(a, owner, "POST", "/api/export/config", on)
	if w.Code != 200 {
		t.Fatalf("开启后生成失败 %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	hosts, ok := resp["bypass_servers"].([]any)
	if !ok || len(hosts) == 0 {
		t.Fatalf("没有回显 bypass_servers: %v", resp["bypass_servers"])
	}
	content, _ := resp["content"].(string)
	if !strings.Contains(content, "节点服务器直连") {
		t.Error("配置头注释里没有直连说明")
	}
	found := false
	for _, h := range hosts {
		s, _ := h.(string)
		var rule string
		if strings.Contains(s, "/") {
			rule = "- IP-CIDR," + s + ",DIRECT,no-resolve"
		} else {
			rule = "- DOMAIN," + s + ",DIRECT"
		}
		if strings.Contains(content, rule) {
			found = true
		}
	}
	if !found {
		t.Errorf("配置里找不到 %v 对应的直连规则", hosts)
	}
}
