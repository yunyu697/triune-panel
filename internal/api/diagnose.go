package api

// 入站连通性自检：
//   1. 配置状态（启用/草稿）
//   2. 内核进程运行状态
//   3. 端口本地监听检测（TCP 拨号；UDP 用「能否独占绑定」反证）
//   4. TLS 握手与证书信息（tls/reality 时）
//   5. 公网可达性（配置了 public_host 时，从本机拨公网地址:端口验证防火墙放行）
//
// 全部为只读探测，不修改任何配置。

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"triune/internal/core"
	"triune/internal/model"
	"triune/internal/update"
)

// semverRe 取版本号里的 x.y.z（xray / sing-box / mihomo 的输出都是这个形状）。
var semverRe = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

type checkItem struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Warn   bool   `json:"warn,omitempty"` // 提示性不通过（无法确认，不算失败）
	Detail string `json:"detail"`
}

// GET /api/inbounds/{id}/check
func (a *Server) checkInbound(w http.ResponseWriter, r *http.Request) {
	in, err := a.Store.GetInbound(pathID(r))
	if err != nil || in == nil {
		fail(w, 404, "入站不存在")
		return
	}
	checks := a.diagnoseInbound(in)
	allOK := true
	for _, c := range checks {
		if !c.OK && !c.Warn {
			allOK = false
		}
	}
	writeJSON(w, 200, map[string]any{"inbound": in, "checks": checks, "all_ok": allOK})
}

func (a *Server) diagnoseInbound(in *model.Inbound) []checkItem {
	var checks []checkItem

	// 1. 配置状态
	if in.Draft {
		checks = append(checks, checkItem{Name: "配置状态", OK: false, Detail: "仍是导入草稿，未参与配置渲染 —— 请在列表中点「取消草稿」并应用配置"})
	} else if !in.Enabled {
		checks = append(checks, checkItem{Name: "配置状态", OK: false, Detail: "入站已停用 —— 打开开关并应用配置后生效"})
	} else {
		checks = append(checks, checkItem{Name: "配置状态", OK: true, Detail: "已启用并参与配置渲染"})
	}

	// 2. 内核进程
	running, _ := a.ProcMgr.Status(in.Core)
	if running {
		checks = append(checks, checkItem{Name: "内核进程", OK: true, Detail: in.Core + " 正在运行"})
	} else {
		checks = append(checks, checkItem{Name: "内核进程", OK: false,
			Detail: in.Core + " 未运行 —— 请先应用配置（或到「组件更新」确认内核已安装）"})
	}

	// 3. 端口监听
	udpProto := in.Protocol == "hysteria2" || in.Protocol == "tuic" || in.Transport == "quic"
	if udpProto {
		busy, _ := checkUDPPort(in.Port)
		if !busy {
			checks = append(checks, checkItem{Name: "端口监听", OK: false,
				Detail: fmt.Sprintf("UDP :%d 无人监听（本机可独占绑定该端口）—— 内核未启动或未配置该入站", in.Port)})
		} else {
			checks = append(checks, checkItem{Name: "端口监听", OK: true,
				Detail: fmt.Sprintf("UDP :%d 已被占用（正常，说明内核正在监听）", in.Port)})
		}
	} else {
		addr := net.JoinHostPort(listenOrLocal(in.Listen), fmt.Sprint(in.Port))
		conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
		if err != nil {
			checks = append(checks, checkItem{Name: "端口监听", OK: false,
				Detail: fmt.Sprintf("TCP %s 连接失败: %v —— 内核未运行或未监听该地址", addr, err)})
		} else {
			conn.Close()
			checks = append(checks, checkItem{Name: "端口监听", OK: true, Detail: "TCP " + addr + " 可连接"})
		}
	}

	// 4. TLS 握手（仅 TCP 类协议且配置了 TLS）
	if !udpProto && (in.TLS == "tls" || in.TLS == "reality") {
		checks = append(checks, probeTLS(in))
	}

	// 5. 公网可达（TCP 类才有意义）
	if host, _ := a.Store.GetSetting("public_host"); host != "" && !udpProto {
		addr := net.JoinHostPort(host, fmt.Sprint(in.Port))
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			checks = append(checks, checkItem{Name: "公网可达", OK: false,
				Detail: fmt.Sprintf("从本机连 %s 失败: %v —— 请检查防火墙/安全组是否放行 %d 端口", addr, err, in.Port)})
		} else {
			conn.Close()
			checks = append(checks, checkItem{Name: "公网可达", OK: true,
				Detail: fmt.Sprintf("%s 可达（防火墙已放行 TCP %d）", addr, in.Port)})
		}
	} else if udpProto {
		checks = append(checks, checkItem{Name: "公网可达", OK: true, Warn: true,
			Detail: "UDP 端口无法从本机验证公网放行，请在云厂商安全组确认已放行 UDP " + fmt.Sprint(in.Port)})
	}

	// 6. 客户端兼容性：REALITY + 新版 xray 会拒绝老客户端（mihomo 等）
	if c, ok := a.realityClientCompat(in); ok {
		checks = append(checks, c)
	}

	return checks
}

// xrayRealityBreakVer 是「REALITY 开始拒绝老客户端」的起始 xray 版本。
//
// 背景（2026-11 实测 + 上游 issue）：
//   - xray 自 26.7.11 起，realitySettings 不写 minClientVer 时默认按 26.3.27 判定；
//   - mihomo 的 REALITY 实现把客户端版本**硬编码**成 1.8.2（MetaCubeX/mihomo#3039）；
//   - 于是 xray ≥26.7.11 + mihomo = `REALITY authentication failed`。
//
// 实测矩阵（同一份订阅配置、同一台机器、同一组 uuid/pbk/sid/sni，2026-11）：
//
//	xray 1.8.24    + mihomo 1.19.33   → 通（362ms）
//	xray 26.3.27   + mihomo 1.19.33   → 通（352ms）
//	xray 26.10.31  + mihomo 1.19.33   → REALITY authentication failed
//	xray 26.10.31  + xray 1.8 客户端  → 同样失败（不是 mihomo 独有的）
//	xray 26.10.31  + xray 26 客户端   → 通
//	xray 26.10.31  + minClientVer=1.8.2 → 仍然失败（26.7.11 上加这一条能救）
//
// 这就是「同一个 VPS，3x-ui 的节点通、面板的不通」最常见的一种成因：3x-ui
// 往往还跑着老版 xray，而面板「一键装最新」会拿到 26.7.11 以上。它和节点参数
// 一点关系都没有，所以对着链接逐字比对永远查不出来。
var xrayRealityBreakVer = [3]int{26, 7, 11}

// xrayVersionNums 从 `xray version` 的输出里取版本号。
// 形如 "Xray 26.10.31 (Xray, Penetrates Everything.) a4a3dff (go1.27.3 darwin/arm64)"。
func xrayVersionNums(v string) ([3]int, bool) {
	m := semverRe.FindStringSubmatch(v)
	if m == nil {
		return [3]int{}, false
	}
	var out [3]int
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// xrayTooNewForOldRealityClients 判断这个版本的服务端会不会拒绝老客户端。
func xrayTooNewForOldRealityClients(v string) bool {
	cur, ok := xrayVersionNums(v)
	if !ok {
		return false
	}
	for i := 0; i < 3; i++ {
		if cur[i] != xrayRealityBreakVer[i] {
			return cur[i] > xrayRealityBreakVer[i]
		}
	}
	return true
}

// realityClientCompat 只对「xray + reality」给出客户端兼容性结论。
//
// 拿不到内核路径或版本号时返回 ok=false（不猜、不误报）。
func (a *Server) realityClientCompat(in *model.Inbound) (checkItem, bool) {
	if in.Core != model.CoreXray || in.TLS != "reality" {
		return checkItem{}, false
	}
	bin, _ := a.Store.GetSetting("bin_xray")
	if bin == "" {
		return checkItem{}, false
	}
	v, err := update.BinaryVersion(bin, update.VersionArgs["xray"])
	if err != nil || strings.TrimSpace(v) == "" {
		return checkItem{}, false
	}
	if !xrayTooNewForOldRealityClients(v) {
		return checkItem{Name: "客户端兼容性", OK: true,
			Detail: fmt.Sprintf("%s 的 REALITY 仍接受老客户端（mihomo / v2rayN 等）", v)}, true
	}
	return checkItem{Name: "客户端兼容性", OK: false, Warn: true,
		Detail: fmt.Sprintf("%s 的 REALITY 默认只接受 xray 26.3.27 以上的客户端："+
			"mihomo / Clash（以及 xray 1.8 内核的 v2rayN）会报 `REALITY authentication failed`，"+
			"而服务端日志只说 `authentication failed or validation criteria not met`。"+
			"节点参数是对的，别再去比对链接。两条出路：① 到「组件更新 → 版本列表」把 xray 降到 26.3.27"+
			"（实测可用；1.8.x 也可以）；② 客户端换用 xray 内核（v2rayN / Shadowrocket / NekoBox）。"+
			"mihomo 侧目前无解：它把 REALITY 客户端版本写死在 1.8.2（MetaCubeX/mihomo#3039）", v)}, true
}

// probeTLS 对 TLS / Reality 入站做一次真实的 TLS 握手探测。
//
// 单独成函数是为了能被单测直接调用——这里两个坑都踩过：
//
//	a) SNI 必须用「渲染时真正生效的值」（core.RealitySNI）。Reality 对不在
//	   serverNames 里的 SNI 是按设计拒绝的（alert handshake failure），探测
//	   要是用占位 SNI，配置完全正常也会被报成「握手失败」。
//	b) 探测地址要对齐入站的实际监听地址。写死 127.0.0.1 的话，只监听某个
//	   具体 IP 的入站会被误判成连不上。
//
// 失败一律是 Warn：握手过不去的成因太多（dest 被墙、客户端要用 shortId），
// 不足以判定「这条入站坏了」，只提示怎么查。
func probeTLS(in *model.Inbound) checkItem {
	addr := net.JoinHostPort(listenOrLocal(in.Listen), fmt.Sprint(in.Port))
	sni := core.Field(in.Settings, "server_name")
	if in.TLS == "reality" {
		sni = core.RealitySNI(in)
		if sni == "" {
			return checkItem{Name: "TLS 握手", OK: false, Warn: true,
				Detail: "Reality 未配置 SNI（serverNames）—— 无法探测，且客户端也必须带 SNI 才能连上。请在入站里补上 sni 后保存并应用"}
		}
	}
	if sni == "" {
		sni = "probe.local"
	}

	d := net.Dialer{Timeout: 4 * time.Second}
	tc, err := tls.DialWithDialer(&d, "tcp", addr, &tls.Config{ServerName: sni, InsecureSkipVerify: true})
	if err != nil {
		detail := fmt.Sprintf("TLS 握手失败（探测 %s，SNI=%s）: %v", addr, sni, err)
		if in.TLS == "reality" {
			detail += fmt.Sprintf(" —— Reality 的普通握手要靠把连接转给伪装目标 %s 来完成。"+
				"失败一般说明本机连不上 dest（被墙 / 目标挂了），或该端口上跑的不是这个入站。"+
				"客户端带 shortId 的真实握手仍可能正常，请结合「端口监听」一起判断", core.RealityDest(in))
		} else {
			detail += " —— 端口能连上却拿不到证书，通常是证书或私钥文件读不到、" +
				"内核没加载这个入站，或端口被别的服务占着"
		}
		return checkItem{Name: "TLS 握手", OK: false, Warn: true, Detail: detail}
	}
	defer tc.Close()

	st := tc.ConnectionState()
	expiry := ""
	if len(st.PeerCertificates) > 0 {
		expiry = "，证书到期 " + st.PeerCertificates[0].NotAfter.Format("2006-01-02")
	}
	if in.TLS == "reality" {
		// 握手能过只说明「dest 转发生效」，不代表客户端能认证成功。
		return checkItem{Name: "TLS 握手", OK: true, Warn: true,
			Detail: fmt.Sprintf("SNI=%s 可完成 TLS 握手%s（Reality 已把连接转给伪装目标；真正的入站认证在协议层，需要客户端带 shortId 才能验证）", sni, expiry)}
	}
	return checkItem{Name: "TLS 握手", OK: true,
		Detail: fmt.Sprintf("TLS 握手成功（SNI=%s%s）", sni, expiry)}
}

// checkUDPPort 用「尝试独占绑定」判断 UDP 端口是否已被监听：
// 绑定失败 = 已有进程占用（对内核是好消息）；绑定成功 = 没人监听。
func checkUDPPort(port int) (busy bool, detail string) {
	addr := &net.UDPAddr{IP: net.IPv4zero, Port: port}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return true, "端口已被占用"
	}
	conn.Close()
	return false, "端口空闲"
}

func listenOrLocal(l string) string {
	if l == "" || l == "::" || l == "0.0.0.0" {
		return "127.0.0.1"
	}
	return l
}
