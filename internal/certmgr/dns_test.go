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

// TestParseDNSEnv 凭据框的格式、别名与「只认本服务商前缀」这条边界。
func TestParseDNSEnv(t *testing.T) {
	cf, _ := LookupDNSProvider("cloudflare")
	got, err := ParseDNSEnv("# 注释\n\nCLOUDFLARE_DNS_API_TOKEN=abc\n", cf)
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if got["CLOUDFLARE_DNS_API_TOKEN"] != "abc" {
		t.Errorf("解析结果不对: %v", got)
	}

	// 别名：CF_ 前缀本来是面板自己声明接受的，就必须真能单独用。
	// 曾经的实现「按前缀放行、再要求必须出现 CLOUDFLARE_DNS_API_TOKEN」，
	// 于是填 CF_DNS_API_TOKEN 得到「缺少 CLOUDFLARE_DNS_API_TOKEN」，自相矛盾。
	got, err = ParseDNSEnv("CF_DNS_API_TOKEN=abc", cf)
	if err != nil {
		t.Fatalf("CF_ 别名不该被拒: %v", err)
	}
	if got["CF_DNS_API_TOKEN"] != "abc" {
		t.Errorf("别名解析结果不对: %v", got)
	}

	// 大小写归一：os.Setenv 与 lego 只认大写名
	got, err = ParseDNSEnv("cloudflare_dns_api_token=abc", cf)
	if err != nil {
		t.Fatalf("小写不该被拒（用户手打小写是常态）: %v", err)
	}
	if got["CLOUDFLARE_DNS_API_TOKEN"] != "abc" {
		t.Errorf("小写未归一成大写: %v", got)
	}

	// 前缀对、名字错：Cloudflare 文档里最常见的是 CLOUDFLARE_API_TOKEN，
	// 但 lego 读的是 CLOUDFLARE_DNS_API_TOKEN。放过去只会在签发时炸，
	// 必须当场说清该写哪个名字。
	if _, err := ParseDNSEnv("CLOUDFLARE_API_TOKEN=abc", cf); err == nil ||
		!strings.Contains(err.Error(), "CLOUDFLARE_DNS_API_TOKEN") {
		t.Errorf("拼错的名字应指到正确的变量名，实际: %v", err)
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

	// 从 Cloudflare 文档复制：值常带引号或 "Bearer " 前缀，令牌本身不会有这两样。
	// 留着它就会进 Authorization 头，签发时报 401，而错误信息完全指不到这里。
	got, err = ParseDNSEnv(`CLOUDFLARE_DNS_API_TOKEN="Bearer cfut_abc123"`, cf)
	if err != nil {
		t.Fatalf("带引号 / Bearer 的值不该报错: %v", err)
	}
	if got["CLOUDFLARE_DNS_API_TOKEN"] != "cfut_abc123" {
		t.Errorf("引号与 Bearer 没剥掉: %q", got["CLOUDFLARE_DNS_API_TOKEN"])
	}

	// 组间是「且」：阿里云少一个键要点名缺哪个
	ali, _ := LookupDNSProvider("alidns")
	if _, err := ParseDNSEnv("ALICLOUD_ACCESS_KEY=ak", ali); err == nil ||
		!strings.Contains(err.Error(), "ALICLOUD_SECRET_KEY") {
		t.Errorf("阿里云缺 SECRET_KEY 应报出键名，实际: %v", err)
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
		if p.ID == "" || p.Label == "" || len(p.Env) == 0 || len(p.Prefixes) == 0 || len(p.Require) == 0 {
			t.Errorf("服务商 %+v 信息不全", p)
		}
		if seen[p.ID] {
			t.Errorf("服务商 ID 重复: %s", p.ID)
		}
		seen[p.ID] = true
		// 每个被接受的变量名都必须落在允许的前缀里，否则「照提示填」反而会被拒
		for _, g := range p.Require {
			if len(g) == 0 {
				t.Errorf("%s 有一个空的 Require 组（会永远缺凭据）", p.ID)
			}
			for _, name := range g {
				if !hasAnyPrefix(name, p.Prefixes) {
					t.Errorf("%s 接受的 %s 不在允许前缀 %v 内", p.ID, name, p.Prefixes)
				}
				if name != strings.ToUpper(name) {
					t.Errorf("%s 的 %s 不是大写：环境变量名会被归一成大写", p.ID, name)
				}
			}
		}
		// 前端提示用的主变量必须是真的能用的名字，否则提示本身就是坑
		if !knownName(p, p.Env[0]) {
			t.Errorf("%s 的提示变量 %s 不在 Require 里", p.ID, p.Env[0])
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

// knownName 判断某个变量名是否是这家服务商接受的（测试用）。
func knownName(p DNSProviderInfo, name string) bool {
	for _, g := range p.Require {
		for _, k := range g {
			if k == name {
				return true
			}
		}
	}
	return false
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
