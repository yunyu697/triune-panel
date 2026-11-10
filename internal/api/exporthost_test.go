package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// isLoopbackHost 判定「导出的节点地址只有本机能用」。
//
// 为什么值得单独测：这个判断只影响一句提示，但提示错了会很吵
// （公网地址被误报成回环，用户会以为配置有问题），漏报又等于没写。
func TestIsLoopbackHost(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"", true},          // public_host 空且拿不到 Host
		{"localhost", true}, // 管理员用 localhost 打开面板
		{"LOCALHOST", true}, // 大小写不该影响判断
		{"127.0.0.1", true}, // 最常见的现场
		{"127.1.2.3", true}, // 整个 127/8 都是回环
		{"::1", true},       // IPv6 回环
		{"[::1]", true},     // 带方括号的写法
		{"0.0.0.0", true},   // 监听地址，不是可连地址
		{"::", true},        //
		{"1.2.3.4", false},  // 公网 IP
		{"node.example.com", false},
		{"192.168.1.10", false}, // 内网地址：客户端同网段时确实可用，不误报
	}
	for _, c := range cases {
		if got := isLoopbackHost(c.host); got != c.want {
			t.Errorf("isLoopbackHost(%q) = %v，期望 %v", c.host, got, c.want)
		}
	}
}

// 导出客户端配置时，如果节点地址是本机回环，必须给出提示。
//
// 现场：管理员在服务器上 `http://127.0.0.1:8080` 打开面板，没填「公网地址」，
// 直接生成配置拿到自己电脑上喂给 mihomo。配置里每一项都是对的——REALITY 的
// pbk/sid/sni 齐全、mihomo -t 也过——但 server 是 127.0.0.1，指向用户自己的
// 电脑，于是「怎么都不通」。这种错在客户端侧看不出来，只能靠导出时提醒。
func TestExportWarnsWhenNodeHostIsLoopback(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)
	exportFixture(t, a, tok,
		`{"name":"hk-01","core":"xray","protocol":"vless","port":24443,"tag":"hk",`+
			`"transport":"tcp","tls":"none","enabled":true,"settings":"{}"}`)

	export := func() string {
		w := apiReq(t, h, tok, "POST", "/api/export/config",
			`{"core":"mihomo","target":"client","inbound_ids":[1],"scenario":"balanced"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("导出应 200，实际 %d: %s", w.Code, w.Body.String())
		}
		var resp struct {
			Warnings []string `json:"warnings"`
			Content  string   `json:"content"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("响应不是 JSON: %v", err)
		}
		return strings.Join(resp.Warnings, "\n")
	}

	// 1) public_host 填成回环 → 提示
	if w := apiReq(t, h, tok, "PUT", "/api/settings",
		`{"public_host":"127.0.0.1"}`); w.Code != http.StatusOK {
		t.Fatalf("改设置失败 %d: %s", w.Code, w.Body.String())
	}
	warn := export()
	if !strings.Contains(warn, "回环") {
		t.Errorf("节点地址是 127.0.0.1 时应有回环提示，实际 warnings: %q", warn)
	}
	// 文案得指出下一步怎么做，否则用户还是只能猜。两处都能改：全局的
	// 「节点公开地址」，或这一条入站的「节点地址」。
	if !strings.Contains(warn, "节点公开地址") || !strings.Contains(warn, "节点地址") {
		t.Errorf("提示里应指出去填「节点公开地址」或入站的「节点地址」，实际: %q", warn)
	}

	// 2) 填了公网域名 → 不该再提示
	if w := apiReq(t, h, tok, "PUT", "/api/settings",
		`{"public_host":"node.example.com"}`); w.Code != http.StatusOK {
		t.Fatalf("改设置失败 %d: %s", w.Code, w.Body.String())
	}
	if warn := export(); strings.Contains(warn, "回环") {
		t.Errorf("公网地址不该触发回环提示，实际 warnings: %q", warn)
	}

	// 3) 服务端自用（main-router）同样会写死这个地址，但用户本来就是在本机
	//    部署路由器配置，回环提示只会变成噪音——不提示。
	if w := apiReq(t, h, tok, "PUT", "/api/settings",
		`{"public_host":"127.0.0.1"}`); w.Code != http.StatusOK {
		t.Fatalf("改设置失败 %d: %s", w.Code, w.Body.String())
	}
	w := apiReq(t, h, tok, "POST", "/api/export/config",
		`{"core":"mihomo","target":"main-router","inbound_ids":[1],"scenario":"balanced"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("导出应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "回环") {
		t.Errorf("main-router 不该出现回环提示，实际: %s", w.Body.String())
	}
}
