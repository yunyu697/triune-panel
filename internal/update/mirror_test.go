package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNormalizeMirror(t *testing.T) {
	ok := map[string]string{
		"":                        "",
		"  ":                      "",
		"https://ghfast.top":      "https://ghfast.top",
		"https://ghfast.top/":     "https://ghfast.top",
		" http://127.0.0.1:8080 ": "http://127.0.0.1:8080",
	}
	for in, want := range ok {
		got, err := NormalizeMirror(in)
		if err != nil || got != want {
			t.Errorf("NormalizeMirror(%q) = %q,%v，期望 %q", in, got, err, want)
		}
	}
	// 只收 http(s)：挡掉 javascript: / file: 这类会被当成「地址」塞进请求的东西
	for _, bad := range []string{"ghfast.top", "ftp://x.com", "javascript:alert(1)", "https://a b.com"} {
		if _, err := NormalizeMirror(bad); err == nil {
			t.Errorf("NormalizeMirror(%q) 应该报错", bad)
		}
	}
}

func TestMirrorCandidatesAndFind(t *testing.T) {
	cands := MirrorCandidates()
	if len(cands) != len(Mirrors)+1 {
		t.Fatalf("候选应为预设 + 官方，实际 %d", len(cands))
	}
	if cands[0].ID != "official" || cands[0].Base != "" {
		t.Errorf("官方地址必须排在第一位且 Base 为空，实际 %+v", cands[0])
	}
	if _, ok := FindMirror("ghfast"); !ok {
		t.Error("按 id 应能找到 ghfast")
	}
	if m, ok := FindMirror("https://ghfast.top/"); !ok || m.ID != "ghfast" {
		t.Errorf("按 base 应能找到 ghfast（忽略结尾斜杠），实际 %+v %v", m, ok)
	}
	if m, ok := FindMirror(""); !ok || m.ID != "official" {
		t.Error("空串应映射到官方地址")
	}
	if _, ok := FindMirror("https://no-such-mirror.example"); ok {
		t.Error("未知镜像不该被认成预设（自定义镜像走 NormalizeMirror，不走这里）")
	}
}

// TestProbeOneHitsRealPath 镜像只转发 release/raw 路径，探针必须打真实下载地址。
// 这里用本地服务器验证「镜像前缀 + 原始 URL」这个拼法，以及 206 算可用。
func TestProbeOneHitsRealPath(t *testing.T) {
	var gotPath, gotRange string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotRange = r.Header.Get("Range")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("x"))
	}))
	defer srv.Close()

	target := "https://github.com/owner/repo/releases/download/v1.0.0/bin.tar.gz"
	res := probeOne(context.Background(), Mirror{ID: "local", Name: "local", Base: srv.URL}, target, 3*time.Second)
	if !res.OK {
		t.Fatalf("206 应判为可用，实际 %+v", res)
	}
	if gotPath != "/"+target {
		t.Errorf("镜像前缀应拼在原始 URL 前面，实际路径 %q", gotPath)
	}
	if gotRange != "bytes=0-0" {
		t.Errorf("应带 Range 只取首字节，实际 %q", gotRange)
	}
}

func TestProbeOneMarksFailures(t *testing.T) {
	cases := []struct {
		code int
		want string
	}{
		{http.StatusForbidden, "HTTP 403"},
		{http.StatusNotFound, "HTTP 404"},
		{http.StatusTooManyRequests, "HTTP 429"},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.code)
		}))
		res := probeOne(context.Background(), Mirror{Base: srv.URL}, "https://github.com/x", 2*time.Second)
		srv.Close()
		if res.OK || !strings.Contains(res.Err, c.want) {
			t.Errorf("状态 %d 应判为不可用并写明原因，实际 ok=%v err=%q", c.code, res.OK, res.Err)
		}
	}
}

// 官方地址（Base 为空）必须直接打原地址，不能拼出 "//https://..." 这种怪东西
func TestProbeOneOfficialUsesRawURL(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	res := probeOne(context.Background(), OfficialMirror, srv.URL+"/f.bin", 2*time.Second)
	if !res.OK || gotPath != "/f.bin" {
		t.Errorf("官方地址应直接请求目标，实际 ok=%v path=%q", res.OK, gotPath)
	}
}
