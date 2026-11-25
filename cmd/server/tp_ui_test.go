package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"triune/internal/webui"
)

// captureStdout 跑一段会打印的函数，把它写到 stdout 的内容收回来。
// cmdUIStatus 直接用 fmt.Printf，没有返回值可断言——只能这么接。
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	return <-done
}

// TestUIReportEmbeddedSizeIsEmbedded 盯住「内嵌页面」那一行的体积。
//
// 它一度取的是 ServedPage 返回的正文长度——那在自定义界面生效时就是**磁盘页**
// 的大小，于是 `tp ui` 一边说页面来源是磁盘上的自定义界面，一边把内嵌页体积
// 报成磁盘页体积。数字看着正常，只是属于另一个文件。
func TestUIReportEmbeddedSizeIsEmbedded(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	custom := "<!-- triune-panel-web/custom -->\n<html>hi</html>\n"
	if err := os.WriteFile(filepath.Join(dir, "web", "index.html"), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() { cmdUIStatus([]string{"--data", dir}) })

	var line string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "内嵌页面") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("输出里没有「内嵌页面」这一行：\n%s", out)
	}
	want := uiBytes(int64(len(webui.EmbeddedPage())))
	if !strings.Contains(line, want) {
		t.Fatalf("内嵌页面体积应为内嵌页大小 %s，实际：%s", want, line)
	}
	if diskSize := uiBytes(int64(len(custom))); diskSize != want && strings.Contains(line, diskSize) {
		t.Fatalf("内嵌页面体积取成了磁盘页大小 %s：%s", diskSize, line)
	}
	if !strings.Contains(out, "磁盘上的自定义界面（custom）") {
		t.Fatalf("自定义界面生效时，页面来源应报 custom：\n%s", out)
	}
}
