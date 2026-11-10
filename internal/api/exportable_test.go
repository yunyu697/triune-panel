package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// 本地代理协议（mixed / socks / http）不能导出为客户端节点。
//
// 用户视角的复现路径：本机装了内核，建了个 socks 入站（很自然的想法——
// 「本机先开个端口试试」），然后在「配置生成」里点生成、点应用，
// 内核回一句 `proxy group[0]: Auto: 'use' or 'proxies' missing`。
// 面板只把内核那句原样抛出来，用户根本看不出问题出在**协议选错了**。
//
// 根因在生成器：`if p, err := link.ToClash(n); err == nil { append }`，
// 转换失败就静默跳过。socks / mixed / http 在 mihomo 与 sing-box 里都只有
// inbound 侧实现，没有 proxy / outbound 类型，于是全被跳过 →
// `proxies: []` + 空 proxy-group → 内核拒绝。
//
// 这里钉三件事：一个都导不出时**不生成**（422 + 说清原因）；部分导不出时
// 进 warnings；订阅端点同理，别把一份空 proxies 的 YAML 发给客户端。

// exportFixture 建若干个入站 + 一个用户，把用户绑到全部入站上，返回用户 ID。
func exportFixture(t *testing.T, a *Server, tok string, ins ...string) int64 {
	t.Helper()
	h := a.Handler()
	ids := make([]int64, 0, len(ins))
	for i, body := range ins {
		// 显式传空 clients：这些用例断言的是「导出怎么处理用户与入站」，
		// 自己会建 alice。不传的话后端会给每条入站自动补一个随机用户
		// （新增入站的默认行为，见 autouser.go），节点数就不是用例算的那样了。
		body = strings.TrimSuffix(strings.TrimSpace(body), "}") + `,"clients":[]}`
		in := mkInbound(t, h, tok, body)
		if in.ID == 0 {
			t.Fatalf("第 %d 个入站没拿到 ID: %s", i, body)
		}
		ids = append(ids, in.ID)
	}
	w := apiReq(t, h, tok, "POST", "/api/clients", `{"name":"alice"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("建用户失败 %d: %s", w.Code, w.Body.String())
	}
	var c struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	bind := fmt.Sprintf(`{"inbound_ids":%s}`, jsonIDs(ids))
	if w := apiReq(t, h, tok, "PUT",
		fmt.Sprintf("/api/clients/%d/inbounds", c.ID), bind); w.Code != http.StatusOK {
		t.Fatalf("绑定入站失败 %d: %s", w.Code, w.Body.String())
	}
	return c.ID
}

func jsonIDs(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprint(id)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// TestExportRejectsUnexportableSingbox socks 入站 + sing-box 核心 → 422。
func TestExportRejectsUnexportableSingbox(t *testing.T) {
	a := newAuthTestServer(t)
	tok := loginForToken(t, a)
	exportFixture(t, a, tok,
		`{"name":"local-socks","core":"singbox","protocol":"socks","port":24501,"tag":"ls","enabled":true}`)

	w := apiReq(t, a.Handler(), tok, "POST", "/api/export/config",
		`{"core":"singbox","target":"client","inbound_ids":[1],"scenario":"balanced","core_version":"1.14"}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("socks 入站应 422 拒绝生成，实际 %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	// 文案得同时回答「为什么」和「怎么办」，否则用户还是只能去猜
	for _, want := range []string{"socks", "本地代理协议", "vless"} {
		if !strings.Contains(body, want) {
			t.Errorf("拒绝文案缺少 %q，实际: %s", want, body)
		}
	}
	// 绝不能返回一份能让人贴进内核的空配置
	if strings.Contains(body, `"content"`) {
		t.Errorf("拒绝时不应带 content 字段，实际: %s", body)
	}
}

// TestExportRejectsUnexportableMihomo mixed 入站 + mihomo 核心 → 422。
// 用户当时踩的就是这条：mihomo 的 mixed 是最常见的本机监听方式。
func TestExportRejectsUnexportableMihomo(t *testing.T) {
	a := newAuthTestServer(t)
	tok := loginForToken(t, a)
	if w := apiReq(t, a.Handler(), tok, "PUT", "/api/settings",
		`{"show_compat_core":"1"}`); w.Code != http.StatusOK {
		t.Fatalf("打开兼容内核开关失败 %d: %s", w.Code, w.Body.String())
	}
	exportFixture(t, a, tok,
		`{"name":"local-mixed","core":"mihomo","protocol":"mixed","port":24502,"tag":"lm","enabled":true}`)

	w := apiReq(t, a.Handler(), tok, "POST", "/api/export/config",
		`{"core":"mihomo","target":"client","inbound_ids":[1],"scenario":"balanced"}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("mixed 入站应 422 拒绝生成，实际 %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "mixed") ||
		!strings.Contains(w.Body.String(), "mihomo") {
		t.Errorf("拒绝文案应点名协议与内核，实际: %s", w.Body.String())
	}
}

// TestExportWarnsOnPartialSkip 一好一坏时照常生成，但跳过的协议必须进 warnings。
// 静默丢节点比报错更难查——用户会以为「我明明选了那个入站」。
func TestExportWarnsOnPartialSkip(t *testing.T) {
	a := newAuthTestServer(t)
	tok := loginForToken(t, a)
	exportFixture(t, a, tok,
		`{"name":"hk","core":"singbox","protocol":"vless","port":24503,"tag":"hk","enabled":true,
		  "transport":"ws","tls":"tls","settings":"{\"path\":\"/ray\",\"server_name\":\"a.example.com\"}"}`,
		`{"name":"local-socks","core":"singbox","protocol":"socks","port":24504,"tag":"ls","enabled":true}`)

	w := apiReq(t, a.Handler(), tok, "POST", "/api/export/config",
		`{"core":"singbox","target":"client","inbound_ids":[1,2],"scenario":"balanced","core_version":"1.14"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("有可导出节点时应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		Content  string   `json:"content"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got.Warnings, " | ")
	if !strings.Contains(joined, "socks") {
		t.Errorf("warnings 应点名被跳过的 socks，实际: %s", joined)
	}
	// vless 那个必须还在
	if !strings.Contains(got.Content, `"vless"`) {
		t.Errorf("可导出的 vless 节点不应丢失，实际配置: %s", got.Content)
	}
}

// TestExportDedupesNodeNames 同名入站 / 同名用户不能让节点名撞车。
//
// 入站只校验 tag 唯一、用户只校验 ID，名字都可以重复。而节点名是
// `入站名-用户名`，在客户端配置里就是 tag —— 重名会被内核直接判死：
//
//	sing-box  FATAL: duplicate outbound/endpoint tag: hk-01-bob
//	mihomo    proxy hk-01-bob is the duplicate name
//
// 这跟「本地代理协议导不出去」是同一类症状（用户只看到「内核拒绝这份配置」），
// 但根因不同，所以单独钉一条。
func TestExportDedupesNodeNames(t *testing.T) {
	a := newAuthTestServer(t)
	tok := loginForToken(t, a)
	// 两个同名入站，绑同一个用户
	exportFixture(t, a, tok,
		`{"name":"hk","core":"singbox","protocol":"vless","port":24511,"tag":"hk-a","enabled":true,
		  "transport":"ws","tls":"tls","settings":"{\"path\":\"/a\",\"server_name\":\"a.example.com\"}"}`,
		`{"name":"hk","core":"singbox","protocol":"vless","port":24512,"tag":"hk-b","enabled":true,
		  "transport":"ws","tls":"tls","settings":"{\"path\":\"/b\",\"server_name\":\"a.example.com\"}"}`)

	w := apiReq(t, a.Handler(), tok, "POST", "/api/export/config",
		`{"core":"singbox","target":"client","inbound_ids":[1,2],"scenario":"balanced","core_version":"1.14"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Outbounds []struct {
			Tag  string `json:"tag"`
			Type string `json:"type"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(got.Content), &doc); err != nil {
		t.Fatalf("配置解析失败: %v", err)
	}
	seen := map[string]bool{}
	n := 0
	for _, o := range doc.Outbounds {
		if o.Type != "vless" {
			continue
		}
		n++
		if seen[o.Tag] {
			t.Errorf("tag 重复: %q（内核会直接拒绝整份配置）", o.Tag)
		}
		seen[o.Tag] = true
	}
	if n != 2 {
		t.Errorf("应有两个 vless 节点，实际 %d: %s", n, got.Content)
	}
}

// TestSubClashSkipsLocalProxyAndKeepsGroupConsistent 订阅端点两个 bug 一起钉：
// ① 全是本地代理协议时不能发一份空 proxies 的 YAML；
// ② proxy-group 里的名字必须都在 proxies 里，否则 mihomo 报 `proxy 'x' not found`。
func TestSubClashSkipsLocalProxyAndKeepsGroupConsistent(t *testing.T) {
	a := newAuthTestServer(t)
	tok := loginForToken(t, a)

	// 先只建 socks：订阅应给出可读错误，而不是一份坏配置
	cid := exportFixture(t, a, tok,
		`{"name":"local-socks","core":"singbox","protocol":"socks","port":24505,"tag":"ls","enabled":true}`)
	cl, err := a.Store.GetClient(cid)
	if err != nil {
		t.Fatal(err)
	}
	w := apiReq(t, a.Handler(), "", "GET", "/sub/"+cl.SubToken+"?target=clash", "")
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("全是本地代理协议时订阅应 422，实际 %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "socks") {
		t.Errorf("订阅错误应点名协议，实际: %s", w.Body.String())
	}

	// 再补一个 vless：这次能出 YAML，且 group 名字与 proxies 对得上
	in := mkInbound(t, a.Handler(), tok,
		`{"name":"hk","core":"singbox","protocol":"vless","port":24506,"tag":"hk","enabled":true,
		  "transport":"ws","tls":"tls","settings":"{\"path\":\"/ray\",\"server_name\":\"a.example.com\"}"}`)
	if w := apiReq(t, a.Handler(), tok, "PUT", fmt.Sprintf("/api/clients/%d/inbounds", cid),
		fmt.Sprintf(`{"inbound_ids":[1,%d]}`, in.ID)); w.Code != http.StatusOK {
		t.Fatalf("重新绑定失败 %d: %s", w.Code, w.Body.String())
	}
	w = apiReq(t, a.Handler(), "", "GET", "/sub/"+cl.SubToken+"?target=clash", "")
	if w.Code != http.StatusOK {
		t.Fatalf("有可导出节点时订阅应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var doc struct {
		Proxies []struct {
			Name string `yaml:"name"`
		} `yaml:"proxies"`
		ProxyGroups []struct {
			Name    string   `yaml:"name"`
			Proxies []string `yaml:"proxies"`
		} `yaml:"proxy-groups"`
	}
	if err := yaml.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("订阅 YAML 解析失败: %v\n%s", err, w.Body.String())
	}
	if len(doc.Proxies) != 1 {
		t.Fatalf("proxies 应只有 vless 一个，实际 %d: %s", len(doc.Proxies), w.Body.String())
	}
	have := map[string]bool{}
	for _, p := range doc.Proxies {
		have[p.Name] = true
	}
	if len(doc.ProxyGroups) == 0 {
		t.Fatal("缺少 proxy-groups")
	}
	for _, g := range doc.ProxyGroups {
		for _, n := range g.Proxies {
			if !have[n] {
				t.Errorf("group %q 引用了不存在的 proxy %q（mihomo 会直接报错）", g.Name, n)
			}
		}
	}
}
