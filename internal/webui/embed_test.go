package webui

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestDemoHTMLMatchesIndex demo.html 是 index.html 的逐字节副本——演示模式
// 本身就是 index.html 内建的（URL 带 #demo 或文件名是 demo.html 时启用），
// 所以两个文件必须完全一致。
//
// 这条断言是防手滑的：每次改 UI 都要 `cp index.html demo.html`，忘了的话
// 仓库里的离线演示会停留在旧版，而 CI 和日常使用都不会碰到它。
func TestDemoHTMLMatchesIndex(t *testing.T) {
	demo, err := os.ReadFile(filepath.Join("..", "..", "demo.html"))
	if err != nil {
		t.Fatalf("读 demo.html 失败: %v", err)
	}
	if !bytes.Equal(demo, indexHTML) {
		t.Error("demo.html 与 internal/webui/index.html 不一致——改完 UI 记得 `cp internal/webui/index.html demo.html`")
	}
}

// TestEmbeddedMarker 页面头部的 build 标记是「升级后不服务旧 UI」的唯一依据，
// 丢了它 EmbedIfNeeded 会永远覆盖用户自定义的 index.html（或者反过来，
// 永远不覆盖导致升级后界面停在旧版）。
func TestEmbeddedMarker(t *testing.T) {
	if embeddedMarker() == "" {
		t.Fatal("index.html 头部缺少 <!-- triune-panel-web/<ver> --> 标记")
	}
}

// TestPWAEmbedded PWA 资源必须都在内嵌文件里——漏一个的表现是浏览器静默
// 忽略清单（图标 404），手机上装不了，而服务端不会报任何错。
func TestPWAEmbedded(t *testing.T) {
	names := []string{
		"manifest.webmanifest",
		"sw.js",
		"icons/icon.svg",
		"icons/icon-192.png",
		"icons/icon-512.png",
		"icons/icon-maskable-512.png",
		"icons/apple-touch-icon.png",
	}
	for _, n := range names {
		data, etag, err := PWAFile(n)
		if err != nil {
			t.Errorf("%s 未内嵌: %v", n, err)
			continue
		}
		if len(data) == 0 {
			t.Errorf("%s 是空文件", n)
		}
		if etag == "" {
			t.Errorf("%s 没有 ETag", n)
		}
	}
	// 同一个文件两次读取的 ETag 必须一致，不同文件必须不同
	_, a, _ := PWAFile("sw.js")
	_, b, _ := PWAFile("sw.js")
	if a != b {
		t.Errorf("同一文件两次 ETag 不一致: %s vs %s", a, b)
	}
	_, c, _ := PWAFile("manifest.webmanifest")
	if a == c {
		t.Error("不同文件的 ETag 相同，说明摘要算错了")
	}
}
