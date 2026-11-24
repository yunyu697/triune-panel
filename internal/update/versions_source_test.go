package update

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 这组用例钉的是「拿不到版本列表」这件事本身，而不是某一条报错文案：
// 后端要能区分「配额用完」（等一会儿就好、可以换通道）与「403 但不是配额」
// （Token 无效 / 被代理拦，等多久都没用），并且真的换了通道之后列表还能用。

func releasesJSON() string {
	return `[
	  {"tag_name":"v1.2.0","body":"new","prerelease":false,"published_at":"2026-01-02T00:00:00Z",
	   "assets":[{"name":"pkg","browser_download_url":"https://example.invalid/pkg","id":7}]},
	  {"tag_name":"v1.1.0","body":"old","prerelease":false,"published_at":"2026-01-01T00:00:00Z","assets":[]},
	  {"tag_name":"v1.3.0-rc1","body":"pre","prerelease":true,"published_at":"2026-01-03T00:00:00Z","assets":[]}
	]`
}

func TestRateLimitErrorSaysWhatToDo(t *testing.T) {
	InvalidateVersions()
	reset := time.Now().Add(7 * time.Minute).Truncate(time.Second)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "60")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Resource", "core")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		w.WriteHeader(403)
	}))
	defer srv.Close()

	_, err := ListVersionsToken(srv.URL, "o/r", "")
	if err == nil {
		t.Fatal("403 应当报错")
	}
	if !IsRateLimited(err) {
		t.Fatalf("配额耗尽必须能被识别出来（调用方靠它换通道），实际: %v", err)
	}
	msg := err.Error()
	for _, want := range []string{"配额", "60 次/小时", "恢复", "Token"} {
		if !strings.Contains(msg, want) {
			t.Errorf("错误里缺 %q：用户看完仍不知道能做什么\n%s", want, msg)
		}
	}
	// 只写「HTTP 403」等于什么都没说——那正是这条链路原来的毛病。
	if strings.Contains(msg, "HTTP 403") {
		t.Errorf("配额耗尽不该退化成裸状态码：%s", msg)
	}
}

func TestForbiddenWithoutQuotaIsNotRateLimit(t *testing.T) {
	InvalidateVersions()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403) // 没有 x-ratelimit-*：Token 权限不足 / 被代理拦
	}))
	defer srv.Close()

	_, err := ListVersionsToken(srv.URL, "o/r", "")
	if err == nil {
		t.Fatal("403 应当报错")
	}
	if IsRateLimited(err) {
		t.Fatal("不配额限流的 403 不能当成限流：那会让人一直等配额恢复，其实是 Token 没权限")
	}
	if !strings.Contains(err.Error(), "HTTP 403") {
		t.Errorf("普通 403 应保留状态码便于排查，实际: %v", err)
	}
}

func TestListVersionsSendsToken(t *testing.T) {
	InvalidateVersions()
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(releasesJSON()))
	}))
	defer srv.Close()

	if _, err := ListVersionsToken(srv.URL, "o/r", "ghp_demo"); err != nil {
		t.Fatalf("带 Token 的请求失败: %v", err)
	}
	if gotAuth != "Bearer ghp_demo" {
		t.Fatalf("Token 没带上，Authorization=%q；配额上限不会从 60 变成 5000", gotAuth)
	}
}

func TestVersionsCacheServesRepeatAndInvalidates(t *testing.T) {
	InvalidateVersions()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write([]byte(releasesJSON()))
	}))
	defer srv.Close()

	first, err := ListVersionsToken(srv.URL, "o/r", "")
	if err != nil {
		t.Fatalf("首次请求失败: %v", err)
	}
	// 面板加载时四个组件并发各来一次、换分支再来一次——不缓存的话，
	// 一次刷新就吃掉好几次配额，而配额是整台机器（整个 NAT）共用的。
	if _, err := ListVersionsToken(srv.URL, "o/r", ""); err != nil {
		t.Fatalf("第二次请求失败: %v", err)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("同一仓库 5 分钟内应只打一次上游，实际 %d 次", n)
	}

	// 缓存必须返回副本：调用方会往上写 MihomoIncompatible / 面板资产 URL，
	// 交出去的是同一个切片的话，第二次读到的是被改过的数据。
	first[0].Tag = "被调用方改过"
	again, _ := ListVersionsToken(srv.URL, "o/r", "")
	if again[0].Tag == "被调用方改过" {
		t.Fatal("缓存被调用方的修改污染了")
	}

	// 改了设置（仓库 / 镜像 / Token）必须能立刻看到新结果。
	InvalidateVersions()
	if _, err := ListVersionsToken(srv.URL, "o/r", ""); err != nil {
		t.Fatalf("作废后请求失败: %v", err)
	}
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Fatalf("作废后应重新拉一次，实际共 %d 次", n)
	}
}

func atomFeedXML() string {
	// 注意第 2 条：release 的**标题**带了一长串说明，tag 只在链接里。
	return `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Release notes</title>
  <entry>
    <title>v2.1.9</title>
    <updated>2026-11-24T16:36:14Z</updated>
    <link rel="alternate" type="text/html" href="https://github.com/o/r/releases/tag/v2.1.9"/>
  </entry>
  <entry>
    <title>v2.1.7 — 局域网主机名解析不了</title>
    <updated>2026-11-24T03:23:29Z</updated>
    <link rel="alternate" type="text/html" href="https://github.com/o/r/releases/tag/v2.1.7"/>
  </entry>
  <entry>
    <title>v2.2.0-beta.1</title>
    <updated>2026-11-20T00:00:00Z</updated>
    <link rel="alternate" type="text/html" href="https://github.com/o/r/releases/tag/v2.2.0-beta.1"/>
  </entry>
</feed>`
}

func TestListVersionsAtomParsesTags(t *testing.T) {
	InvalidateVersions()
	var gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/releases.atom") {
			http.NotFound(w, r)
			return
		}
		gotAccept = r.Header.Get("Accept")
		w.Write([]byte(atomFeedXML()))
	}))
	defer srv.Close()

	rels, err := ListVersionsAtom(srv.URL, "o/r")
	if err != nil {
		t.Fatalf("订阅通道失败: %v", err)
	}
	if !strings.Contains(gotAccept, "atom") {
		t.Errorf("Accept 头应表明要订阅，实际 %q", gotAccept)
	}
	if len(rels) != 3 {
		t.Fatalf("应解析出 3 个版本，实际 %d: %+v", len(rels), rels)
	}
	// 按版本号降序：beta 比 2.1.9 高，所以排第一。
	want := []string{"v2.2.0-beta.1", "v2.1.9", "v2.1.7"}
	for i, w := range want {
		if rels[i].Tag != w {
			t.Errorf("第 %d 条应为 %s，实际 %s", i, w, rels[i].Tag)
		}
	}
	if !rels[0].Prerelease {
		t.Error("beta 必须标成预发布：否则「一键装最新」会把 beta 装到生产上")
	}
	if rels[1].Prerelease {
		t.Error("v2.1.9 是正式版")
	}
}

func TestAtomTagIgnoresReleaseTitle(t *testing.T) {
	// 直接钉住「标题不是 tag」：本项目的 release 标题真的写成
	// 「v2.1.7 — sing-box 解析不了局域网主机名…」，拿标题拼下载地址就是 404。
	e := atomEntry{
		Title: "v2.1.7 — 局域网主机名解析不了",
		Links: []atomLink{{Rel: "alternate", Href: "https://github.com/o/r/releases/tag/v2.1.7"}},
	}
	if got := atomTag(e); got != "v2.1.7" {
		t.Fatalf("tag 应从链接取，实际 %q", got)
	}
	// 含斜杠的 tag 在链接里是 %2F，必须还原，否则下载地址拼错。
	e2 := atomEntry{Links: []atomLink{{Href: "https://github.com/o/r/releases/tag/release%2F2026-01"}}}
	if got := atomTag(e2); got != "release/2026-01" {
		t.Fatalf("URL 编码的 tag 应还原，实际 %q", got)
	}
	if got := atomTag(atomEntry{Title: "x"}); got != "" {
		t.Fatalf("没有 tag 链接应返回空串，实际 %q", got)
	}
}
