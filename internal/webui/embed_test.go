package webui

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// buildDemo 按 deploy/build-demo.py 的同一规则生成离线演示页：把
// /assets/*.js 的引用换回内联脚本。
//
// 为什么不再是「逐字节副本」：CSP 收紧到 script-src 'self' 之后，脚本必须
// 作为独立文件加载；而 demo.html 是双击用 file:// 打开的，拿不到 /assets，
// 只能把两份脚本贴回去。内容仍与面板服务的页面严格对应。
func buildDemo(t *testing.T) []byte {
	t.Helper()
	html := indexHTML
	for _, name := range []string{"qr.js", "app.js"} {
		tag := []byte(`<script src="/assets/` + name + `"></script>`)
		if bytes.Count(html, tag) != 1 {
			t.Fatalf("index.html 里 %s 应恰好出现一次（实际 %d）", tag, bytes.Count(html, tag))
		}
		js, _, err := AssetFile(name)
		if err != nil {
			t.Fatalf("读 %s 失败: %v", name, err)
		}
		inline := append(append([]byte("<script>\n"), js...), []byte("</script>")...)
		html = bytes.Replace(html, tag, inline, 1)
	}
	return html
}

// TestDemoHTMLMatchesIndex demo.html 必须等于「index.html + 内联脚本」的生成结果。
//
// 这条断言是防手滑的：每次改 UI 都要 `python3 deploy/build-demo.py`，
// 忘了的话仓库里的离线演示会停留在旧版，而日常使用与测试都不会碰到它。
func TestDemoHTMLMatchesIndex(t *testing.T) {
	demo, err := os.ReadFile(filepath.Join("..", "..", "demo.html"))
	if err != nil {
		t.Fatalf("读 demo.html 失败: %v", err)
	}
	if want := buildDemo(t); !bytes.Equal(demo, want) {
		t.Error("demo.html 与 index.html 不同步——改完 UI 记得 `python3 deploy/build-demo.py`")
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
