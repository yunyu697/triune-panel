package api

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"triune/internal/link"
)

// ==================== 版本解析与能力门限 ====================

func TestParseSBVersion(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "1.14", false},      // 空 → 默认最新稳定
		{"1.11", "1.11", false},  //
		{"1.12", "1.12", false},  //
		{"v1.12", "1.12", false}, // 带 v 前缀
		{"V1.12", "1.12", false}, //
		{"1.12.25", "1.12.25", false},
		{"1.15.0-alpha.13", "1.15", false}, // 预发布后缀被剥掉
		{"1.14.4", "1.14.4", false},        //
		{"2.0", "2.0", false},              // 未来大版本不拒，由门限决定行为
		{"1", "", true},                    // 只有主版本
		{"1.12.25.1", "", true},            // 段数过多
		{"abc", "", true},                  //
		{"1.x", "", true},                  //
	}
	for _, c := range cases {
		v, err := parseSBVersion(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseSBVersion(%q) 应报错，实际得到 %s", c.in, v)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseSBVersion(%q) 意外报错: %v", c.in, err)
			continue
		}
		if v.String() != c.want {
			t.Errorf("parseSBVersion(%q) = %s，期望 %s", c.in, v, c.want)
		}
	}
}

// TestSBCapsMatrix 把「哪个版本有哪些字段」钉死。
// 这张表就是 README 里那张实测表的代码化版本：改门限必须同步改测试。
func TestSBCapsMatrix(t *testing.T) {
	type want struct {
		newDNS, resolver, stack bool
	}
	cases := map[string]want{
		"1.11": {newDNS: false, resolver: false, stack: true},
		"1.12": {newDNS: true, resolver: true, stack: true},
		"1.13": {newDNS: true, resolver: true, stack: true},
		"1.14": {newDNS: true, resolver: true, stack: true},
		"1.15": {newDNS: true, resolver: true, stack: false}, // stack 自 1.15 废弃
		"1.16": {newDNS: true, resolver: true, stack: false},
	}
	for s, w := range cases {
		v, err := parseSBVersion(s)
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		c := capsFor(v)
		if c.NewDNSFormat != w.newDNS {
			t.Errorf("%s NewDNSFormat = %v，期望 %v", s, c.NewDNSFormat, w.newDNS)
		}
		if c.DomainResolver != w.resolver {
			t.Errorf("%s DomainResolver = %v，期望 %v", s, c.DomainResolver, w.resolver)
		}
		if c.TunStack != w.stack {
			t.Errorf("%s TunStack = %v，期望 %v", s, c.TunStack, w.stack)
		}
	}
}

// ==================== 生成结果差异 ====================

func genSBRaw(t *testing.T, verStr, target string) []byte {
	t.Helper()
	ver, err := parseSBVersion(verStr)
	if err != nil {
		t.Fatalf("版本 %q: %v", verStr, err)
	}
	nodes := []*link.Node{{
		Protocol: "vless", Name: "hk-alice", Host: "203.0.113.7", Port: 443,
		Cred:     "11111111-2222-3333-4444-555555555555",
		Security: "tls", Network: "tcp", SNI: "www.example.com",
		Extra: map[string]any{},
	}}
	sets := []normSet{
		{Tag: "geosite-cn", URL: "https://example.com/cn.srs", Behavior: "domain", Policy: "DIRECT", Format: "binary", Interval: 86400},
	}
	var warns []string
	g := genSingbox(target, ver, nodes, sets, nil, &warns)
	b, err := json.MarshalIndent(g.doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func genSBDoc(t *testing.T, verStr, target string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(genSBRaw(t, verStr, target), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// TestGenSingboxDNSFormat 1.11 用 address，1.12+ 用 type+server。
// 写错一边内核会以 "unknown field" 直接拒绝启动。
func TestGenSingboxDNSFormat(t *testing.T) {
	v11 := genSBDoc(t, "1.11", "client")
	s11 := v11["dns"].(map[string]any)["servers"].([]any)[0].(map[string]any)
	if s11["address"] == nil {
		t.Errorf("1.11 的 DNS 服务器应有 address 字段，实际 %v", s11)
	}
	if _, bad := s11["server"]; bad {
		t.Errorf("1.11 的 DNS 服务器不能有 server 字段（内核会 FATAL），实际 %v", s11)
	}

	for _, ver := range []string{"1.12", "1.13", "1.14", "1.15", "1.16"} {
		doc := genSBDoc(t, ver, "client")
		s := doc["dns"].(map[string]any)["servers"].([]any)[0].(map[string]any)
		if s["server"] == nil || s["type"] == nil {
			t.Errorf("%s 的 DNS 服务器应有 type+server，实际 %v", ver, s)
		}
		if _, bad := s["address"]; bad {
			t.Errorf("%s 不应再写 address（已废弃），实际 %v", ver, s)
		}
	}
}

// TestGenSingboxDefaultDomainResolver 只有 1.12+ 能写这个字段。
func TestGenSingboxDefaultDomainResolver(t *testing.T) {
	route11 := genSBDoc(t, "1.11", "client")["route"].(map[string]any)
	if _, bad := route11["default_domain_resolver"]; bad {
		t.Errorf("1.11 不应有 default_domain_resolver（实测 FATAL），实际 %v", route11)
	}
	for _, ver := range []string{"1.12", "1.14", "1.16"} {
		route := genSBDoc(t, ver, "main-router")["route"].(map[string]any)
		if route["default_domain_resolver"] == nil {
			t.Errorf("%s 应有 default_domain_resolver", ver)
		}
	}
}

// TestGenSingboxTunStack tun.stack 自 1.15 起废弃，1.17 移除。
func TestGenSingboxTunStack(t *testing.T) {
	tunOf := func(ver string) map[string]any {
		inbounds := genSBDoc(t, ver, "main-router")["inbounds"].([]any)
		for _, ib := range inbounds {
			m := ib.(map[string]any)
			if m["type"] == "tun" {
				return m
			}
		}
		t.Fatalf("%s 没生成 tun 入站", ver)
		return nil
	}
	for _, ver := range []string{"1.11", "1.12", "1.13", "1.14"} {
		if got := tunOf(ver)["stack"]; got != "mixed" {
			t.Errorf("%s 的 tun.stack 应为 mixed，实际 %v", ver, got)
		}
	}
	for _, ver := range []string{"1.15", "1.16"} {
		if _, bad := tunOf(ver)["stack"]; bad {
			t.Errorf("%s 不应再写 tun.stack（已废弃）", ver)
		}
	}
}

// TestGenSingboxSameAcrossPatchVersions 1.12/1.13/1.14 生成的配置必须逐字节相同。
//
// 这条是给「每个版本都不一样」这个直觉踩刹车的：实测 1.12.25、1.13.21、1.14.4
// 对同一份 JSON 全部 check 通过，配置没有任何差别。将来若真的出现新断点，
// 这个测试会先失败，提醒去更新 capsFor 而不是靠人记。
func TestGenSingboxSameAcrossPatchVersions(t *testing.T) {
	a := genSBRaw(t, "1.12", "main-router")
	for _, ver := range []string{"1.13", "1.14"} {
		b := genSBRaw(t, ver, "main-router")
		if !bytes.Equal(a, b) {
			t.Errorf("1.12 与 %s 生成的配置应完全一致，实际不同", ver)
		}
	}
}

// TestUrlTestSpelling 两个内核的同名策略组写法不同，混用直接起不来：
//
//	mihomo   → type: url-test   （连字符）
//	sing-box → type: urltest    （连写）
//
// 这条曾经是最容易手滑的地方，钉成测试防止回归。
func TestUrlTestSpelling(t *testing.T) {
	mihomo := genMihomo("client", testNodes(), nil, nil, new([]string))
	mhGroups := mihomo.doc["proxy-groups"].([]any)
	var mhType string
	for _, g := range mhGroups {
		if m := g.(map[string]any); m["name"] == "Auto" {
			mhType, _ = m["type"].(string)
		}
	}
	if mhType != "url-test" {
		t.Errorf("mihomo 的 Auto 组 type 应为 url-test，实际 %q", mhType)
	}

	sb := genSBDoc(t, "1.14", "client")
	var sbType string
	for _, o := range sb["outbounds"].([]any) {
		if m := o.(map[string]any); m["tag"] == "Auto" {
			sbType, _ = m["type"].(string)
		}
	}
	if sbType != "urltest" {
		t.Errorf("sing-box 的 Auto 出站 type 应为 urltest，实际 %q", sbType)
	}
}

func testNodes() []*link.Node {
	return []*link.Node{{
		Protocol: "vless", Name: "hk-alice", Host: "203.0.113.7", Port: 443,
		Cred:     "11111111-2222-3333-4444-555555555555",
		Security: "tls", Network: "tcp", Extra: map[string]any{},
	}}
}

// ==================== HTTP 层：老版本拒绝 ====================

func TestExportConfigRejectsOldSingbox(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")

	// 造一个入站 + 用户，否则会先撞「请至少选择一个入站」
	if w := do(a, owner, "POST", "/api/inbounds", map[string]any{
		"name": "hk", "core": "singbox", "protocol": "vless", "listen": "::",
		"port": 443, "transport": "tcp", "tls": "tls", "enabled": true,
		"settings": `{"server_name":"www.example.com"}`,
	}); w.Code != 200 {
		t.Fatalf("建入站失败 %d: %s", w.Code, w.Body.String())
	}
	w := do(a, owner, "POST", "/api/clients", map[string]any{"name": "alice"})
	if w.Code != 200 {
		t.Fatalf("建用户失败 %d: %s", w.Code, w.Body.String())
	}
	var c struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	if w := do(a, owner, "PUT", "/api/clients/1/inbounds", map[string]any{"inbound_ids": []int64{1}}); w.Code != 200 {
		t.Fatalf("绑定入站失败 %d: %s", w.Code, w.Body.String())
	}

	base := map[string]any{
		"core": "singbox", "target": "client", "inbound_ids": []int64{1},
		"scenario": "balanced",
	}

	// 1.10 及以前：明确拒绝，并说清为什么
	req := map[string]any{}
	for k, v := range base {
		req[k] = v
	}
	req["core_version"] = "1.10"
	w = do(a, owner, "POST", "/api/export/config", req)
	if w.Code != 400 {
		t.Fatalf("1.10 应 400 拒绝，实际 %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "1.11") {
		t.Errorf("拒绝信息应指明最低支持 1.11，实际 %s", w.Body.String())
	}

	// 1.11：能生成，且响应里带版本说明
	req["core_version"] = "1.11"
	w = do(a, owner, "POST", "/api/export/config", req)
	if w.Code != 200 {
		t.Fatalf("1.11 应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		CoreVersion string   `json:"core_version"`
		VersionNote string   `json:"version_note"`
		Warnings    []string `json:"warnings"`
		Content     string   `json:"content"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.CoreVersion != "1.11" {
		t.Errorf("响应 core_version 应为 1.11，实际 %q", got.CoreVersion)
	}
	if !strings.Contains(got.VersionNote, "停止维护") {
		t.Errorf("1.11 应提示已停止维护，实际 %q", got.VersionNote)
	}
	if strings.Contains(got.Content, `"default_domain_resolver"`) {
		t.Error("1.11 的配置里不该出现 default_domain_resolver")
	}
	if !strings.Contains(got.Content, `"address": "https://8.8.8.8/dns-query"`) {
		t.Error("1.11 的 DNS 应使用 address 格式")
	}

	// 未知版本字符串：400
	req["core_version"] = "abc"
	if w := do(a, owner, "POST", "/api/export/config", req); w.Code != 400 {
		t.Errorf("非法版本号应 400，实际 %d", w.Code)
	}
}

// TestExportConfigMihomoIgnoresVersion mihomo 不区分版本，传了也不该报错。
func TestExportConfigMihomoIgnoresVersion(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")
	do(a, owner, "POST", "/api/inbounds", map[string]any{
		"name": "hk", "core": "singbox", "protocol": "vless", "listen": "::",
		"port": 443, "transport": "tcp", "tls": "tls", "enabled": true,
		"settings": `{"server_name":"www.example.com"}`,
	})
	do(a, owner, "POST", "/api/clients", map[string]any{"name": "alice"})
	do(a, owner, "PUT", "/api/clients/1/inbounds", map[string]any{"inbound_ids": []int64{1}})

	w := do(a, owner, "POST", "/api/export/config", map[string]any{
		"core": "mihomo", "core_version": "1.11", "target": "client",
		"inbound_ids": []int64{1}, "scenario": "balanced",
	})
	if w.Code != 200 {
		t.Fatalf("mihomo 传版本号应被忽略而非报错，实际 %d: %s", w.Code, w.Body.String())
	}
}

// ==================== 可选：真实内核交叉校验 ====================
//
// 设 SINGBOX_BINS 为逗号分隔的二进制路径即可启用，例如：
//
//	SINGBOX_BINS=/tmp/sbver/sing-box-1.11.15-darwin-arm64/sing-box,/tmp/sbver/sing-box-1.14.4-darwin-arm64/sing-box \
//	  go test ./internal/api/ -run TestGenSingboxRealBinary -v
//
// CI 上没有这些二进制，自动跳过——但本机跑过一次，就说明这张门限表不是纸上推演。
func TestGenSingboxRealBinary(t *testing.T) {
	bins := os.Getenv("SINGBOX_BINS")
	if bins == "" {
		t.Skip("未设置 SINGBOX_BINS，跳过真实内核校验")
	}
	for _, bin := range strings.Split(bins, ",") {
		bin = strings.TrimSpace(bin)
		if bin == "" {
			continue
		}
		verOut, err := exec.Command(bin, "version").Output()
		if err != nil {
			t.Fatalf("%s 无法执行: %v", bin, err)
		}
		// "sing-box version 1.14.4"
		fields := strings.Fields(string(verOut))
		if len(fields) < 3 {
			t.Fatalf("%s 版本输出异常: %s", bin, verOut)
		}
		binVer := fields[2]

		ver, perr := parseSBVersion(binVer)
		if perr != nil {
			t.Fatalf("%s 版本 %q 解析失败: %v", bin, binVer, perr)
		}

		// 低于最低支持版本（1.10 及以前）：生成的配置**必须**被拒绝。
		// 这不是缺陷，正是 handler 直接 400 的原因——没有 rule action 就没有
		// 可用的路由模板，硬生成只会让用户在客户端上撞到看不懂的 FATAL。
		if !ver.atLeast(minSBVersion.Major, minSBVersion.Minor) {
			raw := genSBRaw(t, "1.11", "client")
			f := t.TempDir() + "/config.json"
			if err := os.WriteFile(f, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command(bin, "check", "-c", f).CombinedOutput(); err == nil {
				t.Errorf("%s 低于最低支持版本 %s，但 1.11 配置居然通过了校验——门限写松了\n%s",
					binVer, minSBVersion, out)
			}
			t.Logf("%s 低于最低支持版本 %s，已确认配置被内核拒绝（符合预期）", binVer, minSBVersion)
			continue
		}

		// 用二进制自身的版本去生成配置，必须能通过它自己的 check
		for _, target := range []string{"client", "main-router"} {
			raw := genSBRaw(t, binVer, target)
			f := t.TempDir() + "/config.json"
			if err := os.WriteFile(f, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command(bin, "check", "-c", f).CombinedOutput()
			if err != nil {
				t.Errorf("%s / %s 生成的配置被内核拒绝: %v\n%s", binVer, target, err, out)
			}
		}
		t.Logf("%s 的 client 与 main-router 配置均校验通过", binVer)
	}
}
