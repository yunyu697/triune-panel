package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const importJSONSample = `[
  {
    "remark": "3xui-vless-reality",
    "port": 24443,
    "protocol": "vless",
    "settings": "{\"clients\":[{\"id\":\"11111111-2222-3333-4444-555555555555\",\"email\":\"alice\",\"flow\":\"xtls-rprx-vision\",\"totalGB\":1073741824,\"enable\":true}],\"decryption\":\"none\"}",
    "streamSettings": "{\"network\":\"tcp\",\"security\":\"reality\",\"realitySettings\":{\"dest\":\"www.apple.com:443\",\"serverNames\":[\"www.apple.com\"],\"privateKey\":\"PRIV\",\"shortIds\":[\"ab12\",\"\"]}}",
    "tag": "in-24443",
    "sniffing": "{\"enabled\":true,\"destOverride\":[\"http\",\"tls\"]}"
  },
  {
    "remark": "3xui-vmess-ws",
    "port": 24444,
    "protocol": "vmess",
    "settings": "{\"clients\":[{\"id\":\"99999999-8888-7777-6666-555555555555\",\"email\":\"bob\"}]}",
    "streamSettings": "{\"network\":\"ws\",\"security\":\"none\",\"wsSettings\":{\"path\":\"/ray\"}}"
  }
]`

func postImportJSON(t *testing.T, h http.Handler, tok, body string) *httpResult {
	t.Helper()
	w := apiReq(t, h, tok, "POST", "/api/inbounds/import-json", body)
	return &httpResult{Code: w.Code, Body: w.Body.String()}
}

type httpResult struct {
	Code int
	Body string
}

// TestImportJSONDryRun 预览不写库：这是「先看清楚再导入」的前提。
func TestImportJSONDryRun(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	body := `{"text":` + mustJSON(t, importJSONSample) + `,"core":"xray","dry_run":true}`
	r := postImportJSON(t, h, tok, body)
	if r.Code != http.StatusOK {
		t.Fatalf("预览应 200，实际 %d: %s", r.Code, r.Body)
	}
	var resp struct {
		DryRun  bool `json:"dry_run"`
		Items   []importPreview
		Summary map[string]int
	}
	if err := json.Unmarshal([]byte(r.Body), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.DryRun || len(resp.Items) != 2 {
		t.Fatalf("预览结果不对：%+v", resp)
	}
	if resp.Items[0].TLS != "reality" || resp.Items[0].Port != 24443 {
		t.Errorf("第一条应是 vless+reality: %+v", resp.Items[0])
	}
	if len(resp.Items[0].Clients) != 1 {
		t.Errorf("客户端没解析出来: %+v", resp.Items[0])
	}
	// 预览必须打码，否则截图就泄漏了 UUID
	if strings.Contains(resp.Items[0].Clients[0].Credential, "2222-3333") {
		t.Errorf("凭据未打码: %q", resp.Items[0].Clients[0].Credential)
	}
	if n, _ := a.Store.ListInbounds(""); len(n) != 0 {
		t.Errorf("dry_run 不该写库，实际写了 %d 条", len(n))
	}
}

// TestImportJSONCreatesDisabledDrafts 导入必须 enabled=false：
// 导入 ≠ 启用，不点开关绝不能进内核配置。
func TestImportJSONCreatesDisabledDrafts(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	body := `{"text":` + mustJSON(t, importJSONSample) + `,"core":"xray"}`
	r := postImportJSON(t, h, tok, body)
	if r.Code != http.StatusOK {
		t.Fatalf("导入应 200，实际 %d: %s", r.Code, r.Body)
	}
	ins, err := a.Store.ListInbounds("")
	if err != nil {
		t.Fatal(err)
	}
	if len(ins) != 2 {
		t.Fatalf("应建 2 条入站，实际 %d", len(ins))
	}
	for _, in := range ins {
		if in.Enabled {
			t.Errorf("导入的入站不该是启用状态: %s", in.Name)
		}
		if in.Core != "xray" {
			t.Errorf("内核应为 xray，实际 %s", in.Core)
		}
	}
	// reality 私钥、多 shortIds、sniffing 都要落到 settings 里
	var reality *struct{ Settings string }
	for _, in := range ins {
		if in.TLS == "reality" {
			reality = &struct{ Settings string }{in.Settings}
		}
	}
	if reality == nil {
		t.Fatal("没找到 reality 入站")
	}
	var s map[string]any
	if err := json.Unmarshal([]byte(reality.Settings), &s); err != nil {
		t.Fatal(err)
	}
	if s["private_key"] != "PRIV" {
		t.Errorf("私钥丢失: %v", s["private_key"])
	}
	if sids, ok := s["short_ids"].([]any); !ok || len(sids) != 2 {
		t.Errorf("多 shortIds 应保留（含空串）: %#v", s["short_ids"])
	}
	if _, ok := s["sniffing"]; !ok {
		t.Errorf("xray 目标内核应保留 sniffing")
	}

	// 用户要跟入站关联上，否则订阅里没有节点
	clients, _ := a.Store.ListClients()
	if len(clients) != 2 {
		t.Fatalf("应建 2 个用户，实际 %d", len(clients))
	}
	if ins[0].ID != 0 {
		ids, _ := a.Store.InboundClientIDs(ins[0].ID)
		if len(ids) == 0 {
			t.Error("用户没有关联到入站")
		}
	}
}

// TestImportJSONSkipsUnsupported 协议在当前内核下不支持时跳过而不是半截写入。
func TestImportJSONSkipsUnsupported(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	sample := `[{"remark":"x","port":8443,"protocol":"shadowsocks","settings":"{\"method\":\"aes-256-gcm\",\"password\":\"p\"}","streamSettings":"{\"network\":\"tcp\",\"security\":\"none\"}"}]`
	body := `{"text":` + mustJSON(t, sample) + `,"core":"mihomo","dry_run":true}`
	r := postImportJSON(t, h, tok, body)
	var resp struct {
		Items []importPreview
	}
	if err := json.Unmarshal([]byte(r.Body), &resp); err != nil {
		t.Fatal(err)
	}
	// mihomo 支持 shadowsocks，这里应可导入
	if resp.Items[0].Skip {
		t.Errorf("mihomo 应支持 shadowsocks: %v", resp.Items[0].Warnings)
	}
}

func TestImportJSONRejectsBadInput(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	for _, body := range []string{
		`{"text":"not json","core":"xray"}`,
		`{"text":"SQLite format 3","core":"xray"}`,
		`{"text":"[]","core":"xray"}`,
		`{"text":"[]","core":"v2ray"}`,
	} {
		if r := postImportJSON(t, h, tok, body); r.Code != 400 {
			t.Errorf("非法输入应 400，实际 %d（%s）: %s", r.Code, body, r.Body)
		}
	}
}

func mustJSON(t *testing.T, raw string) string {
	t.Helper()
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
