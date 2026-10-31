package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"triune/internal/store"
)

func TestLoginLockout(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := New(st, nil, nil, dir)

	doLogin := func() int {
		body, _ := json.Marshal(map[string]string{"user": "admin", "pass": "wrong"})
		req := httptest.NewRequest("POST", "/api/login", bytes.NewReader(body))
		req.RemoteAddr = "10.1.1.1:55555"
		w := httptest.NewRecorder()
		a.handleLogin(w, req)
		return w.Code
	}

	for i := 1; i <= 4; i++ {
		if c := doLogin(); c != http.StatusUnauthorized {
			t.Fatalf("attempt %d: want 401, got %d", i, c)
		}
	}
	if c := doLogin(); c != http.StatusUnauthorized {
		t.Fatalf("attempt 5: want 401 (last allowed), got %d", c)
	}
	for i := 6; i <= 9; i++ {
		if c := doLogin(); c != http.StatusTooManyRequests {
			t.Fatalf("attempt %d: want 429 locked, got %d", i, c)
		}
	}

	// 不同来源不受影响
	body, _ := json.Marshal(map[string]string{"user": "admin", "pass": "wrong"})
	req := httptest.NewRequest("POST", "/api/login", bytes.NewReader(body))
	req.RemoteAddr = "10.1.1.2:55555"
	w := httptest.NewRecorder()
	a.handleLogin(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("other ip: want 401, got %d", w.Code)
	}
}
