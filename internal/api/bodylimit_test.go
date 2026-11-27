package api

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 统一请求体上限。多数 handler 直接 Decode(r.Body)，没有自己的限长；
// 没有这层闸时，一个无限大的 body 会一直占着连接与解析内存。
//
// 走 Handler() 而不是直接测中间件：要钉的是「这层真的装上了」，
// 不是「函数本身能干活」。
func TestHandlerCapsOversizedBody(t *testing.T) {
	a := newRoleTestServer(t)
	h := a.Handler()

	// 超限的 body 必须是**合法 JSON**：否则 handler 自己就会解析失败，
	// 「非 200」证明不了上限存在（第一版测试就是这么假绿的）。用一个
	// 13 MB 的 pad 字段把体积撑上去，无闸时 handler 会忽略它并照常登录。
	var buf bytes.Buffer
	buf.WriteString(`{"user":"admin","pass":"admin","pad":"`)
	buf.Write(bytes.Repeat([]byte("a"), maxRequestBody))
	buf.WriteString(`"}`)
	req := httptest.NewRequest("POST", "/api/login", &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Fatalf("超过上限的请求体应被拒，实际 200 —— 上限没装上")
	}
	// 合法大小的登录必须照常工作，别把闸做成「谁都进不来」。
	req2 := httptest.NewRequest("POST", "/api/login",
		strings.NewReader(`{"user":"admin","pass":"admin"}`))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("正常登录应 200，实际 %d: %s", w2.Code, w2.Body.String())
	}
}

// 中间件本身：超限时报错（handler 拿不到完整 body），未超限时读得完。
func TestBodyLimitMiddleware(t *testing.T) {
	readAll := func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if n == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
	h := (&Server{}).bodyLimit(http.HandlerFunc(readAll))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/x", bytes.NewReader(bytes.Repeat([]byte("a"), maxRequestBody+1))))
	if w.Code != http.StatusBadRequest {
		t.Errorf("超限应 400，实际 %d", w.Code)
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/x", strings.NewReader("small")))
	if w.Code != http.StatusOK {
		t.Errorf("未超限应 200，实际 %d", w.Code)
	}
}
