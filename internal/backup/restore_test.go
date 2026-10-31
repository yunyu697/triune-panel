package backup

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRestoreRoundTrip 备份→清空→恢复，文件应回来，backups/ 应保留。
func TestRestoreRoundTrip(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "triune.db"), "db-v1")
	mustWrite(t, filepath.Join(src, "web", "index.html"), "<html>v1")
	mustWrite(t, filepath.Join(src, "certs", "a.com", "fullchain.pem"), "cert")

	res, err := Create(Options{DataDir: src, Now: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}

	// 目标目录：已有旧数据 + 历史备份，恢复后旧数据应被替换、备份应保留
	dst := t.TempDir()
	mustWrite(t, filepath.Join(dst, "triune.db"), "db-old")
	mustWrite(t, filepath.Join(dst, "stale.txt"), "should-be-gone")
	mustWrite(t, filepath.Join(dst, "backups", "triune-data-20200101-000000.tar.gz"), "keep-me")

	out, err := Restore(RestoreOptions{Archive: res.Path, DataDir: dst, Now: time.Date(2026, 10, 31, 12, 5, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if out.Files != 3 {
		t.Errorf("应恢复 3 个文件，实际 %d", out.Files)
	}

	if b, err := os.ReadFile(filepath.Join(dst, "triune.db")); err != nil || string(b) != "db-v1" {
		t.Errorf("triune.db 未恢复: %q err=%v", string(b), err)
	}
	if _, err := os.Stat(filepath.Join(dst, "web", "index.html")); err != nil {
		t.Errorf("web/index.html 未恢复: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "certs", "a.com", "fullchain.pem")); err != nil {
		t.Errorf("证书未恢复: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "stale.txt")); !os.IsNotExist(err) {
		t.Error("旧的多余文件应被替换掉")
	}
	if _, err := os.Stat(filepath.Join(dst, "backups", "triune-data-20200101-000000.tar.gz")); err != nil {
		t.Errorf("backups/ 必须保留: %v", err)
	}
	if out.Preserved != 1 {
		t.Errorf("应统计到 1 份历史备份，实际 %d", out.Preserved)
	}
	// 临时目录必须清理干净
	entries, _ := os.ReadDir(dst)
	for _, e := range entries {
		if e.Name() == ".restore-"+"" || len(e.Name()) > 9 && (e.Name()[:9] == ".restore-" || e.Name()[:13] == ".pre-restore-") {
			t.Errorf("临时目录未清理: %s", e.Name())
		}
	}
}

// TestRestoreRejectsTraversal 带 ../ 的条目必须被拒绝，且不能写到目录外。
func TestRestoreRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "evil.tar.gz")
	writeTar(t, archive, map[string]string{"triune.db": "x", "../escape.txt": "pwned"})

	dst := t.TempDir()
	_, err := Restore(RestoreOptions{Archive: archive, DataDir: dst})
	if err == nil {
		t.Fatal("路径穿越应被拒绝")
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(dst), "escape.txt")); !os.IsNotExist(statErr) {
		t.Error("不得写出目标目录之外")
	}
}

// TestRestoreRequiresDB 没有 triune.db 的归档应被拒绝（避免误恢复别的东西）。
func TestRestoreRequiresDB(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "other.tar.gz")
	writeTar(t, archive, map[string]string{"readme.txt": "hello"})

	if _, err := Restore(RestoreOptions{Archive: archive, DataDir: t.TempDir()}); err == nil {
		t.Fatal("缺少 triune.db 应报错")
	}
}

// TestRestoreBadArchive 非 gzip 内容应给出可读错误。
func TestRestoreBadArchive(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.tar.gz")
	mustWrite(t, p, "not a gzip")
	if _, err := Restore(RestoreOptions{Archive: p, DataDir: t.TempDir()}); err == nil {
		t.Fatal("非法归档应报错")
	}
}

// TestExtractDBCarriesWAL 只读 schema 检查必须把 WAL 一起解出来。
//
// 面板的库是 WAL 模式，停服后最后一个事务常留在 triune.db-wal 里；
// 只解主库会读到空库，误报「no such table: settings」。
func TestExtractDBCarriesWAL(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "wal.tar.gz")
	writeTar(t, archive, map[string]string{
		"triune.db":      "MAIN",
		"triune.db-wal":  "WAL-FRAMES",
		"triune.db-shm":  "SHM",
		"web/index.html": "x",
	})

	out := t.TempDir()
	p, err := ExtractDB(archive, out, "")
	if err != nil {
		t.Fatal(err)
	}
	if p != filepath.Join(out, "triune.db") {
		t.Fatalf("返回的主库路径不对: %s", p)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "triune.db-wal")); string(b) != "WAL-FRAMES" {
		t.Fatalf("WAL 必须一并解出，实际 %q", string(b))
	}
	// 只解库文件，别把整包都倒出来
	if _, err := os.Stat(filepath.Join(out, "web")); !os.IsNotExist(err) {
		t.Error("不应解出 triune.db 之外的其他文件")
	}
}

// TestExtractDBEncryptedWAL 加密归档同样要带 WAL。
func TestExtractDBEncryptedWAL(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "triune.db"), "MAIN")
	mustWrite(t, filepath.Join(src, "triune.db-wal"), "WAL-FRAMES")
	res, err := Create(Options{DataDir: src, Passphrase: testPass})
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if _, err := ExtractDB(res.Path, out, testPass); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "triune.db-wal")); string(b) != "WAL-FRAMES" {
		t.Fatalf("加密归档的 WAL 未解出，实际 %q", string(b))
	}
}

func writeTar(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
}
