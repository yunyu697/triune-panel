package api

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// momoDoc 把生成的 JSON 结构拿出来，供下面的用例按字段断言。
func momoDoc(t *testing.T, ver string) map[string]any {
	t.Helper()
	v, err := parseSBVersion(ver)
	if err != nil {
		t.Fatal(err)
	}
	warns := []string{}
	g := genMomo(v, testNodes(), nil, nil, &warns, ruleMirrorSel{},
		bypassSpec{Domains: []string{"node.example.com"}})
	raw, err := json.Marshal(g.doc)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func momoInboundMap(t *testing.T, doc map[string]any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	list, ok := doc["inbounds"].([]any)
	if !ok {
		t.Fatal("inbounds 不是数组")
	}
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("inbound 不是对象: %T", item)
		}
		out[m["tag"].(string)] = m
	}
	return out
}

// momo 的启动检查按 tag 找入站：少一个、类型不对、缺 listen_port / interface_name
// 都会在 momo 日志里只留一句 "Check failed" 然后退出，内核根本不启动。
func TestMomoInboundTags(t *testing.T) {
	doc := momoDoc(t, "1.14")
	ins := momoInboundMap(t, doc)

	cases := []struct{ tag, typ string }{
		{"dns-in", "direct"},
		{"redirect-in", "redirect"},
		{"tproxy-in", "tproxy"},
		{"tun-in", "tun"},
	}
	for _, c := range cases {
		in, ok := ins[c.tag]
		if !ok {
			t.Errorf("缺少 momo 要求的入站 %s", c.tag)
			continue
		}
		if in["type"] != c.typ {
			t.Errorf("%s 的 type = %v，期望 %s", c.tag, in["type"], c.typ)
		}
		if c.tag == "tun-in" {
			if in["interface_name"] != "momo" {
				t.Errorf("tun-in 缺 interface_name（momo 的检查读这个字段）: %v", in)
			}
			if in["auto_route"] != false {
				t.Errorf("tun-in 必须 auto_route:false，路由表由 momo 铺，实际 %v", in["auto_route"])
			}
		} else if _, ok := in["listen_port"]; !ok {
			t.Errorf("%s 缺 listen_port，momo 的 jsonfilter 检查会判失败", c.tag)
		}
	}
	// IPv6 劫持默认开着，只绑 0.0.0.0 会让 v6 侧打不到端口
	for _, tag := range []string{"dns-in", "redirect-in", "tproxy-in"} {
		if ins[tag]["listen"] != "::" {
			t.Errorf("%s 应监听 ::（IPv4+IPv6 双栈），实际 %v", tag, ins[tag]["listen"])
		}
	}
	// 不能留主路由模板那个 auto_route:true 的 TUN
	if _, ok := ins["mixed-in"]; !ok {
		t.Error("缺调试用的混合入站")
	}
}

func TestMomoDNSFakeIP(t *testing.T) {
	doc := momoDoc(t, "1.14")
	dns := doc["dns"].(map[string]any)

	var fake map[string]any
	for _, s := range dns["servers"].([]any) {
		m := s.(map[string]any)
		if m["tag"] == momoFakeIPTag {
			fake = m
		}
	}
	if fake == nil {
		t.Fatal("DNS servers 里没有 momo 的 fake-ip-dns-server")
	}
	if fake["type"] != "fakeip" || fake["inet4_range"] != "198.18.0.0/15" || fake["inet6_range"] != "fc00::/18" {
		t.Errorf("fakeip 服务器字段不对: %v", fake)
	}

	rules := dns["rules"].([]any)
	raw, _ := json.Marshal(rules)
	text := string(raw)
	// HTTPS RR 会绕过 A/AAAA，必须挡掉，否则部分 App 拿不到 fakeip 映射
	if !strings.Contains(text, `"query_type":"HTTPS"`) || !strings.Contains(text, `"action":"reject"`) {
		t.Errorf("没有挡掉 HTTPS 类型查询: %s", text)
	}
	// A/AAAA 要落 fakeip；节点服务器域名仍走真实 DNS 且排在它前面
	if !strings.Contains(text, `"server":"fake-ip-dns-server"`) {
		t.Errorf("没有把 A/AAAA 查询指向 fakeip: %s", text)
	}
	idxFake := strings.Index(text, `"server":"fake-ip-dns-server"`)
	idxNode := strings.Index(text, `"node.example.com"`)
	if idxNode < 0 || idxNode > idxFake {
		t.Errorf("节点服务器域名的真实 DNS 规则必须排在 fakeip 之前: %s", text)
	}
	if dns["final"] != "remote" {
		t.Errorf("final = %v，期望 remote（其余查询类型仍走代理侧 DoH）", dns["final"])
	}
}

func TestMomoRouteRules(t *testing.T) {
	doc := momoDoc(t, "1.14")
	route := doc["route"].(map[string]any)
	rules := route["rules"].([]any)
	if len(rules) < 6 {
		t.Fatalf("路由规则太少: %d", len(rules))
	}
	first := rules[0].(map[string]any)
	// 没有 sniff 就只剩 IP 包，rule_set 的域名规则全部失效
	if first["action"] != "sniff" {
		t.Errorf("第一条必须是 sniff，实际 %v", first)
	}
	raw, _ := json.Marshal(rules[:5])
	text := string(raw)
	for _, s := range []string{`"inbound":"dns-in"`, `"action":"hijack-dns"`, `"clash_mode":"direct"`, `"clash_mode":"global"`} {
		if !strings.Contains(text, s) {
			t.Errorf("前五条路由规则缺少 %s: %s", s, text)
		}
	}
	if route["final"] != "PROXY" {
		t.Errorf("route.final = %v", route["final"])
	}
	if r, ok := route["default_domain_resolver"].(map[string]any); !ok || r["server"] != "local" {
		t.Errorf("缺 default_domain_resolver（1.12+）: %v", route["default_domain_resolver"])
	}
	sets := route["rule_set"].([]any)
	if len(sets) == 0 {
		t.Fatal("规则集为空")
	}
	for _, s := range sets {
		if s.(map[string]any)["download_detour"] != "direct" {
			t.Errorf("规则集必须直连下载: %v", s)
		}
	}
}

// 1.15 起 stack 废弃，写进去会在 momo 日志里刷 deprecated；1.14 必须写。
func TestMomoTunStackFollowsVersion(t *testing.T) {
	for _, tc := range []struct {
		ver      string
		hasStack bool
	}{{"1.12", true}, {"1.14", true}, {"1.15", false}} {
		ins := momoInboundMap(t, momoDoc(t, tc.ver))
		_, got := ins["tun-in"]["stack"]
		if got != tc.hasStack {
			t.Errorf("%s 的 tun.stack 存在=%v，期望 %v", tc.ver, got, tc.hasStack)
		}
	}
}

// 不能因为多了个目标就复制一份节点/分流逻辑：这里确认复用主路由模板的产物。
func TestMomoReusesBaseGenerator(t *testing.T) {
	warns := []string{}
	v, _ := parseSBVersion("1.14")
	g := genMomo(v, testNodes(), nil, nil, &warns, ruleMirrorSel{}, bypassSpec{})
	doc := g.doc
	outs := doc["outbounds"].([]any)
	tags := map[string]bool{}
	for _, o := range outs {
		tags[o.(map[string]any)["tag"].(string)] = true
	}
	for _, want := range []string{"PROXY", "Auto", "direct"} {
		if !tags[want] {
			t.Errorf("出站里缺少 %s", want)
		}
	}
	if len(outs) < 4 {
		t.Errorf("节点出站没有带进来: %d", len(outs))
	}
	if !strings.Contains(strings.Join(warns, "\n"), "fake-ip-dns-server") {
		t.Errorf("警告里没写清 momo 的 tag 约定: %v", warns)
	}
}

func TestValidGenTarget(t *testing.T) {
	for _, s := range []string{"client", "main-router", "side-router", genTargetMomo} {
		if !validGenTarget(s) {
			t.Errorf("%s 应被接受", s)
		}
	}
	for _, s := range []string{"", "momo", "openwrt", "client-router"} {
		if validGenTarget(s) {
			t.Errorf("%s 不该被接受", s)
		}
	}
}

// HTTP 层：内核/版本的门限要在生成之前挡住，否则用户拿到一份 momo 起不来的文件。
func TestExportConfigMomoGuards(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")

	w := do(a, owner, "POST", "/api/export/config", map[string]any{
		"core": "mihomo", "target": genTargetMomo, "inbound_ids": []int64{1},
	})
	if w.Code != 400 || !strings.Contains(w.Body.String(), "sing-box") {
		t.Errorf("mihomo + momo 应被挡住并说明原因: %d %s", w.Code, w.Body.String())
	}

	w = do(a, owner, "POST", "/api/export/config", map[string]any{
		"core": "singbox", "core_version": "1.11", "target": genTargetMomo, "inbound_ids": []int64{1},
	})
	if w.Code != 400 || !strings.Contains(w.Body.String(), "1.12") {
		t.Errorf("sing-box 1.11 + momo 应被挡住: %d %s", w.Code, w.Body.String())
	}

	// 造一个能导出的节点，确认 momo 真的能生成（不是只剩一堆 400）
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
	w = do(a, owner, "POST", "/api/export/config", map[string]any{
		"core": "singbox", "core_version": "1.12", "target": genTargetMomo,
		"inbound_ids": []int64{1}, "scenario": "balanced",
	})
	if w.Code != 200 {
		t.Fatalf("momo 生成失败 %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	content, _ := resp["content"].(string)
	for _, s := range []string{`"dns-in"`, `"redirect-in"`, `"tproxy-in"`, `"tun-in"`, `"fake-ip-dns-server"`} {
		if !strings.Contains(content, s) {
			t.Errorf("生成的 momo 配置缺少 %s", s)
		}
	}
	if !strings.HasSuffix(resp["filename"].(string), ".json") {
		t.Errorf("momo 的 profile 必须是 JSON: %v", resp["filename"])
	}
}

// 单测只能证明字段放对了位置。这一条用真内核 `sing-box check` 收口：
// 设 TRINE_SINGBOX_BIN 就跑，CI 没有内核时跳过（与 internal/livecheck 同约定）。
func TestMomoConfigPassesRealSingboxCheck(t *testing.T) {
	bin := os.Getenv("TRINE_SINGBOX_BIN")
	if bin == "" {
		t.Skip("未设置 TRINE_SINGBOX_BIN，跳过真机校验")
	}
	warns := []string{}
	v, _ := parseSBVersion("1.14")
	g := genMomo(v, testNodes(), nil, nil, &warns, ruleMirrorSel{}, bypassSpec{})
	raw, _ := json.MarshalIndent(g.doc, "", "  ")
	file := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(file, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bin, "check", "-c", file).CombinedOutput()
	if err != nil {
		t.Fatalf("sing-box check 不通过: %v\n%s", err, out)
	}
}
