package update

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TestListVersionsAssetAPIURLs 锁死「GitHub API asset 直连兜底」的解析结果：
// 域名/路径必须来自规范化后的 apiBase（自定义 gh_api_base 镜像也对），路径精确
// 是 /repos/<repo>/releases/assets/<id>，不含第三方镜像前缀，id<=0 不生成。
// 回滚掉 ID 解析 / AssetAPIURLs 生成，这个用例会直接编译失败或断言转红。
func TestListVersionsAssetAPIURLs(t *testing.T) {
	const repo = "Owner/Repo"
	const bdl = "https://github.com/Owner/Repo/releases/download/v1.2.3/triune-panel-linux-amd64"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 尾斜杠必须被规范化掉，否则列表 URL 会拼成 //repos/...
		if r.URL.Path != "/repos/Owner/Repo/releases" {
			t.Errorf("列表请求路径应规范化为 /repos/Owner/Repo/releases，实际 %q", r.URL.Path)
		}
		if r.Header.Get("User-Agent") == "" {
			t.Errorf("请求 GitHub API 必须带 User-Agent")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"tag_name":     "v1.2.3",
				"published_at": "2026-01-01T00:00:00Z",
				"assets": []map[string]any{
					{"name": "triune-panel-linux-amd64", "id": 101, "browser_download_url": bdl},
					{"name": "sha256sums.txt", "id": 102, "browser_download_url": "https://github.com/Owner/Repo/releases/download/v1.2.3/sha256sums.txt"},
					{"name": "no-id.bin", "id": 0, "browser_download_url": "https://github.com/Owner/Repo/releases/download/v1.2.3/no-id.bin"},
					{"name": "no-browser-url.txt", "id": 103}, // 无 browser_download_url 也应生成 API 地址
				},
			},
		})
	}))
	defer srv.Close()

	rels, err := ListVersions(srv.URL+"/", repo) // 故意带尾斜杠
	if err != nil {
		t.Fatalf("ListVersions 失败: %v", err)
	}
	if len(rels) != 1 {
		t.Fatalf("应有 1 条 release，实际 %d", len(rels))
	}
	rel := rels[0]

	wantBin := srv.URL + "/repos/Owner/Repo/releases/assets/101"
	wantSum := srv.URL + "/repos/Owner/Repo/releases/assets/102"
	wantNoBrowser := srv.URL + "/repos/Owner/Repo/releases/assets/103"
	if got := rel.AssetAPIURLs["triune-panel-linux-amd64"]; got != wantBin {
		t.Errorf("二进制资产 API 地址不对，期望 %q 实际 %q", wantBin, got)
	}
	if got := rel.AssetAPIURLs["sha256sums.txt"]; got != wantSum {
		t.Errorf("sha256sums API 地址不对，期望 %q 实际 %q", wantSum, got)
	}
	if got := rel.AssetAPIURLs["no-browser-url.txt"]; got != wantNoBrowser {
		t.Errorf("没有 browser_download_url 的资产也应生成 API 地址，实际 %q", got)
	}
	if _, ok := rel.AssetAPIURLs["no-id.bin"]; ok {
		t.Error("id<=0 的资产不该生成 API 兜底地址")
	}

	// 域名正确：跟随自定义 gh_api_base，而不是硬编码 api.github.com。
	apiBin := rel.AssetAPIURLs["triune-panel-linux-amd64"]
	parsed, perr := url.Parse(apiBin)
	if perr != nil {
		t.Fatalf("API 地址无法解析: %v", perr)
	}
	base, _ := url.Parse(srv.URL)
	if parsed.Scheme != base.Scheme || parsed.Host != base.Host {
		t.Errorf("API 地址域名应跟随自定义 gh_api_base(%s)，实际 %s", base.Host, parsed.Host)
	}
	if parsed.Path != "/repos/Owner/Repo/releases/assets/101" {
		t.Errorf("API 路径应为 /repos/<repo>/releases/assets/<id>，实际 %q", parsed.Path)
	}
	// 不含第三方镜像前缀：不是 mirror/https://... 这种拼接，也不硬编码官方域名。
	if apiBin != wantBin || strings.Contains(apiBin, "ghfast.top") ||
		strings.Contains(apiBin, "/https://") || strings.Contains(apiBin, "api.github.com") {
		t.Errorf("API 地址不该被套第三方镜像前缀，实际 %q", apiBin)
	}

	// 既有行为不变：AssetURLs / Assets 照常解析，缺 browser_download_url 不进 AssetURLs。
	if rel.AssetURLs["triune-panel-linux-amd64"] != bdl {
		t.Errorf("AssetURLs 应保持既有行为，实际 %q", rel.AssetURLs["triune-panel-linux-amd64"])
	}
	if _, ok := rel.AssetURLs["no-browser-url.txt"]; ok {
		t.Error("没有 browser_download_url 的资产不该进 AssetURLs")
	}
}

// TestDownloadWithFallbackPrimary504APIOK：主 URL 504、API URL 返回正确字节时，
// 下载成功且 sha256 校验通过；并验证兜底请求不套镜像、带 octet-stream、保留主地址后缀。
func TestDownloadWithFallbackPrimary504APIOK(t *testing.T) {
	good := []byte("triune-panel binary payload\n")
	wantSha := sha256Hex(good)

	var primaryHits, apiHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/download/v1.2.3/pkg.tar.gz":
			atomic.AddInt32(&primaryHits, 1)
			w.WriteHeader(http.StatusGatewayTimeout) // 504：CDN 边缘节点间歇故障
		case "/repos/O/R/releases/assets/555":
			atomic.AddInt32(&apiHits, 1)
			// 不套镜像：兜底地址被原样请求，路径里不该夹带 https:// 之类的前缀
			if strings.Contains(r.URL.Path, "https://") {
				t.Errorf("兜底请求路径被套了镜像前缀: %q", r.URL.Path)
			}
			// GitHub API asset 端点要 octet-stream 才回字节，不带这个头真实环境只回 JSON
			if got := r.Header.Get("Accept"); got != "application/octet-stream" {
				t.Errorf("兜底请求应带 Accept: application/octet-stream，实际 %q", got)
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(good)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	primaryURL := srv.URL + "/releases/download/v1.2.3/pkg.tar.gz"
	apiURL := srv.URL + "/repos/O/R/releases/assets/555"
	dir := t.TempDir()

	path, gotSha, used, err := DownloadWithFallback(primaryURL, func() string { return apiURL }, wantSha, dir)
	if err != nil {
		t.Fatalf("主地址 504 时应改走 API 兜底并成功，实际 err=%v", err)
	}
	if !used {
		t.Error("应标记走了兜底通道")
	}
	if gotSha != wantSha {
		t.Errorf("sha256 应通过校验，期望 %s 实际 %s", wantSha, gotSha)
	}
	// API 地址没有扩展名，兜底必须沿用主地址后缀，Extract 才能选对解压算法
	if !strings.HasSuffix(path, ".tar.gz") {
		t.Errorf("兜底临时文件必须保留主地址后缀，实际 %q", path)
	}
	b, rerr := os.ReadFile(path)
	if rerr != nil || string(b) != string(good) {
		t.Errorf("下载内容不对: %q err=%v", b, rerr)
	}
	if atomic.LoadInt32(&primaryHits) != 1 || atomic.LoadInt32(&apiHits) != 1 {
		t.Errorf("应各请求一次（主 504 + API 兜底），实际 primary=%d api=%d",
			atomic.LoadInt32(&primaryHits), atomic.LoadInt32(&apiHits))
	}
}

// TestDownloadWithFallbackAPIBadBytesSHAFails：API URL 返回坏字节时 sha256 校验
// 必须失败，不能把坏文件静默放行（兜底仍走 Download 的校验逻辑）。
func TestDownloadWithFallbackAPIBadBytesSHAFails(t *testing.T) {
	good := []byte("correct binary")
	bad := []byte("corrupt bytes from API")
	wantSha := sha256Hex(good) // 期望的是正确内容的哈希

	var apiHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/download/v1.2.3/pkg.tar.gz":
			w.WriteHeader(http.StatusGatewayTimeout)
		case "/repos/O/R/releases/assets/555":
			atomic.AddInt32(&apiHits, 1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(bad)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	primaryURL := srv.URL + "/releases/download/v1.2.3/pkg.tar.gz"
	apiURL := srv.URL + "/repos/O/R/releases/assets/555"
	dir := t.TempDir()

	path, _, used, err := DownloadWithFallback(primaryURL, func() string { return apiURL }, wantSha, dir)
	if err == nil {
		t.Fatal("API 地址返回坏字节时 sha256 校验必须失败，不能静默放行")
	}
	if path != "" {
		t.Errorf("失败时不应返回可用文件路径，实际 %q", path)
	}
	if used {
		t.Error("两次都失败不该标记「走了兜底」")
	}
	if got := atomic.LoadInt32(&apiHits); got != 1 {
		t.Errorf("主地址失败后应尝试一次 API 兜底，实际 api 命中 %d 次", got)
	}
	// 直接验证根因：这段坏字节经 Download 的 sha256 校验确实被挡下。
	if _, _, derr := Download(apiURL, wantSha, dir); derr == nil || !strings.Contains(derr.Error(), "sha256") {
		t.Errorf("坏字节应被 sha256 校验挡下，实际 err=%v", derr)
	}
}

// TestDownloadWithFallbackNoAPIURLFails：没有 API 兜底地址时，主 URL 失败就是失败，
// 不静默放行，也不发起第二次下载。
func TestDownloadWithFallbackNoAPIURLFails(t *testing.T) {
	var primaryHits, otherHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/releases/download/v1.2.3/pkg.tar.gz" {
			atomic.AddInt32(&primaryHits, 1)
			w.WriteHeader(http.StatusGatewayTimeout)
			return
		}
		atomic.AddInt32(&otherHits, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	primaryURL := srv.URL + "/releases/download/v1.2.3/pkg.tar.gz"
	dir := t.TempDir()

	path, _, used, err := DownloadWithFallback(primaryURL, func() string { return "" }, "", dir)
	if err == nil {
		t.Fatal("没有 API 兜底地址时，主地址失败就必须失败，不能静默放行")
	}
	if path != "" || used {
		t.Errorf("应返回空路径且未走兜底，实际 path=%q used=%v", path, used)
	}
	if atomic.LoadInt32(&primaryHits) != 1 || atomic.LoadInt32(&otherHits) != 0 {
		t.Errorf("只应请求主地址一次，不应发起第二次下载，实际 primary=%d other=%d",
			atomic.LoadInt32(&primaryHits), atomic.LoadInt32(&otherHits))
	}
}

// TestDownloadWithFallbackResolverIsLazy：主地址成功时绝不调用兜底解析器。
// 内核组件更新的兜底地址要重拉一次 releases API，惰性求值才不会给每次正常
// 更新平白加一次网络往返（保持既有行为）。
func TestDownloadWithFallbackResolverIsLazy(t *testing.T) {
	good := []byte("ok")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(good)
	}))
	defer srv.Close()

	var resolverCalls int32
	path, gotSha, used, err := DownloadWithFallback(
		srv.URL+"/pkg.tar.gz",
		func() string {
			atomic.AddInt32(&resolverCalls, 1)
			return srv.URL + "/repos/O/R/releases/assets/1"
		},
		sha256Hex(good), t.TempDir())
	if err != nil || used {
		t.Fatalf("主地址成功就应直接返回，不应触发兜底，err=%v used=%v", err, used)
	}
	if gotSha != sha256Hex(good) || path == "" {
		t.Errorf("主地址成功应返回正常结果，实际 path=%q sha=%s", path, gotSha)
	}
	if got := atomic.LoadInt32(&resolverCalls); got != 0 {
		t.Errorf("主地址成功时兜底解析器不应被调用，实际调用 %d 次", got)
	}
}
