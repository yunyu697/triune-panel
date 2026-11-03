package api

// 外部节点入口的测试：解析摘要、去重、改名。
//
// 这三件事的共同点仍然是「不报错但是错的」：
//   - 摘要错了，列表上的地址就不是节点地址（vmess/ss 的正文是 base64，
//     前端用 URL 猜出来的是那坨 base64）；
//   - 不去重，订阅源每拉一次就多一份节点，客户端里出现「xxx 2」「xxx 3」；
//   - 改名/换链接不做校验，就给了绕过导入去重的一条侧门。

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"triune/internal/model"
)

type extResp struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	RawLink   string    `json:"raw_link"`
	CreatedAt string    `json:"created_at"`
	Parsed    extParsed `json:"parsed"`
}

func listExt(t *testing.T, h http.Handler, tok string) []extResp {
	t.Helper()
	w := apiReq(t, h, tok, "GET", "/api/external-nodes", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/external-nodes → %d: %s", w.Code, w.Body.String())
	}
	var out []extResp
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析列表响应: %v（原文 %s）", err, w.Body.String())
	}
	return out
}

func importText(t *testing.T, h http.Handler, tok, text string) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"text": text})
	w := apiReq(t, h, tok, "POST", "/api/import", string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("POST /api/import → %d: %s", w.Code, w.Body.String())
	}
	var j map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &j); err != nil {
		t.Fatalf("解析导入响应: %v", err)
	}
	return j
}

// vmessLink 造一条真实的 vmess 分享链接（正文是 base64 的 JSON）。
// 这正是「前端用 URL 猜协议/地址」会猜错的那类链接。
func vmessLink(t *testing.T, name, host string, port int) string {
	t.Helper()
	payload := map[string]any{
		"v": "2", "ps": name, "add": host, "port": port,
		"id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "net": "ws", "tls": "tls", "sni": "cdn.example.org",
	}
	b, _ := json.Marshal(payload)
	return "vmess://" + base64.StdEncoding.EncodeToString(b)
}

func vlessLink(name, host string, port int) string {
	return fmt.Sprintf("vless://11111111-2222-3333-4444-555555555555@%s:%d"+
		"?encryption=none&security=reality&type=tcp&sni=www.example.com&pbk=abc&sid=3f4a#%s",
		host, port, name)
}

// TestExternalListParsedSummary 列表必须给出真实解析结果，尤其是 vmess：
// 它的 host/port 藏在 base64 里，前端按 URL 猜出来的地址是错的。
func TestExternalListParsedSummary(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	text := vmessLink(t, "机场-香港", "198.51.100.24", 8443) + "\n" +
		vlessLink("自建-REALITY", "203.0.113.77", 443)
	importText(t, h, tok, text)

	rows := listExt(t, h, tok)
	if len(rows) != 2 {
		t.Fatalf("应有 2 条外部节点，实际 %d: %+v", len(rows), rows)
	}
	byName := map[string]extResp{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	vm, ok := byName["机场-香港"]
	if !ok {
		t.Fatalf("找不到 vmess 节点，实际 %+v", byName)
	}
	if !vm.Parsed.OK {
		t.Fatalf("vmess 应解析成功，实际 %+v", vm.Parsed)
	}
	if vm.Parsed.Host != "198.51.100.24" || vm.Parsed.Port != 8443 {
		t.Errorf("vmess 地址解析错：想要 198.51.100.24:8443，实际 %s:%d"+
			"（前端用 URL 猜会得到整段 base64）", vm.Parsed.Host, vm.Parsed.Port)
	}
	if vm.Parsed.Protocol != "vmess" || vm.Parsed.Network != "ws" || vm.Parsed.Security != "tls" {
		t.Errorf("vmess 协议/传输/安全解析错：%+v", vm.Parsed)
	}
	if vm.Parsed.SNI != "cdn.example.org" {
		t.Errorf("vmess SNI 应取 json 里的 sni，实际 %q", vm.Parsed.SNI)
	}

	vl, ok := byName["自建-REALITY"]
	if !ok {
		t.Fatalf("找不到 vless 节点，实际 %+v", byName)
	}
	if !vl.Parsed.OK || vl.Parsed.Protocol != "vless" || vl.Parsed.Host != "203.0.113.77" ||
		vl.Parsed.Port != 443 || vl.Parsed.Security != "reality" {
		t.Errorf("vless 解析错：%+v", vl.Parsed)
	}
	if vl.CreatedAt == "" {
		t.Error("created_at 不应为空（旧字段必须保持兼容）")
	}
}

// TestExternalListFlagsUnparsable 解析失败的节点仍然列出，但必须标记出来。
// 订阅渲染时这类节点会被静默跳过——用户看不到这个标记就会以为「加进去了」。
func TestExternalListFlagsUnparsable(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	if err := a.Store.CreateExternalNode(&model.ExternalNode{
		Name: "坏掉的节点", RawLink: "vless://没有主机也没有端口",
	}); err != nil {
		t.Fatal(err)
	}

	rows := listExt(t, h, tok)
	if len(rows) != 1 {
		t.Fatalf("应有 1 条，实际 %d", len(rows))
	}
	if rows[0].Parsed.OK {
		t.Fatalf("坏链接不该判成可导出：%+v", rows[0].Parsed)
	}
	if rows[0].Parsed.Error == "" {
		t.Error("解析失败必须带原因，否则界面只能显示一个红点")
	}
	if rows[0].Name != "坏掉的节点" {
		t.Errorf("坏链接也要能列出来（用户得能删它），实际名字 %q", rows[0].Name)
	}
}

// TestImportSkipsSameLinkTwice 同一段文本导入两次：第二次全部跳过。
func TestImportSkipsSameLinkTwice(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	text := vlessLink("节点A", "203.0.113.10", 443)
	first := importText(t, h, tok, text)
	if got := len(first["created"].([]any)); got != 1 {
		t.Fatalf("第一次导入应新建 1 条，实际 %d: %v", got, first)
	}
	second := importText(t, h, tok, text)
	if got := len(second["created"].([]any)); got != 0 {
		t.Fatalf("第二次导入不该再新建，实际 %d: %v", got, second)
	}
	skipped, _ := second["skipped"].([]any)
	if len(skipped) != 1 {
		t.Fatalf("第二次应报 1 条跳过，实际 %v", second)
	}
	if n := len(listExt(t, h, tok)); n != 1 {
		t.Fatalf("库里应只有 1 条，实际 %d", n)
	}
}

// TestImportDedupesByNodeIdentityNotName 这是订阅场景的真问题：
// 订阅源更新后把节点改了个名，链接文本随之变化，若只按链接精确去重，
// 用户的订阅里就会出现两份同一台服务器。
func TestImportDedupesByNodeIdentityNotName(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	importText(t, h, tok, vlessLink("香港-01", "203.0.113.11", 443))
	res := importText(t, h, tok, vlessLink("香港-01-上新", "203.0.113.11", 443))

	if got := len(res["created"].([]any)); got != 0 {
		t.Fatalf("同一服务器同一凭据只是改了备注，不该再建一条，实际新建 %d: %v", got, res)
	}
	skipped, _ := res["skipped"].([]any)
	if len(skipped) != 1 {
		t.Fatalf("应报 1 条跳过，实际 %v", res)
	}
	if n := len(listExt(t, h, tok)); n != 1 {
		t.Fatalf("库里应只有 1 条（否则订阅里会出现两台一样的服务器），实际 %d", n)
	}

	// 但换成另一台服务器必须照常进
	res2 := importText(t, h, tok, vlessLink("香港-02", "203.0.113.12", 443))
	if got := len(res2["created"].([]any)); got != 1 {
		t.Fatalf("不同服务器应正常导入，实际 %v", res2)
	}
}

// TestAddExternalRejectsDuplicate 单条新增和批量导入必须同一套去重，
// 否则「粘贴一次」成了绕过导入去重的侧门。
func TestAddExternalRejectsDuplicate(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	link := vlessLink("单条", "203.0.113.13", 443)
	body, _ := json.Marshal(map[string]string{"name": "单条", "raw_link": link})
	if w := apiReq(t, h, tok, "POST", "/api/external-nodes", string(body)); w.Code != http.StatusOK {
		t.Fatalf("首次新增应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	w := apiReq(t, h, tok, "POST", "/api/external-nodes", string(body))
	if w.Code != http.StatusConflict {
		t.Fatalf("重复新增应 409，实际 %d: %s", w.Code, w.Body.String())
	}
}

// TestUpdateExternalRename 改名只动名字，链接与导入时间不变。
func TestUpdateExternalRename(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	importText(t, h, tok, vlessLink("原名", "203.0.113.14", 443))
	row := listExt(t, h, tok)[0]

	w := apiReq(t, h, tok, "PUT", fmt.Sprintf("/api/external-nodes/%d", row.ID),
		`{"name":"改过的名字"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("改名应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var got extResp
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	if got.Name != "改过的名字" {
		t.Errorf("响应里的名字没变：%q", got.Name)
	}
	after := listExt(t, h, tok)[0]
	if after.Name != "改过的名字" {
		t.Errorf("列表里名字没变：%q", after.Name)
	}
	if after.RawLink != row.RawLink {
		t.Error("改名不该动链接")
	}
	if after.CreatedAt != row.CreatedAt {
		t.Errorf("改名不该改导入时间：%q → %q", row.CreatedAt, after.CreatedAt)
	}
	if !after.Parsed.OK {
		t.Errorf("改名后摘要应仍可解析：%+v", after.Parsed)
	}
}

// TestUpdateExternalRejectsBadInput 换链接/空名字/超长名字都要 400。
// 换链接不校验，就等于把一条非法链接塞进了订阅渲染路径。
func TestUpdateExternalRejectsBadInput(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	importText(t, h, tok, vlessLink("节点", "203.0.113.15", 443))
	id := listExt(t, h, tok)[0].ID
	path := fmt.Sprintf("/api/external-nodes/%d", id)

	cases := []struct {
		name string
		body string
	}{
		{"换链接但不合法", `{"raw_link":"vless://啥也不是"}`},
		{"名字为空", `{"name":"   "}`},
		{"名字过长", `{"name":"` + strings.Repeat("长", 65) + `"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := apiReq(t, h, tok, "PUT", path, c.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("应 400，实际 %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

// TestUpdateExternalRejectsDuplicateLink 把 A 的链接改成 B 的必须 409，
// 否则「改名 + 粘一条已存在的链接」就能造出重复节点。
func TestUpdateExternalRejectsDuplicateLink(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	importText(t, h, tok, vlessLink("节甲", "203.0.113.16", 443)+"\n"+vlessLink("节乙", "203.0.113.17", 443))
	rows := listExt(t, h, tok)
	if len(rows) != 2 {
		t.Fatalf("应有 2 条，实际 %d", len(rows))
	}
	body, _ := json.Marshal(map[string]string{"raw_link": rows[1].RawLink})
	w := apiReq(t, h, tok, "PUT", fmt.Sprintf("/api/external-nodes/%d", rows[0].ID), string(body))
	if w.Code != http.StatusConflict {
		t.Fatalf("改成别人的链接应 409，实际 %d: %s", w.Code, w.Body.String())
	}

	// 但把自己原来的链接再提交一次不算重复（界面保存时会原样回传）
	body2, _ := json.Marshal(map[string]string{"raw_link": rows[0].RawLink, "name": "节甲-改"})
	if w := apiReq(t, h, tok, "PUT", fmt.Sprintf("/api/external-nodes/%d", rows[0].ID), string(body2)); w.Code != http.StatusOK {
		t.Fatalf("提交自己原有的链接应 200，实际 %d: %s", w.Code, w.Body.String())
	}
}

// TestUpdateExternalNotFound 改不存在的节点要 404，不是 500。
func TestUpdateExternalNotFound(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	w := apiReq(t, h, tok, "PUT", "/api/external-nodes/99999", `{"name":"x"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("应 404，实际 %d: %s", w.Code, w.Body.String())
	}
}
