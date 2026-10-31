package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"triune/internal/store"
)

func TestHealthz(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := New(st, nil, nil, dir)

	oldVer := Version
	Version = "v9.9.9-test"
	defer func() { Version = oldVer }()

	do := func() (*httptest.ResponseRecorder, map[string]any) {
		w := httptest.NewRecorder()
		a.handleHealthz(w, httptest.NewRequest("GET", "/healthz", nil))
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("响应不是合法 JSON: %v: %s", err, w.Body.String())
		}
		return w, body
	}

	w, body := do()
	if w.Code != http.StatusOK {
		t.Fatalf("健康时 want 200, got %d", w.Code)
	}
	if body["status"] != "ok" || body["db"] != "ok" {
		t.Fatalf("健康时字段不对: %v", body)
	}
	if body["version"] != "v9.9.9-test" {
		t.Fatalf("版本号未透传: %v", body["version"])
	}
	if _, ok := body["uptime_sec"]; !ok {
		t.Fatalf("缺少 uptime_sec: %v", body)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("探活不应被缓存, Cache-Control=%q", cc)
	}

	// DB 关掉 → 必须 503，否则监控会把"进程活着但服务坏了"误判为健康
	_ = st.Close()
	w, body = do()
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("DB 不可用时 want 503, got %d", w.Code)
	}
	if body["status"] != "degraded" {
		t.Fatalf("DB 不可用时 want status=degraded, got %v", body["status"])
	}
}

// TestHealthzRoute 确认路由真的挂上了，且不会落到 SPA 兜底处理里。
func TestHealthzRoute(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := New(st, nil, nil, dir)

	h := a.Handler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct == "" || ct[:16] != "application/json" {
		t.Fatalf("应返回 JSON, got %q", ct)
	}
}
