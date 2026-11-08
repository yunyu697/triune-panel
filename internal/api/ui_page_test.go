package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStaleDiskUIPageFallsBackToEmbedded 模拟「升级后 data/web/index.html 没写进去」：
// 磁盘仍是上一版页面时，HTTP 必须给出当前二进制内嵌的界面，而不是旧页面。
// 这是「明明升级了，界面还是旧的」最常见的一类服务端成因。
func TestStaleDiskUIPageFallsBackToEmbedded(t *testing.T) {
	a := newSecurityTestServer(t)
	page := filepath.Join(a.DataDir, "web", "index.html")
	if err := os.MkdirAll(filepath.Dir(page), 0o755); err != nil {
		t.Fatal(err)
	}
	old := `<!DOCTYPE html><!-- triune-panel-web/v0.0.1 old --><html><body>OLD-UI-SENTINEL</body></html>`
	if err := os.WriteFile(page, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("首页应 200，实际 %d", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "OLD-UI-SENTINEL") {
		t.Error("仍在服务磁盘上的旧页面——升级后界面不会变")
	}
	if !strings.Contains(body, `id="cmdbar"`) {
		t.Error("没有拿到二进制内嵌的新界面（缺少命令条骨架）")
	}
	if !strings.Contains(body, "triune-panel-web/") {
		t.Error("响应页面缺少 build 标记")
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("首页 Cache-Control=%q，应为 no-cache", cc)
	}
	etag := w.Header().Get("ETag")
	if etag == "" {
		t.Fatal("首页缺少 ETag，中间缓存无法判断是否该换页面")
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self';") {
		t.Errorf("回退到内置 UI 后 CSP 应变回严格策略: %s", w.Header().Get("Content-Security-Policy"))
	}

	// 同一内容带 If-None-Match 应回 304，不再传整页。
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.Header.Set("If-None-Match", etag)
	a.Handler().ServeHTTP(w2, r2)
	if w2.Code != http.StatusNotModified {
		t.Errorf("带相同 ETag 应 304，实际 %d", w2.Code)
	}
	if w2.Body.Len() != 0 {
		t.Errorf("304 不应带响应体，实际 %d 字节", w2.Body.Len())
	}
}

// TestPageETagChangesWithContent 页面内容变了 ETag 必须跟着变，
// 否则浏览器/反代拿旧 ETag 会一直收到 304，界面永远停在上一版。
// 这里用不带标记的自定义页面，顺便确认 ResolvePage 不会误伤自定义 UI。
func TestPageETagChangesWithContent(t *testing.T) {
	a := newSecurityTestServer(t)
	page := filepath.Join(a.DataDir, "web", "index.html")
	if err := os.MkdirAll(filepath.Dir(page), 0o755); err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	write := func(s string) string {
		t.Helper()
		if err := os.WriteFile(page, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		if !strings.Contains(w.Body.String(), "ONE") && !strings.Contains(w.Body.String(), "TWO") {
			t.Error("无标记的自定义 UI 应原样返回，不该被内嵌页顶掉")
		}
		return w.Header().Get("ETag")
	}
	one := write(`<!DOCTYPE html><html><body>ONE</body></html>`)
	two := write(`<!DOCTYPE html><html><body>TWO</body></html>`)
	if one == "" || two == "" {
		t.Fatal("自定义 UI 也应有 ETag")
	}
	if one == two {
		t.Error("页面内容变了 ETag 却没变，缓存会一直 304")
	}
}

// TestMissingDiskPageServesEmbedded 磁盘页面读不到时不能 404。
//
// 只读挂载、目录被删、属主不对都会让 os.ReadFile 失败；此时若返回 404，
// 用户看到的是一张空白错误页，比「旧界面」更难判断，也无从下手。内嵌页
// 与当前二进制严格对应，直接服务它才是这一层的正确退路。
func TestMissingDiskPageServesEmbedded(t *testing.T) {
	a := newSecurityTestServer(t)
	// 刻意不创建 web/index.html
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("磁盘页面缺失时应服务内嵌页（200），实际 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `id="cmdbar"`) {
		t.Error("没有拿到内嵌界面")
	}
}
