package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestParseNodeHost 钉住入站地址的归一化与校验。
//
// 这一层错的代价很隐蔽：值能存库、内核侧一切正常，只有客户端连不上，
// 而且失败现场在客户端（超时 / 连接被重置），服务端日志干干净净。
func TestParseNodeHost(t *testing.T) {
	ok := []struct{ in, want string }{
		{"", ""},
		{"   ", ""},
		{"1.2.3.4", "1.2.3.4"},
		{"1.2.3.4:443", "1.2.3.4"},          // 端口由入站决定，剥掉
		{"https://node.example.com/", "node.example.com"},       // 粘网址的常见形态
		{"https://node.example.com:443/x?y=1", "node.example.com"},
		{"Node.Example.COM", "node.example.com"}, // 域名大小写不敏感
		{"node.example.com.", "node.example.com"}, // FQDN 尾点
		{"2001:db8::1", "2001:db8::1"},
		{"[2001:db8::1]", "2001:db8::1"},
		{"[2001:db8::1]:8443", "2001:db8::1"},
		{"localhost", "localhost"},
	}
	for _, c := range ok {
		got, msg := parseNodeHost(c.in)
		if msg != "" {
			t.Errorf("parseNodeHost(%q) 不该报错，实际: %s", c.in, msg)
			continue
		}
		if got != c.want {
			t.Errorf("parseNodeHost(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}

	bad := []struct{ in, wantSub string }{
		{"0.0.0.0", "监听"},              // 监听地址，客户端连不上
		{"::", "监听"},
		{"a b.com", "空格"},              // 空格多半是粘了半句话
		{"1.2.3.4 5.6.7.8", "空格"},
		{"node/x.com", "非法字符"},        // 没带协议却有斜杠 = 笔误，不能剥成 node
		{"bad_host.example.com", "非法字符"},
		{"-bad.example.com", "开头或结尾"},
		{"bad-.example.com", "开头或结尾"},
		{"a..b.com", "连续的点"},
		{".a.com", "连续的点"},
		{"1.2.3.4:abc", "端口部分不是数字"},
		{"https://", "只填主机名或 IP"},
	}
	for _, c := range bad {
		got, msg := parseNodeHost(c.in)
		if msg == "" {
			t.Errorf("parseNodeHost(%q) 应当报错，却返回 %q", c.in, got)
			continue
		}
		if !strings.Contains(msg, c.wantSub) {
			t.Errorf("parseNodeHost(%q) 的错误里应含 %q，实际: %s", c.in, c.wantSub, msg)
		}
	}
}

// TestExportUsesInboundNodeHost 入站自己指定了地址时，导出链接必须用它。
//
// 正是这个功能的全部意义：域名解析到 CDN 的机器上，REALITY 必须连源站 IP，
// 而走 CDN 的 ws+tls 必须连域名。以前只有一个全局地址，必然写错一半。
func TestExportUsesInboundNodeHost(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)
	if w := apiReq(t, h, tok, "PUT", "/api/settings",
		`{"public_host":"global.example.com"}`); w.Code != http.StatusOK {
		t.Fatalf("设置公网地址失败 %d: %s", w.Code, w.Body.String())
	}

	mk := func(body string) int64 {
		w := apiReq(t, h, tok, "POST", "/api/inbounds", body)
		if w.Code != http.StatusOK {
			t.Fatalf("建入站失败 %d: %s", w.Code, w.Body.String())
		}
		var in struct {
			ID int64 `json:"id"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &in); err != nil {
			t.Fatalf("解析入站响应: %v", err)
		}
		return in.ID
	}
	ipID := mk(`{"name":"hk-reality","core":"xray","protocol":"vless","port":24443,"tag":"hk1",` +
		`"transport":"tcp","tls":"none","enabled":true,"node_host":"203.0.113.10","settings":"{}"}`)
	inheritID := mk(`{"name":"jp-ws","core":"xray","protocol":"vless","port":24444,"tag":"jp1",` +
		`"transport":"tcp","tls":"none","enabled":true,"settings":"{}"}`)
	links := func(id int64) string {
		w := apiReq(t, h, tok, "GET", "/api/inbounds/"+itoa64(id)+"/export?format=links", "")
		if w.Code != http.StatusOK {
			t.Fatalf("导出失败 %d: %s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	if got := links(ipID); !strings.Contains(got, "@203.0.113.10:24443") {
		t.Errorf("指定了节点地址的入站应导出该地址，实际: %s", got)
	}
	if got := links(inheritID); !strings.Contains(got, "@global.example.com:24444") {
		t.Errorf("没指定节点地址的入站应沿用全局地址，实际: %s", got)
	}
}

// TestNodeHostSurvivesPartialUpdate 白名单式的 PATCH 不能把地址弄丢。
//
// 现场：入站列表的启用开关是「GET 整条 → 改 enabled → PUT 整条」。PUT 的处理
// 按字段白名单解码，漏掉 node_host 的话，开关一次就把地址清空、导出的链接悄悄
// 换回全局地址——而界面上什么都看不出来。
func TestNodeHostSurvivesPartialUpdate(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)
	w := apiReq(t, h, tok, "POST", "/api/inbounds",
		`{"name":"hk","core":"xray","protocol":"vless","port":24445,"tag":"hk2",`+
			`"transport":"tcp","tls":"none","enabled":true,"node_host":"cdn.example.com","settings":"{}"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("建入站失败 %d: %s", w.Code, w.Body.String())
	}
	var in struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &in)

	// 1) 改地址本身必须能存进去——PUT 是按字段白名单解码的，漏掉 node_host
	//    会表现为「编辑保存成功，但地址没变」，比报错更难发现。
	if w := apiReq(t, h, tok, "PUT", "/api/inbounds/"+itoa64(in.ID),
		`{"node_host":"203.0.113.11"}`); w.Code != http.StatusOK {
		t.Fatalf("更新失败 %d: %s", w.Code, w.Body.String())
	}
	got, err := a.Store.GetInbound(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.NodeHost != "203.0.113.11" {
		t.Errorf("改地址没生效：%q", got.NodeHost)
	}

	// 2) 只改 enabled（模拟列表里的开关）不能把地址弄丢
	if w := apiReq(t, h, tok, "PUT", "/api/inbounds/"+itoa64(in.ID),
		`{"enabled":false}`); w.Code != http.StatusOK {
		t.Fatalf("更新失败 %d: %s", w.Code, w.Body.String())
	}
	if got, _ = a.Store.GetInbound(in.ID); got.NodeHost != "203.0.113.11" {
		t.Errorf("部分更新后地址被清空：%q", got.NodeHost)
	}
	if got.Enabled {
		t.Error("enabled 没生效")
	}
}

// TestCreateInboundRejectsBadNodeHost 非法地址在保存期就拒，别等客户端连不上。
func TestCreateInboundRejectsBadNodeHost(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)
	w := apiReq(t, h, tok, "POST", "/api/inbounds",
		`{"name":"bad","core":"xray","protocol":"vless","port":24446,"tag":"bad",`+
			`"transport":"tcp","tls":"none","enabled":true,"node_host":"0.0.0.0","settings":"{}"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("0.0.0.0 应被拒，实际 %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "监听") {
		t.Errorf("错误里应说明 0.0.0.0 是监听地址，实际: %s", w.Body.String())
	}
}

// TestServerAddrCandidates 地址选择器的候选：IP 来源优先级 + 域名去重。
func TestServerAddrCandidates(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	oldFn := egressIPFunc
	defer func() { egressIPFunc = oldFn }()
	egressIPFunc = func() string { return "198.51.100.7" }

	type addrResp struct {
		IP         string   `json:"ip"`
		IPSource   string   `json:"ip_source"`
		Domains    []string `json:"domains"`
		PublicHost string   `json:"public_host"`
	}
	// 直接调 handler 是为了指定 Host：管理员用哪个域名打开面板，也会被当成
	// 一个域名候选（很多时候它就是节点域名），这一条必须有确定的输入。
	get := func(host string) addrResp {
		req := httptest.NewRequest("GET", "/api/server/addr", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		a.handleServerAddr(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("应 200，实际 %d: %s", rec.Code, rec.Body.String())
		}
		var r addrResp
		if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
			t.Fatalf("响应不是 JSON: %v", err)
		}
		return r
	}

	// 1) 没填 server_ip → 用探测值
	if r := get("1.2.3.4:8080"); r.IP != "198.51.100.7" || r.IPSource != "detect" {
		t.Errorf("应使用探测到的出口 IP，实际 %+v", r)
	}

	// 2) 手工填了 server_ip → 以它为准（NAT / 弹性 IP 的机器探测到的是内网地址）
	if w := apiReq(t, h, tok, "PUT", "/api/settings",
		`{"server_ip":"203.0.113.99","public_host":"node.example.com","acme_domain":"acme.example.com"}`); w.Code != http.StatusOK {
		t.Fatalf("改设置失败 %d: %s", w.Code, w.Body.String())
	}
	r := get("panel.example.com")
	if r.IP != "203.0.113.99" || r.IPSource != "setting" {
		t.Errorf("应优先用设置里的 IP，实际 %+v", r)
	}
	// 顺序 = 可信度：证书域名 > 节点公开地址 > 面板访问域名（去重）
	want := []string{"acme.example.com", "node.example.com", "panel.example.com"}
	if len(r.Domains) != len(want) {
		t.Fatalf("域名候选数量不对: %v", r.Domains)
	}
	for i := range want {
		if r.Domains[i] != want[i] {
			t.Errorf("域名候选第 %d 项 = %q，期望 %q（顺序即可信度）", i, r.Domains[i], want[i])
		}
	}
	if r.PublicHost != "node.example.com" {
		t.Errorf("public_host 回显错: %q", r.PublicHost)
	}

	// 3) server_ip 填成域名要拒：那个框只当 IP 用
	if w := apiReq(t, h, tok, "PUT", "/api/settings",
		`{"server_ip":"node.example.com"}`); w.Code != http.StatusBadRequest {
		t.Errorf("server_ip 填域名应被拒，实际 %d: %s", w.Code, w.Body.String())
	}
}

// TestServerAddrIgnoresNonDomains 公开地址填的是 IP / localhost 时，不该进域名候选。
func TestServerAddrIgnoresNonDomains(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)
	if w := apiReq(t, h, tok, "PUT", "/api/settings",
		`{"public_host":"1.2.3.4","acme_domain":""}`); w.Code != http.StatusOK {
		t.Fatalf("改设置失败 %d: %s", w.Code, w.Body.String())
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/server/addr", nil)
	req.Host = "localhost:8080"
	a.handleServerAddr(w, req)
	var r struct {
		Domains []string `json:"domains"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &r)
	if len(r.Domains) != 0 {
		t.Errorf("IP 与 localhost 不该出现在域名候选里: %v", r.Domains)
	}
}
