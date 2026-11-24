package update

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// AtomBase releases 订阅的站点根。
//
// 抽成变量只为一件事：让「接口配额耗尽后改走订阅」这条路径能用本地服务验证。
// 生产环境没人改它，默认 github.com。
var AtomBase = "https://github.com"

// ListVersionsAtom 从 releases.atom 订阅里取版本列表。
//
// 为什么需要第二条通道：/repos/<owner>/<repo>/releases 走 API 配额（匿名
// 60 次/小时，按出口 IP），配额一耗尽，面板的更新检查与内核版本列表会**同时**
// 失效，而报错只说 HTTP 403。releases.atom 是网页通道，不计这套配额——同一个
// 出口 IP 反复刷它也不会被挡。
//
// 代价要说清：订阅只给「有哪些 tag、什么时候发的」，没有资产清单
// （AssetURLs / AssetAPIURLs 为空）。好在下载地址本来就是按版本号拼出来的
// （内核见 AssetURL，面板见 fillPanelAssetURL），版本列表够用；预发布也只能
// 靠 tag 里的 alpha/beta/rc 判断，没有结构化字段。
func ListVersionsAtom(webBase, repo string) ([]Release, error) {
	if repo == "" {
		return nil, fmt.Errorf("未配置仓库（owner/name）")
	}
	if webBase == "" {
		webBase = AtomBase
	}
	webBase = strings.TrimRight(webBase, "/")
	feedURL := fmt.Sprintf("%s/%s/releases.atom", webBase, repo)
	key := "atom|" + feedURL
	if rels, ok := versionsCacheGet(key); ok {
		return rels, nil
	}

	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest("GET", feedURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "triune-panel")
	req.Header.Set("Accept", "application/atom+xml")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("读取 releases 订阅失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, httpFail("读取 releases 订阅失败", resp)
	}
	var feed atomFeed
	// 订阅只有最近二十条，8MB 上限纯属兜底：不能因为上游返回一坨异常 HTML 就把内存吃满。
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&feed); err != nil {
		return nil, fmt.Errorf("解析 releases 订阅失败: %w", err)
	}

	out := make([]Release, 0, len(feed.Entries))
	for _, e := range feed.Entries {
		tag := atomTag(e)
		if tag == "" {
			continue
		}
		_, pre, ok := parseSemver(tag)
		out = append(out, Release{
			Tag:         tag,
			Prerelease:  ok && pre != "",
			PublishedAt: strings.TrimSpace(e.Updated),
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("releases 订阅里没有可识别的版本（%s）", feedURL)
	}
	sortReleases(out)
	versionsCachePut(key, out)
	return out, nil
}

type atomEntry struct {
	Title   string     `xml:"title"`
	Updated string     `xml:"updated"`
	Links   []atomLink `xml:"link"`
}

type atomLink struct {
	Rel  string `xml:"rel,attr"`
	Href string `xml:"href,attr"`
}

type atomFeed struct {
	Entries []atomEntry `xml:"entry"`
}

const atomTagPath = "/releases/tag/"

// atomTag 从条目里取出真正的 tag。
//
// 不能拿 <title>：release 的**标题**是自由文本，本项目的 v2.1.7 就写成
// 「v2.1.7 — sing-box 解析不了局域网主机名…」，直接当 tag 用会拼出
// 404 的下载地址。链接里的 /releases/tag/<tag> 才是 tag 本身（可能被
// URL 编码，含 `/` 的 tag 会变成 %2F）。
func atomTag(e atomEntry) string {
	for _, l := range e.Links {
		i := strings.Index(l.Href, atomTagPath)
		if i < 0 {
			continue
		}
		raw := l.Href[i+len(atomTagPath):]
		if raw == "" {
			continue
		}
		if t, err := url.PathUnescape(raw); err == nil {
			return t
		}
		return raw
	}
	return ""
}
