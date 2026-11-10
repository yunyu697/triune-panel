package certmgr

import (
	"os"
	"strings"
	"testing"
)

// TestParseDomainsAcceptsListAndWildcard 一张证书里带多个名字是泛域名场景的常态：
// 只有 *.example.com 的话，用户访问裸域名会报证书错误。
func TestParseDomainsAcceptsListAndWildcard(t *testing.T) {
	got, err := ParseDomains(" example.com, *.Example.COM ;www.example.com\nexample.com. ")
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	want := []string{"example.com", "*.example.com", "www.example.com"}
	if len(got) != len(want) {
		t.Fatalf("解析结果 %v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 项 = %q，期望 %q", i, got[i], want[i])
		}
	}
}

// TestParseDomainsRejects 错误信息要能指到具体哪一条，否则用户面对一张
// 多域名列表只能一个个试。
func TestParseDomainsRejects(t *testing.T) {
	cases := map[string]string{
		"":                       "没有填域名",
		"1.2.3.4":                "IP",
		"*.example.com, *.*.com": "通配符",
		"a.*.example.com":        "通配符",
		"-bad.example.com":       "连字符",
		"bad-.example.com":       "连字符",
		"exa..mple.com":          "连续的点",
		"localhost":              "点号",
		"under_score.com":        "非法字符",
	}
	for raw, wantSub := range cases {
		_, err := ParseDomains(raw)
		if err == nil {
			t.Errorf("%q 应当被拒", raw)
			continue
		}
		if !strings.Contains(err.Error(), wantSub) {
			t.Errorf("%q 的错误应提到 %q，实际: %v", raw, wantSub, err)
		}
	}
}

// TestCertSlugIsFileSafe 泛域名里有 "*"，它不该出现在文件系统路径里：
// 用户会把路径复制到 nginx 或入站设置里，shell 与 glob 都会踩到。
func TestCertSlugIsFileSafe(t *testing.T) {
	cases := []struct{ in, want string }{
		{"example.com", "example.com"},
		{"*.example.com", "wildcard.example.com"},
		{"*.Example.COM", "wildcard.example.com"},
		{"../../etc/passwd", "etc-passwd"},
		{"..", "cert"},
		{"", "cert"},
	}
	for _, c := range cases {
		if got := CertSlug([]string{c.in}); got != c.want {
			t.Errorf("CertSlug(%q) = %q，期望 %q", c.in, got, c.want)
		}
		if strings.ContainsAny(CertSlug([]string{c.in}), "*") {
			t.Errorf("CertSlug(%q) 仍含通配符: %q", c.in, CertSlug([]string{c.in}))
		}
	}
}

// TestCertSlugKeepsLegacyPath 老用户的目录是 certs/<domain>/，升级后路径不能变，
// 否则入站里填好的证书路径会全部失效。
func TestCertSlugKeepsLegacyPath(t *testing.T) {
	if got := CertDirFor("/data", []string{"panel.example.com"}); got != "/data/certs/panel.example.com" {
		t.Errorf("旧路径变了: %s", got)
	}
}

func TestHasWildcard(t *testing.T) {
	if !HasWildcard([]string{"example.com", "*.example.com"}) {
		t.Error("应当识别出泛域名")
	}
	if HasWildcard([]string{"example.com"}) {
		t.Error("没有泛域名时不该判成有")
	}
}

// TestParseDNSEnv 凭据框的格式与「只认本服务商前缀」这条边界。
func TestParseDNSEnv(t *testing.T) {
	cf, _ := LookupDNSProvider("cloudflare")
	got, err := ParseDNSEnv("# 注释\nCF_EXTRA=x\n\nCLOUDFLARE_DNS_API_TOKEN=abc\n", cf)
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if got["CLOUDFLARE_DNS_API_TOKEN"] != "abc" || got["CF_EXTRA"] != "x" {
		t.Errorf("解析结果不对: %v", got)
	}

	// 必填项缺失
	if _, err := ParseDNSEnv("CF_EXTRA=x", cf); err == nil ||
		!strings.Contains(err.Error(), "CLOUDFLARE_DNS_API_TOKEN") {
		t.Errorf("缺必填项应报出键名，实际: %v", err)
	}
	// 别的服务商前缀：允许任意变量名就等于把 os.Setenv 开放给用户，
	// 一个 PATH=/usr/bin 能让内核下载和命令执行一起失灵。
	if _, err := ParseDNSEnv("PATH=/usr/bin", cf); err == nil ||
		!strings.Contains(err.Error(), "CLOUDFLARE_") {
		t.Errorf("跨前缀的键应当被拒并说明允许的前缀，实际: %v", err)
	}
	if _, err := ParseDNSEnv("CLOUDFLARE_DNS_API_TOKEN", cf); err == nil {
		t.Error("缺等号应报错")
	}
	if _, err := ParseDNSEnv("CLOUDFLARE_DNS_API_TOKEN=", cf); err == nil {
		t.Error("空值应报错")
	}
	if _, err := ParseDNSEnv("", cf); err == nil {
		t.Error("空凭据应报错")
	}
}

// TestProviderTableIsConsistent 表里的每一项都要能被 newDNSProvider 造出来，
// 否则前端会给出一个「选得动、一签发就报错」的选项。
func TestProviderTableIsConsistent(t *testing.T) {
	if len(dnsProviderTable) == 0 {
		t.Fatal("服务商表是空的")
	}
	seen := map[string]bool{}
	for _, p := range dnsProviderTable {
		if p.ID == "" || p.Label == "" || len(p.Env) == 0 || len(p.Prefixes) == 0 {
			t.Errorf("服务商 %+v 信息不全", p)
		}
		if seen[p.ID] {
			t.Errorf("服务商 ID 重复: %s", p.ID)
		}
		seen[p.ID] = true
		// 必填变量必须落在允许的前缀里，否则「照提示填」反而会被拒
		for _, need := range p.Env {
			if !hasAnyPrefix(need, p.Prefixes) {
				t.Errorf("%s 的必填项 %s 不在允许前缀 %v 内", p.ID, need, p.Prefixes)
			}
		}
		// 构造不碰网络：只是把配置读进来。凭据缺失时会报错，那也说明
		// provider 确实被编进来了（不是空壳）。
		if _, err := newDNSProvider(p.ID); err != nil &&
			strings.Contains(err.Error(), "不认识") {
			t.Errorf("%s 没有被编译进来: %v", p.ID, err)
		}
	}
	if _, err := newDNSProvider("nope"); err == nil {
		t.Error("未知服务商应当报错")
	}
	if _, ok := LookupDNSProvider("cloudflare"); !ok {
		t.Error("cloudflare 应当有序号")
	}
}

// TestApplyDNSEnvRestores 环境变量用完要还原，否则换服务商之后旧 token
// 还留在进程里，下次签发可能拿去改错域名。
func TestApplyDNSEnvRestores(t *testing.T) {
	t.Setenv("TRIUNE_TEST_KEEP", "old")
	restore := applyDNSEnv(map[string]string{
		"TRIUNE_TEST_KEEP": "new",
		"TRIUNE_TEST_TEMP": "x",
	})
	if v := os.Getenv("TRIUNE_TEST_KEEP"); v != "new" {
		t.Fatalf("写入失败: %q", v)
	}
	restore()
	if v := os.Getenv("TRIUNE_TEST_KEEP"); v != "old" {
		t.Errorf("旧值没还原: %q", v)
	}
	if v := os.Getenv("TRIUNE_TEST_TEMP"); v != "" {
		t.Errorf("临时变量没清掉: %q", v)
	}
}
