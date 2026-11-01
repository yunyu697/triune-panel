package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// parseOne 发一次 parse-json 请求并解出第一条。
func parseOne(t *testing.T, h http.Handler, tok, sample, core string) (fillItem, string) {
	t.Helper()
	body := `{"text":` + mustJSON(t, sample) + `,"core":"` + core + `"}`
	w := apiReq(t, h, tok, "POST", "/api/inbounds/parse-json", body)
	if w.Code != http.StatusOK {
		t.Fatalf("解析应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Core          string     `json:"core"`
		Items         []fillItem `json:"items"`
		GlobalIgnored string     `json:"global_ignored"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) == 0 {
		t.Fatalf("没有解析到入站: %s", w.Body.String())
	}
	return resp.Items[0], resp.GlobalIgnored
}

const singleRealitySample = `{
  "remark": "hk-reality",
  "port": 24443,
  "protocol": "vless",
  "settings": "{\"clients\":[{\"id\":\"11111111-2222-3333-4444-555555555555\",\"email\":\"alice\",\"flow\":\"xtls-rprx-vision\",\"totalGB\":1073741824}],\"decryption\":\"none\"}",
  "streamSettings": "{\"network\":\"tcp\",\"security\":\"reality\",\"realitySettings\":{\"dest\":\"www.apple.com:443\",\"serverNames\":[\"www.apple.com\"],\"privateKey\":\"PRIVKEY\",\"shortIds\":[\"ab12\",\"\"]}}",
  "tag": "in-24443"
}`

// TestParseJSONFillsEveryField：字段要能直接回填表单，而不是再让人对着
// 预览表格抄一遍——这正是这次把 JSON 导入搬进「新增入站」的原因。
func TestParseJSONFillsEveryField(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	it, _ := parseOne(t, h, tok, singleRealitySample, "xray")
	if it.Skip {
		t.Fatalf("这条不该被跳过: %v", it.Warnings)
	}
	if it.Name != "hk-reality" || it.Protocol != "vless" || it.Port != 24443 {
		t.Fatalf("基础字段不对: %+v", it)
	}
	if it.Transport != "tcp" || it.TLS != "reality" {
		t.Fatalf("传输/TLS 不对: %s / %s", it.Transport, it.TLS)
	}
	if got := it.Settings["handshake_server"]; got != "www.apple.com" {
		t.Errorf("handshake_server = %v", got)
	}
	if got := it.Settings["private_key"]; got != "PRIVKEY" {
		t.Errorf("private_key = %v", got)
	}
	if got := it.Settings["flow"]; got != "xtls-rprx-vision" {
		t.Errorf("flow = %v", got)
	}
	// shortIds 必须带空串：xray 的 "" 是合法短 ID，丢了它客户端会连不上
	sids, ok := it.Settings["short_ids"].([]any)
	if !ok || len(sids) != 2 || sids[1] != "" {
		t.Errorf("short_ids = %#v（应保留空串）", it.Settings["short_ids"])
	}
	if len(it.Clients) != 1 {
		t.Fatalf("用户数 = %d", len(it.Clients))
	}
	c := it.Clients[0]
	if c.Credential != "11111111-2222-3333-4444-555555555555" || c.Auto {
		t.Errorf("已有的 UUID 应原样带出，实际 %+v", c)
	}
	if c.Kind != "UUID" {
		t.Errorf("凭据类型 = %q", c.Kind)
	}
	if c.Name != "alice" || c.QuotaGB != 1 {
		t.Errorf("用户名/配额不对: %+v", c)
	}
}

// TestParseJSONAutoUserWhenNoClients：JSON 里没有用户时自动补一个——
// 否则用户填完表点保存，会得到一个谁都连不上的空入站。
func TestParseJSONAutoUserWhenNoClients(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	sample := `{"remark":"trojan-1","port":24444,"protocol":"trojan",
	  "settings":"{}","streamSettings":"{\"network\":\"tcp\",\"security\":\"tls\",\"tlsSettings\":{\"serverName\":\"t.example.com\"}}"}`
	it, _ := parseOne(t, h, tok, sample, "xray")
	if len(it.Clients) != 1 {
		t.Fatalf("应自动补 1 个用户，实际 %d", len(it.Clients))
	}
	c := it.Clients[0]
	if !c.Auto {
		t.Error("自动补的用户应标记 auto")
	}
	if c.Credential == "" || len(c.Credential) < 16 {
		t.Errorf("密码太短: %q", c.Credential)
	}
	// trojan 要的是密码，塞 UUID 进去内核会拒绝
	if c.Kind != "密码" {
		t.Errorf("凭据类型 = %q，应为密码", c.Kind)
	}
	if c.Name != "trojan-1-user1" {
		t.Errorf("默认用户名 = %q", c.Name)
	}
	// 既然已经自动补了用户，就不能再保留「需要手动加用户」的提示
	for _, w := range it.Warnings {
		if strings.Contains(w, "没有客户端") {
			t.Errorf("已自动建用户，不该再提示「没有客户端」: %v", it.Warnings)
		}
	}
}

// TestParseJSONAutoUUIDForVLESS：vless/vmess 生成的必须是合法 UUID 形态。
func TestParseJSONAutoUUIDForVLESS(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	sample := `{"remark":"v1","port":24445,"protocol":"vless","settings":"{\"clients\":[]}"}`
	it, _ := parseOne(t, h, tok, sample, "xray")
	if len(it.Clients) != 1 || !it.Clients[0].Auto {
		t.Fatalf("应自动补 1 个用户: %+v", it.Clients)
	}
	cred := it.Clients[0].Credential
	if len(cred) != 36 || cred[8] != '-' || cred[13] != '-' || cred[18] != '-' || cred[23] != '-' {
		t.Errorf("不是 UUID 形态: %q", cred)
	}
}

// TestParseJSONNoUserForForwarding：端口转发没有用户概念，不该硬塞一个。
func TestParseJSONNoUserForForwarding(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	sample := `{"remark":"dd","port":24446,"protocol":"dokodemo-door",
	  "settings":"{\"address\":\"1.1.1.1\",\"port\":53,\"network\":\"tcp,udp\"}"}`
	it, _ := parseOne(t, h, tok, sample, "xray")
	if len(it.Clients) != 0 {
		t.Errorf("端口转发不该有用户: %+v", it.Clients)
	}
	if it.Settings["dd_address"] != "1.1.1.1" {
		t.Errorf("dd_address = %v", it.Settings["dd_address"])
	}
}

// TestParseJSONAdaptsToCore：h2 在 xray 26 下已移除，填表时就该是 xhttp，
// 而不是让人保存之后才发现内核起不来。
func TestParseJSONAdaptsToCore(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	sample := `{"remark":"h2-node","port":24447,"protocol":"vmess",
	  "settings":"{\"clients\":[{\"id\":\"99999999-8888-7777-6666-555555555555\"}]}",
	  "streamSettings":"{\"network\":\"h2\",\"h2Settings\":{\"path\":\"/h2\"}}"}`
	it, _ := parseOne(t, h, tok, sample, "xray")
	if it.Transport != "xhttp" {
		t.Fatalf("xray 下 h2 应转成 xhttp，实际 %q", it.Transport)
	}
	if it.Settings["xhttp_mode"] != "stream-one" {
		t.Errorf("xhttp_mode = %v", it.Settings["xhttp_mode"])
	}
	if it.Settings["path"] != "/h2" {
		t.Errorf("path 丢了: %v", it.Settings["path"])
	}
	if len(it.Warnings) == 0 {
		t.Error("转换必须带提示：客户端也要改，否则连不上")
	}
}

// TestParseJSONGlobalIgnored：只解析入站，但必须说清楚全局段没搬。
func TestParseJSONGlobalIgnored(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	sample := `{"inbounds":[` + singleRealitySample + `],
	  "dns":{"servers":["1.1.1.1","8.8.8.8"]},
	  "routing":{"rules":[{"type":"field","outboundTag":"direct","domain":["example.com"]}]}}`
	it, note := parseOne(t, h, tok, sample, "xray")
	if it.Protocol != "vless" {
		t.Fatalf("应取 inbounds 里的入站: %+v", it)
	}
	if note == "" {
		t.Error("JSON 里有 DNS/路由时应提示「未导入」")
	}
}

// TestParseJSONMultipleItems：多条时全部回传，由前端让用户挑一条填入。
func TestParseJSONMultipleItems(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	body := `{"text":` + mustJSON(t, importJSONSample) + `,"core":"xray"}`
	w := apiReq(t, h, tok, "POST", "/api/inbounds/parse-json", body)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []fillItem `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("应回传 2 条，实际 %d", len(resp.Items))
	}
	if resp.Items[0].Name == resp.Items[1].Name {
		t.Error("两条入站应各有名字，便于前端区分")
	}
}

// TestParseJSONRejectsGarbage：错误要能直接给用户看。
func TestParseJSONRejectsGarbage(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	for _, s := range []string{"", "not json", "SQLite format 3\u0000"} {
		body := `{"text":` + mustJSON(t, s) + `,"core":"xray"}`
		w := apiReq(t, h, tok, "POST", "/api/inbounds/parse-json", body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%q 应 400，实际 %d", s, w.Code)
		}
	}
}

// TestCreateInboundWithClients：保存入站时顺带建用户并绑定。
//
// 分两个请求建会出现「入站建好了、用户没建上」——面板上看起来和成功
// 一模一样，只是没人能连上。
func TestCreateInboundWithClients(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	body := `{"name":"vless-fill","core":"xray","protocol":"vless","port":24450,
	  "transport":"tcp","tls":"none","settings":"{}",
	  "clients":[{"name":"u1","credential":"11111111-2222-3333-4444-555555555555"},
	             {"name":"u2","credential":"11111111-2222-3333-4444-555555555555"},
	             {"name":"u3"}]}`
	w := apiReq(t, h, tok, "POST", "/api/inbounds", body)
	if w.Code != http.StatusOK {
		t.Fatalf("建入站应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var in struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &in); err != nil {
		t.Fatal(err)
	}

	w = apiReq(t, h, tok, "GET", "/api/clients", "")
	if w.Code != http.StatusOK {
		t.Fatalf("列用户失败: %s", w.Body.String())
	}
	var clients []struct {
		Client struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			Credential string `json:"credential"`
		} `json:"client"`
		InboundIDs []int64 `json:"inbound_ids"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &clients); err != nil {
		t.Fatal(err)
	}
	// 重复凭据只建一次 + 空凭据自动补 → 2 个
	if len(clients) != 2 {
		t.Fatalf("应建 2 个用户（重复凭据去重），实际 %d: %s", len(clients), w.Body.String())
	}
	for _, c := range clients {
		if len(c.InboundIDs) != 1 || c.InboundIDs[0] != in.ID {
			t.Errorf("用户 %s 未绑定到新入站: %v", c.Client.Name, c.InboundIDs)
		}
		if c.Client.Credential == "" {
			t.Errorf("用户 %s 没凭据", c.Client.Name)
		}
	}
}
