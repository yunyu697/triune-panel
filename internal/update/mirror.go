package update

// GitHub 下载加速镜像。
//
// 这些是**第三方公共服务**，随时可能限速、改域名或直接关站——所以列表只用来
// 「给个候选 + 测速」，绝不作为信任来源：不管走不走镜像，下载回来的二进制仍要
// 过可执行验证、配置校验与健康检查这三关（见 update.go 的可靠性约定）。
//
// 用法：镜像地址拼在原始 GitHub 地址前面，即
//
//	https://ghfast.top/https://github.com/owner/repo/releases/download/...
//
// 这是目前主流加速站的通用形式；个别站点要求别的形式，那就只能走自定义。

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Mirror 一个加速镜像。
// Base 为空串表示「官方地址」——它也在候选里，这样「自动选择最快」可以得出
// 「官方本来就最快，不用镜像」这个结论。
type Mirror struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Base string `json:"base"`
	Note string `json:"note,omitempty"`
}

// OfficialMirror 官方地址（不加速）。ID 固定为 official。
var OfficialMirror = Mirror{ID: "official", Name: "官方地址", Base: "", Note: "github.com 直连，默认选项"}

// Mirrors 预设镜像，按知名度大致排序（顺序只影响下拉列表展示）。
var Mirrors = []Mirror{
	{ID: "ghfast", Name: "ghfast.top", Base: "https://ghfast.top", Note: "老牌，速度稳定"},
	{ID: "ghproxy-net", Name: "ghproxy.net", Base: "https://ghproxy.net"},
	{ID: "ghproxy-cc", Name: "ghproxy.cc", Base: "https://ghproxy.cc"},
	{ID: "gh-proxy", Name: "gh-proxy.com", Base: "https://gh-proxy.com"},
	{ID: "mirror-ghproxy", Name: "mirror.ghproxy.com", Base: "https://mirror.ghproxy.com"},
	{ID: "llkk", Name: "gh.llkk.cc", Base: "https://gh.llkk.cc"},
	{ID: "moeyy", Name: "github.moeyy.xyz", Base: "https://github.moeyy.xyz"},
	{ID: "ghp-ci", Name: "ghp.ci", Base: "https://ghp.ci"},
	{ID: "ghps", Name: "ghps.cc", Base: "https://ghps.cc"},
	{ID: "ddlc", Name: "gh.ddlc.top", Base: "https://gh.ddlc.top"},
	{ID: "slink", Name: "slink.ltd", Base: "https://slink.ltd"},
	{ID: "cf-ghproxy", Name: "cf.ghproxy.cc", Base: "https://cf.ghproxy.cc"},
	{ID: "ghproxy-cfd", Name: "ghproxy.cfd", Base: "https://ghproxy.cfd"},
	{ID: "api-99988866", Name: "gh.api.99988866.xyz", Base: "https://gh.api.99988866.xyz"},
}

// MirrorCandidates 返回「官方 + 全部预设」，用于测速与下拉列表。
func MirrorCandidates() []Mirror {
	out := make([]Mirror, 0, len(Mirrors)+1)
	out = append(out, OfficialMirror)
	return append(out, Mirrors...)
}

// FindMirror 按 id 找预设；也接受直接给出 base 地址（自定义镜像）。
func FindMirror(idOrBase string) (Mirror, bool) {
	s := strings.TrimSpace(idOrBase)
	if s == "" {
		return OfficialMirror, true
	}
	for _, m := range MirrorCandidates() {
		if m.ID == s || (m.Base != "" && strings.EqualFold(strings.TrimRight(m.Base, "/"), strings.TrimRight(s, "/"))) {
			return m, true
		}
	}
	return Mirror{}, false
}

// NormalizeMirror 校验并规范化用户填的镜像前缀。
// 空串 = 官方地址。只接受 http/https，避免 javascript: / file: 之类的意外。
func NormalizeMirror(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		return "", fmt.Errorf("镜像地址必须以 http:// 或 https:// 开头: %s", s)
	}
	if strings.ContainsAny(s, " \t\r\n") {
		return "", fmt.Errorf("镜像地址不能包含空白字符")
	}
	return strings.TrimRight(s, "/"), nil
}

// probeNote 为什么必须拿真实下载地址来测：
// 这些加速站只转发 release / raw / archive 这几类路径，其它一律 403/404。
// 早先拿 github.com/robots.txt 当探针，结果 ghfast.top、gh-proxy.com 全部
// 报 403——它们其实好好的，只是不转发这个路径。所以探针目标由调用方给，
// 就是「你接下来真要下的那个文件」。
//
// ProbeResult 单个镜像的测速结果。
type ProbeResult struct {
	Mirror
	OK  bool   `json:"ok"`
	MS  int64  `json:"ms"`
	Err string `json:"error,omitempty"`
}

// probeOne 测单个镜像：对目标地址发一个只取 1 字节的 GET，量到首字节的时间。
// 用 GET + Range 而不是 HEAD——不少加速站不支持 HEAD，会误判成不可用。
func probeOne(ctx context.Context, m Mirror, target string, timeout time.Duration) ProbeResult {
	res := ProbeResult{Mirror: m}
	url := target
	if m.Base != "" {
		url = m.Base + "/" + target
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, "GET", url, nil)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	req.Header.Set("Range", "bytes=0-0")
	req.Header.Set("User-Agent", "triune-panel")
	client := &http.Client{
		// 跟跳转：有些站会 302 到 CDN，跟过去才算真实可用。
		// 限 5 跳，防止被绕进广告页的死循环。
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		res.MS = time.Since(start).Milliseconds()
		res.Err = shortErr(err)
		return res
	}
	defer resp.Body.Close()
	res.MS = time.Since(start).Milliseconds()
	// 200 = 正常返回；206 = 支持 Range（我们要的就是这个）。
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent {
		res.OK = true
		return res
	}
	res.Err = fmt.Sprintf("HTTP %d", resp.StatusCode)
	return res
}

func shortErr(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 && len(s)-i < 60 {
		return s[i+2:]
	}
	if len(s) > 80 {
		return s[:80]
	}
	return s
}

// ProbeMirrors 并发测速全部候选，按「可用优先、延迟升序」返回。
// target 必须是完整的 https://github.com/... 下载地址（见 probeNote）。
// 慢的镜像不剔除（用户可能就想用它），只是排在后面。
func ProbeMirrors(ctx context.Context, target string, timeout time.Duration) []ProbeResult {
	cands := MirrorCandidates()
	out := make([]ProbeResult, len(cands))
	var wg sync.WaitGroup
	for i, m := range cands {
		wg.Add(1)
		go func(i int, m Mirror) {
			defer wg.Done()
			out[i] = probeOne(ctx, m, target, timeout)
		}(i, m)
	}
	wg.Wait()
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].OK != out[j].OK {
			return out[i].OK // 可用的排前面
		}
		if out[i].OK && out[i].MS != out[j].MS {
			return out[i].MS < out[j].MS
		}
		return out[i].ID < out[j].ID
	})
	return out
}
