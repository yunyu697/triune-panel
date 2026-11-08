package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeDiskPage 在测试服务器的数据目录里放一份 data/web/index.html。
func writeDiskPage(t *testing.T, a *Server, body string) {
	t.Helper()
	page := filepath.Join(a.DataDir, "web", "index.html")
	if err := os.MkdirAll(filepath.Dir(page), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(page, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestStaleDiskPageNeverServed 「升级后界面还是旧的」曾经的服务端成因：
// 磁盘上留着上一版页面，而判据认不出它，于是旧页面被一直发出去。
// 现在页面只从二进制内嵌出，磁盘文件根本不参与渲染。
func TestStaleDiskPageNeverServed(t *testing.T) {
	a := newSecurityTestServer(t)
	writeDiskPage(t, a, `<!DOCTYPE html><!-- triune-panel-web/v0.0.1 old --><html><body>OLD-UI-SENTINEL</body></html>`)

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
	if w.Header().Get("ETag") == "" {
		t.Fatal("首页缺少 ETag，中间缓存无法判断是否该换页面")
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self';") {
		t.Errorf("内置 UI 必须是严格 CSP，实际: %s", csp)
	}
}

// TestAncientUnmarkedDiskPageNeverServed 是最难认、也是真实踩到的那一类：
// v0.8.1 之前的页面没有 build 标记、脚本内联、也没有 cmdbar/workbench。
// 旧判据（比标记 / 看页面形态）对它全部失效，界面永久停在旧版。
func TestAncientUnmarkedDiskPageNeverServed(t *testing.T) {
	a := newSecurityTestServer(t)
	writeDiskPage(t, a, `<!DOCTYPE html>
<html lang="zh-CN" data-theme="light">
<head><meta charset="utf-8"><title>Triune Panel</title></head>
<body>ANCIENT-UI-SENTINEL<div id="app"></div>
<script>fetch("/api/login")</script></body></html>`)

	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	body := w.Body.String()
	if strings.Contains(body, "ANCIENT-UI-SENTINEL") {
		t.Fatal("无标记的早期页面也必须被忽略：它既无标记、也不含任何可识别特征")
	}
	if !strings.Contains(body, `id="cmdbar"`) {
		t.Fatal("应服务当前二进制内嵌页")
	}
}

// TestExplicitCustomPageIsServed 显式声明 custom 的页面由磁盘接管，
// 并保留宽松 CSP（自定义界面可能用内联脚本/内联事件）。
func TestExplicitCustomPageIsServed(t *testing.T) {
	a := newSecurityTestServer(t)
	writeDiskPage(t, a, `<!DOCTYPE html><!-- triune-panel-web/custom -->`+
		`<html><body><button onclick="x()">go</button><script>function x(){}</script>MY-OWN-UI</body></html>`)

	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("自定义 UI 应 200，实际 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "MY-OWN-UI") {
		t.Fatal("声明 custom 的页面应原样服务")
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
		t.Fatalf("自定义 UI 应拿到放宽的 CSP，实际: %s", csp)
	}
}

// TestPageETagStableAcrossRequests 同一页面多次请求 ETag 必须一致，
// 否则每次刷新都当新页面重传，304 形同虚设。
func TestPageETagStableAcrossRequests(t *testing.T) {
	a := newSecurityTestServer(t)
	h := a.Handler()
	get := func() (etag, body string) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		return w.Header().Get("ETag"), w.Body.String()
	}
	e1, b1 := get()
	e2, b2 := get()
	if e1 == "" || e2 == "" {
		t.Fatal("首页必须有 ETag")
	}
	if e1 != e2 {
		t.Errorf("同一页面 ETag 不稳定：%s vs %s", e1, e2)
	}
	if b1 != b2 {
		t.Error("同一二进制两次响应内容应完全一致")
	}
	// 带相同 ETag 应 304，不再传整页。
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("If-None-Match", e1)
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotModified {
		t.Errorf("带相同 ETag 应 304，实际 %d", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Errorf("304 不应带响应体，实际 %d 字节", w.Body.Len())
	}
}

// TestCustomPageETagChanges 自定义 UI 改了内容，ETag 必须跟着变。
func TestCustomPageETagChanges(t *testing.T) {
	a := newSecurityTestServer(t)
	h := a.Handler()
	write := func(body string) string {
		t.Helper()
		writeDiskPage(t, a, `<!-- triune-panel-web/custom -->`+body)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		if !strings.Contains(w.Body.String(), "CUSTOM") {
			t.Fatal("声明 custom 的页面应原样返回")
		}
		return w.Header().Get("ETag")
	}
	one := write(`<html><body>CUSTOM-ONE</body></html>`)
	two := write(`<html><body>CUSTOM-TWO</body></html>`)
	if one == "" || two == "" {
		t.Fatal("自定义 UI 也应有 ETag")
	}
	if one == two {
		t.Error("页面内容变了 ETag 却没变，缓存会一直 304")
	}
}

// TestMissingDiskPageServesEmbedded 磁盘页面读不到时不能 404。
func TestMissingDiskPageServesEmbedded(t *testing.T) {
	a := newSecurityTestServer(t)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("磁盘页面缺失时应服务内嵌页（200），实际 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `id="cmdbar"`) {
		t.Error("没有拿到内嵌界面")
	}
}

// TestUnknownPathStill404 `/` 的兜底不能把未知路径也接管，
// 否则 /assets/写错名字.js 会拿到 200 + HTML，浏览器按 JS 解析时静默报语法错。
func TestUnknownPathStill404(t *testing.T) {
	a := newSecurityTestServer(t)
	for _, p := range []string{"/assets/nope.js", "/whatever", "/foo/bar"} {
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s 应 404，实际 %d", p, w.Code)
		}
	}
}
