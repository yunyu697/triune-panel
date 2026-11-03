package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ==================== 规则集下载源（镜像） ====================
//
// 与 mirror.go 的区别（两个「镜像」不是一回事，别合并）：
//   mirror.go     面板**自己**下载二进制 / 组件时用的加速站，服务于这台服务器；
//   rulemirror.go 生成给**客户端**的配置里，规则集地址该怎么写，服务于用户家里的
//                 手机和路由器——那台机器通常连不上 raw.githubusercontent.com，
//                 而这台服务器往往能直连。两者可用性完全不同，不能共用一份列表。

// ruleMirror 规则集下载源。
//
// 为什么需要它：面板生成的是**客户端**配置，规则集由客户端自己去下载。
// raw.githubusercontent.com 在部分网络下不可达，而规则集拉不到时内核只丢一句
// 「initial rule-set error」/「rule-provider load failed」，表现出来的却是
// 「节点连上了但不能上网」——用户根本想不到问题不在节点而在规则集。
//
// 内置表只收**实测内容一致**的源。2026-11-03 用 geosite/cn.mrs 逐个比对
// sha256，结果：
//
//	gh-proxy.com / gh-proxy.org / ghproxy.net / jsDelivr → 与官方字节一致 ✅
//	ghfast.top                                           → HTTP 403 ❌
//	ghproxy.cn / gh-proxy.net / ghproxy.link             → HTTP 200 但内容是
//	                                                       HTML 挑战页 ❌
//	gh.ddlc.top                                          → HTTP 429 ❌
//
// 后一类正是这功能里最容易骗人的地方：浏览器/curl 看到 200 就以为好了，
// 内核拿到 HTML 只会说格式不对。所以「是否可用」必须看内容，不能看状态码——
// 这也是 checkRuleMirror 那个探测接口存在的理由。
type ruleMirror struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`   // official | prefix | jsdelivr
	Prefix string `json:"prefix"` // kind=prefix 时拼在原地址前面
	Note   string `json:"note"`
}

var ruleMirrors = []ruleMirror{
	{ID: MirrorOfficial, Name: "官方（GitHub 直连）", Kind: "official",
		Note: "raw.githubusercontent.com。境外主机或客户端已能直连 GitHub 时最快，也是默认值"},
	{ID: "gh-proxy", Name: "gh-proxy.com", Kind: "prefix", Prefix: "https://gh-proxy.com/",
		Note: "实测内容与官方一致。前缀型：原地址整串拼在前面"},
	{ID: "gh-proxy-org", Name: "gh-proxy.org", Kind: "prefix", Prefix: "https://gh-proxy.org/",
		Note: "实测内容与官方一致"},
	{ID: "ghproxy-net", Name: "ghproxy.net", Kind: "prefix", Prefix: "https://ghproxy.net/",
		Note: "实测内容与官方一致"},
	{ID: "jsdelivr", Name: "jsDelivr CDN", Kind: "jsdelivr",
		Note: "把 raw.githubusercontent.com/… 改写成 cdn.jsdelivr.net/gh/…，走公共 CDN"},
	{ID: "custom", Name: "自定义前缀", Kind: "prefix",
		Note: "填 https:// 开头的前缀，会拼在原地址前面；建议先点「测试」确认拿到的是文件而不是网页"},
}

// MirrorOfficial 直连官方源（默认值）。
const MirrorOfficial = "official"

// MirrorSetting / MirrorPrefixSetting 记住用户选的下载源，生成页下次打开沿用。
const (
	MirrorSetting       = "ruleset_mirror"
	MirrorPrefixSetting = "ruleset_mirror_prefix"
)

// ruleMirrorSel 一次生成所用的下载源（ID + 自定义前缀）。
type ruleMirrorSel struct {
	ID     string
	Prefix string
}

func mirrorByID(id string) (ruleMirror, bool) {
	for _, m := range ruleMirrors {
		if m.ID == id {
			return m, true
		}
	}
	return ruleMirror{}, false
}

// normalizeMirrorPrefix 校验自定义前缀。
//
// 只接受 https://：http 会被中间人改写规则集内容（那等于把分流结果交给别人），
// 其它 scheme（file:// / javascript:）拼出来根本不是可下载地址。返回带尾斜杠
// 的规范形式，拼接时直接相加。
func normalizeMirrorPrefix(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 200 {
		return "", false
	}
	if !strings.HasPrefix(strings.ToLower(s), "https://") {
		return "", false
	}
	// 引号/空白会把生成的 YAML·JSON 撑破，一律不收
	if strings.ContainsAny(s, " \t\r\n\"'") {
		return "", false
	}
	if !strings.HasSuffix(s, "/") {
		s += "/"
	}
	return s, true
}

// jsdelivrURL 把 raw.githubusercontent.com 地址改写成 jsDelivr 的 gh 形式：
//
//	raw.githubusercontent.com/{owner}/{repo}/{branch}/{path}
//	→ cdn.jsdelivr.net/gh/{owner}/{repo}@{branch}/{path}
func jsdelivrURL(u string) (string, bool) {
	const host = "https://raw.githubusercontent.com/"
	if !strings.HasPrefix(u, host) {
		return "", false
	}
	parts := strings.SplitN(u[len(host):], "/", 4)
	if len(parts) < 4 || parts[0] == "" || parts[1] == "" || parts[2] == "" || parts[3] == "" {
		return "", false
	}
	return "https://cdn.jsdelivr.net/gh/" + parts[0] + "/" + parts[1] + "@" + parts[2] + "/" + parts[3], true
}

// apply 把下载源应用到一条规则集地址上。
//
// 第二个返回值表示「这条地址是否真的被改写」——jsDelivr 只能改 raw 地址，
// 自定义规则集里指向别的站点的 URL 会原样返回，调用方要据此提示一次
// （而不是默默生成一份看起来一样、实际没走镜像的配置）。
func (s ruleMirrorSel) apply(u string) (string, bool) {
	if u == "" || s.ID == "" || s.ID == MirrorOfficial {
		return u, false
	}
	if s.ID == "custom" {
		p, ok := normalizeMirrorPrefix(s.Prefix)
		if !ok {
			return u, false
		}
		return p + u, true
	}
	m, ok := mirrorByID(s.ID)
	if !ok {
		return u, false
	}
	switch m.Kind {
	case "prefix":
		return m.Prefix + u, true
	case "jsdelivr":
		if v, ok := jsdelivrURL(u); ok {
			return v, true
		}
		return u, false
	}
	return u, false
}

// direct 判断这条下载源本身是否仍然直连 GitHub（用于生成时的提示文案）。
func (s ruleMirrorSel) direct() bool {
	return s.ID == "" || s.ID == MirrorOfficial
}

// mirrorLabel 给用户看的短名（写进配置头注释与生成结果的提示）。
func (s ruleMirrorSel) label() string {
	if s.direct() {
		return "官方"
	}
	if s.ID == "custom" {
		if p, ok := normalizeMirrorPrefix(s.Prefix); ok {
			return p
		}
		return "自定义"
	}
	if m, ok := mirrorByID(s.ID); ok {
		return m.Name
	}
	return s.ID
}

// resolveMirror 把请求里的 (id, prefix) 归一成一个可用的选择。
//
// 任何不合法的输入都**退回官方**并给出 warning，而不是让请求失败：生成配置
// 是只读操作，为一个人填错的前缀把整份配置拒掉没有意义，用户看到提示再改就是了。
func resolveMirror(id, prefix string) (ruleMirrorSel, string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return ruleMirrorSel{ID: MirrorOfficial}, ""
	}
	if id == "custom" {
		p, ok := normalizeMirrorPrefix(prefix)
		if !ok {
			return ruleMirrorSel{ID: MirrorOfficial},
				"自定义规则集镜像前缀不合规（须为 https:// 开头、不含空格与引号），已退回官方源"
		}
		return ruleMirrorSel{ID: "custom", Prefix: p}, ""
	}
	if _, ok := mirrorByID(id); !ok {
		return ruleMirrorSel{ID: MirrorOfficial}, "未知的规则集下载源「" + id + "」，已退回官方源"
	}
	return ruleMirrorSel{ID: id}, ""
}

// ==================== 下载源可用性探测 ====================

// mirrorProbeURL 探测用的固定目标：取体积最小的一个真实规则集。
// 用它而不是随便找个 GitHub 文件，是因为要验的正是「这个源能不能给出
// 内核认得的那份二进制」——换成 README 之类的小文本，镜像就算正常代理
// 也证明不了规则集能拉。
func mirrorProbeURL() string { return mihomoSetURL("geosite", "cn") }

// checkRuleMirror 实际拉一次规则集，判断这个下载源能不能用。
//
// 判据是**内容**不是状态码：ghproxy.cn / gh-proxy.net / ghproxy.link 都会
// 回 200 + HTML 挑战页，浏览器上看完全正常，内核拿到只会说「invalid mrs」。
// 所以这里读前 64 KiB：首字节是 '<' 就判失败，并直接把这件事说清楚。
type mirrorProbe struct {
	OK      bool   `json:"ok"`
	Status  int    `json:"status"`
	Bytes   int    `json:"bytes"`   // 实际读到的字节数（最多 64 KiB）
	MS      int64  `json:"ms"`      // 耗时
	Magic   string `json:"magic"`   // 前 4 字节的十六进制
	URL     string `json:"url"`     // 实际探测的地址，便于用户自己复核
	Detail  string `json:"detail"`  // 结论
	Warning string `json:"warning"` // 非致命提示
}

func (a *Server) handleCheckMirror(w http.ResponseWriter, r *http.Request) {
	var q mirrorCheckReq
	if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
		fail(w, 400, "bad request")
		return
	}
	// 未知 ID 直接报错，而不是像生成时那样静默退回官方——用户点的是
	// 「测试这个源」，悄悄测了别的源会让人以为它好使。
	if q.Mirror != "" && q.Mirror != MirrorOfficial && q.Mirror != "custom" {
		if _, ok := mirrorByID(q.Mirror); !ok {
			fail(w, 400, "未知的下载源: "+q.Mirror)
			return
		}
	}
	sel := ruleMirrorSel{ID: q.Mirror, Prefix: q.Prefix}
	if sel.ID == "custom" {
		p, ok := normalizeMirrorPrefix(q.Prefix)
		if !ok {
			fail(w, 400, "前缀不合规：须为 https:// 开头，不含空格与引号")
			return
		}
		sel.Prefix = p
	}
	target, _ := sel.apply(mirrorProbeURL())
	if target == "" {
		fail(w, 400, "无法构造探测地址")
		return
	}
	// 面板会替用户去请求这个地址，先挡住指向内网的写法，别把它变成探针。
	if err := guardOutboundHost(target); err != nil {
		fail(w, 400, err.Error())
		return
	}

	p := probeRuleMirror(r.Context(), target)
	writeJSON(w, 200, p)
}

// probeRuleMirror 拉一次，最多读 64 KiB。
func probeRuleMirror(ctx context.Context, u string) mirrorProbe {
	p := mirrorProbe{URL: u}
	start := time.Now()

	cl := &http.Client{
		Timeout: 8 * time.Second,
		// 跟跳转（有的站会 302 到 CDN，不跟就误判成不可用），但每一跳都要
		// 重新过一遍内网检查——否则一个 `Location: http://169.254.169.254/`
		// 就能把面板变成云元数据读取器。
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return http.ErrUseLastResponse
			}
			return guardOutboundHost(req.URL.String())
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		p.Detail = "地址无法解析：" + err.Error()
		p.MS = time.Since(start).Milliseconds()
		return p
	}
	// 有的镜像会按 UA 决定给文件还是给网页，用内核自己的 UA 最接近真实情况
	req.Header.Set("User-Agent", "mihomo/1.19.0")
	req.Header.Set("Accept", "*/*")

	resp, err := cl.Do(req)
	p.MS = time.Since(start).Milliseconds()
	if err != nil {
		p.Detail = "连接失败：" + trimErr(err.Error())
		return p
	}
	defer resp.Body.Close()
	p.Status = resp.StatusCode

	buf := make([]byte, 64<<10)
	// ReadFull 在文件小于缓冲区时返回 ErrUnexpectedEOF，但 n 是实际读到的
	// 字节数、数据有效——规则集一般几百 KB，这里只取前 64 KiB 判定就够。
	n, _ := io.ReadFull(resp.Body, buf)
	p.Bytes = n

	if resp.StatusCode != 200 {
		p.Detail = fmt.Sprintf("HTTP %d，这个源当前不可用", resp.StatusCode)
		if resp.StatusCode == 403 {
			p.Warning = "403 常见于镜像拒绝代理 raw 文件（ghfast.top 就是这样）"
		}
		if resp.StatusCode == 429 {
			p.Warning = "429 是镜像在限流，过一会儿再试或换个源"
		}
		return p
	}
	if n == 0 {
		p.Detail = "HTTP 200 但响应体是空的"
		return p
	}
	p.Magic = fmt.Sprintf("%02x%02x%02x%02x", buf[0], buf[1], buf[2], buf[3])
	if buf[0] == '<' {
		p.Detail = "拿到的是一段网页（响应以 '<' 开头），不是规则集文件：内核会报格式错误"
		p.Warning = "这类源通常返回「人机校验」页面，请换一个源"
		return p
	}
	if n < 256 {
		p.Detail = fmt.Sprintf("只读到 %d 字节，太小，多半不是完整的规则集", n)
		return p
	}
	p.OK = true
	p.Detail = "内容正常"
	if p.Magic == "28b52ffd" {
		p.Detail = "内容正常（zstd 帧头，mrs/srs 的标准封装）"
	}
	return p
}

func trimErr(s string) string {
	if len(s) > 120 {
		return s[:120] + "…"
	}
	return s
}

// guardOutboundHost 拒绝把面板当作内网探针。
//
// 前缀是用户填的，`https://127.0.0.1/` 这种写法会让面板去请求自己所在的
// 内网地址并回显结果。这里只看主机名：域名先解析，落在私有网段/环回/
// 链路本地就拒。
func guardOutboundHost(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("地址无法解析")
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if isPrivateIP(ip) {
			return errors.New("不允许指向内网地址（" + host + "）")
		}
		return nil
	}
	ips, err := net.DefaultResolver.LookupIP(context.Background(), "ip4", host)
	if err != nil {
		// 解析不了就让下面的实际请求去报错，这里不拦
		return nil
	}
	for _, ip := range ips {
		if isPrivateIP(ip) {
			return errors.New("不允许指向内网地址（" + host + " → " + ip.String() + "）")
		}
	}
	return nil
}

func isPrivateIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

type mirrorCheckReq struct {
	Mirror string `json:"mirror"`
	Prefix string `json:"prefix"`
}
