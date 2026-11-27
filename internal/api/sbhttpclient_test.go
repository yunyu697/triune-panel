package api

import "testing"

// 规则集下载必须直连，否则首启（节点还没就绪）会先失败一轮。
// 表达这件事的字段在 sing-box 1.14 换了：download_detour 废弃、1.16 移除，
// 之后写了是 unknown field → FATAL；而 1.13 及以前不认识 http_clients。
// 所以这不是「新字段可加可不加」，而是按版本二选一——两边的产物都不能有
// 对方那个字段，写错任何一边都是「配置能存、内核起不来」。
func TestSingboxRuleSetDownloadFollowsVersion(t *testing.T) {
	sets := []normSet{{
		Tag: "geosite-cn", URL: "https://mirror/geosite-cn.srs", Format: "binary",
		Behavior: "domain", Policy: "DIRECT", Interval: 86400,
	}}
	cases := []struct {
		ver        string
		httpClient bool
	}{
		{"1.13", false},
		{"1.14", true},
		{"1.16", true}, // 1.16 移除 download_detour，必须已经切到新写法
	}
	for _, tc := range cases {
		v, err := parseSBVersion(tc.ver)
		if err != nil {
			t.Fatalf("%s: %v", tc.ver, err)
		}
		var warns []string
		doc := genSingbox("client", v, testNodes(), sets, nil, &warns, genOpts{}).doc

		rs := doc["route"].(map[string]any)["rule_set"].([]any)
		if len(rs) != 1 {
			t.Fatalf("%s: 规则集数量 = %d", tc.ver, len(rs))
		}
		m := rs[0].(map[string]any)

		clients, hasClients := doc["http_clients"]
		if tc.httpClient {
			if !hasClients {
				t.Errorf("%s: 缺 http_clients（规则集没有直连客户端可引用）", tc.ver)
			} else {
				c := clients.([]any)[0].(map[string]any)
				if c["tag"] != sbDirectHTTPClientTag {
					t.Errorf("%s: http_clients[0].tag = %v，应为 %q", tc.ver, c["tag"], sbDirectHTTPClientTag)
				}
				// 写 detour 指到内置 direct 会 FATAL：
				// `detour to an empty direct outbound makes no sense`。
				// 不写 detour 的客户端本身就是直连，这是实测结论。
				if _, ok := c["detour"]; ok {
					t.Errorf("%s: 直连客户端不应写 detour（会 FATAL）: %v", tc.ver, c)
				}
			}
			if m["http_client"] != sbDirectHTTPClientTag {
				t.Errorf("%s: rule_set.http_client = %v，应为 %q", tc.ver, m["http_client"], sbDirectHTTPClientTag)
			}
			if _, deprecated := m["download_detour"]; deprecated {
				t.Errorf("%s: 仍写了已废弃的 download_detour", tc.ver)
			}
			continue
		}

		// 1.13 及以前：只认 download_detour，写了 http_clients 会 FATAL
		if hasClients {
			t.Errorf("%s: 不认识 http_clients，不应生成", tc.ver)
		}
		if m["download_detour"] != "direct" {
			t.Errorf("%s: download_detour = %v，应为 direct", tc.ver, m["download_detour"])
		}
		if _, ok := m["http_client"]; ok {
			t.Errorf("%s: 不认识 http_client，不应生成", tc.ver)
		}
	}
}

// 能力表是分支的唯一来源：改这里必须同步测试，否则两边各自漂移。
func TestSBCapsHTTPClient(t *testing.T) {
	for _, tc := range []struct {
		ver  string
		want bool
	}{{"1.13", false}, {"1.14", true}, {"1.15", true}} {
		v, _ := parseSBVersion(tc.ver)
		if got := capsFor(v).HTTPClient; got != tc.want {
			t.Errorf("%s: HTTPClient = %v，期望 %v", tc.ver, got, tc.want)
		}
	}
}
