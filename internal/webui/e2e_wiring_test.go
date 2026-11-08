package webui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestE2ECIWhitelist 钉住「首屏回归真的在 CI 里跑」这条链路。
//
// .e2e/ 整目录默认被 .gitignore 忽略，靠逐条白名单把脚本放进来。这里最容易出现
// 一种**不报错的假绿**：新增/改名了脚本却没加白名单，CI 照旧绿，只是那份回归
// 从来没被执行过（Git 不会提示「有文件被忽略了」）。反过来，白名单加了却没写进
// workflow 也是一样。所以从两个方向各查一遍，并顺带钉住 bundled Chromium 与
// demo.html 同步检查——少了前者 CI 上根本没浏览器，少了后者测的是过期页面。
func TestE2ECIWhitelist(t *testing.T) {
	root := filepath.Join("..", "..")
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("读不到 %s: %v", rel, err)
		}
		return string(b)
	}
	ci := read(filepath.Join(".github", "workflows", "ci.yml"))
	ignore := read(".gitignore")

	// 1) workflow 里必须有 first-paint job，且关键开关都在。
	for _, s := range []string{
		"  first-paint:",
		"playwright install --with-deps chromium",
		// 断言完整的 run 行：只搜测试名的话，注释里提一嘴也能蒙混过关。
		"run: go test ./internal/webui/ -run TestDemoHTMLMatchesIndex -count=1",
		"node .e2e/first-paint-server.js",
		"deploy/verify-release.sh",
	} {
		if !strings.Contains(ci, s) {
			t.Errorf("ci.yml 缺少 %q：首屏回归不会按预期在 CI 里执行", s)
		}
	}
	// 离线循环与真后端回归是两个独立 step，各自都要设 bundled；只留一个时
	// 另一个会去 launch 本机 Chrome，CI 上没有，报错还容易被误读成脚本问题。
	if n := strings.Count(ci, "E2E_CHANNEL: bundled"); n < 2 {
		t.Errorf("ci.yml 里 E2E_CHANNEL: bundled 只有 %d 处（两个浏览器 step 都要设）", n)
	}

	// 2) 白名单里的每个 js 必须真实存在，且被 workflow 点名。
	listed := regexp.MustCompile(`(?m)^!(\.e2e/[A-Za-z0-9_.-]+\.js)$`).FindAllStringSubmatch(ignore, -1)
	// 用 Errorf 不用 Fatalf：少了哪一条，下面的逐项检查还能把名字报出来。
	if len(listed) < 8 {
		t.Errorf(".gitignore 白名单的 js 只有 %d 条，是不是整条规则被删了？", len(listed))
	}
	for _, m := range listed {
		rel := m[1]
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("白名单里的 %s 不存在: %v", rel, err)
		}
		name := strings.TrimSuffix(filepath.Base(rel), ".js")
		// browser.js 是公共依赖，由其它脚本 require，不会出现在 node 的循环里。
		if name != "browser" && !strings.Contains(ci, name) {
			t.Errorf("%s 在白名单里但 ci.yml 没点名：这份回归永远不会跑", rel)
		}
	}

	// 3) workflow 循环里点名的每个脚本，必须真的在白名单里（否则 CI 直接
	//    node: cannot find module，或者更糟：本地有、CI 里没有）。
	loop := regexp.MustCompile(`(?s)for s in ([^;]+); do`).FindStringSubmatch(ci)
	if len(loop) != 2 {
		t.Fatal("ci.yml 里找不到 `for s in ...; do` 离线回归循环")
	}
	names := strings.Fields(strings.ReplaceAll(loop[1], "\\", " "))
	if len(names) < 6 {
		t.Fatalf("离线回归循环只剩 %d 个脚本: %v", len(names), names)
	}
	for _, n := range names {
		if !strings.Contains(ignore, "!.e2e/"+n+".js") {
			t.Errorf("ci.yml 要跑 .e2e/%s.js，但 .gitignore 没有白名单它", n)
		}
	}
	if !strings.Contains(ignore, "!.e2e/browser.js") {
		t.Error("browser.js 没白名单，所有脚本 require(\"./browser\") 在 CI 上都会失败")
	}

	// 4) 依赖清单也必须入库，否则 npm ci --prefix .e2e 在 CI 上没东西可装。
	for _, rel := range []string{".e2e/browser.js", ".e2e/package.json", ".e2e/package-lock.json"} {
		if !strings.Contains(ignore, "!"+rel) {
			t.Errorf(".gitignore 没有白名单 %s", rel)
		}
	}
}
