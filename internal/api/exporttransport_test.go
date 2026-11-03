package api

import (
	"net/http"
	"strings"
	"testing"
)

// xhttp / httpupgrade 这类传输在客户端侧不一定有对应类型，而**服务端支持不等于
// 客户端支持**。以前 applyNet 只认 ws / grpc，其它传输什么都不写——不写 network
// 就等于 tcp，服务端却在用 xhttp。用户拿到的是「能加载、一连就不通」的配置，
// 而且面板上什么提示都没有。
//
// 现在这种节点会被跳过并说明原因，且原因必须说成「传输不支持」而不是
// 「协议不支持」——后者会把用户引去换协议，其实换传输就行。
func TestExportRejectsUnrepresentableTransport(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)
	exportFixture(t, a, tok,
		`{"name":"xhttp-01","core":"xray","protocol":"vless","port":24501,"tag":"x1",`+
			`"transport":"xhttp","tls":"none","enabled":true,"settings":"{\"path\":\"/xh\"}"}`)

	w := apiReq(t, h, tok, "POST", "/api/export/config",
		`{"core":"mihomo","target":"client","inbound_ids":[1],"scenario":"balanced"}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("xhttp 传输在 mihomo 里没有对应类型，应 422，实际 %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "传输") {
		t.Errorf("文案应点名「传输」，实际: %s", body)
	}
	if strings.Contains(body, "本地代理协议") {
		t.Errorf("不该说成协议不支持（会把人引去换协议），实际: %s", body)
	}
	if !strings.Contains(body, "tcp") || !strings.Contains(body, "ws") {
		t.Errorf("文案应给出可行的传输选项，实际: %s", body)
	}
	// 拒绝时不能附带一份贴进内核的空配置
	if strings.Contains(body, `"content"`) {
		t.Errorf("拒绝时不应带 content，实际: %s", body)
	}
}

// 部分节点导不出时不能静默消失：要进 warnings，并且带上传输名。
func TestExportWarnsOnPartialTransportSkip(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)
	exportFixture(t, a, tok,
		`{"name":"xhttp-01","core":"xray","protocol":"vless","port":24501,"tag":"x1",`+
			`"transport":"xhttp","tls":"none","enabled":true,"settings":"{\"path\":\"/xh\"}"}`,
		`{"name":"tcp-01","core":"xray","protocol":"vless","port":24502,"tag":"t2",`+
			`"transport":"tcp","tls":"none","enabled":true,"settings":"{}"}`)

	w := apiReq(t, h, tok, "POST", "/api/export/config",
		`{"core":"mihomo","target":"client","inbound_ids":[1,2],"scenario":"balanced"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("有一个能导出就应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "xhttp") {
		t.Errorf("跳过的传输名应出现在 warnings 里，实际: %s", body)
	}
	if !strings.Contains(body, "tcp-01-alice") {
		t.Errorf("能导出的那个节点应仍在配置里，实际: %s", body)
	}
}
