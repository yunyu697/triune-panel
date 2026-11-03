package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// 同端口冲突的保存期拦截。
//
// 背景：xray 的 `run -test` 与 sing-box 的 `check` 都只校验配置语法、不 bind socket，
// 所以「两个入站抢同一个端口」能一路存库 → 渲染 → 替换配置，直到内核启动失败才
// 在健康检查里暴露；而那时旧配置已经被换掉，**该内核下的所有节点一起断**。
// 这几条用例锁住「保存期就拦下」这个行为，以及不该被误拦的三种情况。

func inBody(t *testing.T, name, core, proto string, port int, extra string) string {
	t.Helper()
	b := `{"name":"` + name + `","core":"` + core + `","protocol":"` + proto + `",` +
		`"listen":"127.0.0.1","port":` + strconv.Itoa(port) + `,"tag":"` + name + `",` +
		`"transport":"tcp","tls":"none","settings":"{}","enabled":true,"draft":false`
	if extra != "" {
		b += "," + extra
	}
	return b + "}"
}

// TestPortConflictRejected 同端口 + 同协议族 → 400，且错误里能看出跟谁撞了。
func TestPortConflictRejected(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	mkInbound(t, h, tok, inBody(t, "first", "xray", "vless", 20001, ""))

	w := apiReq(t, h, tok, "POST", "/api/inbounds", inBody(t, "second", "xray", "vmess", 20001, ""))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("同端口同协议族应 400，实际 %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "20001") || !strings.Contains(body, "first") {
		t.Fatalf("错误里应指出端口与被占用的入站，实际: %s", body)
	}
}

// TestPortConflictCrossCore 不同内核也抢同一个系统端口，必须一起看。
func TestPortConflictCrossCore(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	mkInbound(t, h, tok, inBody(t, "xr", "xray", "vless", 20002, ""))
	w := apiReq(t, h, tok, "POST", "/api/inbounds", inBody(t, "sb", "singbox", "trojan", 20002, ""))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("跨内核同端口也应 400，实际 %d: %s", w.Code, w.Body.String())
	}
}

// TestPortSharedTCPAndUDPAllowed 一个 TCP 一个 UDP 共用端口是常见搭配，不能拦。
func TestPortSharedTCPAndUDPAllowed(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	mkInbound(t, h, tok, inBody(t, "tcp443", "singbox", "vless", 20003, ""))
	// hysteria2 是 UDP 协议，端口与上面的 TCP 入站相同
	w := apiReq(t, h, tok, "POST", "/api/inbounds", inBody(t, "udp443", "singbox", "hysteria2", 20003, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("TCP + UDP 同端口应允许，实际 %d: %s", w.Code, w.Body.String())
	}
}

// TestPortConflictIgnoresDisabledAndDraft 停用的、草稿的入站不占端口，不参与冲突。
func TestPortConflictIgnoresDisabledAndDraft(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	mkInbound(t, h, tok, inBody(t, "off", "xray", "vless", 20004, `"enabled":false`))
	mkInbound(t, h, tok, inBody(t, "draft", "xray", "vless", 20005, `"draft":true`))

	if w := apiReq(t, h, tok, "POST", "/api/inbounds", inBody(t, "on1", "xray", "vless", 20004, "")); w.Code != http.StatusOK {
		t.Fatalf("停用的入站不应占端口，实际 %d: %s", w.Code, w.Body.String())
	}
	if w := apiReq(t, h, tok, "POST", "/api/inbounds", inBody(t, "on2", "xray", "vmess", 20005, "")); w.Code != http.StatusOK {
		t.Fatalf("草稿不应占端口，实际 %d: %s", w.Code, w.Body.String())
	}
}

// TestPortConflictOnEnable 停用的入站改成同端口并启用时也要拦——否则「先建着、
// 以后再开」这条路径就绕过了校验，而开的那一下正好会把内核搞挂。
func TestPortConflictOnEnable(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	mkInbound(t, h, tok, inBody(t, "live", "xray", "vless", 20006, ""))
	off := mkInbound(t, h, tok, inBody(t, "sleeping", "xray", "vmess", 20007, `"enabled":false`))
	path := "/api/inbounds/" + strconv.FormatInt(off.ID, 10)

	if w := apiReq(t, h, tok, "PUT", path, `{"port":20006}`); w.Code != http.StatusOK {
		t.Fatalf("停用状态下改端口不该拦，实际 %d: %s", w.Code, w.Body.String())
	}
	w := apiReq(t, h, tok, "PUT", path, `{"enabled":true}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("启用时撞端口应 400，实际 %d: %s", w.Code, w.Body.String())
	}
}

// TestPortConflictDistinctListenAllowed 明确绑在不同地址上不算冲突（各自一个 socket）。
func TestPortConflictDistinctListenAllowed(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	mkInbound(t, h, tok, `{"name":"a","core":"xray","protocol":"vless","listen":"10.0.0.1",
		"port":20008,"tag":"a","enabled":true,"settings":"{}"}`)
	w := apiReq(t, h, tok, "POST", "/api/inbounds", `{"name":"b","core":"xray","protocol":"vless",
		"listen":"10.0.0.2","port":20008,"tag":"b","enabled":true,"settings":"{}"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("不同 listen 地址同端口应允许，实际 %d: %s", w.Code, w.Body.String())
	}
}
