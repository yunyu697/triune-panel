package api

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// 入站的「客户端连接地址」（model.Inbound.NodeHost）相关逻辑。
//
// 为什么需要它：一台机器上常常同时挂着两类入站——
//
//   - REALITY / 直连 TLS：域名解析到 CDN（Cloudflare 小黄云）时，客户端**必须**
//     连源站 IP，连域名等于连到 CDN，握手进不来；
//   - ws + tls 走 CDN：反过来**必须**连域名，连 IP 会绕过 CDN 且证书对不上。
//
// 以前只有一个全局「节点公开地址」，这两类入站里必然有一类写错，而且错得很隐蔽：
// 链接看着完整，只是连不上。所以地址下沉到入站级：留空跟随全局，填了就按填的走。

// egressIPFunc 探测本机出口 IP。做成包级变量是为了让测试能替换——
// 单测里跑真实的 UDP 拨号在 CI 容器里结果不确定（可能拿到 10.x）。
var egressIPFunc = egressIP

// egressIP 取本机默认路由的源地址。
//
// 用 UDP「拨号」是因为它不发包、只做路由选择：connect 之后 LocalAddr 就是内核
// 选中的出口地址。对绝大多数 VPS（公网 IP 直接挂在网卡上）这就是公网 IP；
// 弹性 IP / NAT 场景拿到的是内网地址，此时返回空串，让用户自己填——
// 与其预填一个必然连不通的 10.x，不如留空。
func egressIP() string {
	for _, target := range []string{"1.1.1.1:53", "8.8.8.8:53", "223.5.5.5:53"} {
		conn, err := net.DialTimeout("udp", target, 600*time.Millisecond)
		if err != nil {
			continue
		}
		la, ok := conn.LocalAddr().(*net.UDPAddr)
		conn.Close()
		if !ok || la.IP == nil {
			continue
		}
		ip := la.IP
		if ip.IsLoopback() || ip.IsUnspecified() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
			continue
		}
		return ip.String()
	}
	return ""
}

// parseNodeHost 归一化并校验入站地址：返回（归一化结果, 给用户看的错误）。
//
// 与 genconfig.go 里的 normalizeNodeHost 是两件事，别合并：那个只做「抹掉端口 /
// 方括号 / 尾点」，不判合法性，用在 public_host 生成的直连规则上（宽松是对的，
// 一个手滑的地址不该让整份配置生成失败）；这里要挡住非法值并给出可读原因，
// 因为它会写进库、再进到每一条导出链接里。
//
// 空串是合法值，表示「跟随面板设置」。这里对用户宽容一点：从浏览器地址栏或
// 别处粘过来的 https://node.example.com:443/ 也照收，把协议、端口、路径都剥掉
// ——客户端连哪个端口由入站自己决定，带着一个旧端口反而会误导。
//
// 但「剥路径」只在真的带了协议时才做：`node/x.com` 这种没有协议却有斜杠的输入
// 是笔误，剥成 `node` 会变成一个能存库、能连、但指向别的地方的地址。
func parseNodeHost(raw string) (string, string) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", ""
	}
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
		if j := strings.IndexAny(s, "/?#"); j >= 0 {
			s = s[:j]
		}
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "节点地址只填主机名或 IP（如 1.2.3.4 或 node.example.com），不要填网址"
	}
	if strings.ContainsAny(s, " \t\r\n,;、，") {
		return "", "节点地址不能含空格或标点，请只填一个主机名或 IP"
	}
	// 端口：host:port 的写法直接取 host。IPv6 的 [::1]:443 也走这条。
	if host, port, err := net.SplitHostPort(s); err == nil {
		if _, perr := strconv.Atoi(port); perr != nil {
			return "", "节点地址的端口部分不是数字：" + s
		}
		s = host
	}
	s = strings.TrimSuffix(strings.Trim(s, "[]"), ".")

	if ip := net.ParseIP(s); ip != nil {
		if ip.IsUnspecified() {
			return "", "0.0.0.0 / :: 是「监听全部网卡」的写法，客户端连不上；请填服务器的公网 IP 或域名"
		}
		return ip.String(), ""
	}
	// 主机名：逐段校验，出错时点名是错在哪一段，比一句「格式不对」好排查。
	if len(s) > 253 {
		return "", "节点地址过长（域名最多 253 个字符）"
	}
	low := strings.ToLower(s)
	for _, label := range strings.Split(low, ".") {
		if label == "" {
			return "", "节点地址里有连续的点或位于首尾的点：" + s
		}
		if len(label) > 63 {
			return "", "节点地址的某一段超过 63 个字符：" + label
		}
		if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", "节点地址的每一段不能以 - 开头或结尾：" + label
		}
		for _, c := range label {
			okChar := c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-'
			if !okChar {
				return "", "节点地址含非法字符 " + string(c) + "：只允许字母、数字、点和连字符"
			}
		}
	}
	return low, ""
}

// hostFromRequest 取请求里的主机名，剥掉面板端口。
func hostFromRequest(r *http.Request) string {
	h := r.Host
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return strings.Trim(h, "[]")
}

// handleServerAddr 给「新建入站」的地址选择器提供候选：本机公网 IP + 已知域名。
//
// IP 的来源按可信度排：设置里手填的 > 默认路由探测 > 面板这次的访问地址
// （用 IP 打开面板时那个 IP 一定是对的）。拿不到就返回空串，前端让用户自己填
// ——绝不猜一个看起来像那么回事的地址，那比留空更容易让人踩坑。
func (a *Server) handleServerAddr(w http.ResponseWriter, r *http.Request) {
	ip, src := "", ""
	if v, _ := a.Store.GetSetting("server_ip"); v != "" {
		if n, msg := parseNodeHost(v); msg == "" && n != "" {
			ip, src = n, "setting"
		}
	}
	if ip == "" {
		if v := egressIPFunc(); v != "" {
			ip, src = v, "detect"
		}
	}
	if ip == "" {
		if h := hostFromRequest(r); net.ParseIP(h) != nil {
			ip, src = h, "request"
		}
	}
	publicHost, _ := a.Store.GetSetting("public_host")
	writeJSON(w, 200, map[string]any{
		"ip":          ip,
		"ip_source":   src,
		"domains":     a.serverDomains(r),
		"public_host": publicHost,
	})
}

// serverDomains 已知域名候选，按可信度排：ACME 证书域名 > 节点公开地址 > 面板访问域名。
// 全部过一遍 parseNodeHost，IP、0.0.0.0、localhost 这类值不进候选
// ——选择器里列出一个不能当域名用的值，只会让人选错。
func (a *Server) serverDomains(r *http.Request) []string {
	var out []string
	seen := map[string]bool{}
	add := func(v string) {
		n, msg := parseNodeHost(v)
		if msg != "" || n == "" || net.ParseIP(n) != nil || seen[n] {
			return
		}
		if strings.EqualFold(n, "localhost") {
			return
		}
		seen[n] = true
		out = append(out, n)
	}
	for _, key := range []string{"acme_domain", "public_host"} {
		if v, _ := a.Store.GetSetting(key); v != "" {
			add(v)
		}
	}
	add(hostFromRequest(r))
	return out
}
