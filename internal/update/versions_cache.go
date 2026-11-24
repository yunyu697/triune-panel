package update

import (
	"sync"
	"time"
)

// 版本列表缓存。
//
// 为什么必须有：面板每次加载都会并发确认四个组件的最新版（组件卡片上的
// 「可更新」徽标），换分支、打开版本列表、点重试又各来一次。GitHub 对匿名
// 请求按**出口 IP** 限 60 次/小时——不缓存的话，一个人一天刷新十几次页面就
// 把全家（同一个 NAT 后面的所有设备）的配额用光，之后报的错还是「HTTP 403」，
// 看起来像面板坏了。
//
// 只缓存成功结果：失败不缓存，否则一次网络抖动会让接下来 5 分钟内反复拿到
// 同一条旧错误，用户点了重试也没用。
const versionsCacheTTL = 5 * time.Minute

type versionsCacheEntry struct {
	at   time.Time
	rels []Release
}

var (
	versionsCacheMu sync.Mutex
	versionsCache   = map[string]versionsCacheEntry{}
)

// versionsCacheGet 返回缓存副本。必须是副本：调用方会往上写
// MihomoIncompatible 与面板资产的 URL，直接把缓存里的切片交出去，
// 第二个读的人就会看到别人改过的字段。
func versionsCacheGet(key string) ([]Release, bool) {
	versionsCacheMu.Lock()
	defer versionsCacheMu.Unlock()
	e, ok := versionsCache[key]
	if !ok {
		return nil, false
	}
	if time.Since(e.at) > versionsCacheTTL {
		delete(versionsCache, key)
		return nil, false
	}
	return cloneReleases(e.rels), true
}

func versionsCachePut(key string, rels []Release) {
	if len(rels) == 0 {
		return
	}
	versionsCacheMu.Lock()
	defer versionsCacheMu.Unlock()
	// 组件就四个，键数天然很少。上限只防「一直改 gh_api_base 试镜像」
	// 把内存撑大，顺手把过期条目一起丢掉。
	if len(versionsCache) >= 32 {
		versionsCache = map[string]versionsCacheEntry{}
	}
	versionsCache[key] = versionsCacheEntry{at: time.Now(), rels: cloneReleases(rels)}
}

// InvalidateVersions 清空版本列表缓存。
//
// 设置里改了仓库 / 镜像后必须调用：同一份列表换了来源就不再等价，继续用
// 旧结果会表现为「改完设置没反应」，而用户唯一的判断依据就是那个列表。
func InvalidateVersions() {
	versionsCacheMu.Lock()
	defer versionsCacheMu.Unlock()
	versionsCache = map[string]versionsCacheEntry{}
}

func cloneReleases(in []Release) []Release {
	out := make([]Release, len(in))
	for i, r := range in {
		out[i] = cloneRelease(r)
	}
	return out
}

func cloneRelease(r Release) Release {
	out := r
	out.Assets = append([]string(nil), r.Assets...)
	out.AssetURLs = cloneStrMap(r.AssetURLs)
	out.AssetAPIURLs = cloneStrMap(r.AssetAPIURLs)
	return out
}

func cloneStrMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
