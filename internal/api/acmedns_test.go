package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"triune/internal/certmgr"
)

// TestAcmeDNSCredentialsMasked 凭据绝不回显。
//
// 这条不是洁癖：一个 Cloudflare token 能把整个域名的解析指向别处，再签一张
// 证书冒充这个站。回显到浏览器 DOM 里，等于把它交给了任何一次截图、录屏、
// 或者「打开控制台看一眼」的人。所以接口只给占位符，前端把占位符原样提交
// 回来表示「没改过」。
func TestAcmeDNSCredentialsMasked(t *testing.T) {
	a := newRoleTestServer(t)
	const real = "CLOUDFLARE_DNS_API_TOKEN=super-secret-token"

	w := authedReq(t, a, "PUT", "/api/settings", map[string]string{
		"acme_dns_provider": "cloudflare",
		"acme_dns_env":      real,
	})
	if w.Code != 200 {
		t.Fatalf("保存凭据失败: %d %s", w.Code, w.Body.String())
	}

	// 1) GET 只给占位符
	w = authedReq(t, a, "GET", "/api/settings", nil)
	if strings.Contains(w.Body.String(), "super-secret-token") {
		t.Fatalf("凭据被回显了: %s", w.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if got[certmgr.SettingDNSEnv] != certmgr.MaskedSecret {
		t.Errorf("应当是占位符，实际 %q", got[certmgr.SettingDNSEnv])
	}
	if got[certmgr.SettingDNSProvider] != "cloudflare" {
		t.Errorf("服务商应当正常回显，实际 %q", got[certmgr.SettingDNSProvider])
	}

	// 2) 把占位符原样交回来 = 没改过，库里的真值不能被覆盖
	if w := authedReq(t, a, "PUT", "/api/settings", map[string]string{
		"acme_dns_env": certmgr.MaskedSecret, "acme_email": "ops@example.com",
	}); w.Code != 200 {
		t.Fatalf("二次保存失败: %d %s", w.Code, w.Body.String())
	}
	if v, _ := a.Store.GetSetting(certmgr.SettingDNSEnv); v != real {
		t.Errorf("占位符覆盖了真凭据: %q", v)
	}

	// 3) 提交新值才真的替换
	if w := authedReq(t, a, "PUT", "/api/settings", map[string]string{
		"acme_dns_env": "CLOUDFLARE_DNS_API_TOKEN=rotated",
	}); w.Code != 200 {
		t.Fatalf("轮换失败: %d %s", w.Code, w.Body.String())
	}
	if v, _ := a.Store.GetSetting(certmgr.SettingDNSEnv); !strings.Contains(v, "rotated") {
		t.Errorf("凭据没更新: %q", v)
	}

	// 4) 变更通知/审计里的 diff 必须打码
	if d := settingDiff(certmgr.SettingDNSEnv, "a", "b"); strings.Contains(d, "→") {
		t.Errorf("diff 泄漏了凭据: %s", d)
	}
}

// TestAcmeWildcardRequiresDNS 泛域名只能用 DNS-01，这是 Let's Encrypt 的硬性规定。
// 在保存期就拦住：签发是异步的，用户等一分钟看到一句英文报错，基本只能来问人。
func TestAcmeWildcardRequiresDNS(t *testing.T) {
	a := newRoleTestServer(t)

	w := authedReq(t, a, "PUT", "/api/settings", map[string]string{
		"acme_domain": "*.example.com",
	})
	if w.Code != 400 || !strings.Contains(w.Body.String(), "DNS-01") {
		t.Fatalf("泛域名 + HTTP-01 应当被拒并说明原因，实际 %d %s", w.Code, w.Body.String())
	}

	w = authedReq(t, a, "PUT", "/api/settings", map[string]string{
		"acme_domain":       "example.com, *.example.com",
		"acme_dns_provider": "cloudflare",
		"acme_dns_env":      "CLOUDFLARE_DNS_API_TOKEN=t",
	})
	if w.Code != 200 {
		t.Fatalf("配了 DNS-01 就该放行: %d %s", w.Code, w.Body.String())
	}

	// 反过来：域名里还留着泛域名时，把验证方式切回 HTTP-01 也要拦住——
	// 否则库里躺着一个必然失败的组合，下次检查才报错。
	w = authedReq(t, a, "PUT", "/api/settings", map[string]string{
		"acme_dns_provider": "",
	})
	if w.Code != 400 || !strings.Contains(w.Body.String(), "DNS-01") {
		t.Fatalf("切回 HTTP-01 时应当提醒泛域名，实际 %d %s", w.Code, w.Body.String())
	}
}

// TestAcmeDNSValidation 四种错法各给一句能看懂的原因。
func TestAcmeDNSValidation(t *testing.T) {
	a := newRoleTestServer(t)
	cases := []struct {
		name string
		body map[string]string
		want string
	}{
		{"未知服务商", map[string]string{"acme_dns_provider": "nope"},
			"不支持的 DNS 服务商"},
		{"跨前缀的键", map[string]string{
			"acme_dns_provider": "cloudflare", "acme_dns_env": "PATH=/usr/bin"},
			"凭据有误"},
		{"缺必填项", map[string]string{
			"acme_dns_provider": "alidns", "acme_dns_env": "ALICLOUD_ACCESS_KEY=ak"},
			"SECRET_KEY"},
		{"选了服务商却没凭据", map[string]string{"acme_dns_provider": "dnspod"},
			"需要凭据"},
		{"有凭据没选服务商", map[string]string{
			"acme_dns_env": "CLOUDFLARE_DNS_API_TOKEN=t"},
			"没有选服务商"},
		{"域名非法", map[string]string{"acme_domain": "1.2.3.4"},
			"IP"},
	}
	for _, c := range cases {
		w := authedReq(t, a, "PUT", "/api/settings", c.body)
		if w.Code != 400 {
			t.Errorf("%s：应当 400，实际 %d %s", c.name, w.Code, w.Body.String())
			continue
		}
		if !strings.Contains(w.Body.String(), c.want) {
			t.Errorf("%s：错误里应提到 %q，实际 %s", c.name, c.want, w.Body.String())
		}
	}

	// 别名与小写必须能存：CF_ 是面板自己声明接受的前缀，CLOUDFLARE_ 之外还要有它；
	// 小写是用户手打的常态，归一大写即可，不该报「前缀不对」。
	for _, env := range []string{
		"CF_DNS_API_TOKEN=t",
		"cloudflare_dns_api_token=t",
	} {
		if w := authedReq(t, a, "PUT", "/api/settings", map[string]string{
			"acme_dns_provider": "cloudflare", "acme_dns_env": env,
		}); w.Code != 200 {
			t.Errorf("%q 应当能保存，实际 %d %s", env, w.Code, w.Body.String())
		}
	}
	// 前缀对但名字错：必须当场拒绝并指到正确的变量名，而不是等到签发时炸
	if w := authedReq(t, a, "PUT", "/api/settings", map[string]string{
		"acme_dns_provider": "cloudflare", "acme_dns_env": "CLOUDFLARE_API_TOKEN=t",
	}); w.Code != 400 || !strings.Contains(w.Body.String(), "CLOUDFLARE_DNS_API_TOKEN") {
		t.Errorf("拼错的变量名应 400 并给出正确名字，实际 %d %s", w.Code, w.Body.String())
	}

	// 域名会被归一化：大小写、尾点、分隔符统一成逗号串
	if w := authedReq(t, a, "PUT", "/api/settings", map[string]string{
		"acme_domain": "Example.COM. ",
	}); w.Code != 200 {
		t.Fatalf("保存失败: %d %s", w.Code, w.Body.String())
	}
	if v, _ := a.Store.GetSetting("acme_domain"); v != "example.com" {
		t.Errorf("域名没归一化: %q", v)
	}
}

// TestCertProvidersEndpoint 服务商清单由后端给：它是「编译进来的那几个」，
// 前端硬编码一份必然漂移，漂移的后果是用户照着填了一个不存在的变量名。
func TestCertProvidersEndpoint(t *testing.T) {
	a := newRoleTestServer(t)
	w := authedReq(t, a, "GET", "/api/certs/providers", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Providers []struct {
			ID   string   `json:"id"`
			Env  []string `json:"env"`
			Hint string   `json:"hint"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	byID := map[string][]string{}
	for _, p := range resp.Providers {
		byID[p.ID] = p.Env
		if p.Hint == "" {
			t.Errorf("%s 没有提示文案，用户不知道该去哪申请", p.ID)
		}
	}
	for _, want := range []string{"cloudflare", "alidns", "dnspod"} {
		if _, ok := byID[want]; !ok {
			t.Errorf("缺少常用的服务商 %s", want)
		}
	}
	if len(byID["cloudflare"]) == 0 || byID["cloudflare"][0] != "CLOUDFLARE_DNS_API_TOKEN" {
		t.Errorf("cloudflare 的必填变量不对: %v", byID["cloudflare"])
	}
}
