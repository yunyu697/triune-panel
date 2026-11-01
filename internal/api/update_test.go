package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeGitHub 起一个假的 GitHub Releases 接口。
// 顺序故意打乱：GitHub 是按创建时间返回的，补丁回填会让顺序不等于版本序。
func fakeGitHub(t *testing.T) *httptest.Server {
	t.Helper()
	rels := []map[string]any{
		{"tag_name": "v1.14.5", "published_at": "2026-10-20T00:00:00Z",
			"assets": []map[string]string{{"name": "sing-box-1.14.5-linux-amd64.tar.gz"}}},
		{"tag_name": "v1.15.0-alpha.13", "prerelease": true, "published_at": "2026-09-01T00:00:00Z",
			"body": "alpha 预览版"},
		{"tag_name": "v1.15.0", "published_at": "2026-08-15T00:00:00Z", "body": "## 正式版"},
		{"tag_name": "v1.16.0-rc.1", "prerelease": true, "draft": false, "published_at": "2026-10-28T00:00:00Z"},
		{"tag_name": "v1.17.0", "draft": true, "published_at": "2026-10-30T00:00:00Z"}, // 草稿不该出现
		{"tag_name": "v1.13.0", "published_at": "2026-06-01T00:00:00Z"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Errorf("请求 GitHub 必须带 User-Agent，否则会被 403")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rels)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func versionsOf(t *testing.T, a *Server, tok, query string) ([]string, map[string]any) {
	t.Helper()
	w := do(a, tok, "GET", "/api/updates/versions?component=singbox"+query, nil)
	if w.Code != 200 {
		t.Fatalf("版本列表 %s 应 200，实际 %d: %s", query, w.Code, w.Body.String())
	}
	var resp struct {
		Channel  string           `json:"channel"`
		Counts   map[string]int   `json:"counts"`
		Releases []map[string]any `json:"releases"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	tags := make([]string, len(resp.Releases))
	for i, r := range resp.Releases {
		tags[i] = r["tag"].(string)
	}
	return tags, map[string]any{"channel": resp.Channel, "counts": resp.Counts}
}

// TestUpdateVersionsChannel 是「一键安装加分支选择」的服务端契约：
// 默认不筛（老调用方照旧），stable 只给正式版，预发布靠 tag 后缀也能认出来。
func TestUpdateVersionsChannel(t *testing.T) {
	gh := fakeGitHub(t)
	a := newRoleTestServer(t)
	if err := a.Store.SetSetting("gh_api_base", gh.URL); err != nil {
		t.Fatal(err)
	}
	owner := loginAs(t, a, "admin", "admin")

	// 默认 = all：草稿被丢掉，其余 5 条都在，且按版本号降序
	all, meta := versionsOf(t, a, owner, "")
	if len(all) != 5 {
		t.Fatalf("全部版本应有 5 条（草稿不算），实际 %v", all)
	}
	if all[0] != "v1.16.0-rc.1" || all[1] != "v1.15.0" || all[4] != "v1.13.0" {
		t.Errorf("应按版本号降序且 v1.16.0-rc.1 在最前，实际 %v", all)
	}
	if meta["channel"] != "all" {
		t.Errorf("未传 channel 时应回 all，实际 %v", meta["channel"])
	}
	counts := meta["counts"].(map[string]int)
	if counts["all"] != 5 || counts["stable"] != 3 || counts["prerelease"] != 2 {
		t.Errorf("分支计数应为 all=5 stable=3 prerelease=2，实际 %v", counts)
	}

	// stable：只剩正式版，且最高版本是 1.15.0（不是 1.16.0-rc.1）
	stable, meta := versionsOf(t, a, owner, "&channel=stable")
	if len(stable) != 3 {
		t.Fatalf("稳定版应有 3 条，实际 %v", stable)
	}
	if stable[0] != "v1.15.0" {
		t.Errorf("稳定版首条应为 v1.15.0，实际 %v", stable[0])
	}
	for _, tag := range stable {
		if tag == "v1.16.0-rc.1" || tag == "v1.15.0-alpha.13" {
			t.Errorf("稳定版里不该出现预发布 %s", tag)
		}
	}
	if meta["counts"].(map[string]int)["all"] != 5 {
		t.Error("counts 应始终按全量统计，否则前端无法显示「预发布 2」")
	}

	// prerelease：只剩两条预发布
	pre, _ := versionsOf(t, a, owner, "&channel=prerelease")
	if len(pre) != 2 || pre[0] != "v1.16.0-rc.1" {
		t.Errorf("预发布应为 2 条且首条 v1.16.0-rc.1，实际 %v", pre)
	}

	// 未知分支：明确 400，不静默当 all
	if w := do(a, owner, "GET", "/api/updates/versions?component=singbox&channel=beta", nil); w.Code != 400 {
		t.Errorf("未知 channel 应 400，实际 %d", w.Code)
	}
}

// TestUpdateVersionsMarksPrereleaseByTag 有些仓库发 alpha 时不勾 pre-release 标记，
// 只靠 tag 后缀也要能认出来——否则「稳定版」里会混进 alpha。
func TestUpdateVersionsMarksPrereleaseByTag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"tag_name": "v2.0.0-beta.3"}, // 没勾 prerelease
			{"tag_name": "v1.9.0"},
		})
	}))
	defer srv.Close()
	a := newRoleTestServer(t)
	if err := a.Store.SetSetting("gh_api_base", srv.URL); err != nil {
		t.Fatal(err)
	}
	owner := loginAs(t, a, "admin", "admin")
	stable, _ := versionsOf(t, a, owner, "&channel=stable")
	if len(stable) != 1 || stable[0] != "v1.9.0" {
		t.Errorf("v2.0.0-beta.3 未勾 prerelease 也应被归为预发布，稳定版只剩 v1.9.0，实际 %v", stable)
	}
}
