package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 规则集下载源的核心是一条 URL 重写：把官方地址换成镜像地址。
// 下面几例把每个分支的产物钉死，改坏了立刻红。

func TestMirrorOfficialKeepsURL(t *testing.T) {
	raw := mihomoSetURL("geosite", "cn")
	for _, id := range []string{"", MirrorOfficial} {
		got, changed := ruleMirrorSel{ID: id}.apply(raw)
		if got != raw || changed {
			t.Fatalf("id=%q 应原样返回，实际 %q（changed=%v）", id, got, changed)
		}
	}
}

func TestMirrorPrefixPrepends(t *testing.T) {
	raw := mihomoSetURL("geosite", "cn")
	got, changed := ruleMirrorSel{ID: "gh-proxy"}.apply(raw)
	want := "https://gh-proxy.com/" + raw
	if !changed || got != want {
		t.Fatalf("前缀镜像应拼在原地址前：\n want %s\n got  %s", want, got)
	}
	// 拼出来的地址必须仍是 https，且原地址完整保留在尾部——
	// 少一个斜杠就会变成 https://gh-proxy.comhttps://…
	if !strings.HasPrefix(got, "https://gh-proxy.com/https://raw.githubusercontent.com/") {
		t.Fatalf("拼接结果不对：%s", got)
	}
}

func TestMirrorJsdelivrRewrites(t *testing.T) {
	got, changed := ruleMirrorSel{ID: "jsdelivr"}.apply(mihomoSetURL("geosite", "cn"))
	want := "https://cdn.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@meta/geo/geosite/cn.mrs"
	if !changed || got != want {
		t.Fatalf("jsDelivr 改写不对：\n want %s\n got  %s", want, got)
	}
	// sing-box 的仓库也要能改写（同一套规则，分支名不同）
	got, _ = ruleMirrorSel{ID: "jsdelivr"}.apply(singboxSetURL("geoip", "cn"))
	want = "https://cdn.jsdelivr.net/gh/SagerNet/sing-geoip@rule-set/geoip-cn.srs"
	if got != want {
		t.Fatalf("sing-geoip 改写不对：\n want %s\n got  %s", want, got)
	}
}

func TestMirrorJsdelivrLeavesNonRaw(t *testing.T) {
	// jsDelivr 只认 raw.githubusercontent.com。自定义规则集可能指向任何站点，
	// 那种地址必须原样保留（changed=false），不能被编出一个不存在的 CDN 路径。
	u := "https://example.com/rules/my.mrs"
	got, changed := ruleMirrorSel{ID: "jsdelivr"}.apply(u)
	if got != u || changed {
		t.Fatalf("非 raw 地址不应被改写，实际 %q（changed=%v）", got, changed)
	}
}

func TestMirrorCustomPrefixValidation(t *testing.T) {
	raw := mihomoSetURL("geosite", "cn")
	cases := []struct {
		prefix string
		ok     bool
	}{
		{"https://mirror.example.com/gh/", true},
		{"https://mirror.example.com/gh", true}, // 自动补尾斜杠
		{"http://mirror.example.com/", false},   // 不允许明文 http
		{"https://mirror.example.com/a b/", false},
		{"https://mirror.example.com/\"x/", false},
		{"file:///etc/", false},
		{"", false},
	}
	for _, c := range cases {
		got, changed := ruleMirrorSel{ID: "custom", Prefix: c.prefix}.apply(raw)
		if c.ok {
			if !changed || !strings.HasPrefix(got, "https://mirror.example.com/gh/") {
				t.Errorf("前缀 %q 应生效，实际 %q（changed=%v）", c.prefix, got, changed)
			}
		} else if changed || got != raw {
			t.Errorf("前缀 %q 应被拒绝并保留原地址，实际 %q", c.prefix, got)
		}
	}
}

func TestResolveMirrorFallsBack(t *testing.T) {
	// 未知 ID / 非法自定义前缀都退回官方，并给出原因——不能默认让请求失败
	if s, warn := resolveMirror("nope", ""); s.ID != MirrorOfficial || warn == "" {
		t.Fatalf("未知 ID 应退回官方并提示，实际 %+v warn=%q", s, warn)
	}
	if s, warn := resolveMirror("custom", "http://x/"); s.ID != MirrorOfficial || warn == "" {
		t.Fatalf("非法前缀应退回官方并提示，实际 %+v warn=%q", s, warn)
	}
	if s, warn := resolveMirror("custom", "https://x/"); s.ID != "custom" || warn != "" {
		t.Fatalf("合法自定义前缀应被接受，实际 %+v warn=%q", s, warn)
	}
}

// TestNormalizeSetsAppliesMirror 是这次改动的核心断言：两个内核、目录规则集，
// URL 都必须带上所选下载源。
func TestNormalizeSetsAppliesMirror(t *testing.T) {
	for _, core := range []string{"mihomo", "singbox"} {
		sets, _ := normalizeSets(genReq{
			Core:       core,
			Sets:       []setPolicy{{ID: "geosite-cn", Policy: "DIRECT"}},
			RuleMirror: "gh-proxy",
		})
		if len(sets) != 1 {
			t.Fatalf("%s: 期望 1 个规则集，实际 %d", core, len(sets))
		}
		if !strings.HasPrefix(sets[0].URL, "https://gh-proxy.com/https://raw.githubusercontent.com/") {
			t.Fatalf("%s: 规则集 URL 没走镜像：%s", core, sets[0].URL)
		}
	}
}

func TestNormalizeSetsCustomSetURLUntouched(t *testing.T) {
	// 自定义规则集是用户自己填的地址，可能本来就在用自家镜像；
	// 替人改写只会帮倒忙，所以必须原样保留。
	custom := "https://example.com/my-rules.mrs"
	sets, _ := normalizeSets(genReq{
		Core:       "mihomo",
		CustomSets: []customSetReq{{Name: "mine", URL: custom}},
		RuleMirror: "gh-proxy",
	})
	var found bool
	for _, s := range sets {
		if s.URL == custom {
			found = true
		}
		if strings.HasPrefix(s.URL, "https://gh-proxy.com/https://example.com/") {
			t.Fatalf("自定义 URL 被误改写成：%s", s.URL)
		}
	}
	if !found {
		t.Fatalf("自定义规则集应原样保留，实际 %+v", sets)
	}
}

// TestGeoProviderUsesMirror：GEOSITE/GEOIP 自定义规则会动态补一个 provider，
// 这个口子很容易漏——它和目录规则集走的是两条代码路径。
func TestGeoProviderUsesMirror(t *testing.T) {
	var sets []normSet
	geoProviderFor("mihomo", "GEOSITE", "bilibili", "DIRECT", &sets, ruleMirrorSel{ID: "gh-proxy"})
	if len(sets) != 1 {
		t.Fatalf("应补一个 provider，实际 %d 个", len(sets))
	}
	if !strings.HasPrefix(sets[0].URL, "https://gh-proxy.com/") {
		t.Fatalf("动态 provider 没走镜像：%s", sets[0].URL)
	}
	if sets[0].Tag != "geosite-bilibili" {
		t.Fatalf("tag 不对：%s", sets[0].Tag)
	}
}

// 规则集必须直连下载：写成走代理会让「下载规则集」依赖「节点已经通了」，
// 首启时互为前提，失败表现为「分流失效」而不是「节点不通」，很难归因。
func TestMihomoRuleProvidersDownloadDirect(t *testing.T) {
	warns := new([]string)
	g := genMihomo("client", testNodes(), []normSet{{
		Tag: "geosite-cn", URL: "https://example.com/cn.mrs",
		Behavior: "domain", Format: "mrs", Interval: 86400,
	}}, nil, warns, ruleMirrorSel{}, bypassSpec{})
	provs, ok := g.doc["rule-providers"].(map[string]any)
	if !ok || len(provs) == 0 {
		t.Fatalf("没生成 rule-providers：%#v", g.doc["rule-providers"])
	}
	for tag, v := range provs {
		m, _ := v.(map[string]any)
		if m["proxy"] != "DIRECT" {
			t.Errorf("provider %s 应由 DIRECT 下载，实际 proxy=%v", tag, m["proxy"])
		}
	}
}

func TestGenMihomoHeaderShowsMirror(t *testing.T) {
	warns := new([]string)
	g := genMihomo("client", testNodes(), nil, nil, warns, ruleMirrorSel{ID: "gh-proxy"}, bypassSpec{})
	if !strings.Contains(g.header, "规则集下载源：gh-proxy.com") {
		t.Fatalf("头注释应写明下载源，实际：\n%s", g.header)
	}
	// 官方源不写这一行，免得默认配置多一条噪声
	g = genMihomo("client", testNodes(), nil, nil, warns, ruleMirrorSel{ID: MirrorOfficial}, bypassSpec{})
	if strings.Contains(g.header, "规则集下载源") {
		t.Fatalf("官方源不该写下载源注释：\n%s", g.header)
	}
}

// ==================== 探测接口 ====================

// 200 + HTML 是最需要识别的一种「假可用」：状态码漂亮，内核拿到却不能用。
func TestProbeMirrorRejectsHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<!DOCTYPE html><html><body>checking your browser…</body></html>")
	}))
	defer srv.Close()

	p := probeRuleMirror(context.Background(), srv.URL)
	if p.OK {
		t.Fatalf("HTML 挑战页必须判为不可用，实际 ok=%v detail=%q", p.OK, p.Detail)
	}
	if !strings.Contains(p.Detail, "网页") {
		t.Fatalf("结论应说明拿到的是网页，实际 %q", p.Detail)
	}
}

func TestProbeMirrorAcceptsBinary(t *testing.T) {
	body := make([]byte, 4096)
	body[0], body[1], body[2], body[3] = 0x28, 0xb5, 0x2f, 0xfd // zstd 帧头
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	p := probeRuleMirror(context.Background(), srv.URL)
	if !p.OK {
		t.Fatalf("正常二进制应判为可用，实际 detail=%q", p.Detail)
	}
	if p.Magic != "28b52ffd" {
		t.Fatalf("应识别出 zstd 帧头，实际 %q", p.Magic)
	}
	if p.Bytes != len(body) {
		t.Fatalf("字节数应为 %d，实际 %d", len(body), p.Bytes)
	}
}

func TestProbeMirrorReportsStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_, _ = io.WriteString(w, "forbidden")
	}))
	defer srv.Close()

	p := probeRuleMirror(context.Background(), srv.URL)
	if p.OK || p.Status != 403 {
		t.Fatalf("403 应判为不可用并回显状态码，实际 ok=%v status=%d", p.OK, p.Status)
	}
	if !strings.Contains(p.Detail, "403") {
		t.Fatalf("结论应带上状态码，实际 %q", p.Detail)
	}
}

func TestGuardOutboundHostBlocksPrivate(t *testing.T) {
	bad := []string{
		"https://127.0.0.1/x", "https://10.0.0.5/x", "https://192.168.1.1/x",
		"https://[::1]/x", "https://169.254.169.254/latest/meta-data",
	}
	for _, u := range bad {
		if err := guardOutboundHost(u); err == nil {
			t.Errorf("应拒绝内网地址: %s", u)
		}
	}
	if err := guardOutboundHost("https://gh-proxy.com/https://raw.githubusercontent.com/x"); err != nil {
		t.Errorf("公网地址不该被拦: %v", err)
	}
}

// 30x 跳到内网是绕过「只检查入口地址」的经典手法，跳转链必须逐跳校验。
func TestProbeMirrorBlocksRedirectToPrivate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/secret", http.StatusFound)
	}))
	defer srv.Close()

	p := probeRuleMirror(context.Background(), srv.URL)
	if p.OK {
		t.Fatalf("跳转到内网必须失败，实际 ok=%v detail=%q", p.OK, p.Detail)
	}
	if !strings.Contains(p.Detail, "内网") {
		t.Fatalf("结论应指出被拦在内网检查，实际 %q", p.Detail)
	}
}
