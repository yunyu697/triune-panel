package api

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// mihomo 的产物以前只跑过 `-t`（配置校验），而 -t 不 bind 端口、不初始化
// DNS 与规则提供者——端口被占、DNS 段写坏、启动即退出它都看不见。
// v2.1.0 的 sing-box detour 事故就是这个盲区的另一面，所以 mihomo 也补一条
// 真正启动的用例。设 TRINE_MIHOMO_BIN 才跑。
func TestMihomoClientConfigStarts(t *testing.T) {
	bin := os.Getenv("TRINE_MIHOMO_BIN")
	if bin == "" {
		t.Skip("未设置 TRINE_MIHOMO_BIN，跳过真机启动校验")
	}
	warns := []string{}
	g := genMihomo("client", testNodes(), nil, nil, &warns, genOpts{})

	// 端口全部换掉：本机 7890 / 9090 很可能是用户自己在跑的实例。
	g.doc["mixed-port"] = freeTCPPort(t)
	g.doc["external-controller"] = "127.0.0.1:" + strconv.Itoa(freeTCPPort(t))

	raw, err := yaml.Marshal(g.doc)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(file, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	cmd := exec.Command(bin, "-d", dir, "-f", file)
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动 mihomo 失败: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		t.Fatalf("mihomo 启动即退出（%v）：\n%s", err, buf.String())
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
	if strings.Contains(buf.String(), "level=fatal") {
		t.Fatalf("启动日志里有 fatal：\n%s", buf.String())
	}
}

// router 模式（旁路由）的配置在 client 用例里完全覆盖不到：TUN、sniffer、
// DNS listen 都只在这一支出现。真启动 TUN 要 root，这里用 `-t` 只验结构：
// 它能抓「字段类型不对、嵌套层级错」，抓不到「键名拼错」——mihomo 对未知键
// 是静默忽略的。键名本身的正确性只能靠生成器断言
// （genconfig_hardening_test.go 的 TestMihomoSnifferCoversQUIC）。
func TestMihomoRouterConfigPassesCheck(t *testing.T) {
	bin := os.Getenv("TRINE_MIHOMO_BIN")
	if bin == "" {
		t.Skip("未设置 TRINE_MIHOMO_BIN，跳过真机校验")
	}
	warns := []string{}
	g := genMihomo("main-router", testNodes(), nil, nil, &warns, genOpts{})
	// 端口换掉：本机 7890 / 9090 很可能被用户自己的实例占着。
	g.doc["mixed-port"] = freeTCPPort(t)
	g.doc["external-controller"] = "127.0.0.1:" + strconv.Itoa(freeTCPPort(t))

	raw, err := yaml.Marshal(g.doc)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(file, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command(bin, "-t", "-d", dir, "-f", file).CombinedOutput()
	if err != nil {
		t.Fatalf("mihomo -t 拒绝 router 配置（%v）：\n%s", err, out)
	}
	if strings.Contains(string(out), "level=fatal") {
		t.Fatalf("校验日志里有 fatal：\n%s", out)
	}
}

// 豁免规则用的是 DOMAIN-KEYWORD / DOMAIN-SUFFIX。关键字规则类型写错时
// mihomo 未必报错——宽容忽略之后表现为「规则在、永不命中」，正是这次红果
// 事故的形态。所以除了产物断言，还要让真内核亲口认一次。
//
// 这里走自定义规则通道传同一份清单，不带远程规则集：要证的是「类型合法」，
// 不是「镜像拉得下来」（后者归 livecheck 与门禁管，别让本用例依赖网络）。
func TestMihomoRouterAcceptsBlockExemptRules(t *testing.T) {
	bin := os.Getenv("TRINE_MIHOMO_BIN")
	if bin == "" {
		t.Skip("未设置 TRINE_MIHOMO_BIN，跳过真机校验")
	}
	warns := []string{}
	g := genMihomo("main-router", testNodes(), nil, blockExemptRules, &warns, genOpts{})
	g.doc["mixed-port"] = freeTCPPort(t)
	g.doc["external-controller"] = "127.0.0.1:" + strconv.Itoa(freeTCPPort(t))

	raw, err := yaml.Marshal(g.doc)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(file, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bin, "-t", "-d", dir, "-f", file).CombinedOutput()
	if err != nil {
		t.Fatalf("mihomo -t 拒绝豁免规则（%v）：\n%s", err, out)
	}
	if strings.Contains(string(out), "level=fatal") {
		t.Fatalf("校验日志里有 fatal：\n%s", out)
	}
}

// ==================== 嗅探能不能真拿到非标端口的域名 ====================

// socks5Dial 走 mihomo 的混合端口连到 target（IP:port），返回已建连的 TCP。
// 手写而不是引第三方库：Socks5 就这么十来行，少一个依赖少一处版本漂移。
func socks5Dial(t *testing.T, proxyAddr, target string, kernLog *bytes.Buffer) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", proxyAddr, 3*time.Second)
	if err != nil {
		t.Fatalf("连 socks 端口 %s: %v", proxyAddr, err)
	}
	// 没这个超时，内核一旦不回应就是整条测试挂到 go test 的全局超时，
	// 报出来的是「测试超时」而不是「socks 握手没回」——查错成本天差地别。
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	// 握手：VER=5, NMETHODS=1, METHODS=[0 无认证]
	if _, err := c.Write([]byte{5, 1, 0}); err != nil {
		c.Close()
		t.Fatalf("socks 握手写入: %v\n内核日志：\n%s", err, kernLog.String())
	}
	var sel [2]byte
	if _, err := io.ReadFull(c, sel[:]); err != nil {
		c.Close()
		t.Fatalf("socks 握手应答: %v（内核没回 socks greeting，可能这个端口不是 SOCKS）\n内核日志：\n%s", err, kernLog.String())
	}
	if sel[0] != 5 || sel[1] != 0 {
		c.Close()
		t.Fatalf("socks 不认可无认证方式: %v", sel)
	}
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		c.Close()
		t.Fatalf("解析目标 %s: %v", target, err)
	}
	ip := net.ParseIP(host).To4()
	port, err := strconv.Atoi(portStr)
	if err != nil {
		c.Close()
		t.Fatalf("解析端口 %s: %v", portStr, err)
	}
	// 故意用 ATYP=IPv4 发裸 IP：这样 mihomo 拿到的 metadata.Host 是空的，
	// 与「设备绕过 DNS、直接连真实 IP」的真实场景一致——不这么做就测不到嗅探。
	req := append([]byte{5, 1, 0, 1}, ip...)
	req = append(req, byte(port>>8), byte(port))
	if _, err := c.Write(req); err != nil {
		c.Close()
		t.Fatalf("socks CONNECT 写入: %v", err)
	}
	var head [4]byte
	if _, err := io.ReadFull(c, head[:]); err != nil {
		c.Close()
		t.Fatalf("socks CONNECT 应答: %v", err)
	}
	if head[1] != 0 {
		c.Close()
		t.Fatalf("socks CONNECT 被拒，REP=%d", head[1])
	}
	var addrLen int
	switch head[3] {
	case 1:
		addrLen = 4
	case 4:
		addrLen = 16
	case 3:
		var l [1]byte
		if _, err := io.ReadFull(c, l[:]); err != nil {
			c.Close()
			t.Fatalf("读 BND.ADDR 长度: %v", err)
		}
		addrLen = int(l[0])
	default:
		c.Close()
		t.Fatalf("未知 ATYP=%d", head[3])
	}
	if _, err := io.ReadFull(c, make([]byte, addrLen+2)); err != nil {
		c.Close()
		t.Fatalf("读 BND 地址: %v", err)
	}
	return c
}

// 非标端口的 TLS 也必须能读出域名。
//
// 这是 v2.1.5 的核心不变量，也是 APNs 那次踩坑的通用形状：直连清单里的域名被
// 派生进 fake-ip-filter，DNS 返回真实 IP，连接进来时 metadata.Host 是空的；
// 域名规则走 RuleHost()，此时唯一来源就是 SniffHost（SNI）。而 mihomo 的嗅探
// 有端口硬门槛——SupportPort(DstPort) 为假直接 return false。端口一旦写成枚举
// 清单，「规则是否生效」就变成「这个服务碰巧用了我猜的端口没有」，而端口由
// 第三方决定。2026-11-24 实测 APNs 走 5223，清单里只有 443/8443，
// push.apple.com 的直连规则**完全没生效**，苹果设备推送长连接照样绕海外节点。
//
// 这里用真内核 + 真 TLS 服务验：连一个系统随机分配的高位端口，看 mihomo 能
// 不能从 ClientHello 里读出域名。只断言配置里写了 "1-65535" 是不够的——
// 那是「我写了」，不是「它认了」。
func TestMihomoSniffsDomainOnNonStandardPort(t *testing.T) {
	bin := os.Getenv("TRINE_MIHOMO_BIN")
	if bin == "" {
		t.Skip("未设置 TRINE_MIHOMO_BIN，跳过真机嗅探校验")
	}

	// 真 TLS 服务，端口由系统随机分配——必然不在 443/8443/5223 之列。
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	g := genMihomo("client", testNodes(), nil, nil, new([]string), genOpts{})
	// client 模式不生成 sniffer（没有 TUN），但嗅探机制与 TUN 无关，
	// 直接借用 router 模式那份，测的是同一个 ports 配置。
	rg := genMihomo("main-router", testNodes(), nil, nil, new([]string), genOpts{})
	g.doc["sniffer"] = rg.doc["sniffer"]

	mixed := freeTCPPort(t)
	g.doc["mixed-port"] = mixed
	ctrlPort := freeTCPPort(t)
	g.doc["external-controller"] = "127.0.0.1:" + strconv.Itoa(ctrlPort)

	raw, err := yaml.Marshal(g.doc)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(file, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "-d", dir, "-f", file)
	var kernLog bytes.Buffer
	cmd.Stdout = &kernLog
	cmd.Stderr = &kernLog
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动 mihomo: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	// 等混合端口真的能连上：进程 fork 出来不代表监听已就绪。
	mixedAddr := "127.0.0.1:" + strconv.Itoa(mixed)
	ready := false
	for i := 0; i < 60; i++ {
		if c, err := net.DialTimeout("tcp", mixedAddr, 200*time.Millisecond); err == nil {
			c.Close()
			ready = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		t.Fatalf("mihomo 启动后 %s 一直连不上，内核日志：\n%s", mixedAddr, kernLog.String())
	}

	conn := socks5Dial(t, mixedAddr, srv.Listener.Addr().String(), &kernLog)
	defer conn.Close()
	tc := tls.Client(conn, &tls.Config{ServerName: "push.apple.com", InsecureSkipVerify: true})
	if err := tc.Handshake(); err != nil {
		t.Fatalf("TLS 握手失败（嗅探读走了 ClientHello？）: %v", err)
	}

	// 嗅探发生在首包，握手成功时 sniffHost 应该已经写好；留几轮余量。
	deadline := time.Now().Add(5 * time.Second)
	var last []byte
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(ctrlPort) + "/connections")
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		last = body
		var payload struct {
			Connections []struct {
				Metadata struct {
					Host      string `json:"host"`
					SniffHost string `json:"sniffHost"`
				} `json:"metadata"`
			} `json:"connections"`
		}
		if json.Unmarshal(body, &payload) == nil {
			for _, c := range payload.Connections {
				if c.Metadata.SniffHost == "push.apple.com" && c.Metadata.Host == "" {
					return
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("非标端口的 TLS 没嗅出域名：直连清单里这类域名的规则会静默失效。"+
		"最近一次 /connections 快照：%s", truncateForLog(last, 600))
}

func truncateForLog(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
