package api

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"triune/internal/store"
)

func TestParseTrustedProxies(t *testing.T) {
	cases := []struct {
		in   string
		want int
		bad  bool
	}{
		{"", 0, false},
		{"   ", 0, false},
		{"127.0.0.1", 1, false},
		{"127.0.0.1,10.0.0.0/8", 2, false},
		{"127.0.0.1 10.0.0.0/8\n192.168.0.0/16;172.16.0.0/12", 4, false},
		{"::1", 1, false},
		{"fd00::/8", 1, false},
		{"127.0.0.1,not-an-ip", 0, true},
		{"10.0.0.0/33", 0, true},
	}
	for _, c := range cases {
		got, err := parseTrustedProxies(c.in)
		if c.bad {
			if err == nil {
				t.Fatalf("%q: want error, got %d nets", c.in, len(got))
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q: unexpected error: %v", c.in, err)
		}
		if len(got) != c.want {
			t.Fatalf("%q: want %d nets, got %d", c.in, c.want, len(got))
		}
	}
}

// newReq 造一个带 RemoteAddr 与可选转发头的请求。
func newReq(remote string, hdr map[string]string) *http.Request {
	r := httptest.NewRequest("POST", "/api/login", nil)
	r.RemoteAddr = remote
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return r
}

// trustedList 把设置文本解析成网段列表，空串表示不信任任何代理。
func trustedList(t *testing.T, s string) []*net.IPNet {
	t.Helper()
	if s == "" {
		return nil
	}
	nets, err := parseTrustedProxies(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return nets
}

func TestClientIP(t *testing.T) {
	cases := []struct {
		name    string
		trusted string // 设置原文
		remote  string
		hdr     map[string]string
		want    string
	}{
		{
			name:   "默认不信任任何代理：忽略伪造的转发头",
			remote: "203.0.113.7:1000",
			hdr:    map[string]string{"X-Forwarded-For": "1.2.3.4", "X-Real-IP": "1.2.3.4"},
			want:   "203.0.113.7",
		},
		{
			name:    "直连对端不可信：即使配了别的网段也不采信转发头",
			trusted: "10.0.0.0/8",
			remote:  "203.0.113.7:1000",
			hdr:     map[string]string{"X-Forwarded-For": "1.2.3.4"},
			want:    "203.0.113.7",
		},
		{
			name:    "本机 nginx 代理：取 XFF 客户端地址",
			trusted: "127.0.0.1",
			remote:  "127.0.0.1:50000",
			hdr:     map[string]string{"X-Forwarded-For": "198.51.100.9"},
			want:    "198.51.100.9",
		},
		{
			name:    "多级代理：从右往左跳过可信代理，取第一个不可信的",
			trusted: "127.0.0.1,10.0.0.0/8",
			remote:  "127.0.0.1:50000",
			hdr:     map[string]string{"X-Forwarded-For": "198.51.100.9, 10.1.2.3"},
			want:    "198.51.100.9",
		},
		{
			name:    "客户端伪造左侧地址无效：仍取最右侧真实客户端",
			trusted: "127.0.0.1",
			remote:  "127.0.0.1:50000",
			hdr:     map[string]string{"X-Forwarded-For": "6.6.6.6, 198.51.100.9"},
			want:    "198.51.100.9",
		},
		{
			name:    "全链路可信：退回最左一项",
			trusted: "127.0.0.1,10.0.0.0/8",
			remote:  "127.0.0.1:50000",
			hdr:     map[string]string{"X-Forwarded-For": "10.9.9.9, 10.1.2.3"},
			want:    "10.9.9.9",
		},
		{
			name:    "只有 X-Real-IP 时用它",
			trusted: "127.0.0.1",
			remote:  "127.0.0.1:50000",
			hdr:     map[string]string{"X-Real-IP": "198.51.100.9"},
			want:    "198.51.100.9",
		},
		{
			name:    "XFF 全是垃圾值：退回直连对端",
			trusted: "127.0.0.1",
			remote:  "127.0.0.1:50000",
			hdr:     map[string]string{"X-Forwarded-For": "unknown, n/a"},
			want:    "127.0.0.1",
		},
		{
			name:    "IPv6 回环代理",
			trusted: "::1",
			remote:  "[::1]:50000",
			hdr:     map[string]string{"X-Forwarded-For": "2001:db8::1"},
			want:    "2001:db8::1",
		},
	}

	for _, c := range cases {
		got := clientIPFrom(newReq(c.remote, c.hdr), trustedList(t, c.trusted))
		if got != c.want {
			t.Fatalf("%s: want %q, got %q", c.name, c.want, got)
		}
	}
}

// TestLoginLockoutRespectsForwardedIP 反向代理下不同客户端不应共享锁定计数。
func TestLoginLockoutRespectsForwardedIP(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetSetting(TrustedProxiesSetting, "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	a := New(st, nil, nil, dir)

	attempt := func(xff string) int {
		req := httptest.NewRequest("POST", "/api/login",
			bytes.NewReader([]byte(`{"user":"admin","pass":"wrong"}`)))
		req.RemoteAddr = "127.0.0.1:50000"
		req.Header.Set("X-Forwarded-For", xff)
		w := httptest.NewRecorder()
		a.handleLogin(w, req)
		return w.Code
	}

	// 客户端 A 连续失败 5 次后被锁
	for i := 1; i <= 5; i++ {
		if c := attempt("198.51.100.9"); c != http.StatusUnauthorized {
			t.Fatalf("A attempt %d: want 401, got %d", i, c)
		}
	}
	if c := attempt("198.51.100.9"); c != http.StatusTooManyRequests {
		t.Fatalf("A after lock: want 429, got %d", c)
	}
	// 客户端 B 不受影响（旧实现下两者来源都是 127.0.0.1，B 会被连坐）
	if c := attempt("198.51.100.10"); c != http.StatusUnauthorized {
		t.Fatalf("B: want 401 (not locked), got %d", c)
	}
}
