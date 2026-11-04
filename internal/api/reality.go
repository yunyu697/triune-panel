package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"triune/internal/core"
	"triune/internal/model"
)

// ==================== REALITY 的短 ID 与握手目标 ====================

// validateRealitySettings 校验 REALITY 入站里「填错了不会报错、只会连不上」的两个字段。
//
// shortIds：xray 只接受十六进制、最长 16 个字符（8 字节）、且必须成对。
// 填成 `ab1` 或 `xyz` 时 xray 的报错埋在启动日志里，面板这边看起来「已启用」，
// 客户端则是连不上且原因不明。空串是合法的（表示允许不带短 ID 的客户端）。
//
// spiderX：只在客户端侧生效（面板导出链接时写 spx=），必须是路径形式。
// 少了开头的斜杠时客户端会拿到一个语义不明的值，抓包才看得出来。
func validateRealitySettings(settings string) string {
	ids := core.ShortIDs(settings)
	for i, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if len(id) > 16 {
			return fmt.Sprintf("shortIds 第 %d 个「%s」有 %d 个字符：REALITY 最多 16 个（8 字节），"+
				"多出来的部分会被忽略，等于换了一个短 ID", i+1, id, len(id))
		}
		if len(id)%2 != 0 {
			return fmt.Sprintf("shortIds 第 %d 个「%s」长度是 %d：必须是偶数（2/4/…/16），"+
				"它本质是十六进制字节串，奇数长度内核无法解析", i+1, id, len(id))
		}
		for _, r := range id {
			if !isHexRune(r) {
				return fmt.Sprintf("shortIds 第 %d 个「%s」含非十六进制字符：只能是 0-9 与 a-f", i+1, id)
			}
		}
	}
	if sx := strings.TrimSpace(core.Field(settings, "spider_x")); sx != "" && !strings.HasPrefix(sx, "/") {
		return fmt.Sprintf("spiderX「%s」必须以 / 开头（它是客户端爬取的路径，例如 / 或 /x）", sx)
	}
	return ""
}

func isHexRune(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

// normalizeRealitySettings 让存下来的 pbk 永远等于 private_key 推导出的公钥。
//
// 为什么要在保存期改：公钥是私钥的纯函数，两者一旦不一致，服务端用的是私钥、
// 客户端用的是公钥，**每一次握手都会被服务端判成「不是自己人」**，于是转给
// dest，客户端收到的是真证书，报
//
//	REALITY: received real certificate (potential MITM or redirection)
//
// 而服务端日志一切正常（连接被正常接受、正常回落），两边都看不出问题在哪。
//
// 导出侧早就不采信这个字段了（见 completeRealityNode），但**表单里那一栏是给人
// 看的**：用户会点「复制」把公钥拿去手填 v2rayN。只要它可能过时，就还有一条
// 通往这个报错的路。与其到处兜底，不如在入口把它归一化掉。
func normalizeRealitySettings(in *model.Inbound) {
	if in.TLS != "reality" {
		return
	}
	priv := core.Field(in.Settings, "private_key")
	if priv == "" {
		return
	}
	pub, err := realityPubFromPriv(priv)
	if err != nil || pub == "" {
		// 私钥本身解不开时不动它：交给渲染/校验去报，别在这里把配置改坏
		return
	}
	if core.Field(in.Settings, "pbk") == pub {
		return
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(in.Settings), &m); err != nil {
		return
	}
	m["pbk"] = pub
	if b, err := json.Marshal(m); err == nil {
		in.Settings = string(b)
	}
}

// ---- 握手目标（dest）择优 ----
//
// 为什么需要实测：REALITY 的 dest 不是「随便填个大站」就行，三条都得成立——
//
//	1) 这台服务器真能连上、且握手够快。客户端每次握手都要靠服务端把连接转给
//	   dest 来完成，dest 慢 = 每个客户端每次握手都慢，而这个延迟只取决于
//	   **服务器到目标**的那段网络，在家里测是测不出来的；
//	2) 支持 TLS 1.3。REALITY 只在 1.3 上「偷证书」，目标只给 1.2 就直接不可用；
//	3) 证书与 serverNames 对得上。对不上时正常连接看不出问题，但一旦走到回落
//	   （真实访客 / 主动探测），对方拿到的是一个不属于该域名的证书——伪装的
//	   意义就没了。
//
// 这三条都没法用眼睛判断，所以给一个接口真握手几次，把结论摊开让用户自己挑。
//
// 与「入站自检」的分工：自检探的是**本机上已经跑起来的入站**（127.0.0.1:端口），
// 用来判断「这条入站活着吗」；这里探的是**公网上的候选目标**，用来判断
// 「这个 dest 值不值得填」。两者都会握手，但对象完全不同。

const (
	destProbeMaxCandidates = 16              // 一次最多测这么多个，避免把面板拖住
	destProbeAttempts      = 3               // 每个目标握手 3 次取中位数，单次抖动不算数
	destProbeConcurrency   = 6               // 并发握手数
	destProbeDialTimeout   = 4 * time.Second // 单次握手
	destProbeTotalTimeout  = 14 * time.Second
)

// destCandidate 一个候选握手目标。
type destCandidate struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	SNI  string `json:"sni"` // 留空 = 用 Host
}

// realityDestResult 一个目标实测后的结论。
type realityDestResult struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	SNI      string `json:"sni"`
	OK       bool   `json:"ok"`   // TLS 1.3 握手成功 = 可用
	Best     bool   `json:"best"` // 综合排序第一，界面标「推荐」
	MS       int64  `json:"ms"`   // 握手耗时中位数
	TLS      string `json:"tls"`  // "1.3" / "1.2"
	ALPN     string `json:"alpn"` // 谈成的 ALPN
	H2       bool   `json:"h2"`
	CertOK   bool   `json:"cert_ok"` // 证书链 + 域名都对得上
	Issuer   string `json:"issuer"`
	NotAfter string `json:"not_after"`
	DaysLeft int    `json:"days_left"`
	Detail   string `json:"detail"`
	Warning  string `json:"warning"`
}

// handleRealityDestProbe POST /api/tools/reality-dest
//
//	{"candidates":[{"host":"www.apple.com","port":443}]}
//
// 会替用户去连外网，和「下载源自检」同一档（write）。私网地址一律拒绝：
// 这个接口的入参来自浏览器，不设防就成了内网端口扫描器。
func (a *Server) handleRealityDestProbe(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Candidates []destCandidate `json:"candidates"`
	}
	if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
		fail(w, 400, "bad request")
		return
	}
	cands, msg := normalizeDestCandidates(q.Candidates)
	if msg != "" {
		fail(w, 400, msg)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), destProbeTotalTimeout)
	defer cancel()

	results := probeRealityDestAll(ctx, cands, nil)
	best := ""
	for _, res := range results {
		if res.Best {
			best = net.JoinHostPort(res.Host, strconv.Itoa(res.Port))
			break
		}
	}
	writeJSON(w, 200, map[string]any{
		"results": results,
		"best":    best,
		"timeout": destProbeTotalTimeout.Milliseconds(),
	})
}

// ---- 同站 SNI 收集 ----
//
// REALITY 只接受 serverNames 里列出的 SNI：客户端发一个不在名单里的名字，
// 服务端直接拒握手，而两边的日志都看不出所以然。可 serverNames 到底该填什么，
// 取决于 dest 那张证书覆盖了谁——只能去问 dest 自己。
//
// 所以这里不猜：先按目标自己的名字握一次，把证书里的域名清单读出来，
// 再逐个真握手验证（TLS 1.3 + 证书对得上），只返回验过的。
//
// 「证书覆盖」和「域名真实存在」是两件事，REALITY 只需要前者：通配符证书
// （*.example.com）下任何单级子域都能当 SNI。但我们仍然逐个实测——有些站点
// 按 SNI 分流，不存在的子域会拿到另一张证书，那种填进去就会露馅。

const (
	sniCollectMaxCandidates = 12
	sniCollectTotalTimeout  = 18 * time.Second
)

// sniWildcardPrefixes 通配符展开时试的几个前缀。不是「猜哪个域名存在」，
// 而是给实测提供候选：验不过的不会出现在结果里。
var sniWildcardPrefixes = []string{"www", "api", "cdn"}

// realityCertInfo dest 证书里「能当 SNI 的名字」清单。
type realityCertInfo struct {
	Issuer   string   `json:"issuer"`
	NotAfter string   `json:"not_after"`
	DaysLeft int      `json:"days_left"`
	Names    []string `json:"names"`     // 写明的具体域名（可直接当 SNI）
	Wildcard []string `json:"wildcards"` // *.example.com，需要展开成具体名字
}

// realitySNIResult 一个验过的 SNI。
type realitySNIResult struct {
	SNI    string `json:"sni"`
	OK     bool   `json:"ok"`
	MS     int64  `json:"ms"`
	Source string `json:"source"` // 目标本身 / 证书 SAN / 通配符展开
	Detail string `json:"detail"` // 没验过时的原因
}

// sniSeed 一个待验证的候选 SNI 及其来源（来源只是给人看的解释）。
type sniSeed struct {
	Name   string
	Source string
}

// realitySNICollectResult 收集接口的完整结论。
type realitySNICollectResult struct {
	Host     string             `json:"host"`
	Port     int                `json:"port"`
	SNIs     []realitySNIResult `json:"snis"`
	Rejected []realitySNIResult `json:"rejected"`
	Cert     realityCertInfo    `json:"cert"`
	Tried    int                `json:"tried"`
	Note     string             `json:"note"`
}

// handleRealitySNICollect POST /api/tools/reality-sni
//
//	{"host":"www.amd.com","port":443}
//
// 与 reality-dest 同一档（write）：都会替用户连外网。
func (a *Server) handleRealitySNICollect(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
		fail(w, 400, "bad request")
		return
	}
	cands, msg := normalizeDestCandidates([]destCandidate{{Host: q.Host, Port: q.Port}})
	if msg != "" {
		fail(w, 400, msg)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), sniCollectTotalTimeout)
	defer cancel()

	res, errMsg := collectRealitySNI(ctx, cands[0], nil)
	if errMsg != "" {
		fail(w, 502, errMsg)
		return
	}
	writeJSON(w, 200, map[string]any{
		"host":     res.Host,
		"port":     res.Port,
		"snis":     res.SNIs,
		"rejected": res.Rejected,
		"cert":     res.Cert,
		"tried":    res.Tried,
		"note":     res.Note,
		"timeout":  sniCollectTotalTimeout.Milliseconds(),
	})
}

// collectRealitySNI 读 dest 的证书、展开候选、逐个真握手验证。
//
// 第二个返回值是给用户看的失败原因（非空时 SNIs 无意义）。拆出来是为了
// 能对着本地 TLS 服务测——handler 里的 guardDestHost 不允许 127.0.0.1，
// roots 也就是为此留的口子（生产传 nil = 系统根证书）。
func collectRealitySNI(ctx context.Context, base destCandidate, roots *x509.CertPool) (realitySNICollectResult, string) {
	out := realitySNICollectResult{
		Host: base.Host, Port: base.Port,
		// 空切片而不是 nil：JSON 里要出 []，前端才不用到处兜 null
		SNIs:     []realitySNIResult{},
		Rejected: []realitySNIResult{},
	}

	// 第一次握手：SNI 就用目标自己的名字，拿到的证书就是这个 dest 的
	// 「合法域名清单」。这一次也顺带证明目标本身可不可用。
	h := handshakeDest(ctx, base, roots)
	if h.err != nil {
		detail, warning := describeDestError(h.err, base)
		return out, detail + "。" + warning
	}
	if h.state.Version != tls.VersionTLS13 {
		return out, fmt.Sprintf("目标只协商到 TLS %s：REALITY 必须在 TLS 1.3 上工作，"+
			"这个目标不能当 dest，也就无所谓 SNI", tlsVersionName(h.state.Version))
	}
	leaf := h.leaf()
	if leaf == nil {
		return out, "目标没有提供证书：读不到它的域名清单"
	}
	cert := certInfoOf(leaf)
	out.Cert = cert
	// 「目标本身」这一项用的是握手时发的那个名字（base.SNI，缺省即 host）：
	// 用户指定了 SNI 时，名单的起点就是它，而不是拨号用的那个地址。
	primary := destSNI(base)
	seeds, total := sniSeeds(primary, cert)

	results := make([]realityDestResult, 0, len(seeds))
	// 目标自己已经在上面的握手验过了，不用再握一次。
	if h.certErr == nil {
		results = append(results, realityDestResult{
			Host: base.Host, Port: base.Port, SNI: primary,
			OK: true, TLS: "1.3", ALPN: h.state.NegotiatedProtocol,
			H2: h.state.NegotiatedProtocol == "h2", CertOK: true,
			MS: h.took.Milliseconds(), Detail: "TLS 1.3 握手成功",
			Issuer: cert.Issuer, NotAfter: cert.NotAfter, DaysLeft: cert.DaysLeft,
		})
	} else {
		out.Rejected = append(out.Rejected, realitySNIResult{
			SNI: primary, Source: "目标本身",
			Detail: "证书与 SNI 对不上（" + trimErr(h.certErr.Error()) + "）",
		})
	}

	rest := make([]destCandidate, 0, len(seeds))
	for _, s := range seeds {
		if strings.EqualFold(s.Name, primary) {
			continue
		}
		rest = append(rest, destCandidate{Host: base.Host, Port: base.Port, SNI: s.Name})
	}
	if len(rest) > 0 {
		// 这里要的是「行不行」而不是「多快」，握一次就够。
		results = append(results, probeRealityDestAllN(ctx, rest, roots, 1)...)
	}
	sortDestResults(results)

	srcOf := make(map[string]string, len(seeds))
	for _, s := range seeds {
		srcOf[strings.ToLower(s.Name)] = s.Source
	}
	out.SNIs = make([]realitySNIResult, 0, len(results))
	for _, r := range results {
		if r.OK && r.CertOK {
			out.SNIs = append(out.SNIs, realitySNIResult{
				SNI: r.SNI, OK: true, MS: r.MS,
				Source: srcOf[strings.ToLower(r.SNI)],
			})
			continue
		}
		out.Rejected = append(out.Rejected, realitySNIResult{
			SNI: r.SNI, Source: srcOf[strings.ToLower(r.SNI)],
			Detail: sniRejectReason(r),
		})
	}
	// 试过的名字个数就是候选个数：可用与被拒是同一批候选的两种结局，
	// 用两边相加会把自己重复算一次。
	out.Tried = len(seeds)

	var notes []string
	switch {
	case len(out.SNIs) == 0:
		notes = append(notes, "一个都没验过：这个目标可能按 SNI 分流，或证书里没有可用的名字。"+
			"先确认目标本身能不能当 dest。")
	case len(out.SNIs) == 1 && total == 1:
		// 只有「证书里本来就没别的名字」才这么说。证书写了两个、只验过一个时
		// 别下这个结论——被拒的那个已经在 rejected 里说明原因了。
		notes = append(notes, "这张证书只写了这一个域名，没有别的可选。")
	}
	if total > len(seeds) {
		notes = append(notes, fmt.Sprintf("证书里一共 %d 个可用名字，这里实测了前 %d 个——"+
			"填两三个就够轮换了，不必全要。", total, len(seeds)))
	}
	out.Note = strings.Join(notes, " ")
	return out, ""
}

// sniRejectReason 把「没验过」压成一句话。
func sniRejectReason(res realityDestResult) string {
	if !res.OK {
		if res.Detail != "" {
			return res.Detail
		}
		return "握手失败"
	}
	return "证书与 SNI 对不上"
}

// certInfoOf 拆出证书里的具体域名与通配符。
//
// 顺序保持证书里的原样（第一项通常是主域名），不排序——用户看到的第一个
// 往往就是他该选的那个。
func certInfoOf(leaf *x509.Certificate) realityCertInfo {
	info := realityCertInfo{
		Issuer:   issuerName(leaf),
		NotAfter: leaf.NotAfter.Format("2006-01-02"),
		DaysLeft: int(time.Until(leaf.NotAfter).Hours() / 24),
		Names:    []string{},
		Wildcard: []string{},
	}
	seen := map[string]bool{}
	add := func(n string) {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == "" || seen[n] {
			return
		}
		seen[n] = true
		if strings.HasPrefix(n, "*.") {
			info.Wildcard = append(info.Wildcard, n)
			return
		}
		info.Names = append(info.Names, n)
	}
	for _, n := range leaf.DNSNames {
		add(n)
	}
	// 老证书可能只把域名写在 CN 里，SAN 是空的
	add(leaf.Subject.CommonName)
	return info
}

// sniSeeds 候选 SNI 列表：目标本身 + 证书里的具体域名 + 通配符展开。
//
// 通配符本身不能当 SNI（客户端发的必须是具体名字），所以展开成几个前缀试，
// 验不过的不返回。
//
// 第二个返回值是「本来有多少个候选」——大站证书动辄二三十个 SAN，
// 截断之后得让人知道还有没测的，否则会以为名单就这么点。
func sniSeeds(host string, cert realityCertInfo) ([]sniSeed, int) {
	all := []sniSeed{{Name: strings.ToLower(host), Source: "目标本身"}}
	seen := map[string]bool{strings.ToLower(host): true}
	push := func(name, source string) {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || seen[name] {
			return
		}
		if strings.Contains(name, "*") {
			return
		}
		seen[name] = true
		all = append(all, sniSeed{Name: name, Source: source})
	}
	for _, n := range cert.Names {
		push(n, "证书 SAN")
	}
	for _, w := range cert.Wildcard {
		base := strings.TrimPrefix(w, "*.")
		if base == "" || base == w {
			continue
		}
		for _, p := range sniWildcardPrefixes {
			push(p+"."+base, "通配符展开")
		}
	}
	total := len(all)
	if total > sniCollectMaxCandidates {
		all = all[:sniCollectMaxCandidates]
	}
	return all, total
}

// normalizeDestCandidates 归一化候选列表：去重、补默认端口、挡私网与畸形输入。
//
// 容忍「host:port」这种写法（用户从别处复制过来时基本都是这样），
// 但拒绝带协议前缀 / 路径 / 空格的输入——那些拼出来的地址连不上，
// 与其让用户对着一个超时发呆，不如当场说清。
func normalizeDestCandidates(in []destCandidate) ([]destCandidate, string) {
	if len(in) == 0 {
		return nil, "至少给一个握手目标"
	}
	if len(in) > destProbeMaxCandidates {
		return nil, fmt.Sprintf("一次最多测 %d 个目标（收到 %d 个）", destProbeMaxCandidates, len(in))
	}
	seen := map[string]bool{}
	out := make([]destCandidate, 0, len(in))
	for _, c := range in {
		raw := strings.TrimSpace(c.Host)
		if raw == "" {
			continue
		}
		if strings.Contains(raw, "://") {
			return nil, fmt.Sprintf("目标「%s」不要带协议前缀：只填域名或 IP（端口用 :443 的形式或单独填）", raw)
		}
		if strings.ContainsAny(raw, "/@?#\"' \t\r\n") {
			return nil, fmt.Sprintf("目标「%s」里有非法字符：只填域名或 IP，不要带路径、空格或引号", raw)
		}
		host, port, ok := splitDestHostPort(raw, c.Port)
		if !ok {
			return nil, fmt.Sprintf("目标「%s」的端口不合法：应为 1-65535 的整数", raw)
		}
		sni := strings.TrimSpace(c.SNI)
		if sni != "" && (strings.ContainsAny(sni, "/@?#\"' \t\r\n") || strings.Contains(sni, "://")) {
			return nil, fmt.Sprintf("SNI「%s」里含有非法字符", sni)
		}
		key := strings.ToLower(host) + ":" + strconv.Itoa(port)
		if seen[key] {
			continue
		}
		seen[key] = true
		if err := guardDestHost(host); err != nil {
			return nil, err.Error()
		}
		out = append(out, destCandidate{Host: host, Port: port, SNI: sni})
	}
	if len(out) == 0 {
		return nil, "至少给一个握手目标"
	}
	return out, ""
}

// splitDestHostPort 拆 host[:port]。没写端口时用 defPort（默认 443）。
func splitDestHostPort(raw string, defPort int) (string, int, bool) {
	if defPort <= 0 || defPort > 65535 {
		defPort = 443
	}
	s := strings.TrimSpace(raw)
	if h, p, err := net.SplitHostPort(s); err == nil {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return "", 0, false
		}
		return strings.Trim(h, "[]"), n, true
	}
	// 没端口（含裸 IPv6）→ 整个当主机名；带冒号但拆不开的算非法
	if strings.Count(s, ":") == 1 {
		return "", 0, false
	}
	return strings.Trim(s, "[]"), defPort, true
}

// guardDestHost 拒绝把面板变成内网探针。
//
// 与 guardOutboundHost（规则集镜像）同一套判据，只是这里拿到的是裸主机名。
// 需要 dest 指向内网（例如 127.0.0.1 的「偷自己」玩法）时，直接在表单里填，
// 那条路径不经过这个接口——探测是面板替你去连，配置只是写进文件。
func guardDestHost(host string) error {
	if host == "" {
		return errors.New("目标为空")
	}
	if ip := net.ParseIP(host); ip != nil {
		if isPrivateIP(ip) {
			return errors.New("不允许测内网地址（" + host + "）：需要内网 dest 请直接填在表单里")
		}
		return nil
	}
	ips, err := net.DefaultResolver.LookupIP(context.Background(), "ip4", host)
	if err != nil {
		// 解析不了就让真实拨号去报错，这里不拦——DNS 临时故障不该被说成「非法地址」
		return nil
	}
	for _, ip := range ips {
		if isPrivateIP(ip) {
			return errors.New("不允许测内网地址（" + host + " → " + ip.String() + "）：需要内网 dest 请直接填在表单里")
		}
	}
	return nil
}

// probeRealityDestAll 并发实测整组候选，按「可用 → 证书对得上 → 快」排序。
//
// 每个目标握手 destProbeAttempts 次取中位数——这是给「挑最快的那个」用的，
// 单次抖动不该改变排名。要的是「行不行」而不是「多快」时（收集 SNI），
// 走 probeRealityDestAllN 传 1，省掉三分之二的握手。
func probeRealityDestAll(ctx context.Context, cands []destCandidate, roots *x509.CertPool) []realityDestResult {
	return probeRealityDestAllN(ctx, cands, roots, destProbeAttempts)
}

func probeRealityDestAllN(ctx context.Context, cands []destCandidate, roots *x509.CertPool, attempts int) []realityDestResult {
	out := make([]realityDestResult, len(cands))
	sem := make(chan struct{}, destProbeConcurrency)
	var wg sync.WaitGroup
	for i, c := range cands {
		wg.Add(1)
		go func(i int, c destCandidate) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				out[i] = realityDestResult{Host: c.Host, Port: c.Port, SNI: destSNI(c),
					Detail: "总耗时用完，没轮到这一个——减少候选后重试"}
				return
			}
			defer func() { <-sem }()
			out[i] = probeRealityDestN(ctx, c, roots, attempts)
		}(i, c)
	}
	wg.Wait()
	sortDestResults(out)
	return out
}

func destSNI(c destCandidate) string {
	if c.SNI != "" {
		return c.SNI
	}
	return c.Host
}

// probeRealityDest 对一个目标握手多次，取中位耗时，并给出可用性结论。
func probeRealityDest(ctx context.Context, c destCandidate, roots *x509.CertPool) realityDestResult {
	return probeRealityDestN(ctx, c, roots, destProbeAttempts)
}

func probeRealityDestN(ctx context.Context, c destCandidate, roots *x509.CertPool, attempts int) realityDestResult {
	if attempts < 1 {
		attempts = 1
	}
	res := realityDestResult{Host: c.Host, Port: c.Port, SNI: destSNI(c)}

	var samples []time.Duration
	var last destHandshake
	for i := 0; i < attempts; i++ {
		h := handshakeDest(ctx, c, roots)
		if h.err != nil {
			// 第一次连不上就不再试：连续三次超时只会让界面等 12 秒才给同样的结论
			res.Detail, res.Warning = describeDestError(h.err, c)
			return res
		}
		samples = append(samples, h.took)
		last = h
		if ctx.Err() != nil {
			break
		}
	}
	if len(samples) == 0 {
		res.Detail = "没有完成任何一次握手"
		return res
	}
	res.MS = medianDuration(samples).Milliseconds()
	res.TLS = tlsVersionName(last.state.Version)
	res.ALPN = last.state.NegotiatedProtocol
	res.H2 = last.state.NegotiatedProtocol == "h2"
	res.CertOK = last.certErr == nil
	if leaf := last.leaf(); leaf != nil {
		res.Issuer = issuerName(leaf)
		res.NotAfter = leaf.NotAfter.Format("2006-01-02")
		res.DaysLeft = int(time.Until(leaf.NotAfter).Hours() / 24)
	}

	if last.state.Version != tls.VersionTLS13 {
		res.Detail = fmt.Sprintf("只协商到 TLS %s：REALITY 必须在 TLS 1.3 上工作，这个目标不能用", res.TLS)
		res.Warning = "换一个支持 TLS 1.3 的目标；同一台服务器上换个域名通常就好了"
		return res
	}
	res.OK = true
	res.Detail = "TLS " + res.TLS + " 握手成功"
	if !res.CertOK {
		res.Warning = "证书与 SNI 对不上（" + trimErr(last.certErr.Error()) + "）：平时看不出来，" +
			"但一旦走到回落（真实访客或主动探测），对方会看到一个不属于该域名的证书，伪装就失效了"
	} else if !res.H2 {
		res.Warning = "目标不支持 HTTP/2（ALPN 只谈到 " + alpnOrNone(res.ALPN) + "）：" +
			"客户端默认会带 h2，个别客户端可能握手失败"
	}
	if res.DaysLeft >= 0 && res.DaysLeft < 14 {
		res.Warning = joinWarn(res.Warning, fmt.Sprintf("目标证书 %d 天后到期（%s）：到期后这个目标会握手失败", res.DaysLeft, res.NotAfter))
	}
	return res
}

// destHandshake 一次握手的结果。
type destHandshake struct {
	took    time.Duration
	state   tls.ConnectionState
	certErr error
	err     error
}

func (h destHandshake) leaf() *x509.Certificate {
	if len(h.state.PeerCertificates) == 0 {
		return nil
	}
	return h.state.PeerCertificates[0]
}

// handshakeDest 对目标做一次真实握手。
//
// InsecureSkipVerify 是**有意的**：这里要的正是「目标到底给了什么」——先握上，
// 再拿叶子证书自己去验（verifyDestCert），这样「连上了但证书不对」和
// 「根本连不上」能分开报，后者对用户毫无信息量。
func handshakeDest(ctx context.Context, c destCandidate, roots *x509.CertPool) destHandshake {
	d := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: destProbeDialTimeout},
		Config: &tls.Config{
			ServerName: destSNI(c),
			// 允许 1.2 握上来：只谈成 1.2 是个**结论**（不可用），
			// 直接拒绝的话用户只会看到一句 `protocol version not supported`。
			MinVersion:         tls.VersionTLS12,
			NextProtos:         []string{"h2", "http/1.1"},
			InsecureSkipVerify: true,
		},
	}
	start := time.Now()
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(c.Host, strconv.Itoa(c.Port)))
	took := time.Since(start)
	if err != nil {
		return destHandshake{took: took, err: err}
	}
	defer conn.Close()

	tc, ok := conn.(*tls.Conn)
	if !ok {
		return destHandshake{took: took, err: errors.New("连接不是 TLS")}
	}
	st := tc.ConnectionState()
	out := destHandshake{took: took, state: st}
	if len(st.PeerCertificates) == 0 {
		out.certErr = errors.New("目标没有提供证书")
		return out
	}
	out.certErr = verifyDestCert(st, destSNI(c), roots)
	return out
}

// verifyDestCert 校验证书链与域名（等价于普通 HTTPS 客户端的校验）。
func verifyDestCert(st tls.ConnectionState, sni string, roots *x509.CertPool) error {
	if len(st.PeerCertificates) == 0 {
		return errors.New("目标没有提供证书")
	}
	if roots == nil {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		roots = pool
	}
	inter := x509.NewCertPool()
	for _, ic := range st.PeerCertificates[1:] {
		inter.AddCert(ic)
	}
	_, err := st.PeerCertificates[0].Verify(x509.VerifyOptions{
		DNSName:       sni,
		Roots:         roots,
		Intermediates: inter,
	})
	return err
}

// describeDestError 把拨号错误翻成人话，并给出下一步。
func describeDestError(err error, c destCandidate) (detail, warning string) {
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		return "连接超时（" + net.JoinHostPort(c.Host, strconv.Itoa(c.Port)) + "）",
			"目标被墙、宕机或这个端口不通。换一个目标试试；如果所有目标都超时，先确认这台服务器能不能正常出网"
	case strings.Contains(err.Error(), "certificate"):
		return "握手失败：" + trimErr(err.Error()),
			"目标给的证书不可用；换一个目标"
	default:
		return "连接失败：" + trimErr(err.Error()),
			"确认域名拼写与端口；目标是 CDN 时也可以换它背后的另一个域名"
	}
}

func sortDestResults(rs []realityDestResult) {
	sort.SliceStable(rs, func(i, j int) bool {
		a, b := rs[i], rs[j]
		if a.OK != b.OK {
			return a.OK
		}
		if a.CertOK != b.CertOK {
			return a.CertOK
		}
		if a.H2 != b.H2 {
			return a.H2
		}
		if a.OK && a.MS != b.MS {
			return a.MS < b.MS
		}
		return false
	})
	// 只有「能用 + 证书对得上」的第一个才配叫推荐：把证书不对的目标推给用户，
	// 等于把「看起来最快、实际会露馅」的那个挑出来。
	for i := range rs {
		if rs[i].OK && rs[i].CertOK {
			rs[i].Best = true
			return
		}
	}
	for i := range rs {
		if rs[i].OK {
			rs[i].Best = true
			return
		}
	}
}

func medianDuration(ds []time.Duration) time.Duration {
	cp := append([]time.Duration(nil), ds...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	return cp[len(cp)/2]
}

func tlsVersionName(v uint16) string {
	switch v {
	case tls.VersionTLS13:
		return "1.3"
	case tls.VersionTLS12:
		return "1.2"
	case tls.VersionTLS11:
		return "1.1"
	case tls.VersionTLS10:
		return "1.0"
	}
	return "未知"
}

func alpnOrNone(s string) string {
	if s == "" {
		return "空"
	}
	return s
}

func issuerName(c *x509.Certificate) string {
	if c.Issuer.Organization != nil && len(c.Issuer.Organization) > 0 {
		return c.Issuer.Organization[0]
	}
	return c.Issuer.CommonName
}

func joinWarn(a, b string) string {
	if a == "" {
		return b
	}
	return a + "；" + b
}
