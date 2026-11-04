package api

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// 导不出来的节点不能静默消失。
//
// 现场：入站是 vless + xhttp（3x-ui 的常见组合），点「下载 Clash 配置」得到一个
// `proxies: []`、PROXY 组也为空的 YAML。mihomo 对空组是**硬拒绝**：
//
//	proxy group[0]: PROXY: `use` or `proxies` missing
//	configuration file ... test failed
//
// 于是用户拿到的是一个连加载都加载不了的文件，文件里一个字的原因都没有，
// 界面上只显示「已下载」。真正的原因（mihomo 的客户端节点里没有 xhttp 这种
// 传输）藏在 `if err == nil` 后面，谁都看不见。
//
// 现在：失败的节点与原因写进文件头；一个节点都没有时 PROXY 组里放 DIRECT
// 占位，让文件至少能加载（否则连看都看不了），同时明说「等于直连」。
func TestClashExportExplainsSkippedNodes(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	body := `{"name":"xh-01","core":"xray","protocol":"vless","listen":"127.0.0.1",
		"port":24561,"tag":"xh-01","transport":"xhttp","tls":"none","enabled":true,
		"settings":"{\"path\":\"/xh\"}","clients":[{"name":"u1"}]}`
	in := mkInbound(t, h, tok, body)

	w := apiReq(t, h, tok, "GET", "/api/inbounds/"+itoa64(in.ID)+"/export?format=clash", "")
	if w.Code != 200 {
		t.Fatalf("导出应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	out := w.Body.String()

	if !strings.Contains(out, "没能导出") {
		t.Errorf("节点被跳过却没在文件里说明\n%s", firstLines(out, 4))
	}
	if !strings.Contains(out, "xhttp") {
		t.Errorf("原因里应点名 xhttp 这个传输\n%s", firstLines(out, 4))
	}
	if !strings.Contains(out, "直连") {
		t.Errorf("必须说明「等于直连」，否则用户会以为代理在生效\n%s", firstLines(out, 4))
	}

	// 关键：PROXY 组的 proxies 里必须真有 DIRECT。只靠文件头的文字提到
	// DIRECT 不算——那只是注释，mihomo 看的是组里的列表，空组直接拒收。
	doc := parseClash(t, out)
	if len(doc.Proxies) != 0 {
		t.Errorf("xhttp 节点不该被转成 proxy，实际 %d 个", len(doc.Proxies))
	}
	if len(doc.Groups) != 1 {
		t.Fatalf("应只有一个 PROXY 组，实际 %d", len(doc.Groups))
	}
	if got := doc.Groups[0].Proxies; len(got) != 1 || got[0] != "DIRECT" {
		t.Errorf("PROXY 组里应是 [DIRECT] 占位（否则 mihomo 拒收整份配置），实际 %v", got)
	}
}

// clashDoc 只解析我们关心的两段。
type clashDoc struct {
	Proxies []struct {
		Name string `yaml:"name"`
		Type string `yaml:"type"`
	} `yaml:"proxies"`
	Groups []struct {
		Name    string   `yaml:"name"`
		Type    string   `yaml:"type"`
		Proxies []string `yaml:"proxies"`
	} `yaml:"proxy-groups"`
	Rules []string `yaml:"rules"`
}

func parseClash(t *testing.T, out string) clashDoc {
	t.Helper()
	var doc clashDoc
	if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("导出的不是合法 YAML：%v\n%s", err, firstLines(out, 6))
	}
	return doc
}

// TestClashExportNormalHasNoPlaceholder 能正常转换时不该出现占位与噪音。
func TestClashExportNormalHasNoPlaceholder(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	body := `{"name":"ws-01","core":"xray","protocol":"vless","listen":"127.0.0.1",
		"port":24562,"tag":"ws-01","transport":"ws","tls":"none","enabled":true,
		"settings":"{\"path\":\"/ray\"}","clients":[{"name":"u1"}]}`
	in := mkInbound(t, h, tok, body)

	out := apiReq(t, h, tok, "GET", "/api/inbounds/"+itoa64(in.ID)+"/export?format=clash", "").Body.String()
	if strings.Contains(out, "⚠") {
		t.Errorf("一切正常时不该有警告注释（否则就成了「总在响」的警告）\n%s", firstLines(out, 4))
	}
	doc := parseClash(t, out)
	if len(doc.Proxies) != 1 || doc.Proxies[0].Type != "vless" {
		t.Fatalf("应导出 1 个 vless 节点，实际 %+v", doc.Proxies)
	}
	if len(doc.Groups) != 1 || len(doc.Groups[0].Proxies) != 1 || doc.Groups[0].Proxies[0] != doc.Proxies[0].Name {
		t.Errorf("PROXY 组应只引用这个节点，实际 %v", doc.Groups)
	}
	if !strings.Contains(out, "ws-opts") {
		t.Errorf("ws 节点应带 ws-opts\n%s", out)
	}
}

// TestExportSummaryListsSkipped 弹窗在下载前问的就是这个接口。
func TestExportSummaryListsSkipped(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	body := `{"name":"xh-02","core":"xray","protocol":"vless","listen":"127.0.0.1",
		"port":24563,"tag":"xh-02","transport":"xhttp","tls":"none","enabled":true,
		"settings":"{\"path\":\"/xh\"}","clients":[{"name":"u1"}]}`
	in := mkInbound(t, h, tok, body)

	w := apiReq(t, h, tok, "GET", "/api/inbounds/"+itoa64(in.ID)+"/export?format=summary", "")
	if w.Code != 200 {
		t.Fatalf("summary 应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var s struct {
		Nodes int `json:"nodes"`
		Clash struct {
			Skipped []struct {
				Name   string `json:"name"`
				Reason string `json:"reason"`
			} `json:"skipped"`
		} `json:"clash"`
		Singbox struct {
			Skipped []struct {
				Name string `json:"name"`
			} `json:"skipped"`
		} `json:"singbox"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		t.Fatalf("summary 不是合法 JSON：%v", err)
	}
	if s.Nodes != 1 {
		t.Errorf("应有 1 个节点，实际 %d", s.Nodes)
	}
	if len(s.Clash.Skipped) != 1 {
		t.Fatalf("clash 应报 1 个跳过，实际 %d：%s", len(s.Clash.Skipped), w.Body.String())
	}
	if !strings.Contains(s.Clash.Skipped[0].Reason, "xhttp") {
		t.Errorf("原因里应点名 xhttp，实际 %q", s.Clash.Skipped[0].Reason)
	}
	// 原因不该带哨兵前缀（"传输无法导出为客户端节点: ..."），那句话对用户没信息量
	if strings.Contains(s.Clash.Skipped[0].Reason, "传输无法导出为客户端节点") {
		t.Errorf("原因应去掉哨兵前缀，实际 %q", s.Clash.Skipped[0].Reason)
	}
	if len(s.Singbox.Skipped) != 1 {
		t.Errorf("sing-box 侧同样转不出来，应报 1 个跳过，实际 %d", len(s.Singbox.Skipped))
	}
}

// TestExportSummaryCountsZeroNodes 没有用户时 summary 要能说清「0 个节点」。
func TestExportSummaryCountsZeroNodes(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	body := `{"name":"nouser-01","core":"xray","protocol":"vless","listen":"127.0.0.1",
		"port":24564,"tag":"nouser-01","transport":"tcp","tls":"none","enabled":true,
		"settings":"{}"}`
	in := mkInbound(t, h, tok, body)

	w := apiReq(t, h, tok, "GET", "/api/inbounds/"+itoa64(in.ID)+"/export?format=summary", "")
	if !strings.Contains(w.Body.String(), `"nodes":0`) {
		t.Errorf("没有用户时 nodes 应为 0，实际 %s", w.Body.String())
	}

	// Clash 文件仍然要能加载（DIRECT 占位），并说明为什么没有节点
	out := apiReq(t, h, tok, "GET", "/api/inbounds/"+itoa64(in.ID)+"/export?format=clash", "").Body.String()
	if !strings.Contains(out, "DIRECT") || !strings.Contains(out, "没有有效用户") {
		t.Errorf("零节点时应给占位并说明原因\n%s", firstLines(out, 4))
	}
}
