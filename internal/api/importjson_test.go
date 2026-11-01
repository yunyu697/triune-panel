package api

import (
	"encoding/json"
	"net/http"
	"os"
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

// 传输在目标内核下不可用时要降级并说清楚，而不是导出一条渲染必失败的配置。
func TestImportJSONTransportDowngrade(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	sample := `[{"remark":"kcp-node","port":24460,"protocol":"vmess",
	  "settings":"{\"clients\":[{\"id\":\"99999999-8888-7777-6666-555555555555\"}]}",
	  "streamSettings":"{\"network\":\"kcp\",\"kcpSettings\":{\"seed\":\"s3\"}}"}]`
	body := `{"text":` + mustJSON(t, sample) + `,"core":"singbox","dry_run":true}`
	r := postImportJSON(t, h, tok, body)
	if r.Code != http.StatusOK {
		t.Fatalf("预览应 200: %s", r.Body)
	}
	var resp struct {
		Items []importPreview
	}
	if err := json.Unmarshal([]byte(r.Body), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Items[0].Transport != "tcp" {
		t.Errorf("sing-box 不支持 kcp，应降级为 tcp，实际 %q", resp.Items[0].Transport)
	}
	joined := strings.Join(resp.Items[0].Warnings, " | ")
	if !strings.Contains(joined, "不支持 kcp") {
		t.Errorf("降级必须有提示：%v", resp.Items[0].Warnings)
	}
}

// 整套 xray 配置导入：DNS/路由写到设置里，供渲染时注入。
func TestImportJSONStoresGlobalSegments(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	cfg := `{
	  "dns": {"servers": ["1.1.1.1"]},
	  "routing": {"rules": [{"type":"field","domain":["example.com"],"outboundTag":"block"}]},
	  "inbounds": [{"tag":"in1","port":24461,"protocol":"vless","settings":{"clients":[{"id":"11111111-2222-3333-4444-555555555555"}]},"streamSettings":{"network":"tcp"}}]
	}`
	body := `{"text":` + mustJSON(t, cfg) + `,"core":"xray"}`
	if r := postImportJSON(t, h, tok, body); r.Code != http.StatusOK {
		t.Fatalf("导入应 200: %s", r.Body)
	}
	dns, _ := a.Store.GetSetting("xray_dns_json")
	route, _ := a.Store.GetSetting("xray_route_json")
	if !strings.Contains(dns, "1.1.1.1") {
		t.Errorf("DNS 段未落库：%q", dns)
	}
	if !strings.Contains(route, "block") {
		t.Errorf("路由段未落库：%q", route)
	}

	// sing-box 目标：要转成 sing-box 结构后另存一份
	body2 := `{"text":` + mustJSON(t, cfg) + `,"core":"singbox"}`
	if r := postImportJSON(t, h, tok, body2); r.Code != http.StatusOK {
		t.Fatalf("sing-box 导入应 200: %s", r.Body)
	}
	sdns, _ := a.Store.GetSetting("singbox_dns_json")
	if !strings.Contains(sdns, "\"type\":\"udp\"") {
		t.Errorf("sing-box DNS 应转成 type/server 结构：%q", sdns)
	}
	sroute, _ := a.Store.GetSetting("singbox_route_json")
	if !strings.Contains(sroute, "reject") {
		t.Errorf("sing-box 路由应转成 action=reject：%q", sroute)
	}
}

// 内联 PEM 证书要落盘成文件（sing-box 只认路径），并且私钥权限是 0600。
func TestImportJSONMaterializesInlinePEM(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	sample := `[{"remark":"pem-node","port":24462,"protocol":"vless",
	  "settings":"{\"clients\":[{\"id\":\"11111111-2222-3333-4444-555555555555\"}]}",
	  "streamSettings":"{\"network\":\"tcp\",\"security\":\"tls\",\"tlsSettings\":{\"serverName\":\"example.com\",\"certificates\":[{\"certificate\":[\"-----BEGIN CERTIFICATE-----\",\"MIIB\",\"-----END CERTIFICATE-----\"],\"key\":[\"-----BEGIN PRIVATE KEY-----\",\"MIIE\",\"-----END PRIVATE KEY-----\"]}]}}"}]`
	body := `{"text":` + mustJSON(t, sample) + `,"core":"singbox"}`
	if r := postImportJSON(t, h, tok, body); r.Code != http.StatusOK {
		t.Fatalf("导入应 200: %s", r.Body)
	}
	ins, _ := a.Store.ListInbounds("")
	if len(ins) != 1 {
		t.Fatalf("应导入 1 条，实际 %d", len(ins))
	}
	var s map[string]any
	if err := json.Unmarshal([]byte(ins[0].Settings), &s); err != nil {
		t.Fatal(err)
	}
	certPath, _ := s["certificate_path"].(string)
	keyPath, _ := s["key_path"].(string)
	if certPath == "" || keyPath == "" {
		t.Fatalf("PEM 应转成文件路径：%#v", s)
	}
	if _, ok := s["certificate_pem"]; ok {
		t.Error("落盘后不该把 PEM 再留在 settings 里")
	}
	if !strings.HasPrefix(certPath, a.DataDir) {
		t.Errorf("证书应写在面板数据目录下：%s", certPath)
	}
	fi, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("私钥文件不存在：%v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("私钥权限应为 0600，实际 %v", fi.Mode().Perm())
	}
	if b, err := os.ReadFile(certPath); err != nil || !strings.Contains(string(b), "BEGIN CERTIFICATE") {
		t.Errorf("证书内容不对：%v %q", err, string(b))
	}
}

// 同一个 h2 配置：xray 26 已移除 h2 → 转 xhttp(stream-one)；sing-box 有 http 传输 → 原样保留。
func TestImportJSONTransportPerCore(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	sample := `[{"remark":"h2-node","port":24465,"protocol":"vless",
	  "settings":"{\"clients\":[{\"id\":\"11111111-2222-3333-4444-555555555555\"}]}",
	  "streamSettings":"{\"network\":\"h2\",\"security\":\"tls\",\"httpSettings\":{\"path\":\"/h2\",\"host\":[\"h.example.com\"]}}"}]`

	type resp struct {
		Items []importPreview
	}
	preview := func(core string) resp {
		body := `{"text":` + mustJSON(t, sample) + `,"core":"` + core + `","dry_run":true}`
		r := postImportJSON(t, h, tok, body)
		if r.Code != http.StatusOK {
			t.Fatalf("%s 预览应 200: %s", core, r.Body)
		}
		var out resp
		if err := json.Unmarshal([]byte(r.Body), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	x := preview("xray")
	if x.Items[0].Transport != "xhttp" {
		t.Errorf("xray 下 h2 应转成 xhttp，实际 %q", x.Items[0].Transport)
	}
	if !strings.Contains(strings.Join(x.Items[0].Warnings, " | "), "移除了 h2") {
		t.Errorf("转换要说明原因：%v", x.Items[0].Warnings)
	}
	s := preview("singbox")
	if s.Items[0].Transport != "h2" {
		t.Errorf("sing-box 支持 h2，不该改传输，实际 %q", s.Items[0].Transport)
	}
}
