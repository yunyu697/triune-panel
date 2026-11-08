package webui

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// writePage 在临时数据目录里放一份 data/web/index.html。
func writePage(t *testing.T, content []byte) string {
	t.Helper()
	dir := t.TempDir()
	web := filepath.Join(dir, "web")
	if err := os.MkdirAll(web, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(web, "index.html"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestServedPageIgnoresStaleDiskPage 是这个架构改动的核心断言：
// 磁盘上躺着上一版页面（无论带不带标记），都不会被发出去。
//
// 旧实现「磁盘优先 + 标记比对」，只要标记读不出来（v0.8.1 之前的页面根本
// 没有标记）就会把旧页面当成自定义 UI 保留，界面于是永久停在旧版本。
func TestServedPageIgnoresStaleDiskPage(t *testing.T) {
	cases := []struct {
		name string
		page string
	}{
		{"带旧标记", "<!-- triune-panel-web/v0.9.0 -->\n<html>STALE</html>"},
		{"无标记但像内置页", `<html><body>STALE<script src="/assets/app.js"></script></body></html>`},
		{"无标记也不像内置页", `<!DOCTYPE html><html data-theme="light"><title>Triune Panel</title>
			<body>STALE-ANCIENT</body></html>`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := writePage(t, []byte(c.page))
			got, custom := ServedPage(dir)
			if custom {
				t.Fatal("未声明 custom 的磁盘页面不应被判为自定义界面")
			}
			if bytes.Contains(got, []byte("STALE")) {
				t.Fatal("磁盘上的旧页面绝不能参与渲染")
			}
			if !bytes.Equal(got, indexHTML) {
				t.Fatal("应服务二进制内嵌页")
			}
		})
	}
}

// TestServedPageUsesExplicitCustom 显式声明 custom 时，页面由磁盘接管。
func TestServedPageUsesExplicitCustom(t *testing.T) {
	custom := []byte(`<!DOCTYPE html><!-- triune-panel-web/custom -->` +
		`<html><body>MY-OWN-UI</body></html>`)
	dir := writePage(t, custom)

	got, isCustom := ServedPage(dir)
	if !isCustom {
		t.Fatal("声明 custom 的页面应被判为自定义界面")
	}
	if !bytes.Equal(got, custom) {
		t.Fatal("自定义页面应原样服务")
	}
}

// TestServedPageMissingFile 没有磁盘页面时服务内嵌页。
func TestServedPageMissingFile(t *testing.T) {
	got, custom := ServedPage(t.TempDir())
	if custom {
		t.Fatal("没有磁盘页面时不该被判为自定义界面")
	}
	if !bytes.Equal(got, indexHTML) {
		t.Fatal("应服务二进制内嵌页")
	}
}

// TestServedPageUnreadableFile 目录不可读（权限/只读挂载）也要能出页面。
func TestServedPageUnreadableFile(t *testing.T) {
	dir := t.TempDir()
	// 放一个目录占住 index.html 的位置，读取必然失败。
	if err := os.MkdirAll(filepath.Join(dir, "web", "index.html"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, custom := ServedPage(dir)
	if custom {
		t.Fatal("读不到页面时不该被判为自定义界面")
	}
	if !bytes.Equal(got, indexHTML) {
		t.Fatal("读不到磁盘页面时应服务内嵌页，而不是 404")
	}
}

func TestIsCustomUI(t *testing.T) {
	if !IsCustomUI([]byte(`<!-- triune-panel-web/custom --><html></html>`)) {
		t.Fatal("含 custom 标记应判定为自定义界面")
	}
	if IsCustomUI([]byte(`<!-- triune-panel-web/v1.5.0 --><html></html>`)) {
		t.Fatal("版本标记不是 custom 标记")
	}
	if IsCustomUI([]byte(`<html>plain</html>`)) {
		t.Fatal("普通页面不是自定义界面")
	}
}

// TestEmbeddedPageIsCopy 返回副本，调用方改它不能污染后续响应。
func TestEmbeddedPageIsCopy(t *testing.T) {
	p := EmbeddedPage()
	if len(p) == 0 {
		t.Fatal("内嵌页不应为空")
	}
	first := indexHTML[0]
	p[0] = 'X'
	if indexHTML[0] != first {
		t.Fatal("EmbeddedPage 必须返回副本")
	}
}

// TestEmbeddedVersionMatchesMarker 内嵌页必须带可读的 build 标记，
// release.sh 靠它校验「发布的 UI 与版本号一致」。
func TestEmbeddedVersionMatchesMarker(t *testing.T) {
	v := EmbeddedVersion()
	if v == "" {
		t.Fatal("内嵌页缺少 build 标记")
	}
	if PageMarker(indexHTML) != v {
		t.Fatal("PageMarker 与 EmbeddedVersion 应返回同一标记")
	}
}
