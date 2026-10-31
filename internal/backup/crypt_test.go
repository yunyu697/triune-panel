package backup

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testPass = "correct-horse-battery-staple"

// TestEncryptedRoundTrip 加密备份能被同口令恢复，且内容一致。
func TestEncryptedRoundTrip(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "triune.db"), "db-bytes")
	mustWrite(t, filepath.Join(src, "certs", "fullchain.pem"), "PEM")

	out := filepath.Join(t.TempDir(), "bk")
	res, err := Create(Options{
		DataDir: src, OutDir: out, Keep: 3, Passphrase: testPass,
		Now: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Encrypted {
		t.Fatal("应标记为已加密")
	}
	if !strings.HasSuffix(res.Path, ".tar.gz"+EncryptedSuffix) {
		t.Fatalf("加密归档应以 %s 结尾: %s", EncryptedSuffix, res.Path)
	}

	// 目标目录要先存在（Restore 要求数据目录可用）
	dst := t.TempDir()
	rr, err := Restore(RestoreOptions{Archive: res.Path, DataDir: dst, Passphrase: testPass})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if rr.Files != 2 {
		t.Fatalf("应恢复 2 个文件，实际 %d", rr.Files)
	}
	if got, _ := os.ReadFile(filepath.Join(dst, "triune.db")); string(got) != "db-bytes" {
		t.Fatalf("恢复内容不对: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dst, "certs", "fullchain.pem")); string(got) != "PEM" {
		t.Fatalf("嵌套文件未恢复: %q", got)
	}
}

// TestEncryptedArchiveHasNoPlaintext 加密后磁盘上不能出现明文内容。
// 这是「加密到底有没有生效」最直接的验证——比检查文件头更可靠。
func TestEncryptedArchiveHasNoPlaintext(t *testing.T) {
	src := t.TempDir()
	const secret = "TOP-SECRET-STATS-TOKEN-42"
	mustWrite(t, filepath.Join(src, "triune.db"), secret)

	res, err := Create(Options{DataDir: src, Passphrase: testPass})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(secret)) {
		t.Fatal("加密归档里出现了明文内容，加密没有生效")
	}
	if !bytes.HasPrefix(raw, ageMagic) {
		t.Fatalf("缺少 age 魔数，实际头部: %q", raw[:min(24, len(raw))])
	}

	// 对照：同一份数据不加密时，解压后必须能搜到 secret。
	// 注意不能在 gzip 后的字节里直接搜——压缩会把短文本压掉，导致假阴性；
	// 这里先解压再搜，确保「密文里搜不到」不是因为数据本来就没有这段内容。
	plain, err := Create(Options{DataDir: src, OutDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	rawPlain, err := os.ReadFile(plain.Path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.HasPrefix(rawPlain, ageMagic) {
		t.Fatal("未加密归档不应带 age 魔数")
	}
	gz, err := gzip.NewReader(bytes.NewReader(rawPlain))
	if err != nil {
		t.Fatalf("未加密归档应是合法 gzip: %v", err)
	}
	tarBytes, err := io.ReadAll(gz)
	gz.Close()
	if err != nil {
		t.Fatalf("解压失败: %v", err)
	}
	if !bytes.Contains(tarBytes, []byte(secret)) {
		t.Fatal("未加密归档解压后应能搜到 secret（用于对照）")
	}
}

func TestIsEncrypted(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "triune.db"), "x")

	plain, err := Create(Options{DataDir: dir, OutDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if enc, err := IsEncrypted(plain.Path); err != nil || enc {
		t.Fatalf("明文归档应判定为未加密: enc=%v err=%v", enc, err)
	}

	encRes, err := Create(Options{DataDir: dir, OutDir: t.TempDir(), Passphrase: testPass})
	if err != nil {
		t.Fatal(err)
	}
	if enc, err := IsEncrypted(encRes.Path); err != nil || !enc {
		t.Fatalf("加密归档应判定为已加密: enc=%v err=%v", enc, err)
	}

	// 改名成 .tar.gz 也不能骗过魔数判断
	renamed := filepath.Join(t.TempDir(), "renamed.tar.gz")
	if err := os.Rename(encRes.Path, renamed); err != nil {
		t.Fatal(err)
	}
	if enc, _ := IsEncrypted(renamed); !enc {
		t.Fatal("改扩展名后仍应识别为加密")
	}
}

// TestRestoreEncryptedWithoutPassphrase 缺口令应返回可识别的哨兵错误，
// CLI 靠它决定"提示输入"而不是直接失败。
func TestRestoreEncryptedWithoutPassphrase(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "triune.db"), "db")
	res, err := Create(Options{DataDir: src, Passphrase: testPass})
	if err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	_, err = Restore(RestoreOptions{Archive: res.Path, DataDir: dst})
	if !errors.Is(err, ErrPassphraseRequired) {
		t.Fatalf("want ErrPassphraseRequired, got %v", err)
	}
	// 目标目录不应被写入任何东西
	entries, _ := os.ReadDir(dst)
	if len(entries) != 0 {
		t.Fatalf("失败时不应写入目标目录，发现 %d 项", len(entries))
	}
}

func TestRestoreWrongPassphrase(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "triune.db"), "db")
	res, err := Create(Options{DataDir: src, Passphrase: testPass})
	if err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	_, err = Restore(RestoreOptions{Archive: res.Path, DataDir: dst, Passphrase: "wrong-passphrase-123"})
	if err == nil {
		t.Fatal("口令错误时必须失败")
	}
	if !strings.Contains(err.Error(), "口令") {
		t.Fatalf("错误信息应提示口令问题，实际: %v", err)
	}
}

func TestShortPassphraseRejected(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "triune.db"), "db")
	if _, err := Create(Options{DataDir: src, Passphrase: "short"}); err == nil {
		t.Fatal("过短口令应被拒绝")
	}
	// 拒绝时要保证没留下半个文件
	entries, _ := os.ReadDir(filepath.Join(src, "backups"))
	if len(entries) != 0 {
		t.Fatalf("口令不合格时不应产生归档，发现 %d 个文件", len(entries))
	}
}

func TestRotateMixedSuffixes(t *testing.T) {
	dir := t.TempDir()
	names := []string{
		"triune-data-20260101-000000.tar.gz",
		"triune-data-20260202-000000.tar.bz2", // 非本包生成的命名，应被忽略
		"triune-data-20260303-000000.tar.gz.age",
		"triune-data-20260404-000000.tar.gz",
		"triune-data-20260505-000000.tar.gz.age",
	}
	for _, n := range names {
		mustWrite(t, filepath.Join(dir, n), "x")
	}
	// 合法归档 4 个（20260101 明文、20260303/20260505 加密、20260404 明文）
	removed, err := Rotate(dir, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 {
		t.Fatalf("应删除 2 份，实际 %d: %v", len(removed), removed)
	}
	left, _ := os.ReadDir(dir)
	leftNames := map[string]bool{}
	for _, e := range left {
		leftNames[e.Name()] = true
	}
	for _, want := range []string{"triune-data-20260404-000000.tar.gz", "triune-data-20260505-000000.tar.gz.age"} {
		if !leftNames[want] {
			t.Errorf("应保留最新的 %s，实际 %v", want, leftNames)
		}
	}
	if !leftNames["triune-data-20260202-000000.tar.bz2"] {
		t.Error("非法命名的文件不应被轮转删除")
	}
}

// TestEncryptedBackupRotatesWithPlain 加密与明文混存时，轮转仍只按时间保留最新 N 份。
func TestEncryptedBackupRotatesWithPlain(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "triune.db"), "db")
	out := t.TempDir()
	base := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

	for i := 0; i < 2; i++ {
		if _, err := Create(Options{DataDir: src, OutDir: out, Keep: 2,
			Now: base.Add(time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 2; i < 4; i++ {
		if _, err := Create(Options{DataDir: src, OutDir: out, Keep: 2, Passphrase: testPass,
			Now: base.Add(time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(out)
	if len(entries) != 2 {
		t.Fatalf("保留份数应为 2，实际 %d", len(entries))
	}
	// 保留的应是最新两个（都是加密的）
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), EncryptedSuffix) {
			t.Fatalf("应保留最新的加密归档，发现 %s", e.Name())
		}
	}
}

// TestExtractDBEncrypted 恢复前的 schema 只读检查也要能解开加密归档。
func TestExtractDBEncrypted(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "triune.db"), "db-bytes")
	res, err := Create(Options{DataDir: src, Passphrase: testPass})
	if err != nil {
		t.Fatal(err)
	}
	out, err := ExtractDB(res.Path, t.TempDir(), testPass)
	if err != nil {
		t.Fatalf("ExtractDB: %v", err)
	}
	if got, _ := os.ReadFile(out); string(got) != "db-bytes" {
		t.Fatalf("提取内容不对: %q", got)
	}
	if _, err := ExtractDB(res.Path, t.TempDir(), ""); !errors.Is(err, ErrPassphraseRequired) {
		t.Fatalf("缺口令应返回哨兵错误，实际 %v", err)
	}
}
