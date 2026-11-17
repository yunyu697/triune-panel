package api

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"triune/internal/store"
)

// ==================== 类别表 / 体检脚本 / 规则漂移 ====================

// bash 3.2 会把手写的中文紧跟在 $var 后面当成变量名的一部分：
// "$ip（直连）" 会被读成变量 ip（直连），展开成空，屏幕上只剩一个括号。
// 这个坑第一次跑脚本就踩了（IP 整段消失，看起来像解析失败）。
// 机制上钉住：模板里任何变量引用后面紧跟多字节字符，必须写成 ${var}。
func TestHealthCheckScriptBracesVarsBeforeCJK(t *testing.T) {
	s := healthCheckScript()
	isName := func(c byte) bool {
		return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
	}
	for i := 0; i < len(s); i++ {
		if s[i] != '$' || i+1 >= len(s) {
			continue
		}
		if s[i+1] == '{' || s[i+1] == '(' {
			continue // ${var} / $(cmd) 本来就安全
		}
		j := i + 1
		for j < len(s) && isName(s[j]) {
			j++
		}
		if j == i+1 {
			continue // 裸 $，不是变量引用
		}
		if j < len(s) && s[j] >= 0x80 {
			line := strings.SplitN(s[i:j+4], "\n", 2)[0]
			t.Errorf("变量引用后面紧跟中文且没写花括号（bash 3.2 会展开成空）：%q", line)
		}
	}
}

// 体检脚本里的探针必须真的在清单里。
//
// 这是这套机制最容易腐化的地方：改了 systemDirectRules（删掉某个域名、
// 换成更细的子域）却忘了改探针，脚本就会去测一条已经不存在的规则——
// 通过与否都不再说明任何问题，而且没人会发现。
func TestSystemClassProbesAreInRuleLists(t *testing.T) {
	if len(systemClasses) == 0 {
		t.Fatal("systemClasses 为空：体检与覆盖提示都会失去依据")
	}
	sawDirect, sawProxy := 0, 0
	for _, c := range systemClasses {
		switch c.Expect {
		case "direct":
			sawDirect++
			for _, p := range c.Probes {
				if !ruleListMatches(systemDirectRules, "DIRECT", p) {
					t.Errorf("「%s」的探针 %s 不在强制直连清单里（探针已失效，体检结果没有意义）", c.Name, p)
				}
			}
		case "proxy":
			sawProxy++
			for _, p := range c.Probes {
				if !ruleListMatches(systemProxyDomains, "PROXY", p) {
					t.Errorf("「%s」的探针 %s 不在强制代理清单里", c.Name, p)
				}
			}
		case "port":
			// 端口类没有域名，靠 NTP 实测，不参与探针断言
		default:
			t.Errorf("「%s」的 Expect=%q 不认识（只允许 direct / proxy / port）", c.Name, c.Expect)
		}
		if len(c.Probes) == 0 && c.Expect != "port" {
			t.Errorf("「%s」没有探针，体检脚本会漏掉这一类", c.Name)
		}
		if strings.TrimSpace(c.Hint) == "" {
			t.Errorf("「%s」没有 hint：体检不通过时用户不知道会坏在哪", c.Name)
		}
	}
	if sawDirect == 0 || sawProxy == 0 {
		t.Errorf("直连类与代理类各自至少要有一条（direct=%d proxy=%d）", sawDirect, sawProxy)
	}
}

// 类别表里引用的规则集 id 必须真的存在，否则界面上会提示「由 geosite-xxx
// 覆盖」，而那个规则集根本没得勾。
func TestSystemClassSetsExistInCatalog(t *testing.T) {
	known := map[string]bool{}
	for _, s := range ruleCatalog {
		known[s.ID] = true
	}
	for _, c := range systemClasses {
		for _, id := range c.Sets {
			if !known[id] {
				t.Errorf("「%s」引用了不存在的规则集 %s", c.Name, id)
			}
		}
	}
}

// 脚本必须是合法 shell。语法错的话用户在设备上是「一跑就报错」，
// 而且报的是脚本自身的错，很容易被当成配置有问题。
func TestHealthCheckScriptSyntax(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("没有 sh，跳过语法检查")
	}
	p := filepath.Join(t.TempDir(), "tp-check.sh")
	if err := os.WriteFile(p, []byte(healthCheckScript()), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(sh, "-n", p).CombinedOutput()
	if err != nil {
		t.Fatalf("体检脚本语法错误：%v\n%s", err, out)
	}
}

// 每一类都要在脚本里有对应的一行，否则「覆盖了全部类别」是假的。
func TestHealthCheckScriptCoversEveryClass(t *testing.T) {
	s := healthCheckScript()
	for _, c := range systemClasses {
		if c.Expect == "port" {
			continue
		}
		if !strings.Contains(s, shq(c.Name)) {
			t.Errorf("脚本里缺少类别「%s」", c.Name)
		}
		for _, p := range c.Probes {
			if !strings.Contains(s, shq(p)) {
				t.Errorf("脚本里缺少探针 %s", p)
			}
		}
	}
	// 版本要进脚本头：用户发出来时才知道是哪个版本生成的
	if !strings.Contains(s, Version) {
		t.Errorf("脚本头里没有面板版本（%s）", Version)
	}
}

// 快照算出来的漂移：自己比自己必须为空，少一条必须报「新增 1 条」。
func TestSystemRuleSnapshotDrift(t *testing.T) {
	snap := systemRuleSnapshot()
	if len(snap) < 20 {
		t.Fatalf("快照只有 %d 条，太少了，八成是清单没接进来", len(snap))
	}
	cur := systemRuleSnapshotJSON()
	if d := driftOf(cur); d.Stale || d.Unknown {
		t.Fatalf("同一版本不该报漂移：%+v", d)
	}

	old := snap[:len(snap)-2]
	oldJSON, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	d := driftOf(string(oldJSON))
	if !d.Stale {
		t.Fatal("少了两条规则却没报漂移")
	}
	if d.AddedCount != 2 || len(d.Added) != 2 {
		t.Errorf("应报新增 2 条，实际 %d 条：%v", d.AddedCount, d.Added)
	}
	if d.RemovedCount != 0 {
		t.Errorf("不该有删除项，实际 %v", d.Removed)
	}
	// 明细要能对上：删掉的那两条就是「新增」的两条
	if d.Added[0] != snap[len(snap)-2] || d.Added[1] != snap[len(snap)-1] {
		t.Errorf("新增项与顺序不符：%v", d.Added)
	}

	// 旧库记录（空快照）必须报 unknown，而不是「最新」
	if d := driftOf(""); !d.Unknown || !d.Stale {
		t.Errorf("空快照应标记 unknown+stale，实际 %+v", d)
	}
	if d := driftOf("{坏 JSON"); !d.Unknown {
		t.Error("解析失败应标记 unknown")
	}
}

// 保存配置时要把版本与规则快照一起落库，列表接口据此报「落后」。
func TestConfigSaveRecordsDriftAndReportsStale(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")
	c := saveOne(t, a, owner, "带快照")

	stored, err := a.Store.GetSavedConfig(cfgID(c))
	if err != nil {
		t.Fatal(err)
	}
	if stored.PanelVersion != Version {
		t.Errorf("应记下面板版本 %q，实际 %q", Version, stored.PanelVersion)
	}
	if stored.RuleSnapshot == "" {
		t.Fatal("没记规则快照，升级后无法判断这份配置是否落后")
	}

	// 刚保存的一定不落后
	got := getDrift(t, a, owner, cfgID(c))
	if got["stale"].(bool) || got["unknown"].(bool) {
		t.Fatalf("刚保存的配置不该报落后：%v", got)
	}

	// 把快照改成「少两条」，模拟用旧版本生成的配置
	var snap []string
	if err := json.Unmarshal([]byte(stored.RuleSnapshot), &snap); err != nil {
		t.Fatal(err)
	}
	oldJSON, _ := json.Marshal(snap[:len(snap)-2])
	if err := a.Store.UpdateSavedConfig(&store.SavedConfig{
		ID: cfgID(c), RuleSnapshot: string(oldJSON), PanelVersion: "v0.0.1",
	}); err != nil {
		t.Fatal(err)
	}

	got = getDrift(t, a, owner, cfgID(c))
	if !got["stale"].(bool) || got["unknown"].(bool) {
		t.Fatalf("少了规则却没报落后：%v", got)
	}
	if int(got["added_count"].(float64)) != 2 {
		t.Errorf("added_count 应为 2，实际 %v", got["added_count"])
	}
	if got["panel_version"] != "v0.0.1" {
		t.Errorf("应带出生成时的版本，实际 %v", got["panel_version"])
	}

	// 列表接口也要带上同样的摘要（前端不逐行发请求）
	w := do(a, owner, "GET", "/api/configs", nil)
	var list []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("应有 1 份配置，实际 %d", len(list))
	}
	d, ok := list[0]["drift"].(map[string]any)
	if !ok {
		t.Fatalf("列表应带 drift 字段：%v", list[0])
	}
	if !d["stale"].(bool) || int(d["added_count"].(float64)) != 2 {
		t.Errorf("列表里的漂移摘要不对：%v", d)
	}
}

// 重新生成并覆盖内容后，漂移要清零，否则界面会一直挂着旧提示。
func TestConfigUpdateClearsDrift(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")
	c := saveOne(t, a, owner, "覆盖后清零")
	id := cfgID(c)

	stored, _ := a.Store.GetSavedConfig(id)
	var snap []string
	_ = json.Unmarshal([]byte(stored.RuleSnapshot), &snap)
	oldJSON, _ := json.Marshal(snap[:len(snap)-1])
	if err := a.Store.UpdateSavedConfig(&store.SavedConfig{
		ID: id, RuleSnapshot: string(oldJSON), PanelVersion: "v0.0.1",
	}); err != nil {
		t.Fatal(err)
	}
	if d := driftOf(mustSnapshot(t, a, id)); !d.Stale {
		t.Fatal("前置条件不成立：应先处于落后状态")
	}

	if w := do(a, owner, "PUT", "/api/configs/"+itoa(id), map[string]any{
		"content": "{\n  \"log\": {\"level\":\"warn\"}\n}\n",
	}); w.Code != 200 {
		t.Fatalf("覆盖内容失败 %d: %s", w.Code, w.Body.String())
	}
	if d := driftOf(mustSnapshot(t, a, id)); d.Stale || d.Unknown {
		t.Errorf("覆盖内容后漂移应清零，实际 %+v", d)
	}
}

// 诊断摘要：贴给维护者的那段文本，不能是空的，也不该泄漏凭据。
func TestDiagnoseSummary(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")
	saveOne(t, a, owner, "摘要用")

	w := do(a, owner, "GET", "/api/diagnose/summary", nil)
	if w.Code != 200 {
		t.Fatalf("诊断摘要应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"triune-panel 诊断摘要", "面板版本", "内置系统规则", "保存的配置"} {
		if !strings.Contains(body, want) {
			t.Errorf("摘要里缺少「%s」：\n%s", want, body)
		}
	}
	if !strings.Contains(body, Version) {
		t.Errorf("摘要里没有面板版本 %s", Version)
	}
	if strings.Contains(body, "Bearer") || strings.Contains(body, "password") {
		t.Error("摘要里出现了凭据相关内容")
	}
}

// 体检脚本接口：纯文本、能直接存成文件
func TestHealthCheckEndpoint(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")

	w := do(a, owner, "GET", "/api/export/healthcheck", nil)
	if w.Code != 200 {
		t.Fatalf("应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Errorf("Content-Type 应是 text/plain，实际 %q", ct)
	}
	body := w.Body.String()
	if !strings.HasPrefix(body, "#!/bin/sh") {
		t.Errorf("脚本应以 #!/bin/sh 开头：%.60s", body)
	}
	if !strings.Contains(body, "run_case ") {
		t.Error("脚本里没有 run_case 调用，等于什么都不测")
	}

	// 只读账号不给（脚本会把规则策略暴露出去）
	viewer := loginAs(t, a, "guest", "guest-password-1")
	if w := do(a, viewer, "GET", "/api/export/healthcheck", nil); w.Code != 403 {
		t.Errorf("只读账号应 403，实际 %d", w.Code)
	}
}

// 生成页目录要带上类别表，否则前端没法做覆盖提示。
func TestExportCatalogCarriesClasses(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")
	w := do(a, owner, "GET", "/api/export/catalog", nil)
	if w.Code != 200 {
		t.Fatalf("catalog 应 200，实际 %d", w.Code)
	}
	var res struct {
		Classes []map[string]any `json:"classes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Classes) != len(systemClasses) {
		t.Fatalf("类别数应为 %d，实际 %d", len(systemClasses), len(res.Classes))
	}
	if res.Classes[0]["key"] == "" || res.Classes[0]["name"] == "" {
		t.Errorf("类别缺少 key/name：%v", res.Classes[0])
	}
}

func getDrift(t *testing.T, a *Server, tok string, id int64) map[string]any {
	t.Helper()
	w := do(a, tok, "GET", "/api/configs/"+itoa(id)+"/drift", nil)
	if w.Code != 200 {
		t.Fatalf("取漂移失败 %d: %s", w.Code, w.Body.String())
	}
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func mustSnapshot(t *testing.T, a *Server, id int64) string {
	t.Helper()
	c, err := a.Store.GetSavedConfig(id)
	if err != nil {
		t.Fatal(err)
	}
	return c.RuleSnapshot
}
