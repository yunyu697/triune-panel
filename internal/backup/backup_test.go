package backup

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCreateExcludesBackups 归档必须排除 backups/ 自身，否则会越滚越大。
func TestCreateExcludesBackups(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "triune.db"), "db-bytes")
	mustWrite(t, filepath.Join(dir, "web", "index.html"), "<html>")
	mustWrite(t, filepath.Join(dir, "backups", "triune-data-20200101-000000.tar.gz"), "old")

	res, err := Create(Options{DataDir: dir, Now: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Size == 0 {
		t.Fatal("归档不应为空")
	}
	if filepath.Base(res.Path) != "triune-data-20261031-120000.tar.gz" {
		t.Fatalf("归档命名异常: %s", res.Path)
	}

	names := archiveNames(t, res.Path)
	for _, want := range []string{"triune.db", "web/index.html"} {
		if !names[want] {
			t.Errorf("归档缺少 %s，实际包含 %v", want, keys(names))
		}
	}
	for n := range names {
		if strings.HasPrefix(n, "backups/") {
			t.Errorf("归档不应包含 backups/，发现 %s", n)
		}
	}
}

// TestRotateKeepsNewest 轮转只保留最新 keep 份。
func TestRotateKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{
		"triune-data-20260101-000000.tar.gz",
		"triune-data-20260202-000000.tar.gz",
		"triune-data-20260303-000000.tar.gz",
		"triune-data-20260404-000000.tar.gz",
		"unrelated.txt",
	} {
		mustWrite(t, filepath.Join(dir, n), "x")
	}
	removed, err := Rotate(dir, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 {
		t.Fatalf("应删除 2 份，实际 %d: %v", len(removed), removed)
	}
	left := map[string]bool{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		left[e.Name()] = true
	}
	for _, keep := range []string{"triune-data-20260303-000000.tar.gz", "triune-data-20260404-000000.tar.gz", "unrelated.txt"} {
		if !left[keep] {
			t.Errorf("应保留 %s，实际 %v", keep, left)
		}
	}
	for _, gone := range []string{"triune-data-20260101-000000.tar.gz", "triune-data-20260202-000000.tar.gz"} {
		if left[gone] {
			t.Errorf("应删除 %s", gone)
		}
	}
}

// TestCreateRotatesWithKeep Create 内部会按 Keep 轮转。
func TestCreateRotatesWithKeep(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "triune.db"), "db")
	out := filepath.Join(dir, "bk")
	base := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		if _, err := Create(Options{DataDir: dir, OutDir: out, Keep: 2, Now: base.Add(time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(out)
	if len(entries) != 2 {
		t.Fatalf("保留份数应为 2，实际 %d", len(entries))
	}
}

// TestCreateMissingDataDir 数据目录不存在时明确报错，而不是造一个空归档。
func TestCreateMissingDataDir(t *testing.T) {
	if _, err := Create(Options{DataDir: filepath.Join(t.TempDir(), "nope")}); err == nil {
		t.Fatal("应报错")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func archiveNames(t *testing.T, path string) map[string]bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		out[hdr.Name] = true
	}
	return out
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
