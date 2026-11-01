package update

import "testing"

// TestParseSemver 锁死 tag 解析：预发布后缀必须先剥掉再分段，
// 否则 "1.15.0-alpha.13" 会被拆成 4 段直接解析失败。
func TestParseSemver(t *testing.T) {
	cases := []struct {
		tag  string
		nums [3]int
		pre  string
		ok   bool
	}{
		{"v1.14.4", [3]int{1, 14, 4}, "", true},
		{"1.14.4", [3]int{1, 14, 4}, "", true},
		{"v1.15.0-alpha.13", [3]int{1, 15, 0}, "alpha.13", true},
		{"v1.8.24", [3]int{1, 8, 24}, "", true},
		{"v1.2", [3]int{1, 2, 0}, "", true},
		{"v1.2.3+build5", [3]int{1, 2, 3}, "build5", true},
		{"20260102", [3]int{}, "", false}, // 日期型 tag：认不出，交给发布时间排序
		{"nightly", [3]int{}, "", false},
	}
	for _, c := range cases {
		nums, pre, ok := parseSemver(c.tag)
		if ok != c.ok || pre != c.pre || (ok && nums != c.nums) {
			t.Errorf("parseSemver(%q) = %v %q %v，期望 %v %q %v", c.tag, nums, pre, ok, c.nums, c.pre, c.ok)
		}
	}
}

// TestPrereleaseIsNotNewer 是这次改动的核心断言：
// 1.15.0-alpha.13 的版本号比 1.14.4 大，但它不该被当成「最新稳定版」。
func TestPrereleaseIsNotNewer(t *testing.T) {
	alpha := Release{Tag: "v1.15.0-alpha.13", Prerelease: true}
	stable := Release{Tag: "v1.14.4"}
	if !newerThan(alpha, stable) {
		t.Error("按版本号 1.15.0 > 1.14.4，alpha 仍应排在前面（排序不等于推荐）")
	}
	// 同版本号时正式版优先
	rc := Release{Tag: "v1.15.0-rc.1", Prerelease: true}
	rel := Release{Tag: "v1.15.0"}
	if !newerThan(rel, rc) {
		t.Error("1.15.0 正式版应排在 1.15.0-rc.1 之前")
	}
}

func TestFilterChannel(t *testing.T) {
	rels := []Release{
		{Tag: "v1.15.0-alpha.13", Prerelease: true},
		{Tag: "v1.14.4"},
		{Tag: "v1.14.3"},
		{Tag: "v1.13.0-beta.2", Prerelease: true},
	}
	if got := FilterChannel(rels, ChannelStable); len(got) != 2 || got[0].Tag != "v1.14.4" {
		t.Errorf("稳定版应只剩 2 条且首条为 v1.14.4，实际 %v", tags(got))
	}
	if got := FilterChannel(rels, ChannelPrerelease); len(got) != 2 || got[0].Tag != "v1.15.0-alpha.13" {
		t.Errorf("预发布应只剩 2 条，实际 %v", tags(got))
	}
	if got := FilterChannel(rels, ChannelAll); len(got) != 4 {
		t.Errorf("全部分支应原样返回 4 条，实际 %v", tags(got))
	}
	// 空值等同于 all（保持旧接口行为，老调用方不用改）
	if got := FilterChannel(rels, ""); len(got) != 4 {
		t.Errorf("空 channel 应等同 all，实际 %v", tags(got))
	}
}

func TestNormalizeChannel(t *testing.T) {
	for in, want := range map[string]string{
		"": "all", "all": "all", "ALL": "all", " stable ": "stable",
		"prerelease": "prerelease", "Prerelease": "prerelease",
	} {
		got, ok := NormalizeChannel(in)
		if !ok || got != want {
			t.Errorf("NormalizeChannel(%q) = %q,%v，期望 %q,true", in, got, ok, want)
		}
	}
	if _, ok := NormalizeChannel("beta"); ok {
		t.Error("未知分支应返回 ok=false（调用方据此回 400，而不是静默当 all）")
	}
}

// TestSortReleasesByVersion GitHub 按创建时间返回，补丁回填会让
// 「第一条」不是最高版本。这里锁死按版本号降序。
func TestSortReleasesByVersion(t *testing.T) {
	rels := []Release{
		{Tag: "v1.14.5", PublishedAt: "2026-10-01T00:00:00Z"}, // 晚发布，但版本低于 1.15
		{Tag: "v1.15.0", PublishedAt: "2026-09-01T00:00:00Z"},
		{Tag: "v1.15.0-alpha.1", Prerelease: true, PublishedAt: "2026-08-01T00:00:00Z"},
		{Tag: "nightly-20260101", PublishedAt: "2026-07-01T00:00:00Z"},
	}
	sortReleases(rels)
	want := []string{"v1.15.0", "v1.15.0-alpha.1", "v1.14.5", "nightly-20260101"}
	for i := range want {
		if rels[i].Tag != want[i] {
			t.Fatalf("排序结果 %v，期望 %v", tags(rels), want)
		}
	}
}

func tags(rels []Release) []string {
	out := make([]string, len(rels))
	for i, r := range rels {
		out[i] = r.Tag
	}
	return out
}

// TestComparePrerelease 预发布段必须按 semver 规则比，不能比字符串。
// 字典序会把 alpha.9 排在 alpha.13 前面（'9' > '1'），v1.0.3 之前
// 「预发布」分支默认选中的就是 alpha.9 —— 用户看到的第一条不是最新构建。
func TestComparePrerelease(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"alpha.13", "alpha.9", 1},    // 数值比较：13 > 9
		{"alpha.9", "alpha.13", -1},   // 反过来
		{"alpha.10", "alpha.9", 1},    // 10 > 9（字典序会判成 10 < 9）
		{"alpha.13", "alpha.13", 0},   // 完全相等
		{"alpha", "alpha.1", -1},      // 段数少的更低
		{"alpha.1", "alpha.beta", -1}, // 纯数字段低于字母段
		{"beta.17", "beta.15", 1},
		{"rc.5", "rc.4", 1},
	}
	for _, c := range cases {
		if got := comparePrerelease(c.a, c.b); got != c.want {
			t.Errorf("comparePrerelease(%q,%q) = %d，期望 %d", c.a, c.b, got, c.want)
		}
	}
}

// TestSortReleasesAlphaOrdering 真实回归：sing-box 同一版本下一串 alpha，
// 最新的 alpha.13 必须排在第一条（预发布分支默认选中它）。
func TestSortReleasesAlphaOrdering(t *testing.T) {
	rels := []Release{
		{Tag: "v1.15.0-alpha.9", Prerelease: true},
		{Tag: "v1.15.0-alpha.13", Prerelease: true},
		{Tag: "v1.15.0-alpha.1", Prerelease: true},
		{Tag: "v1.14.4"},
		{Tag: "v1.14.3"},
	}
	sortReleases(rels)
	want := []string{"v1.15.0-alpha.13", "v1.15.0-alpha.9", "v1.15.0-alpha.1", "v1.14.4", "v1.14.3"}
	for i := range want {
		if rels[i].Tag != want[i] {
			t.Fatalf("排序结果 %v，期望 %v", tags(rels), want)
		}
	}
}

// TestSortReleasesRollingTag 滚动 tag（mihomo 的 Prerelease-Alpha）用
// updated_at 排序：它的 published_at 停在首次创建，只有资产刷新时间会前进，
// 只按 published_at 会把最新的 alpha 构建排到列表最末。
func TestSortReleasesRollingTag(t *testing.T) {
	rels := []Release{
		{Tag: "Prerelease-Alpha", Prerelease: true,
			PublishedAt: "2024-08-12T17:01:02Z", UpdatedAt: "2026-10-30T09:51:01Z"},
		{Tag: "nightly-20250901", PublishedAt: "2025-09-01T00:00:00Z"},
	}
	sortReleases(rels)
	if rels[0].Tag != "Prerelease-Alpha" {
		t.Fatalf("滚动 tag 应按 updated_at 排在最前，实际 %v", tags(rels))
	}
}
