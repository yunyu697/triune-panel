package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 时间用整点，AgeDays 的整除结果可以直接手算，断言不依赖「大概几天」。
var invNow = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

func at(day, hour int) time.Time {
	return time.Date(2026, 11, day, hour, 0, 0, 0, time.UTC)
}

func put(t *testing.T, path, content string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// buildTree 造出四类备份 + 一处安装脚本的额外归档目录，并故意混入若干
// 「长得像但不是面板生成」的对照文件。
func buildTree(t *testing.T) (dataDir, extraDir string) {
	t.Helper()
	base := t.TempDir()
	dataDir = filepath.Join(base, "data")
	extraDir = filepath.Join(filepath.Join(base, "install"), "backups")
	db := filepath.Join(dataDir, "backups")

	put(t, filepath.Join(db, "triune-data-20261101-030000.tar.gz"), strings.Repeat("a", 100), at(1, 3))
	put(t, filepath.Join(db, "triune-data-20261103-030000.tar.gz"), strings.Repeat("b", 100), at(3, 3))
	put(t, filepath.Join(db, "triune-data-20261106-030000.tar.gz"), strings.Repeat("c", 100), at(6, 3))
	put(t, filepath.Join(db, "triune-data-20261108-030000.tar.gz.age"), strings.Repeat("d", 100), at(8, 3))
	put(t, filepath.Join(db, "unrelated.txt"), "keep me", at(1, 3))
	put(t, filepath.Join(extraDir, "triune-data-20261102-030000.tar.gz"), strings.Repeat("e", 100), at(2, 3))

	pan := filepath.Join(db, "panel")
	put(t, filepath.Join(pan, "v1.3.1-20261101-030000.bak"), strings.Repeat("p", 50), at(1, 3))
	put(t, filepath.Join(pan, "v1.3.2-20261108-030000.bak"), strings.Repeat("p", 50), at(8, 3))
	put(t, filepath.Join(pan, "notes.bak.bak"), "not ours", at(1, 3))

	xray := filepath.Join(db, "cores", "xray")
	put(t, filepath.Join(xray, "20261101-030000.bak"), strings.Repeat("x", 50), at(1, 3))
	put(t, filepath.Join(xray, "20261108-030000.bak"), strings.Repeat("x", 50), at(8, 3))
	put(t, filepath.Join(db, "cores", "sing-box", "20261108-030000.bak"), strings.Repeat("s", 50), at(8, 3))

	rev := filepath.Join(dataDir, "revisions", "xray")
	put(t, filepath.Join(rev, "20261101-030000.json.bak"), strings.Repeat("r", 20), at(1, 3))
	put(t, filepath.Join(rev, "20261108-030000.json.bak"), strings.Repeat("r", 20), at(8, 3))
	put(t, filepath.Join(rev, "unrelated.txt"), "keep me", at(1, 3))
	return dataDir, extraDir
}

func opts(t *testing.T) ScanOptions {
	t.Helper()
	dataDir, extraDir := buildTree(t)
	return ScanOptions{DataDir: dataDir, ExtraDataDirs: []string{extraDir}, Now: invNow}
}

func names(entries []Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name)
	}
	return out
}

func contains(list []Entry, name string) bool {
	for _, e := range list {
		if e.Name == name {
			return true
		}
	}
	return false
}

func TestScanFourCategoriesAndWhitelist(t *testing.T) {
	o := opts(t)
	inv, err := Scan(o)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Summary.Count != 12 {
		t.Fatalf("期望 12 个条目，实际 %d：%v", inv.Summary.Count, names(inv.Entries))
	}
	if inv.Summary.TotalBytes != 790 {
		t.Errorf("总占用应为 790，实际 %d", inv.Summary.TotalBytes)
	}
	wantCat := map[string]int64{"data": 500, "panel": 100, "core": 150, "revision": 40}
	for k, v := range wantCat {
		if inv.Summary.ByCategory[k] != v {
			t.Errorf("%s 小计应为 %d，实际 %d", k, v, inv.Summary.ByCategory[k])
		}
	}
	for _, bad := range []string{"unrelated.txt", "notes.bak.bak"} {
		if contains(inv.Entries, bad) {
			t.Errorf("对照文件 %q 不该进入盘点", bad)
		}
	}
	if len(inv.Summary.Roots) != 6 {
		t.Errorf("期望 6 个扫描根目录（含安装目录的额外归档），实际 %d：%v",
			len(inv.Summary.Roots), inv.Summary.Roots)
	}
	if !strings.Contains(inv.Summary.Hint, "内核二进制") {
		t.Errorf("提示里应说明归档含内核二进制，实际：%s", inv.Summary.Hint)
	}
	// 加密档与明文按同一类处理，AgeDays 按注入时间算
	for _, e := range inv.Entries {
		if e.Name == "triune-data-20261108-030000.tar.gz.age" {
			if e.Category != string(CatData) || e.AgeDays != 0 {
				t.Errorf("加密档分类/天数不对：%+v", e)
			}
			if e.ModTime == "" || e.Key == "" || e.Path == "" {
				t.Errorf("条目字段缺失：%+v", e)
			}
		}
		if e.Name == "triune-data-20261101-030000.tar.gz" && e.AgeDays != 7 {
			t.Errorf("11-01 的归档应为 7 天，实际 %d", e.AgeDays)
		}
	}
}

func TestScanMissingDirsIsSilent(t *testing.T) {
	inv, err := Scan(ScanOptions{DataDir: filepath.Join(t.TempDir(), "nope"), Now: invNow})
	if err != nil {
		t.Fatal(err)
	}
	if inv.Summary.Count != 0 || len(inv.Entries) != 0 {
		t.Fatal("目录不存在应返回空清单而不是报错")
	}
	if _, err := Scan(ScanOptions{}); err == nil {
		t.Fatal("DataDir 为空应报错")
	}
}

func TestScanIgnoresSymlinkAndSubdirectory(t *testing.T) {
	o := opts(t)
	db := filepath.Join(o.DataDir, "backups")
	outside := filepath.Join(t.TempDir(), "outside.bak")
	put(t, outside, "outside", at(1, 3))
	if err := os.Symlink(outside, filepath.Join(db, "triune-data-20261104-030000.tar.gz")); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(db, "panel", "nested", "20261101-030000.bak"), "nested", at(1, 3))
	inv, err := Scan(o)
	if err != nil {
		t.Fatal(err)
	}
	if contains(inv.Entries, "triune-data-20261104-030000.tar.gz") {
		t.Error("符号链接不得进入盘点")
	}
	// nested/ 子目录里的同名 .bak 不该被递归扫到：core/xray 下只有一份 11-01
	coreOld := 0
	for _, e := range inv.Entries {
		if e.Category == string(CatCore) && e.Group == "xray" && e.Name == "20261101-030000.bak" {
			coreOld++
		}
	}
	if coreOld != 1 {
		t.Errorf("子目录里的 .bak 被递归扫进来了（命中 %d 次）", coreOld)
	}
	if inv.Summary.Count != 12 {
		t.Errorf("条目数应仍是 12，实际 %d：%v", inv.Summary.Count, names(inv.Entries))
	}
}

func newestKeys(inv *Inventory) map[string]string {
	out := map[string]string{}
	byGroup := map[string]Entry{}
	for _, e := range inv.Entries {
		g := groupOf(e)
		if cur, ok := byGroup[g]; !ok || e.UnixNano > cur.UnixNano {
			byGroup[g] = e
		}
	}
	for g, e := range byGroup {
		out[g] = e.Key
	}
	return out
}

func assertKeepsNewest(t *testing.T, inv *Inventory, planned []Entry) {
	t.Helper()
	newest := newestKeys(inv)
	inPlan := map[string]bool{}
	for _, e := range planned {
		inPlan[e.Key] = true
	}
	for g, k := range newest {
		if inPlan[k] {
			t.Errorf("组 %s 的最新一份 %s 被列入删除，违反硬底线", g, k)
		}
	}
	if len(newest) != 5 {
		t.Errorf("期望 5 个分组，实际 %d", len(newest))
	}
}

func TestPlanKeepCountPerGroup(t *testing.T) {
	o := opts(t)
	inv, _ := Scan(o)
	planned := Plan(inv, Policy{KeepCount: 1})
	if len(planned) != 7 {
		t.Errorf("每组留 1 份应删 7 条，实际 %d：%v", len(planned), names(planned))
	}
	assertKeepsNewest(t, inv, planned)
}

func TestPlanMaxDays(t *testing.T) {
	o := opts(t)
	inv, _ := Scan(o)
	planned := Plan(inv, Policy{MaxDays: 3})
	if len(planned) != 6 {
		t.Errorf("超过 3 天应删 6 条，实际 %d：%v", len(planned), names(planned))
	}
	for _, e := range planned {
		if e.AgeDays <= 3 {
			t.Errorf("未超期的 %s 被列入删除", e.Name)
		}
	}
	assertKeepsNewest(t, inv, planned)
}

func TestPlanMaxBytesStopsAtLimit(t *testing.T) {
	o := opts(t)
	inv, _ := Scan(o)
	const limit int64 = 600
	planned := Plan(inv, Policy{MaxBytes: limit})
	if len(planned) == 0 {
		t.Fatal("总占用 790 > 600，应有清理计划")
	}
	remaining := inv.Summary.TotalBytes
	for _, e := range planned {
		remaining -= e.Size
	}
	if remaining > limit {
		t.Errorf("清理后仍占 %d，超过上限 %d", remaining, limit)
	}
	// 不能多删：少删任何一份都会重新超限
	if remaining+planned[len(planned)-1].Size <= limit {
		t.Error("删多了：去掉最后一份仍在上限内")
	}
	assertKeepsNewest(t, inv, planned)
}

func TestPlanHardFloorUnderExtremePolicy(t *testing.T) {
	o := opts(t)
	inv, _ := Scan(o)
	planned := Plan(inv, Policy{KeepCount: 1, MaxBytes: 1, MaxDays: 0})
	assertKeepsNewest(t, inv, planned)
	if len(planned) != inv.Summary.Count-5 {
		t.Errorf("极端策略下应只删掉每组最新之外的 %d 条，实际 %d", inv.Summary.Count-5, len(planned))
	}
}

func TestPlanEmptyPolicyRemovesNothing(t *testing.T) {
	o := opts(t)
	inv, _ := Scan(o)
	if got := Plan(inv, Policy{}); len(got) != 0 {
		t.Errorf("策略全 0 不该自动删任何东西，实际 %v", names(got))
	}
	if got := Plan(&Inventory{}, Policy{MaxDays: 1}); len(got) != 0 {
		t.Error("空清单不该 panic 或返回条目")
	}
}

func TestPruneByKeys(t *testing.T) {
	o := opts(t)
	key := "data/data/triune-data-20261101-030000.tar.gz"
	res, err := Prune(o, []string{key}, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 1 || res.Removed[0].Key != key {
		t.Fatalf("应删 1 条，实际 %+v / failed=%v", res.Removed, res.Failed)
	}
	if res.Freed != 100 {
		t.Errorf("释放空间应为 100，实际 %d", res.Freed)
	}
	if _, err := os.Stat(filepath.Join(o.DataDir, "backups", "triune-data-20261101-030000.tar.gz")); !os.IsNotExist(err) {
		t.Error("目标文件没有真的被删")
	}
	for _, p := range []string{
		filepath.Join(o.DataDir, "backups", "unrelated.txt"),
		filepath.Join(o.DataDir, "backups", "panel", "notes.bak.bak"),
		filepath.Join(o.DataDir, "revisions", "xray", "unrelated.txt"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("对照文件被误删：%s", p)
		}
	}
}

func TestPruneRejectsIllegalAndForeignKeys(t *testing.T) {
	o := opts(t)
	keys := []string{
		"../../etc/passwd",
		"/etc/passwd",
		`data\..\..\etc\passwd`,
		"data/data/unrelated.txt",
		"panel/panel/notes.bak.bak",
		"data/data/not-exist.tar.gz",
	}
	res, err := Prune(o, keys, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 0 {
		t.Errorf("非法 key 不该删掉任何文件，实际 %v", names(res.Removed))
	}
	if len(res.Failed) != len(keys) {
		t.Errorf("每个非法 key 都该进 Failed，实际 %v", res.Failed)
	}
}

func TestPruneNeverRemovesForeignOrOutsideFiles(t *testing.T) {
	o := opts(t)
	base := filepath.Dir(o.DataDir)
	outside := filepath.Join(base, "outside", "20261101-030000.bak")
	put(t, outside, "outside", at(1, 3))
	inv, err := Scan(o)
	if err != nil {
		t.Fatal(err)
	}
	// 就算有人手滑把外部路径塞进一个「看起来合法」的条目，第二道防线也要挡住
	fake := Entry{Key: "core/xray/20261101-030000.bak", Name: "20261101-030000.bak",
		Category: string(CatCore), Path: outside}
	if pathWithinRoots(fake.Path, inv.Summary.Roots) {
		t.Error("允许根目录之外的路径被判为可删")
	}
	res, err := Prune(o, []string{"core/xray/20261101-030000.bak"}, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 1 {
		t.Fatalf("组内旧份应可删，实际 %v / %v", names(res.Removed), res.Failed)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Error("根目录外的同名 .bak 被动过")
	}
}

func TestPruneKeepsNewestEvenWhenExplicitlyRequested(t *testing.T) {
	o := opts(t)
	res, err := Prune(o, []string{"core/sing-box/20261108-030000.bak"}, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 0 {
		t.Error("某分组唯一一份（也是最新一份）不该被手动删掉")
	}
	if len(res.Failed) != 1 || !strings.Contains(res.Failed[0], "至少保留") {
		t.Errorf("应给出「至少保留最新一份」的提示，实际 %v", res.Failed)
	}
}

func TestPruneEmptyKeysAppliesPolicy(t *testing.T) {
	o := opts(t)
	res, err := Prune(o, nil, Policy{MaxDays: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 6 {
		t.Errorf("按策略应删 6 条，实际 %d / failed=%v", len(res.Removed), res.Failed)
	}
	if len(res.Planned) != len(res.Removed) {
		t.Error("预览数与实际删除数不一致")
	}
	if res.Freed != 100+100+100+50+50+20 {
		t.Errorf("释放空间算错：%d", res.Freed)
	}
	inv2, _ := Scan(o)
	if inv2.Summary.Count != 6 {
		t.Errorf("清理后应剩 6 份，实际 %d：%v", inv2.Summary.Count, names(inv2.Entries))
	}
	// 每个分组的最新一份都还在，回滚能力没被清掉
	for _, want := range []string{
		"triune-data-20261108-030000.tar.gz.age", "v1.3.2-20261108-030000.bak",
		"20261108-030000.bak", "20261108-030000.json.bak",
	} {
		if !contains(inv2.Entries, want) {
			t.Errorf("最新一份 %s 被删掉了", want)
		}
	}
	if len(newestKeys(inv2)) != 5 {
		t.Errorf("清理后应仍有 5 个分组，实际 %d", len(newestKeys(inv2)))
	}
}

func TestArchiveAllowedNames(t *testing.T) {
	good := []string{
		"20261108-030000.bak", "v1.3.2-20261108-030000.bak", "20261108-030000.json.bak",
		"triune-data-20261108-030000.tar.gz", "triune-data-20261108-030000.tar.gz.age",
	}
	for _, n := range good {
		cat := CatPanel
		if strings.HasPrefix(n, "triune-data-") {
			cat = CatData
		}
		if !archiveAllowed(cat, n) {
			t.Errorf("合法文件名被拒：%s", n)
		}
	}
	bad := []string{"notes.bak.bak", ".hidden-20261108-030000.bak", "../20261108-030000.bak",
		"20261108-030000.txt", "unrelated.txt", "triune-data-20261108-030000.zip", "backup.bak"}
	for _, n := range bad {
		if archiveAllowed(CatPanel, n) {
			t.Errorf("非法文件名被放行：%s", n)
		}
	}
}
