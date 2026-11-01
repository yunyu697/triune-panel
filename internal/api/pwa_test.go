package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPWARoutesServeEmbedded 校验 PWA 资源真的挂在站点根上、内容类型正确。
//
// 这几条路由特别容易被后来的改动碰坏：它们排在「/」兜底之前，一旦有人
// 调整顺序或改成子目录，表现是「手机上装不了」——浏览器不会报错，
// 只会静默忽略清单，排查起来很费劲。
func TestPWARoutesServeEmbedded(t *testing.T) {
	a := newSecurityTestServer(t)
	h := a.Handler()

	cases := []struct {
		path string
		ct   string
		want string // 内容里必须出现的片段
	}{
		{"/manifest.webmanifest", "application/manifest+json", `"short_name"`},
		{"/sw.js", "text/javascript; charset=utf-8", "self.addEventListener(\"fetch\""},
		{"/icons/icon-192.png", "image/png", ""},
		{"/icons/icon-512.png", "image/png", ""},
		{"/icons/icon-maskable-512.png", "image/png", ""},
		{"/icons/apple-touch-icon.png", "image/png", ""},
		{"/icons/icon.svg", "image/svg+xml", "<svg"},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", c.path, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("状态码 = %d，期望 200", w.Code)
			}
			if got := w.Header().Get("Content-Type"); got != c.ct {
				t.Errorf("Content-Type = %q，期望 %q", got, c.ct)
			}
			if w.Header().Get("ETag") == "" {
				t.Error("缺少 ETag：图标文件名不变，没有 ETag 就只能靠 no-cache 全量重下")
			}
			if c.want != "" && !strings.Contains(w.Body.String(), c.want) {
				t.Errorf("响应体里没有 %q", c.want)
			}
			// 逐个校验文件头：图标路由共用同一个 handler 实例，一旦有人在
			// 闭包里就地改写文件名，第二个请求就会拿到第一个请求的文件，
			// 而 Content-Type 和状态码都还是对的——只有内容对不上。
			if c.ct == "image/png" && !bytes.HasPrefix(w.Body.Bytes(), []byte("\x89PNG\r\n\x1a\n")) {
				t.Errorf("响应体不是 PNG（%d 字节）", w.Body.Len())
			}
			// 免认证：这些文件在登录墙外也必须能取到，否则未登录时
			// 浏览器拉不到清单，安装入口直接消失。
			if w.Code == http.StatusUnauthorized {
				t.Error("PWA 资源不该要求登录")
			}
		})
	}
}

// TestPWAManifestIsValidJSON 清单是纯 JSON，写错一个逗号浏览器就整份丢弃，
// 而且不会有任何提示。这里直接解析一遍兜住。
func TestPWAManifestIsValidJSON(t *testing.T) {
	a := newSecurityTestServer(t)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/manifest.webmanifest", nil))

	var m struct {
		Name       string `json:"name"`
		ShortName  string `json:"short_name"`
		StartURL   string `json:"start_url"`
		Scope      string `json:"scope"`
		Display    string `json:"display"`
		ThemeColor string `json:"theme_color"`
		Icons      []struct {
			Src     string `json:"src"`
			Sizes   string `json:"sizes"`
			Type    string `json:"type"`
			Purpose string `json:"purpose"`
		} `json:"icons"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("manifest 不是合法 JSON: %v", err)
	}
	if m.Name == "" || m.ShortName == "" {
		t.Error("name / short_name 不能为空：Chrome 靠它们决定安装后的名字")
	}
	if m.StartURL != "/" || m.Scope != "/" {
		t.Errorf("start_url=%q scope=%q，都必须是 /：面板挂在根上，装到主屏后不能打开子路径",
			m.StartURL, m.Scope)
	}
	if m.Display != "standalone" {
		t.Errorf("display=%q，期望 standalone（否则装上后还是带地址栏的浏览器）", m.Display)
	}
	if m.ThemeColor == "" {
		t.Error("缺少 theme_color")
	}

	// Chrome 的可安装性硬性要求：192 与 512 各一张
	var has192, has512, hasMaskable bool
	for _, ic := range m.Icons {
		if !strings.HasPrefix(ic.Src, "/icons/") {
			t.Errorf("图标路径 %q 不在 /icons/ 下：相对路径在子路径部署时会解析错", ic.Src)
		}
		switch ic.Sizes {
		case "192x192":
			has192 = true
		case "512x512":
			has512 = true
		}
		if strings.Contains(ic.Purpose, "maskable") {
			hasMaskable = true
		}
	}
	if !has192 || !has512 {
		t.Error("缺少 192x192 或 512x512 图标：Chrome 不认为这是可安装的站点")
	}
	if !hasMaskable {
		t.Error("缺少 maskable 图标：Android 会自己裁形状，没留安全区会被切掉边")
	}

	// 清单里引用的每个图标都必须真能取到，否则安装照样失败
	h := a.Handler()
	for _, ic := range m.Icons {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", ic.Src, nil))
		if w.Code != http.StatusOK {
			t.Errorf("图标 %s 取不到：状态码 %d", ic.Src, w.Code)
		}
	}
}

// TestPWAMissingIconIs404 不存在的图标要 404，不能落到「/」的兜底上返回
// 一个 HTML 页面——浏览器拿到 Content-Type: text/html 的图标会静默失败。
func TestPWAMissingIconIs404(t *testing.T) {
	a := newSecurityTestServer(t)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/icons/nope.png", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("状态码 = %d，期望 404", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q，期望非 HTML", ct)
	}
}

// TestPWAETagRevalidation 带 If-None-Match 时应回 304 且不带响应体。
func TestPWAETagRevalidation(t *testing.T) {
	a := newSecurityTestServer(t)
	h := a.Handler()

	first := httptest.NewRecorder()
	h.ServeHTTP(first, httptest.NewRequest("GET", "/sw.js", nil))
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("首次响应没有 ETag")
	}

	req := httptest.NewRequest("GET", "/sw.js", nil)
	req.Header.Set("If-None-Match", etag)
	second := httptest.NewRecorder()
	h.ServeHTTP(second, req)
	if second.Code != http.StatusNotModified {
		t.Fatalf("状态码 = %d，期望 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Errorf("304 不该带响应体，实际 %d 字节", second.Body.Len())
	}
}
