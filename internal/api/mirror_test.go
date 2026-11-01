package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

// TestPanelVersionsWithoutConfig 面板的「版本列表」必须开箱可用。
// 以前没配 panel_repo 直接 400，用户得先知道该填什么才能用——这就是
// 「组件更新中面板无法获取版本列表」的成因。
func TestPanelVersionsWithoutConfig(t *testing.T) {
	exe := fmt.Sprintf("triune-panel-%s-%s", runtime.GOOS, runtime.GOARCH)
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"tag_name": "v0.11.0", "published_at": "2026-11-01T00:00:00Z", "assets": []map[string]string{
				{"name": "triune-panel-linux-amd64", "browser_download_url": "https://example.com/linux-amd64"},
				{"name": exe, "browser_download_url": "https://example.com/" + exe},
				{"name": "sha256sums.txt", "browser_download_url": "https://example.com/sums"},
			}},
			{"tag_name": "v0.10.0", "published_at": "2026-10-31T00:00:00Z", "assets": []map[string]string{
				{"name": exe, "browser_download_url": "https://example.com/old/" + exe},
			}},
		})
	}))
	defer gh.Close()

	a := newRoleTestServer(t)
	if err := a.Store.SetSetting("gh_api_base", gh.URL); err != nil {
		t.Fatal(err)
	}
	owner := loginAs(t, a, "admin", "admin")

	w := do(a, owner, "GET", "/api/updates/versions?component=panel", nil)
	if w.Code != 200 {
		t.Fatalf("未配置 panel_repo 也应能列出版本，实际 %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Source   string           `json:"source"`
		Releases []map[string]any `json:"releases"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Source != "default" {
		t.Errorf("应标记 source=default（用的是内置仓库，不是用户配的），实际 %q", resp.Source)
	}
	if len(resp.Releases) != 2 {
		t.Fatalf("应有 2 条，实际 %d", len(resp.Releases))
	}
	// 自更新只认 URL，必须按当前平台挑好资产，不能让前端自己拼
	if got := resp.Releases[0]["url"]; got != "https://example.com/"+exe {
		t.Errorf("应填入本机平台的资产直链，实际 %v", got)
	}
}

// TestPanelVersionsUsesConfiguredRepo 配了 panel_repo 就以配置为准（fork 场景）。
func TestPanelVersionsUsesConfiguredRepo(t *testing.T) {
	var hitPath string
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitPath = r.URL.Path
		_, _ = w.Write([]byte(`[]`))
	}))
	defer gh.Close()

	a := newRoleTestServer(t)
	_ = a.Store.SetSetting("gh_api_base", gh.URL)
	_ = a.Store.SetSetting("panel_repo", "myorg/my-panel")
	owner := loginAs(t, a, "admin", "admin")

	w := do(a, owner, "GET", "/api/updates/versions?component=panel", nil)
	if w.Code != 200 {
		t.Fatalf("应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(hitPath, "/repos/myorg/my-panel/releases") {
		t.Errorf("应请求配置的仓库，实际打到 %q", hitPath)
	}
	var resp struct {
		Source string `json:"source"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Source != "github" {
		t.Errorf("用户配的仓库应标 source=github，实际 %q", resp.Source)
	}
}

func TestMirrorsListIncludesOfficial(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")
	w := do(a, owner, "GET", "/api/mirrors", nil)
	if w.Code != 200 {
		t.Fatalf("应 200，实际 %d", w.Code)
	}
	var resp struct {
		Mirrors []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Base string `json:"base"`
		} `json:"mirrors"`
		Default string `json:"default"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Mirrors) < 5 {
		t.Errorf("预设镜像太少（用户要的是「大量」），实际 %d", len(resp.Mirrors))
	}
	if resp.Mirrors[0].ID != "official" || resp.Mirrors[0].Base != "" {
		t.Errorf("官方地址必须排第一且 Base 为空，实际 %+v", resp.Mirrors[0])
	}
	if resp.Default != "" {
		t.Errorf("默认必须是官方地址（空串），实际 %q", resp.Default)
	}
}

// TestMirrorTestRejectsNonGitHub 测速接口会把地址交给十几个第三方站点去请求，
// 放开任意地址等于把面板变成公开的探活器。
func TestMirrorTestRejectsNonGitHub(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")

	cases := []map[string]any{
		{"url": "https://evil.example/x"},
		{"url": "http://github.com/x"},          // 只收 https
		{"component": "singbox"},                // 缺 version
		{"component": "panel", "version": "v1"}, // 面板资产名随平台变，拼不出来
		{},
	}
	for _, body := range cases {
		w := do(a, owner, "POST", "/api/mirrors/test", body)
		if w.Code != 400 {
			t.Errorf("body=%v 应 400，实际 %d: %s", body, w.Code, w.Body.String())
		}
	}
}

// TestResolveMirror 决定「这次下载走不走镜像」。
// 指针字段是为了区分三件事：没传（沿用设置）、传空串（强制官方）、传地址（用这个）。
// 直接测这个方法而不是走 HTTP：api 的测试二进制没有 import 内核实现包，
// registry 是空的，handleCoreUpdate 一进门就会以「unknown core」退出。
func TestResolveMirror(t *testing.T) {
	a := newRoleTestServer(t)
	if err := a.Store.SetSetting("gh_mirror", "https://ghfast.top"); err != nil {
		t.Fatal(err)
	}
	str := func(s string) *string { return &s }

	// 没传 → 沿用设置
	if got, err := a.resolveMirror(nil); err != nil || got != "https://ghfast.top" {
		t.Errorf("未传 mirror 应沿用设置，实际 %q,%v", got, err)
	}
	// 传空串 → 强制官方（设置里明明有镜像也不许用）
	if got, err := a.resolveMirror(str("")); err != nil || got != "" {
		t.Errorf("传空串应强制官方地址，实际 %q,%v", got, err)
	}
	// 传地址 → 用它，并去掉结尾斜杠
	if got, err := a.resolveMirror(str("https://slink.ltd/")); err != nil || got != "https://slink.ltd" {
		t.Errorf("应使用请求里的镜像并规范化，实际 %q,%v", got, err)
	}
	// 非法地址 → 报错（在下载之前就拒掉，不浪费一次几十 MB 的尝试）
	if _, err := a.resolveMirror(str("ghfast.top")); err == nil {
		t.Error("缺少协议的镜像应报错")
	}
}
