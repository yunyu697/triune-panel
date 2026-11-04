package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// 导出的「客户端配置」必须自己说明「服务端没在监听」。
//
// 现场表现：用 3x-ui 的入站 JSON 导入一条 vless+reality，保存后忘了打开启用开关
// （新建/导入默认都是停用），点「导出」拿到的链接看起来完全正常——security、sni、
// sid、pbk、fp 一个不少——但那个端口根本没有监听，客户端一律连不上。
// 于是排查会一路跑到客户端配置、防火墙、REALITY 参数上去。
//
// 订阅端点（/sub/<token>）早就跳过停用入站了，这条手工导出路径以前什么都不说。
func TestExportWarnsWhenInboundNotListening(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	// 停用入站：settings 故意给全，导出的链接本身是"对"的，只有启用状态不对
	settings := `{"handshake_server":"www.apple.com","server_names":"www.apple.com",` +
		`"short_ids":["ab12cd34"]}`
	body := `{"name":"off-01","core":"xray","protocol":"vless","listen":"127.0.0.1",
		"port":24551,"tag":"off-01","transport":"tcp","tls":"reality",
		"enabled":false,"draft":false,"settings":` + strconvQuote(settings) + `,
		"clients":[{"name":"u1"}]}`
	in := mkInbound(t, h, tok, body)

	for _, format := range []string{"links", "clash"} {
		w := apiReq(t, h, tok, "GET", "/api/inbounds/"+itoa64(in.ID)+"/export?format="+format, "")
		if w.Code != 200 {
			t.Fatalf("%s: 导出应 200，实际 %d: %s", format, w.Code, w.Body.String())
		}
		out := w.Body.String()
		if !strings.Contains(out, "已停用") {
			t.Errorf("%s: 停用入站的导出里没有「已停用」提示，用户只会看到一份\"正常\"的配置\n%s",
				format, firstLines(out, 6))
		}
		if !strings.Contains(out, "24551") {
			t.Errorf("%s: 提示里应带上端口号 24551\n%s", format, firstLines(out, 6))
		}
		// 提示不能顶掉真正的内容
		if !strings.Contains(out, "vless") {
			t.Errorf("%s: 导出内容被提示挤掉了\n%s", format, firstLines(out, 6))
		}
	}

	// 草稿是另一种状态：用户还没确认，措辞不同
	draftBody := `{"name":"draft-01","core":"xray","protocol":"vless","listen":"127.0.0.1",
		"port":24552,"tag":"draft-01","transport":"tcp","tls":"reality",
		"enabled":true,"draft":true,"settings":` + strconvQuote(settings) + `,
		"clients":[{"name":"u1"}]}`
	din := mkInbound(t, h, tok, draftBody)
	w := apiReq(t, h, tok, "GET", "/api/inbounds/"+itoa64(din.ID)+"/export?format=links", "")
	if !strings.Contains(w.Body.String(), "草稿") {
		t.Errorf("草稿入站的导出里没有「草稿」提示\n%s", firstLines(w.Body.String(), 4))
	}

	// 启用的入站不该有这句噪音——否则它就成了"总在响"的警告
	if w := apiReq(t, h, tok, "PUT", "/api/inbounds/"+itoa64(in.ID), `{"enabled":true}`); w.Code != 200 {
		t.Fatalf("启用入站失败：%d %s", w.Code, w.Body.String())
	}
	w = apiReq(t, h, tok, "GET", "/api/inbounds/"+itoa64(in.ID)+"/export?format=links", "")
	if strings.Contains(w.Body.String(), "已停用") {
		t.Errorf("启用后仍带着「已停用」提示\n%s", firstLines(w.Body.String(), 4))
	}
	if !strings.Contains(w.Body.String(), "vless://") {
		t.Errorf("启用后导出应给出链接\n%s", firstLines(w.Body.String(), 4))
	}
}

// 订阅输出（/sub/<token>）跳过停用入站这条行为不能被上面的改动带偏：
// 它走的是另一条路径，早就有自己的过滤。
func TestSubStillSkipsDisabledInbound(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	body := `{"name":"off-02","core":"xray","protocol":"vless","listen":"127.0.0.1",
		"port":24553,"tag":"off-02","transport":"tcp","tls":"none","settings":"{}",
		"enabled":false,"draft":false,"clients":[{"name":"u1"}]}`
	mkInbound(t, h, tok, body)

	clients, err := a.Store.ListClients()
	if err != nil || len(clients) != 1 {
		t.Fatalf("应有 1 个用户，实际 %d（%v）", len(clients), err)
	}
	w := apiReq(t, h, tok, "GET", "/sub/"+clients[0].SubToken, "")
	if w.Code != 200 {
		t.Fatalf("订阅应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "24553") {
		t.Fatalf("停用入站不该出现在订阅里\n%s", w.Body.String())
	}
}

// strconvQuote 把 settings 字符串塞进 JSON 请求体。
func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// itoa64 避免测试文件为了一个 id 引入 strconv。
func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
