package api

// 新增入站时自动建用户的行为约束（见 autouser.go）。
//
// 这一条很容易被「顺手优化」破坏：把自动补用户改成无条件执行，导入的用户就被
// 随机值顶掉了；把名字改回 user1，两个入站撞名后内核把它们当同一个人。
// 所以四种输入形态各钉一条。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"triune/internal/store"
)

func clientsOf(t *testing.T, a *Server) []map[string]any {
	t.Helper()
	cs, err := a.Store.ListClients()
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, c := range cs {
		out = append(out, map[string]any{"name": c.Name, "credential": c.Credential, "id": c.ID})
	}
	return out
}

// TestCreateInboundAutoCreatesUser 新增入站（请求里没提 clients）自动建一个用户。
//
// 不建的话：入站建好、开关打开、应用成功，界面一片绿，但导不出链接、订阅是空的，
// 用户完全看不出缺了什么。
func TestCreateInboundAutoCreatesUser(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	body := `{"name":"auto-01","core":"singbox","protocol":"vless","port":24601,
		"transport":"tcp","tls":"none","settings":"{}","enabled":false}`
	w := apiReq(t, h, tok, "POST", "/api/inbounds", body)
	if w.Code != http.StatusOK {
		t.Fatalf("建入站应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		ID         int64  `json:"id"`
		AutoClient string `json:"auto_client"`
		Users      int    `json:"users"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应解析失败: %v（%s）", err, w.Body.String())
	}
	if resp.AutoClient == "" || len(resp.AutoClient) != autoUserNameLen {
		t.Fatalf("应返回 %d 位自动用户名，实际 %q", autoUserNameLen, resp.AutoClient)
	}

	cs := clientsOf(t, a)
	if len(cs) != 1 {
		t.Fatalf("应自动建 1 个用户，实际 %d 个: %v", len(cs), cs)
	}
	if cs[0]["name"] != resp.AutoClient {
		t.Errorf("响应里的用户名与库里不一致: %q vs %q", resp.AutoClient, cs[0]["name"])
	}
	// vless 的凭据必须是 UUID（36 位带连字符），不能是密码
	cred, _ := cs[0]["credential"].(string)
	if len(cred) != 36 || strings.Count(cred, "-") != 4 {
		t.Errorf("vless 的自动凭据应是 UUID，实际 %q", cred)
	}
	// 必须绑到这条入站上，否则导不出任何链接
	ids, err := a.Store.ClientInboundIDs(cs[0]["id"].(int64))
	if err != nil || len(ids) != 1 || ids[0] != resp.ID {
		t.Errorf("用户应绑定新建的入站 %d，实际 %v（%v）", resp.ID, ids, err)
	}
	// 审计要能看出是谁被自动建出来的
	entries, _ := a.Store.ListAudit(store.AuditFilter{Limit: 20})
	found := false
	for _, e := range entries {
		if strings.Contains(e.Detail, resp.AutoClient) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("审计里应出现自动用户名 %q，实际 %+v", resp.AutoClient, entries)
	}
}

// TestCreateInboundKeepsImportedClients 导入带了用户：一律按导入信息建，不额外补。
func TestCreateInboundKeepsImportedClients(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	const uuid = "11111111-2222-3333-4444-555555555555"
	body := `{"name":"imp-01","core":"xray","protocol":"vless","port":24602,
		"transport":"tcp","tls":"none","settings":"{}","enabled":false,
		"clients":[{"name":"alice","credential":"` + uuid + `"}]}`
	w := apiReq(t, h, tok, "POST", "/api/inbounds", body)
	if w.Code != http.StatusOK {
		t.Fatalf("建入站应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		AutoClient string `json:"auto_client"`
		Users      int    `json:"users"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.AutoClient != "" {
		t.Errorf("导入已带用户时不该再自动建，实际建了 %q", resp.AutoClient)
	}
	cs := clientsOf(t, a)
	if len(cs) != 1 || cs[0]["name"] != "alice" || cs[0]["credential"] != uuid {
		t.Fatalf("应按导入信息建 alice（凭据不变），实际 %v", cs)
	}
}

// TestCreateInboundEmptyClientsMeansNoUsers 显式传空数组 = 这条入站不要用户。
//
// 对应界面上的「导入时取消了『一并创建用户』」：用户刚刚把勾去掉，后端不该
// 再替他补一个随机用户回来。
func TestCreateInboundEmptyClientsMeansNoUsers(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	body := `{"name":"none-01","core":"singbox","protocol":"vless","port":24603,
		"transport":"tcp","tls":"none","settings":"{}","enabled":false,"clients":[]}`
	if w := apiReq(t, h, tok, "POST", "/api/inbounds", body); w.Code != http.StatusOK {
		t.Fatalf("建入站应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	if cs := clientsOf(t, a); len(cs) != 0 {
		t.Fatalf("显式空 clients 不该建用户，实际 %v", cs)
	}
}

// TestCreateInboundSkipsUserlessProtocols 没有「用户」概念的协议不建用户。
//
// direct（端口转发）、wireguard（peers 不是用户）、免认证的 socks / http / mixed
// 都属于这一类：建出来的人在用户页里只是个看不懂的记录，内核也不会读它。
// 开了密码认证的 socks 则必须建——否则认证开着却没有任何账号，谁也连不上。
func TestCreateInboundSkipsUserlessProtocols(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantUser bool
	}{
		{"direct 端口转发",
			`{"name":"d1","core":"singbox","protocol":"direct","port":24610,"transport":"tcp",
			  "tls":"none","settings":"{\"override_address\":\"example.com\",\"override_port\":443}"}`, false},
		{"免认证 socks",
			`{"name":"s1","core":"singbox","protocol":"socks","port":24611,"transport":"tcp",
			  "tls":"none","settings":"{}"}`, false},
		{"开密码认证 socks",
			`{"name":"s2","core":"singbox","protocol":"socks","port":24612,"transport":"tcp",
			  "tls":"none","settings":"{\"account_auth\":\"password\"}"}`, true},
		{"wireguard（peers 不是用户）",
			`{"name":"w1","core":"xray","protocol":"wireguard","port":24613,"transport":"tcp",
			  "tls":"none","settings":"{\"peers\":[]}"}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := newAuthTestServer(t)
			tok := loginForToken(t, a)
			if w := apiReq(t, a.Handler(), tok, "POST", "/api/inbounds", c.body); w.Code != http.StatusOK {
				t.Fatalf("建入站应 200，实际 %d: %s", w.Code, w.Body.String())
			}
			cs := clientsOf(t, a)
			if got := len(cs) > 0; got != c.wantUser {
				t.Fatalf("应建用户=%v，实际 %d 个: %v", c.wantUser, len(cs), cs)
			}
			if c.wantUser {
				cred, _ := cs[0]["credential"].(string)
				if !strings.Contains(cred, ":") {
					t.Errorf("socks 认证账号应是「用户名:密码」，实际 %q", cred)
				}
			}
		})
	}
}

// TestAutoUserNamesAreDistinctAndShort 两条入站的自动用户名不能相同——
// 内核里同名用户等于同一个人（xray 的 email、sing-box 的 name 都要求唯一）。
func TestAutoUserNamesAreDistinctAndShort(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	for i, port := range []int{24620, 24621} {
		body := `{"name":"u-` + string(rune('a'+i)) + `","core":"singbox","protocol":"trojan","port":` +
			itoa64(int64(port)) + `,"transport":"tcp","tls":"none","settings":"{}"}`
		if w := apiReq(t, h, tok, "POST", "/api/inbounds", body); w.Code != http.StatusOK {
			t.Fatalf("建入站应 200，实际 %d: %s", w.Code, w.Body.String())
		}
	}
	cs := clientsOf(t, a)
	if len(cs) != 2 {
		t.Fatalf("应有两个用户，实际 %d", len(cs))
	}
	if cs[0]["name"] == cs[1]["name"] {
		t.Fatalf("两条入站的自动用户名撞了: %v", cs[0]["name"])
	}
	for _, c := range cs {
		name, _ := c["name"].(string)
		if len(name) != autoUserNameLen {
			t.Errorf("用户名应是 %d 位，实际 %q", autoUserNameLen, name)
		}
		for _, r := range name {
			if !strings.ContainsRune(autoUserNameAlphabet, r) {
				t.Errorf("用户名 %q 出现字母表外的字符 %q", name, r)
			}
			// 易混字符不进这批名字：它是要被人抄进客户端的
			if strings.ContainsRune("l1o0", r) {
				t.Errorf("用户名 %q 含易混字符 %q", name, r)
			}
		}
	}
}

// TestRandomUserNameCoverage 抽 200 次，检查长度与字符集（模偏置不会影响这个）。
func TestRandomUserNameCoverage(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		n := randomUserName()
		if len(n) != autoUserNameLen {
			t.Fatalf("长度应为 %d，实际 %d（%q）", autoUserNameLen, len(n), n)
		}
		for _, r := range n {
			if !strings.ContainsRune(autoUserNameAlphabet, r) {
				t.Fatalf("出现字母表外的字符 %q（%q）", r, n)
			}
		}
		seen[n] = true
	}
	if len(seen) < 190 {
		t.Errorf("200 次抽样只有 %d 个不同名字，随机性可疑", len(seen))
	}
}
