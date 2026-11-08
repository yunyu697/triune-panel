package webui

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// buildDemo 按 deploy/build-demo.py 的同一规则生成离线演示页：把
// /assets/*.js 的引用换回内联脚本。
//
// 为什么不再是「逐字节副本」：CSP 收紧到 script-src 'self' 之后，脚本必须
// 作为独立文件加载；而 demo.html 是双击用 file:// 打开的，拿不到 /assets，
// 只能把两份脚本贴回去。内容仍与面板服务的页面严格对应。
func buildDemo(t *testing.T) []byte {
	t.Helper()
	html := indexHTML
	for _, name := range []string{"boot.js", "qr.js", "app.js"} {
		tag := []byte(`<script src="/assets/` + name + `"></script>`)
		if bytes.Count(html, tag) != 1 {
			t.Fatalf("index.html 里 %s 应恰好出现一次（实际 %d）", tag, bytes.Count(html, tag))
		}
		js, _, err := AssetFile(name)
		if err != nil {
			t.Fatalf("读 %s 失败: %v", name, err)
		}
		inline := append(append([]byte("<script>\n"), js...), []byte("</script>")...)
		html = bytes.Replace(html, tag, inline, 1)
	}
	return html
}

// TestDemoHTMLMatchesIndex demo.html 必须等于「index.html + 内联脚本」的生成结果。
//
// 这条断言是防手滑的：每次改 UI 都要 `python3 deploy/build-demo.py`，
// 忘了的话仓库里的离线演示会停留在旧版，而日常使用与测试都不会碰到它。
func TestDemoHTMLMatchesIndex(t *testing.T) {
	demo, err := os.ReadFile(filepath.Join("..", "..", "demo.html"))
	if err != nil {
		t.Fatalf("读 demo.html 失败: %v", err)
	}
	if want := buildDemo(t); !bytes.Equal(demo, want) {
		t.Error("demo.html 与 index.html 不同步——改完 UI 记得 `python3 deploy/build-demo.py`")
	}
}

// TestFirstPaintBootGuard 刷新时「先闪一下登录页」的修复由四个环节组成，
// 少任何一环都会静默退化成旧行为（页面不报错，只是又闪一下），所以整条链钉住。
func TestFirstPaintBootGuard(t *testing.T) {
	html := string(indexHTML)
	appJS, _, err := AssetFile("app.js")
	if err != nil {
		t.Fatalf("读 app.js 失败: %v", err)
	}
	bootJS, _, err := AssetFile("boot.js")
	if err != nil {
		t.Fatalf("读 boot.js 失败: %v (检查 embed.go 与 api.go 的 /assets 路由)", err)
	}

	// 1) boot.js 必须在 <head> 里同步加载：放到 body 末尾就晚了，登录页已经画过了。
	head := html
	if i := strings.Index(head, "<body"); i > 0 {
		head = head[:i]
	}
	if !strings.Contains(head, `<script src="/assets/boot.js"></script>`) {
		t.Error("index.html 的 <head> 里没有同步加载 boot.js（首屏仍会闪登录页）")
	}
	// 2) CSS 必须据此在首帧把登录页藏掉、把主界面显示出来。
	for _, rule := range []string{
		`html[data-boot="app"] #login{display:none!important}`,
		`html[data-boot="app"] #app{display:flex!important}`,
	} {
		if !strings.Contains(html, rule) {
			t.Errorf("index.html 缺少首屏规则 %q", rule)
		}
	}
	// 3) boot.js 真的按 token 写标记，且不能是 defer/async 之后才生效的形态。
	if !strings.Contains(string(bootJS), `localStorage.getItem("triune_token")`) ||
		!strings.Contains(string(bootJS), `setAttribute("data-boot"`) {
		t.Error("boot.js 没有按本地 token 设置 data-boot")
	}
	// 4) app.js 接手显隐后必须撤掉标记，否则 !important 会一直压着 .hide，
	//    会话过期时回不去登录页。showLogin / showApp 两条路径都要撤。
	src := string(appJS)
	if !strings.Contains(src, "function clearBootFlag()") {
		t.Fatal("app.js 缺少 clearBootFlag()")
	}
	if !strings.Contains(src, "function showLogin(){ clearBootFlag();") {
		t.Error("showLogin() 没有撤掉 data-boot——会话过期回不去登录页")
	}
	showApp := funcBody(src, "function showApp(){")
	if !strings.Contains(showApp, "clearBootFlag();") {
		t.Error("showApp() 没有撤掉 data-boot——data-boot 会一直压着 .hide")
	}
	// 5) 启动分支不能再用 /api/status 的回包当 showApp() 的开关。
	if !strings.Contains(src, "  applyRole();\n  showApp();") {
		t.Error("启动仍在等 /api/status 才 showApp()（首屏优化会失效）")
	}
	// 6) 401 必须清掉本地身份，否则失效 token 会让每次刷新都重演一次闪屏。
	if !strings.Contains(src, `if (r.status === 401) { dropSession(); showLogin();`) {
		t.Error("401 没有清会话：失效 token 会一直触发「画主界面 → 打回登录」")
	}
}

// funcBody 取出一个顶层函数体，用来断言语句**顺序**。
// 截到下一个顶层函数而不是第一个 }：函数体里有 if / try / 箭头函数，
// 按 } 截会把后面的语句全丢掉，断言就变成假绿。
func funcBody(src, sig string) string {
	i := strings.Index(src, sig)
	if i < 0 {
		return ""
	}
	rest := src[i+len(sig):]
	if j := strings.Index(rest, "\nfunction "); j >= 0 {
		return rest[:j]
	}
	return rest
}

// keysIn 抽一段数组字面量里的双引号键（VIEWS / BOOT_VIEWS 都写成
// ["key", ...] 或 ["key", ...]，取第一个字符串即可）。
func keysIn(block string) []string {
	re := regexp.MustCompile(`"([a-z]+)"`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(block, -1) {
		out = append(out, m[1])
	}
	return out
}

// TestViewRestoreOnReload 刷新后停留在原页面的链路：
//
//	goto() 持久化 → boot.js 首帧打标记 → CSS 首帧画对页 →
//	showApp() 恢复视图并补齐标题/数据 → clearBootFlag() 撤标记
//
// 少任何一环都不报错，只是用户又看到「刷新跳回概览」，所以整条链钉住，
// 连三份视图清单（HTML / app.js / boot.js）也钉住同步——新增页面时漏补
// CSS 的表现是「只有那一页刷新会白屏」，很难在别处发现。
func TestViewRestoreOnReload(t *testing.T) {
	html := string(indexHTML)
	appJS, _, err := AssetFile("app.js")
	if err != nil {
		t.Fatal(err)
	}
	bootJS, _, err := AssetFile("boot.js")
	if err != nil {
		t.Fatal(err)
	}
	src, boot := string(appJS), string(bootJS)

	// 1) 三份视图清单必须一致。boot.js 只认白名单里的值，CSS 只给这些值写规则。
	var htmlViews []string
	for _, m := range regexp.MustCompile(`id="v-([a-z]+)" class="view`).FindAllStringSubmatch(html, -1) {
		htmlViews = append(htmlViews, m[1])
	}
	vi := strings.Index(src, "const VIEWS = [")
	if vi < 0 {
		t.Fatal("app.js 里找不到 VIEWS")
	}
	vend := strings.Index(src[vi:], "];")
	appViews := keysIn(src[vi : vi+vend])
	bi := strings.Index(boot, "var BOOT_VIEWS = [")
	if bi < 0 {
		t.Fatal("boot.js 里找不到 BOOT_VIEWS 白名单")
	}
	bend := strings.Index(boot[bi:], "];")
	bootViews := keysIn(boot[bi : bi+bend])
	if len(htmlViews) == 0 || len(appViews) == 0 {
		t.Fatalf("视图清单解析失败: html=%v app=%v", htmlViews, appViews)
	}
	if strings.Join(htmlViews, ",") != strings.Join(appViews, ",") {
		t.Errorf("index.html 的视图与 app.js VIEWS 不一致: %v vs %v", htmlViews, appViews)
	}
	if strings.Join(bootViews, ",") != strings.Join(appViews, ",") {
		t.Errorf("boot.js 白名单与 app.js VIEWS 不一致: %v vs %v", bootViews, appViews)
	}

	// 2) 首帧 CSS：概览被藏掉、每个非概览页都有对应显示规则、入场动画压掉。
	for _, rule := range []string{
		`html[data-boot-view] #v-overview{display:none!important}`,
		`html[data-boot-view] .view{animation:none!important}`,
	} {
		if !strings.Contains(html, rule) {
			t.Errorf("index.html 缺少首屏视图规则 %q", rule)
		}
	}
	for _, v := range appViews {
		if v == "overview" {
			continue
		}
		sel := `html[data-boot-view="` + v + `"] #v-` + v
		if !strings.Contains(html, sel) {
			t.Errorf("index.html 缺少 %s 的首屏显示规则（刷新这一页会白屏）", v)
		}
	}

	// 3) boot.js 读偏好、校验白名单后才打标记（非法值必须退回默认概览）。
	for _, s := range []string{
		`localStorage.getItem("triune_view")`,
		`BOOT_VIEWS.indexOf(view) >= 0`,
		`setAttribute("data-boot-view", view)`,
	} {
		if !strings.Contains(boot, s) {
			t.Errorf("boot.js 缺少 %q", s)
		}
	}

	// 4) goto() 持久化的是纠正后的 v；restoreView() 校验非法值并恢复页面。
	if !strings.Contains(funcBody(src, "function goto(v, sect) {"),
		`localStorage.setItem("triune_view", v)`) {
		t.Error("goto() 没有保存当前页，刷新时无从恢复")
	}
	restore := funcBody(src, "function restoreView() {")
	for _, s := range []string{
		`localStorage.getItem("triune_view")`,
		"VIEWS.some(([k]) => k === v)",
		"goto(v);",
	} {
		if !strings.Contains(restore, s) {
			t.Errorf("restoreView() 缺少 %q", s)
		}
	}

	// 5) 顺序：先恢复视图（.active 落到目标页）再撤标记，反了会露一帧概览。
	showApp := funcBody(src, "function showApp(){")
	ri, ci := strings.Index(showApp, "restoreView();"), strings.Index(showApp, "clearBootFlag();")
	if ri < 0 || ci < 0 || ri > ci {
		t.Errorf("showApp() 必须先 restoreView() 再 clearBootFlag()，实际: %q", showApp)
	}
	clear := funcBody(src, "function clearBootFlag() {")
	if !strings.Contains(clear, `removeAttribute("data-boot-view")`) {
		t.Error("clearBootFlag() 没有撤掉 data-boot-view，之后切页会被首屏 CSS 压住")
	}
}

// TestEmbeddedMarker 页面头部的 build 标记是「升级后不服务旧 UI」的唯一依据，
// 丢了它 EmbedIfNeeded 会永远覆盖用户自定义的 index.html（或者反过来，
// 永远不覆盖导致升级后界面停在旧版）。
func TestEmbeddedMarker(t *testing.T) {
	if embeddedMarker() == "" {
		t.Fatal("index.html 头部缺少 <!-- triune-panel-web/<ver> --> 标记")
	}
}

// TestPWAEmbedded PWA 资源必须都在内嵌文件里——漏一个的表现是浏览器静默
// 忽略清单（图标 404），手机上装不了，而服务端不会报任何错。
func TestPWAEmbedded(t *testing.T) {
	names := []string{
		"manifest.webmanifest",
		"sw.js",
		"icons/icon.svg",
		"icons/icon-192.png",
		"icons/icon-512.png",
		"icons/icon-maskable-512.png",
		"icons/apple-touch-icon.png",
	}
	for _, n := range names {
		data, etag, err := PWAFile(n)
		if err != nil {
			t.Errorf("%s 未内嵌: %v", n, err)
			continue
		}
		if len(data) == 0 {
			t.Errorf("%s 是空文件", n)
		}
		if etag == "" {
			t.Errorf("%s 没有 ETag", n)
		}
	}
	// 同一个文件两次读取的 ETag 必须一致，不同文件必须不同
	_, a, _ := PWAFile("sw.js")
	_, b, _ := PWAFile("sw.js")
	if a != b {
		t.Errorf("同一文件两次 ETag 不一致: %s vs %s", a, b)
	}
	_, c, _ := PWAFile("manifest.webmanifest")
	if a == c {
		t.Error("不同文件的 ETag 相同，说明摘要算错了")
	}
}
