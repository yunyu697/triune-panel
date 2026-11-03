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
func probeRealityDestAll(ctx context.Context, cands []destCandidate, roots *x509.CertPool) []realityDestResult {
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
			out[i] = probeRealityDest(ctx, c, roots)
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
	res := realityDestResult{Host: c.Host, Port: c.Port, SNI: destSNI(c)}

	var samples []time.Duration
	var last destHandshake
	for i := 0; i < destProbeAttempts; i++ {
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
