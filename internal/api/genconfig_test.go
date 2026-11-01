package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// 规则集的默认策略是前端勾选时的起手值。
// v1.0.0 之前前端一律按 PROXY 起手，勾「中国大陆域名」「拼多多」也会被塞进代理，
// 这是用户实际反馈的问题，所以这里把「不该代理的」钉在测试里。
func TestRuleCatalogDefaultPolicies(t *testing.T) {
	valid := map[string]bool{"DIRECT": true, "PROXY": true, "REJECT": true}
	for _, d := range ruleCatalog {
		if !valid[d.Policy] {
			t.Errorf("%s 的默认策略 %q 非法", d.ID, d.Policy)
		}
	}

	// 国内站点 / 私有网络：默认必须直连，否则「国内服务走代理」的观感又回来了。
	direct := []string{
		"geosite-cn", "geoip-cn", "geosite-private", "geoip-private",
		"geosite-category-ai-cn", "geosite-deepseek", "geosite-doubao",
		"geosite-bilibili", "geosite-netease", "geosite-iqiyi", "geosite-youku",
		"geosite-category-media-cn", "geosite-category-entertainment-cn",
		"geosite-category-games-cn", "geosite-category-game-accelerator-cn",
		"geosite-category-dev-cn", "geosite-category-cdn-cn",
		"geosite-category-bank-cn", "geosite-category-securities-cn", "geosite-pinduoduo",
		"geosite-category-scholar-cn", "geosite-category-mooc-cn",
		"geosite-category-education-cn", "geosite-category-documents-cn",
		"geosite-category-hospital-cn", "geosite-category-food-cn",
		"geosite-category-logistics-cn", "geosite-category-blog-cn",
	}
	for _, id := range direct {
		d, ok := ruleSetIndex[id]
		if !ok {
			t.Errorf("目录里缺少 %s", id)
			continue
		}
		if d.Policy != "DIRECT" {
			t.Errorf("%s（%s）应默认 DIRECT，实际 %q", id, d.Name, d.Policy)
		}
	}

	// 广告 / 追踪 / 遥测：默认必须拦截，默认 PROXY 等于没开。
	reject := []string{
		"geosite-category-ads-all", "geosite-category-ads",
		"geosite-category-public-tracker", "geosite-category-telemetry",
		"geosite-category-consent-management", "geosite-category-ip-geo-detect",
		"geosite-category-porn",
	}
	for _, id := range reject {
		d, ok := ruleSetIndex[id]
		if !ok {
			t.Errorf("目录里缺少 %s", id)
			continue
		}
		if d.Policy != "REJECT" {
			t.Errorf("%s（%s）应默认 REJECT，实际 %q", id, d.Name, d.Policy)
		}
	}

	// 境外站点不能被默认直连（等于分流失效）。
	for _, id := range []string{"geosite-geolocation-!cn", "geosite-netflix", "geosite-openai", "geosite-telegram"} {
		if d := ruleSetIndex[id]; d.Policy != "PROXY" {
			t.Errorf("%s 应默认 PROXY，实际 %q", id, d.Policy)
		}
	}
}

// 目录 JSON 必须带 policy：只有场景预设带 policy 时，手动勾选就拿不到推荐值。
func TestCatalogJSONCarriesPolicy(t *testing.T) {
	raw, err := json.Marshal(ruleCatalog)
	if err != nil {
		t.Fatal(err)
	}
	var sets []map[string]any
	if err := json.Unmarshal(raw, &sets); err != nil {
		t.Fatal(err)
	}
	if len(sets) != len(ruleCatalog) {
		t.Fatalf("序列化条数 %d != 目录条数 %d", len(sets), len(ruleCatalog))
	}
	for _, s := range sets {
		if p, _ := s["policy"].(string); p == "" {
			t.Errorf("规则集 %v 的 JSON 里缺少 policy", s["id"])
		}
	}
}

// 场景预设引用的规则集必须都存在，且策略合法——拼错 ID 会让场景静默少一条规则。
func TestScenarioSetsExist(t *testing.T) {
	valid := map[string]bool{"DIRECT": true, "PROXY": true, "REJECT": true}
	for _, sc := range scenarioPresets {
		for _, sp := range sc.Sets {
			if _, ok := ruleSetIndex[sp.ID]; !ok {
				t.Errorf("场景 %s 引用了不存在的规则集 %s", sc.ID, sp.ID)
			}
			if !valid[sp.Policy] {
				t.Errorf("场景 %s 的 %s 策略 %q 非法", sc.ID, sp.ID, sp.Policy)
			}
		}
	}
}

// 场景里出现的国内规则集不允许走代理：这正是用户反馈「有些国内服务默认 proxy」的形态，
// 而且点场景卡片是最容易被忽略的一条路径。
func TestScenarioNeverProxiesDomesticSets(t *testing.T) {
	for _, sc := range scenarioPresets {
		for _, sp := range sc.Sets {
			if !isDomesticSet(sp.ID) {
				continue
			}
			if sp.Policy != "DIRECT" {
				t.Errorf("场景 %s 把国内规则集 %s 设成了 %s", sc.ID, sp.ID, sp.Policy)
			}
		}
	}
}

func isDomesticSet(id string) bool {
	if id == "geosite-cn" || id == "geoip-cn" || id == "geosite-private" || id == "geoip-private" {
		return true
	}
	return strings.HasSuffix(id, "-cn")
}

// 请求里不带 policy 时必须回落到目录推荐值，而不是一律 PROXY。
// 老版本 UI 的缓存页面、直接调 /api/export/config 的脚本都走这条路——
// 只要还退回 PROXY，「国内站点默认走代理」就会从另一条路绕回来。
func TestNormalizeSetsFallsBackToCatalogPolicy(t *testing.T) {
	cases := []struct{ id, want string }{
		{"geosite-cn", "DIRECT"},
		{"geosite-category-ads-all", "REJECT"},
		{"geosite-netflix", "PROXY"},
	}
	for _, c := range cases {
		sets, _ := normalizeSets(genReq{Core: "mihomo", Sets: []setPolicy{{ID: c.id}}})
		if len(sets) != 1 {
			t.Fatalf("%s 生成 %d 条，期望 1 条", c.id, len(sets))
		}
		if sets[0].Policy != c.want {
			t.Errorf("%s 不带 policy 时应回落到 %s，实际 %q", c.id, c.want, sets[0].Policy)
		}
	}

	// 用户显式改过就以用户为准，推荐值不能盖掉它
	sets, _ := normalizeSets(genReq{Core: "mihomo", Sets: []setPolicy{{ID: "geosite-cn", Policy: "PROXY"}}})
	if len(sets) != 1 || sets[0].Policy != "PROXY" {
		t.Errorf("显式指定的 PROXY 被改掉了: %+v", sets)
	}

	// 自定义规则集没有目录条目，没有推荐值可回落，仍是 PROXY
	sets, _ = normalizeSets(genReq{Core: "mihomo",
		CustomSets: []customSetReq{{Name: "x", URL: "https://e.com/a.mrs"}}})
	if len(sets) != 1 || sets[0].Policy != "PROXY" {
		t.Errorf("自定义规则集应默认 PROXY，实际 %+v", sets)
	}
}
