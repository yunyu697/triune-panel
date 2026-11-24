package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"triune/internal/update"
)

// 这组用例钉的是「接口配额耗尽时还能不能拿到版本列表」——也就是用户实际
// 撞上的那个场景：面板与三个内核一起报「获取版本列表失败: HTTP 403」。
//
// 用本地服务而不是真 GitHub：这条路径的价值就在于「上游拒绝时换一条通道」，
// 换通道的逻辑必须能离线验证，否则每次回归都要赌匿名配额还剩多少。

const testAtomFeed = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <entry><title>v9.9.9</title><updated>2026-11-24T16:36:14Z</updated>
    <link rel="alternate" href="https://github.com/o/r/releases/tag/v9.9.9"/></entry>
  <entry><title>v9.9.8 — 带说明的标题</title><updated>2026-11-20T00:00:00Z</updated>
    <link rel="alternate" href="https://github.com/o/r/releases/tag/v9.9.8"/></entry>
</feed>`

func quotaExhaustedServer(t *testing.T) *httptest.Server {
	t.Helper()
	reset := time.Now().Add(5 * time.Minute).Unix()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "60")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset, 10))
		w.WriteHeader(403)
	}))
}

func TestVersionsFallBackToAtomWhenQuotaExhausted(t *testing.T) {
	a := newAuthTestServer(t)
	update.InvalidateVersions()
	gh := quotaExhaustedServer(t)
	defer gh.Close()
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(testAtomFeed))
	}))
	defer feed.Close()

	oldBase := update.AtomBase
	update.AtomBase = feed.URL
	defer func() { update.AtomBase = oldBase }()
	defer update.InvalidateVersions()

	if err := a.Store.SetSetting("gh_api_base", gh.URL); err != nil {
		t.Fatal(err)
	}

	rels, via, err := a.listVersions("o/r")
	if err != nil {
		t.Fatalf("配额耗尽后应改走订阅通道，实际报错: %v", err)
	}
	if via != "atom" {
		t.Fatalf("通道应为 atom，实际 %q", via)
	}
	if len(rels) != 2 || rels[0].Tag != "v9.9.9" {
		t.Fatalf("订阅里的版本没解析对: %+v", rels)
	}

	// 订阅没有资产清单，面板资产的直链要按我们自己定的命名拼出来；
	// 拼不出来就等于「列表能看、点了装不了」。
	fillPanelAssetURL(rels, "o/r", "atom")
	if !strings.HasPrefix(rels[0].URL, "https://github.com/o/r/releases/download/v9.9.9/triune-panel-") {
		t.Fatalf("面板资产直链没拼出来: %q", rels[0].URL)
	}
	if !strings.Contains(rels[1].URL, "/v9.9.8/") {
		t.Fatalf("第二条应使用链接里的 tag，而不是标题: %q", rels[1].URL)
	}
}

func TestVersionsReportBothChannelsWhenNeitherWorks(t *testing.T) {
	a := newAuthTestServer(t)
	update.InvalidateVersions()
	gh := quotaExhaustedServer(t)
	defer gh.Close()
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", 500)
	}))
	defer dead.Close()

	oldBase := update.AtomBase
	update.AtomBase = dead.URL
	defer func() { update.AtomBase = oldBase }()
	defer update.InvalidateVersions()

	if err := a.Store.SetSetting("gh_api_base", gh.URL); err != nil {
		t.Fatal(err)
	}

	_, _, err := a.listVersions("o/r")
	if err == nil {
		t.Fatal("两条通道都不通时必须报错，不能返回空列表让前端显示「没有更新」")
	}
	msg := err.Error()
	// 只说「订阅失败」会让人以为再点一次就好；必须把配额那条也带出来，
	// 用户才知道「换通道也救不了，得等或配 Token」。
	if !strings.Contains(msg, "配额") || !strings.Contains(msg, "订阅") {
		t.Fatalf("错误应同时说明两条通道，实际: %v", err)
	}
	if !update.IsRateLimited(err) {
		t.Error("包住配额错误时应保留可识别性（errors.Is 链），便于上层将来复用")
	}
}

func TestGHTokenSettingIsNeverEchoed(t *testing.T) {
	a := newAuthTestServer(t)
	tok := "ghp_" + strings.Repeat("x", 36)
	if err := a.Store.SetSetting(SettingGHToken, tok); err != nil {
		t.Fatal(err)
	}
	admin := loginAs(t, a, "admin", "admin")

	w := do(a, admin, "GET", "/api/settings", nil)
	if w.Code != 200 {
		t.Fatalf("读设置应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, tok) {
		t.Fatal("GitHub Token 明文回显了：一个 PAT 能读该账号所有私有仓库，回显等于把它发给每个打开设置页的浏览器")
	}
	if !strings.Contains(body, "********") {
		t.Fatalf("应回占位符，便于前端「原样提交 = 没改」: %s", body)
	}

	// 原样提交占位符不能把真 Token 覆盖掉（与 DNS 凭据同一条规矩）。
	do(a, admin, "PUT", "/api/settings", map[string]string{SettingGHToken: "********"})
	if got, _ := a.Store.GetSetting(SettingGHToken); got != tok {
		t.Fatalf("提交占位符把真 Token 冲掉了: %q", got)
	}
	// 显式清空才是「退回匿名」。
	w = do(a, admin, "PUT", "/api/settings", map[string]string{SettingGHToken: ""})
	if w.Code != 200 {
		t.Fatalf("清空 Token 应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	if got, _ := a.Store.GetSetting(SettingGHToken); got != "" {
		t.Fatalf("清空后库里应为空，实际 %q", got)
	}
}

func TestGHTokenRejectsWholeCommandLine(t *testing.T) {
	a := newAuthTestServer(t)
	admin := loginAs(t, a, "admin", "admin")
	// 最常见的粘贴错误：把 shell 那一整行命令粘进输入框。存进去的就不是令牌，
	// 结果是之后每次请求都 401，而用户以为「Token 填了」。
	w := do(a, admin, "PUT", "/api/settings",
		map[string]string{SettingGHToken: "export GH_TOKEN=ghp_abc"})
	if w.Code != 400 {
		t.Fatalf("带空格的一整行命令应拒收，实际 %d: %s", w.Code, w.Body.String())
	}
	if got, _ := a.Store.GetSetting(SettingGHToken); got != "" {
		t.Fatalf("被拒的值不能落库，实际 %q", got)
	}
}
