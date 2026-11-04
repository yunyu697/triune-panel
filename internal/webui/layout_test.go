package webui

import (
	"strings"
	"testing"
)

// 布局的两条硬约束。这类问题**不会报错**，只会让人用不了：导航滚不动、
// 手机上内容被地址栏盖住。改 CSS 时很容易顺手删掉，所以钉在这里。

// cssRule 取 index.html 里以 sel 开头的第一条规则体（到下一个 "}" 为止）。
func cssRule(t *testing.T, css, sel string) string {
	t.Helper()
	i := strings.Index(css, sel+"{")
	if i < 0 {
		t.Fatalf("index.html 里找不到 %s 规则", sel)
	}
	body := css[i+len(sel)+1:]
	if j := strings.Index(body, "}"); j >= 0 {
		body = body[:j]
	}
	return body
}

// TestShellUsesDVH .shell 的高度必须同时给 100vh 与 100dvh。
//
// 手机浏览器的 100vh 是「地址栏收起时」的高度，竖屏下比可视区高一截：底部内容
// （设置页的常驻操作条、最后一张卡片）落在地址栏后面，滑不到也点不到。
// dvh 跟着可视区变；不认识 dvh 的浏览器取前一行，所以两行都要留。
func TestShellUsesDVH(t *testing.T) {
	body := cssRule(t, string(indexHTML), ".shell")
	if !strings.Contains(body, "100dvh") {
		t.Error(".shell 没有 100dvh：手机竖屏下底部内容会落到地址栏后面")
	}
	if !strings.Contains(body, "100vh") {
		t.Error(".shell 缺少 100vh 兜底（老浏览器不认识 dvh）")
	}
}

// TestNavScrollsWhenShort 导航区必须自己滚。
//
// .shell 是 overflow:hidden，窗口高度不够时（笔记本半屏、浏览器控制台占了下半屏）
// 十一个入口加起来比视口高，没有 overflow-y 就被裁掉且滑不到——表现是「导航滚不动，
// 下面几个页面进不去」。min-height:0 必须一起给：flex 子项默认 min-height:auto，
// 不归零它不会收缩，只会把 .side 撑破。
func TestNavScrollsWhenShort(t *testing.T) {
	body := cssRule(t, string(indexHTML), "#nav")
	if !strings.Contains(body, "overflow-y:auto") {
		t.Error("#nav 没有 overflow-y:auto：窗口不够高时导航滚不动")
	}
	if !strings.Contains(body, "min-height:0") {
		t.Error("#nav 缺 min-height:0：flex 子项默认 min-height:auto，不会收缩，会撑破侧栏")
	}
}

// TestNoInlineGridColumns 不许在 HTML 上写内联 grid-template-columns。
//
// 内联样式的优先级高于媒体查询，窄屏退不回一列：手机上两列各 ~150px，
// 里面的分段按钮组撑破列宽，把整页顶出横向滚动条（「界面显示不全」）。
// 需要固定列数就用 .frm-2 / .grid-2 这类带媒体查询的类。
// stripCSSComments 去掉整段 CSS 注释：注释里为了说明问题会引用旧写法，不该算违规。
func stripCSSComments(css string) string {
	for {
		i := strings.Index(css, "/*")
		if i < 0 {
			return css
		}
		j := strings.Index(css[i:], "*/")
		if j < 0 {
			return css[:i]
		}
		css = css[:i] + css[i+j+2:]
	}
}

func TestNoInlineGridColumns(t *testing.T) {
	css := stripCSSComments(string(indexHTML))
	for _, line := range strings.Split(css, "\n") {
		if strings.Contains(line, "grid-template-columns") && strings.Contains(line, "style=") {
			t.Errorf("index.html 用内联 grid-template-columns 写死了列数（媒体查询盖不掉）: %s",
				strings.TrimSpace(line))
		}
	}
}

// ---- Android 老内核（微信 X5 / Chromium 86）----
// 这几条都是「桌面和现代手机完全正常、X5 上直接坏掉」的写法，预览看不出来。

// 目标下限是 Android 微信内置浏览器。它不认 dvh（108+）、inset 简写（87+），
// backdrop-filter 缺失时半透明顶栏会透出滚动内容（看着像闪烁）。四处兜底缺一不可。
func TestAndroidCompatFallbacks(t *testing.T) {
	css := string(indexHTML)
	for _, frag := range []string{
		"text-size-adjust:100%",            // 关掉 Android 的 Text Autosizing
		"@supports not (height:100dvh)",    // 老内核改用 --app-h
		"@supports not ((backdrop-filter",  // 顶栏换不透明底
		"bottom:calc(12px + env(safe-area", // 底部条让开 Home Indicator
	} {
		if !strings.Contains(css, frag) {
			t.Errorf("缺 Android 老内核兜底 %q", frag)
		}
	}
	if !strings.Contains(string(mustReadAsset(t, "app.js")), "--app-h") {
		t.Error("app.js 没有写入 --app-h：老内核上 .shell 会回落到 100vh，底部被地址栏盖住")
	}
}

// inset 简写是 Chrome 87+ 的。X5 不认，position:fixed/absolute 的元素会直接
// 没有尺寸——弹窗遮罩铺不满、开关轨道消失。必须写四边。
func TestNoInsetShorthand(t *testing.T) {
	css := stripCSSComments(string(indexHTML))
	for _, line := range strings.Split(css, "\n") {
		if strings.Contains(line, "inset:") {
			t.Errorf("用了 inset 简写（X5 / Chromium 86 不支持，元素会没有尺寸）: %s",
				strings.TrimSpace(line))
		}
	}
}

func mustReadAsset(t *testing.T, name string) []byte {
	t.Helper()
	b, err := scriptAssets.ReadFile(name)
	if err != nil {
		t.Fatalf("读不到内嵌资源 %s: %v", name, err)
	}
	return b
}

// ---- 手机端 ----
// 这几条同样「改坏了不报错，只会在真机上用不了」，而且桌面预览完全看不出来。

// viewport-fit=cover 是 env(safe-area-inset-*) 生效的前提；少了它，下面所有
// 让开安全区的写法都取 0，加到主屏后底部按钮会被 Home Indicator 压住。
//
// 导航条挪到底部之后，顶部安全区改由顶栏让开、底部安全区由导航条自己让开
// （--nav-h 把这段内距一起量进去，所以内容区的底部留白自动跟着变）。
func TestMobileSafeArea(t *testing.T) {
	css := string(indexHTML)
	if !strings.Contains(css, "initial-scale=1,viewport-fit=cover") {
		t.Error("viewport 缺 viewport-fit=cover：env(safe-area-inset-*) 会全部取 0")
	}
	for _, frag := range []string{
		"padding:calc(12px + env(safe-area-inset-top))", // 顶栏让开状态栏 / 刘海
		"calc(8px + env(safe-area-inset-bottom))",       // 底部导航条让开 Home Indicator
		"max(8px,env(safe-area-inset-right))",           // 横屏刘海
		"bottom:calc(var(--nav-h,60px) + 10px)",         // 设置页常驻保存条抬到导航条上方
		"calc(28px + var(--nav-h,60px))",                // .content 底部留白
	} {
		if !strings.Contains(css, frag) {
			t.Errorf("CSS 缺安全区适配 %q：iPhone 上内容会被状态栏/Home Indicator 遮住", frag)
		}
	}
}

// iOS Safari 遇到 font-size < 16px 的输入控件会自动放大整页，且不缩回。
// 搜索框、规则集策略下拉、2FA 地址框都带内联 font-size，普通选择器盖不住，
// 所以窄屏那条规则必须是 !important。
func TestMobileInputFontPreventsIOSZoom(t *testing.T) {
	if !strings.Contains(string(indexHTML), "font-size:16px!important") {
		t.Error("窄屏输入控件缺 font-size:16px!important：iOS 上点输入框会放大整页且不会缩回")
	}
}

// 小屏弹窗（320×568 的「新增入站」）内容比视口长，底部「保存」原本要滚完
// 整屏表单才看得见。操作条必须吸附在滚动容器底部。
func TestMobileModalFootSticky(t *testing.T) {
	if !strings.Contains(string(indexHTML), ".modal .foot{position:sticky;bottom:0") {
		t.Error("窄屏弹窗操作条缺 sticky：小屏上「保存」按钮落在视口外")
	}
}

// 窄屏导航条是横向可滑的一行，切到靠后的页面时选中项会在视口外；表格被
// 横向截断时也得给用户「还能往右滑」的提示。两件事都由 app.js 在布局后处理，
// 只写 CSS 不行（display:none 的 view 量不出 scrollWidth）。
func TestMobileNavAndTableHints(t *testing.T) {
	js, err := scriptAssets.ReadFile("app.js")
	if err != nil {
		t.Fatal(err)
	}
	s := string(js)
	// 减掉函数定义本身，数的是「真的被调用了几处」：切页、数据加载完、
	// 窗口缩放，少一处就有一个场景漏掉。只检查函数是否存在会假绿。
	navCalls := strings.Count(s, "scrollNavToActive()") - strings.Count(s, "function scrollNavToActive()")
	if navCalls < 3 {
		t.Errorf("scrollNavToActive 只被调了 %d 处（应至少 3 处：切页/加载完/缩放）", navCalls)
	}
	tblCalls := strings.Count(s, "markScrollableTables()") - strings.Count(s, "function markScrollableTables()")
	if tblCalls < 4 {
		t.Errorf("markScrollableTables 只被调了 %d 处（应至少 4 处：切页/加载完/开弹窗/缩放）", tblCalls)
	}
	if !strings.Contains(s, "class = \"tbl-hint\"") && !strings.Contains(s, "className = \"tbl-hint\"") {
		t.Error("表格提示元素类名变了，CSS 里的 .tbl-hint 会失配")
	}
}

// cssBlock 取出以 header 开头的那段（含 @media 头）大括号平衡的内容。
// 窄屏规则全在 @media 里，cssRule 只会命中桌面那条同名规则，所以需要这个。
func cssBlock(t *testing.T, css, header string) string {
	t.Helper()
	i := strings.Index(css, header)
	if i < 0 {
		t.Fatalf("index.html 里找不到 %q", header)
	}
	depth := 0
	for j := i; j < len(css); j++ {
		switch css[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return css[i : j+1]
			}
		}
	}
	t.Fatalf("%q 的大括号不闭合", header)
	return ""
}

// TestMobileUsesDocumentScroll 窄屏必须交还给文档滚动。
//
// 原来窄屏是「.shell 固定 100dvh + .main 内部滚动」。Android（Edge / 系统浏览器 /
// Chrome 都一样）上地址栏伸缩会让 .shell 反复改高度，而滚动发生在 .main 内部，
// 合成器在重排中途会丢栅格 tile——表现是整页从某一行开始变白、后面的内容不再
// 绘制、底部滑也滑不到。桌面没有地址栏伸缩，所以这个问题在桌面上完全看不出来。
//
// 现在窄屏改成文档滚动（外壳高度由内容决定、.main 不再裁剪），导航条固定在底部，
// 顶栏不 sticky。这几条少一条就会退回「白块」或「顶部导航」。
func TestMobileUsesDocumentScroll(t *testing.T) {
	block := cssBlock(t, string(indexHTML), "@media (max-width:960px){")
	for _, frag := range []string{
		"html,body{height:auto}",
		".shell{flex-direction:column;height:auto;min-height:0;overflow:visible}",
		".main{overflow:visible;min-height:0}",
		"position:fixed;left:0;right:0;bottom:0", // 导航条钉在底部
		"position:static;background:var(--bg)",   // 顶栏
	} {
		if !strings.Contains(block, frag) {
			t.Errorf("窄屏缺 %q：Android 上会退回「固定高度外壳 + 内部滚动」或顶部导航", frag)
		}
	}
}

// 导航条固定到底部之后，内容区必须让出它的高度，否则最后一张卡片被压在导航条下面。
// 高度用 JS 量的 --nav-h（导航条随字号、安全区变化），不能写死。
func TestMobileContentClearsBottomNav(t *testing.T) {
	block := cssBlock(t, string(indexHTML), "@media (max-width:960px){")
	if !strings.Contains(block, "calc(28px + var(--nav-h,60px))") {
		t.Error("窄屏 .content 底部没让出 --nav-h：最后一张卡片会被底部导航条压住")
	}
	if !strings.Contains(string(indexHTML), ".set-bar{bottom:calc(var(--nav-h,60px) + 10px)}") {
		t.Error("窄屏常驻保存条没抬到导航条上方：保存按钮会被导航条盖住")
	}
}

// 「应用配置」是每次改完配置都要走的动作（渲染 → 校验 → 原子替换），埋在导航列表里
// 每次都得翻，所以两端都常驻。
//
// 桌面：钉在侧栏底部、账号区上方。侧栏是固定高度、只有 #nav 自己滚，导航滚到哪儿
// 它都在；早先只给手机做，PC 上还得去「配置」分组里找「应用 / 修订」。
// 手机：导航条横排，它占最左侧一格（order:-1，DOM 里排在 #nav 之后）。
// 两处都在导航区域内部——浮在内容上会挡住卡片，还得为它多留一段底部空白。
func TestPinnedApplyButton(t *testing.T) {
	css := string(indexHTML)
	// 桌面：默认就显示，且是侧栏里的一条整宽按钮
	base := cssRule(t, css, ".side-apply")
	if strings.Contains(base, "display:none") {
		t.Error("桌面把「应用」按钮藏起来了：PC 上又得去导航列表里翻「应用 / 修订」")
	}
	for _, frag := range []string{"display:inline-flex", "width:100%"} {
		if !strings.Contains(base, frag) {
			t.Errorf("桌面「应用」按钮缺 %q：不是侧栏底部那条整宽按钮", frag)
		}
	}
	// DOM 位置：夹在 #nav 与 .side-foot 之间。放进 #nav 会跟着导航一起滚走，
	// 放到 .side-foot 之后会被账号区挤到视口外。
	iNav := strings.Index(css, `<nav id="nav"></nav>`)
	iBtn := strings.Index(css, `id="sideApply"`)
	iFoot := strings.Index(css, `<div class="side-foot">`)
	if iNav < 0 || iBtn < 0 || iFoot < 0 {
		t.Fatalf("侧栏结构变了：nav=%d btn=%d foot=%d", iNav, iBtn, iFoot)
	}
	if !(iNav < iBtn && iBtn < iFoot) {
		t.Error("「应用」按钮必须夹在 #nav 与 .side-foot 之间：放进 #nav 会跟着导航滚走")
	}
	// 窄屏：让出品牌图标的位置，并用 order 挪到导航条最左侧
	block := cssBlock(t, css, "@media (max-width:960px){\n  .side .logo{display:none}")
	for _, frag := range []string{".side .logo{display:none}", "order:-1", "width:auto"} {
		if !strings.Contains(block, frag) {
			t.Errorf("窄屏缺 %q：常驻「应用」按钮没地方放", frag)
		}
	}
}

// 整页入场动画会为整个 view 建一个合成层，部分 Android GPU 上这层画不出来，
// 切页后就是一片白；全屏 backdrop-filter 则会让弹窗打开瞬间白闪/黑闪。
// 两者都只在窄屏关掉。
func TestMobileDropsHeavyEffects(t *testing.T) {
	css := string(indexHTML)
	if !strings.Contains(css, "@media (max-width:960px){.view.active{animation:none}}") {
		t.Error("窄屏没关掉 .view 入场动画：整页合成层在部分 Android GPU 上画不出来")
	}
	if !strings.Contains(css, "@media (max-width:960px){.overlay{backdrop-filter:none;-webkit-backdrop-filter:none}}") {
		t.Error("窄屏没关掉遮罩的 backdrop-filter：弹窗打开瞬间会白闪/黑闪")
	}
}

// 切页要把滚动位置归零：窄屏是文档滚动、桌面是 .main 内部滚动，两个都要清。
// 不清的话从长页切到短页会停在半空，看着像「上面缺了一块」。
func TestGotoResetsScroll(t *testing.T) {
	js, err := scriptAssets.ReadFile("app.js")
	if err != nil {
		t.Fatal(err)
	}
	s := string(js)
	for _, frag := range []string{"window.scrollTo(0, 0)", "mn.scrollTop = 0"} {
		if !strings.Contains(s, frag) {
			t.Errorf("goto 里缺 %q：切页会停在上一个页面的滚动位置", frag)
		}
	}
}

// 弹窗开着时要锁住页面滚动（窄屏是文档滚动，不锁的话手指在遮罩上一拖，
// 背后的页面跟着滑）。锁必须按 DOM 现状重算，不能计数——计数漏减会把页面永久锁死。
func TestBodyScrollLockFollowsModal(t *testing.T) {
	js, err := scriptAssets.ReadFile("app.js")
	if err != nil {
		t.Fatal(err)
	}
	s := string(js)
	if !strings.Contains(s, `document.querySelector(".overlay.show") ? "hidden" : ""`) {
		t.Error("syncBodyLock 没按 .overlay.show 重算：用计数锁会漏减，页面可能被永久锁死")
	}
	if n := strings.Count(s, "syncBodyLock()") - strings.Count(s, "function syncBodyLock()"); n < 4 {
		t.Errorf("syncBodyLock 只被调了 %d 处（openModal/closeModal/mMustChange/mHttps）", n)
	}
}

// 图表画布绝不能把宽度钉成内联像素值。
//
// 旧实现是 `clientWidth || 父宽 || 600` 再 `style.width = W + "px"`：容器还没布局
// （display:none 的 view、刚插入的节点）时量到 0，就回落到 600 并把画布钉成 600px
// 固定宽。卡片内宽只有 314px，右边直接被裁掉——手机上「模块太宽、显示不全」就是
// 这么来的。宽度一律交给 CSS（.chart-wrap canvas{width:100%}），JS 只设位图尺寸。
func TestChartSetupNeverPinsWidth(t *testing.T) {
	js, err := scriptAssets.ReadFile("app.js")
	if err != nil {
		t.Fatal(err)
	}
	s := string(js)
	if strings.Contains(s, `cv.style.width = W + "px"`) {
		t.Error("chartSetup 又把画布宽度钉成内联像素了：容器量到 0 时会画成 600px 宽，手机上右侧被裁掉")
	}
	if !strings.Contains(s, "if (!W) return null;") {
		t.Error("chartSetup 量不到宽度时必须放弃这一轮（返回 null），否则会拿兜底值硬画")
	}
}
