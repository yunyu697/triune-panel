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
	"time"

	"triune/internal/core"
	"triune/internal/model"
)

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

	return checks
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
