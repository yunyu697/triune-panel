package certmgr

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"
)

// maxDomains 一张证书里最多写几个名字。
//
// 不是技术上限：Let's Encrypt 允许 100 个，但每多一个名字就多一次 DNS
// 记录操作，而 UI 里真正会用到的是「主域名 + 泛域名」两张。设个上限只为
// 挡住「把整个域名列表粘进来」这种误操作。
const maxDomains = 10

// ParseDomains 解析设置里的域名：逗号 / 分号 / 空白分隔，允许 *.example.com。
//
// 一张证书可以带多个名字（SAN），这样 example.com 与 *.example.com 能一次
// 签下来、一次续期——这正是泛域名场景的常态用法：只有 *.example.com 的话，
// 用户访问不带 www 的裸域名会报证书错误。
func ParseDomains(raw string) ([]string, error) {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ',', '，', ';', '；', ' ', '\t', '\n', '\r':
			return true
		}
		return false
	})
	out := make([]string, 0, len(fields))
	seen := map[string]bool{}
	for _, f := range fields {
		d := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(f), "."))
		if d == "" {
			continue
		}
		if msg := validateDomain(d); msg != "" {
			return nil, fmt.Errorf("%s：%s", d, msg)
		}
		if seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("没有填域名")
	}
	if len(out) > maxDomains {
		return nil, fmt.Errorf("一次最多 %d 个域名（现在有 %d 个）", maxDomains, len(out))
	}
	return out, nil
}

// validateDomain 单个名字的合法性，返回空串表示合法。
func validateDomain(d string) string {
	if net.ParseIP(d) != nil {
		return "这是 IP，不是域名；IP 证书需要走商业 CA，面板不支持"
	}
	base := d
	if strings.HasPrefix(d, "*.") {
		base = d[2:]
		if base == "" {
			return "通配符后面要跟域名，例如 *.example.com"
		}
	}
	if strings.Contains(base, "*") {
		return "通配符只能出现在最前面（*.example.com），不能写在中间"
	}
	if !strings.Contains(base, ".") {
		return "缺少点号，看起来不是公网域名"
	}
	for _, label := range strings.Split(base, ".") {
		if label == "" {
			return "有连续的点"
		}
		if len(label) > 63 {
			return "有一段超过 63 个字符"
		}
		if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "每一段不能以连字符开头或结尾"
		}
		for _, r := range label {
			ok := r == '-' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
			if !ok {
				return "含有非法字符（只允许字母、数字、连字符）"
			}
		}
	}
	if len(base) > 253 {
		return "域名太长"
	}
	return ""
}

// CertSlug 证书目录名：certs/<slug>/。
//
// 不能直接把域名拼进路径：泛域名里有 "*"，虽然 Linux 允许这种文件名，
// 但它在 shell 里必须转义（用户复制路径去改 nginx / 设入站时会踩到），
// 而且 `*` 出现在路径里对任何后续的 glob 都是隐患。所以：
//   - *.example.com → wildcard.example.com
//   - 其余原样（老用户的 certs/example.com/ 一个字节都不变）
func CertSlug(domains []string) string {
	if len(domains) == 0 {
		return "cert"
	}
	primary := strings.ToLower(strings.TrimSpace(domains[0]))
	if strings.HasPrefix(primary, "*.") {
		primary = "wildcard." + primary[2:]
	}
	var b strings.Builder
	for _, r := range primary {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	slug := b.String()
	// 折叠连续的点、去掉首尾的点与连字符："" / "." / ".." 都会被 filepath.Join 吃掉一层
	for strings.Contains(slug, "..") {
		slug = strings.ReplaceAll(slug, "..", ".")
	}
	slug = strings.Trim(slug, ".-")
	if slug == "" || slug == "." {
		return "cert"
	}
	return slug
}

// CertDirFor 证书落盘目录。
func CertDirFor(dataDir string, domains []string) string {
	return filepath.Join(dataDir, "certs", CertSlug(domains))
}

// HasWildcard 域名列表里是否有泛域名（决定能不能走 HTTP-01）。
func HasWildcard(domains []string) bool {
	for _, d := range domains {
		if strings.HasPrefix(d, "*.") {
			return true
		}
	}
	return false
}
