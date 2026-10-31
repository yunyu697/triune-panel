package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRclone 写一个假的 rclone：用本地目录模拟远端。
// 只实现本包用到的四个子命令（copy / lsjson / lsf / deletefile），
// 这样测试不需要网络、不需要真装 rclone。
func fakeRclone(t *testing.T) (bin, store string) {
	t.Helper()
	dir := t.TempDir()
	store = filepath.Join(dir, "remote")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	bin = filepath.Join(dir, "rclone")
	script := `#!/bin/sh
set -e
STORE="$FAKE_RCLONE_STORE"
cmd="$1"; shift
case "$cmd" in
  copy)
    src="$1"; dst="$2"
    d="${dst#*:}"
    mkdir -p "$STORE/$d"
    cp "$src" "$STORE/$d/$(basename "$src")"
    ;;
  lsjson)
    p="$1"
    d="${p%/*}"; n="${p##*/}"; d="${d#*:}"
    [ -f "$STORE/$d/$n" ] || { echo "object not found" >&2; exit 3; }
    sz=$(wc -c < "$STORE/$d/$n" | tr -d ' ')
    printf '[{"Size":%s,"Name":"%s"}]\n' "$sz" "$n"
    ;;
  lsf)
    p="$1"
    d="${p#*:}"
    [ -d "$STORE/$d" ] && ls -1 "$STORE/$d" || true
    ;;
  deletefile)
    p="$1"
    d="${p%/*}"; n="${p##*/}"; d="${d#*:}"
    rm -f "$STORE/$d/$n"
    ;;
  *)
    echo "unsupported subcommand: $cmd" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_RCLONE_STORE", store)
	return bin, store
}

func TestUploadCopiesAndVerifies(t *testing.T) {
	bin, store := fakeRclone(t)
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "triune.db"), "db")
	res, err := Create(Options{DataDir: src, Passphrase: testPass})
	if err != nil {
		t.Fatal(err)
	}

	up, err := Upload(context.Background(), res.Path, UploadOptions{
		Remote: "r2:triune-backups", RcloneBin: bin,
	})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if !strings.HasPrefix(up.RemotePath, "r2:triune-backups/") {
		t.Fatalf("远端路径不对: %s", up.RemotePath)
	}
	if up.Bytes != res.Size {
		t.Fatalf("回读字节数应与本地一致: remote=%d local=%d", up.Bytes, res.Size)
	}
	// 远端真的落盘了
	got := filepath.Join(store, "triune-backups", filepath.Base(res.Path))
	if fi, err := os.Stat(got); err != nil || fi.Size() != res.Size {
		t.Fatalf("远端文件缺失或大小不符: %v", err)
	}
}

// TestUploadDetectsTruncation 上传后回读校验必须能发现截断。
// 这是整个上传流程里最重要的一条：备份最怕"看起来传上去了"。
func TestUploadDetectsTruncation(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "remote")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	// 故意只写前 8 字节，模拟网络截断
	bin := filepath.Join(dir, "rclone")
	script := `#!/bin/sh
set -e
STORE="$FAKE_RCLONE_STORE"
cmd="$1"; shift
case "$cmd" in
  copy)
    src="$1"; dst="$2"
    d="${dst#*:}"
    mkdir -p "$STORE/$d"
    head -c 8 "$src" > "$STORE/$d/$(basename "$src")"
    ;;
  lsjson)
    p="$1"; d="${p%/*}"; n="${p##*/}"; d="${d#*:}"
    sz=$(wc -c < "$STORE/$d/$n" | tr -d ' ')
    printf '[{"Size":%s,"Name":"%s"}]\n' "$sz" "$n"
    ;;
  *) exit 0 ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_RCLONE_STORE", store)

	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "triune.db"), strings.Repeat("x", 4096))
	res, err := Create(Options{DataDir: src})
	if err != nil {
		t.Fatal(err)
	}
	_, err = Upload(context.Background(), res.Path, UploadOptions{Remote: "r2:b", RcloneBin: bin})
	if err == nil {
		t.Fatal("截断的上传必须报错")
	}
	if !strings.Contains(err.Error(), "截断") {
		t.Fatalf("错误信息应说明截断，实际: %v", err)
	}
}

// TestUploadRotatesRemote 远端只保留最新 N 份，且只动本包命名的归档。
func TestUploadRotatesRemote(t *testing.T) {
	bin, store := fakeRclone(t)
	remoteDir := filepath.Join(store, "bk")
	if err := os.MkdirAll(remoteDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 远端已有 4 份旧归档 + 1 个别人的文件
	for _, n := range []string{
		"triune-data-20260101-000000.tar.gz",
		"triune-data-20260202-000000.tar.gz.age",
		"triune-data-20260303-000000.tar.gz",
		"triune-data-20260404-000000.tar.gz.age",
		"someone-else.txt",
	} {
		mustWrite(t, filepath.Join(remoteDir, n), "x")
	}

	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "triune.db"), "db")
	res, err := Create(Options{DataDir: src})
	if err != nil {
		t.Fatal(err)
	}
	up, err := Upload(context.Background(), res.Path, UploadOptions{
		Remote: "r2:bk", RcloneBin: bin, Keep: 2,
	})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	// 上传后共 5 份归档，保留 2 份 → 删掉 3 份
	if len(up.Removed) != 3 {
		t.Fatalf("应清理 3 份，实际 %d: %v", len(up.Removed), up.Removed)
	}
	entries, _ := os.ReadDir(remoteDir)
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if len(left) != 3 { // 2 份归档 + someone-else.txt
		t.Fatalf("远端应剩 3 项，实际 %v", left)
	}
	if _, err := os.Stat(filepath.Join(remoteDir, "someone-else.txt")); err != nil {
		t.Fatal("不得删除远端目录里非本包命名的文件")
	}
	// 最新的（刚上传的）必须还在
	if _, err := os.Stat(filepath.Join(remoteDir, filepath.Base(res.Path))); err != nil {
		t.Fatal("刚上传的归档不应被自己轮转掉")
	}
}

// TestUploadKeepZeroLeavesRemoteAlone Keep<=0 时不做任何删除。
func TestUploadKeepZeroLeavesRemoteAlone(t *testing.T) {
	bin, store := fakeRclone(t)
	remoteDir := filepath.Join(store, "bk")
	if err := os.MkdirAll(remoteDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"triune-data-20260101-000000.tar.gz", "triune-data-20260202-000000.tar.gz"} {
		mustWrite(t, filepath.Join(remoteDir, n), "x")
	}
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "triune.db"), "db")
	res, err := Create(Options{DataDir: src})
	if err != nil {
		t.Fatal(err)
	}
	up, err := Upload(context.Background(), res.Path, UploadOptions{Remote: "r2:bk", RcloneBin: bin})
	if err != nil {
		t.Fatal(err)
	}
	if len(up.Removed) != 0 {
		t.Fatalf("Keep<=0 时不应删除任何远端文件: %v", up.Removed)
	}
	entries, _ := os.ReadDir(remoteDir)
	if len(entries) != 3 {
		t.Fatalf("远端应保留原有 2 份 + 新增 1 份，实际 %d", len(entries))
	}
}

// TestUploadMissingRclone 没装 rclone 时要给出可操作的提示，而不是裸错误。
func TestUploadMissingRclone(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "triune.db"), "db")
	res, err := Create(Options{DataDir: src})
	if err != nil {
		t.Fatal(err)
	}
	_, err = Upload(context.Background(), res.Path, UploadOptions{
		Remote: "r2:bk", RcloneBin: "/nonexistent/rclone-binary",
	})
	if err == nil {
		t.Fatal("缺少 rclone 时必须报错")
	}
	if !strings.Contains(err.Error(), "rclone config") {
		t.Fatalf("错误信息应给出安装/配置提示，实际: %v", err)
	}
}

func TestUploadRequiresRemote(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "triune.db"), "db")
	res, err := Create(Options{DataDir: src})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Upload(context.Background(), res.Path, UploadOptions{}); err == nil {
		t.Fatal("未指定远端应报错")
	}
}
