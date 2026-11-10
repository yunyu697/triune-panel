package certmgr

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/providers/dns/alidns"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/providers/dns/dnspod"
	"github.com/go-acme/lego/v4/providers/dns/duckdns"
	"github.com/go-acme/lego/v4/providers/dns/godaddy"
	"github.com/go-acme/lego/v4/providers/dns/namesilo"
)

// 设置键：验证方式与凭据。
//
// 空 provider = 沿用 HTTP-01（老行为，不需要任何凭据）。
const (
	SettingDNSProvider = "acme_dns_provider"
	SettingDNSEnv      = "acme_dns_env"
)

// MaskedSecret 凭据在接口里的占位值。
//
// 凭据是**第三方账号的钥匙**（一个 Cloudflare token 能改整个域名的解析），
// 和面板自己的 stats_secret 不是一个量级：拿到它的人可以把域名指向别处，
// 再签一张证书冒充这个站。所以这里破一次「设置项照常回显」的惯例，只回
// 这个占位；前端把占位原样提交回来表示「没改过」。
const MaskedSecret = "********"

// DNSProviderInfo 一个可选的服务商。
//
// 只收录**编译进来了的**：lego 有近两百个 provider，全量引入会把二进制
// 撑到几十 MB（每个都拖着自己的云厂商 SDK），而 99% 的用法集中在这几个。
type DNSProviderInfo struct {
	// ID 与 lego 的包名一致，也用于设置值。
	ID string `json:"id"`
	// Label 给人看的名字。
	Label string `json:"label"`
	// Env 必须提供的环境变量（第一个是「主凭据」，前端照它渲染提示）。
	Env []string `json:"env"`
	// Prefixes 允许出现在凭据框里的键前缀。
	//
	// 这一条是安全边界而不是风格问题：凭据框最终会进 os.Setenv，
	// 若放任任意键，用户手滑写一个 PATH=/usr/bin 就能让内核下载、
	// 命令执行一起失灵，而报错会以「找不到 go」这种面貌出现在别处。
	Prefixes []string `json:"-"`
	// Hint 一段话：去哪申请、给什么权限。
	Hint string `json:"hint"`
	// Example 凭据框的示例（含占位符，不是真实凭据）。
	Example string `json:"example"`
}

// dnsProviderTable 顺序即 UI 下拉顺序：先放国内用户量最大的两家 + 通用 CDN。
var dnsProviderTable = []DNSProviderInfo{
	{
		ID: "cloudflare", Label: "Cloudflare（小黄云）",
		Env:      []string{"CLOUDFLARE_DNS_API_TOKEN"},
		Prefixes: []string{"CLOUDFLARE_", "CF_"},
		Hint: "Cloudflare 控制台 → 我的个人资料 → API 令牌 → 创建令牌，用「编辑区域 DNS」模板，" +
			"区域选这一个域名即可。用这个令牌就不用填邮箱和 Global Key。",
		Example: "CLOUDFLARE_DNS_API_TOKEN=你的令牌",
	},
	{
		ID: "alidns", Label: "阿里云 DNS",
		Env:      []string{"ALICLOUD_ACCESS_KEY", "ALICLOUD_SECRET_KEY"},
		Prefixes: []string{"ALICLOUD_"},
		Hint: "RAM 控制台建一个子账号，只授「云解析 DNS」的 AliyunDNSFullAccess，" +
			"给它创建 AccessKey。用主账号的 AccessKey 等于把整个账号交出去，别这么干。",
		Example: "ALICLOUD_ACCESS_KEY=LTAI...\nALICLOUD_SECRET_KEY=...",
	},
	{
		ID: "dnspod", Label: "DNSPod / 腾讯云",
		Env:      []string{"DNSPOD_API_KEY"},
		Prefixes: []string{"DNSPOD_"},
		Hint: "DNSPod 控制台 → 用户中心 → 密钥管理 → 创建密钥，值是「ID,Token」两段，" +
			"中间那个逗号要一起填。腾讯云 DNSPod 的 API 密钥是同一套。",
		Example: "DNSPOD_API_KEY=123456,xxxxxxxxxxxxxxxx",
	},
	{
		ID: "godaddy", Label: "GoDaddy",
		Env:      []string{"GODADDY_API_KEY", "GODADDY_API_SECRET"},
		Prefixes: []string{"GODADDY_"},
		Hint:     "GoDaddy 开发者门户创建 API Key（生产环境），得到 Key 与 Secret 两个值。",
		Example:  "GODADDY_API_KEY=...\nGODADDY_API_SECRET=...",
	},
	{
		ID: "namesilo", Label: "NameSilo",
		Env:      []string{"NAMESILO_API_KEY"},
		Prefixes: []string{"NAMESILO_"},
		Hint:     "NameSilo → API Manager 生成 API Key（生成后要等几分钟才生效）。",
		Example:  "NAMESILO_API_KEY=...",
	},
	{
		ID: "duckdns", Label: "DuckDNS",
		Env:      []string{"DUCKDNS_TOKEN"},
		Prefixes: []string{"DUCKDNS_"},
		Hint:     "DuckDNS 首页登录后能看到 token。只能签 <名字>.duckdns.org 这类免费子域。",
		Example:  "DUCKDNS_TOKEN=xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx",
	},
}

// DNSProviders 供接口与前端渲染下拉用（副本，调用方改不到表）。
func DNSProviders() []DNSProviderInfo {
	out := make([]DNSProviderInfo, len(dnsProviderTable))
	copy(out, dnsProviderTable)
	return out
}

// LookupDNSProvider 按 ID 找服务商信息。
func LookupDNSProvider(id string) (DNSProviderInfo, bool) {
	for _, p := range dnsProviderTable {
		if p.ID == id {
			return p, true
		}
	}
	return DNSProviderInfo{}, false
}

// DNSProviderIDs 用于错误文案里列出可选值。
func DNSProviderIDs() []string {
	out := make([]string, 0, len(dnsProviderTable))
	for _, p := range dnsProviderTable {
		out = append(out, p.ID)
	}
	sort.Strings(out)
	return out
}

// newDNSProvider 构造 lego 的 DNS-01 provider。
//
// 各 provider 都从环境变量读凭据（lego 的约定），所以必须先 applyDNSEnv。
func newDNSProvider(id string) (challenge.Provider, error) {
	switch id {
	case "cloudflare":
		return cloudflare.NewDNSProvider()
	case "alidns":
		return alidns.NewDNSProvider()
	case "dnspod":
		return dnspod.NewDNSProvider()
	case "godaddy":
		return godaddy.NewDNSProvider()
	case "namesilo":
		return namesilo.NewDNSProvider()
	case "duckdns":
		return duckdns.NewDNSProvider()
	}
	return nil, fmt.Errorf("不认识的 DNS 服务商 %q（可选：%s）", id, strings.Join(DNSProviderIDs(), " / "))
}

// ParseDNSEnv 解析凭据框：每行 KEY=VALUE，# 开头与空行忽略。
//
// 只接受该服务商前缀的键：见 DNSProviderInfo.Prefixes 的说明。
func ParseDNSEnv(raw string, info DNSProviderInfo) (map[string]string, error) {
	out := map[string]string{}
	for i, line := range strings.Split(raw, "\n") {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		k, v, ok := strings.Cut(s, "=")
		if !ok {
			return nil, fmt.Errorf("第 %d 行缺少等号：应写成 KEY=VALUE", i+1)
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k == "" {
			return nil, fmt.Errorf("第 %d 行的键是空的", i+1)
		}
		if !hasAnyPrefix(k, info.Prefixes) {
			return nil, fmt.Errorf("第 %d 行的 %s 不是 %s 的凭据变量（应以 %s 开头）",
				i+1, k, info.Label, strings.Join(info.Prefixes, " 或 "))
		}
		if v == "" {
			return nil, fmt.Errorf("第 %d 行的 %s 没有值", i+1, k)
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("没有填凭据：至少需要 %s", strings.Join(info.Env, "、"))
	}
	for _, need := range info.Env {
		if _, ok := out[need]; !ok {
			return nil, fmt.Errorf("缺少 %s（%s 需要 %s）",
				need, info.Label, strings.Join(info.Env, " 与 "))
		}
	}
	return out, nil
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if p != "" && strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// applyDNSEnv 把凭据写进进程环境，返回还原函数。
//
// 为什么是环境变量而不是传结构体：lego 的 provider 只认环境变量（每个
// provider 都有一份 env 映射），没有统一的构造参数。写完必须还原，
// 否则「换了一个服务商」之后旧凭据还留在环境里，下次签发可能用错账号。
func applyDNSEnv(vals map[string]string) func() {
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	type prev struct {
		key string
		val string
		had bool
	}
	saved := make([]prev, 0, len(keys))
	for _, k := range keys {
		old, had := os.LookupEnv(k)
		saved = append(saved, prev{k, old, had})
		_ = os.Setenv(k, vals[k])
	}
	return func() {
		for _, s := range saved {
			if s.had {
				_ = os.Setenv(s.key, s.val)
				continue
			}
			_ = os.Unsetenv(s.key)
		}
	}
}
