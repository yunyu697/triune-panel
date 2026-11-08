package webui

import (
	"strings"
	"testing"
)

// TestFirstPaintSettingsSection 钉住「停在设置 / 运维刷新，不能先画一帧常用」。
//
// 修复前浏览器实测（.e2e/section-restore.js）：首帧 common 卡片显示、common
// 按钮高亮，+15ms 仍是常用，30ms 后 setSectSaved() 才跳到运维。要同时钉住
// 三个环节：boot.js 在 <head> 打标记 → CSS 首帧只留目标组 → app.js 同步
// 恢复（不是 setTimeout）。少任何一环都不报错，只是又闪一下。
func TestFirstPaintSettingsSection(t *testing.T) {
	appJS, _, err := AssetFile("app.js")
	if err != nil {
		t.Fatal(err)
	}
	bootJS, _, err := AssetFile("boot.js")
	if err != nil {
		t.Fatal(err)
	}
	app, boot, html := string(appJS), string(bootJS), string(indexHTML)

	// 1) 分组白名单必须与 app.js 的 SET_SECTS 键一致。boot.js 少认一个，
	//    那一组刷新就退回「常用」，等 app.js 改回来又是一次可见跳变。
	appSects := modeKeys(blockUntil(app, "const SET_SECTS = {", "};"))
	bootSects := keysIn(blockUntil(boot, "var BOOT_SECTS = [", "];"))
	if len(appSects) == 0 || strings.Join(appSects, ",") != strings.Join(bootSects, ",") {
		t.Errorf("设置分组清单不一致: app.js=%v boot.js=%v", appSects, bootSects)
	}

	// 2) boot.js 读偏好、校验白名单、只在设置页打标记；非法值要回落 common，
	//    否则首帧是「三组卡片全摊开」，app.js 收起来时照样闪。
	for _, s := range []string{
		`localStorage.getItem("triune_settings_sect")`,
		`BOOT_SECTS.indexOf(savedSect) >= 0`,
		`boot === "app" && view === "settings"`,
		`setAttribute("data-boot-sect", sect)`,
	} {
		if !strings.Contains(boot, s) {
			t.Errorf("boot.js 缺少 %q：设置页分组不会在首帧生效", s)
		}
	}
	if !strings.Contains(boot, `sect = "common"`) {
		t.Error("boot.js 的分组默认值不是 common：没存过偏好/值非法时首帧会三组全显示")
	}

	// 3) 首帧 CSS：只藏非目标组（不用 !important 强行显示目标组，那会盖掉
	//    .need-owner 之类的角色隐藏），并把写死的「常用 .on」还原成未选中。
	for _, rule := range []string{
		`html[data-boot-sect="common"] #v-settings .card[data-sect]:not([data-sect="common"])`,
		`html[data-boot-sect="security"] #v-settings .card[data-sect]:not([data-sect="security"])`,
		`html[data-boot-sect="maintain"] #v-settings .card[data-sect]:not([data-sect="maintain"]){display:none!important}`,
		`html[data-boot-sect] #set_sect_seg button.on{background:none;color:var(--muted);box-shadow:none}`,
		`html[data-boot-sect="maintain"] #set_sect_seg button[data-sect="maintain"]`,
	} {
		if !strings.Contains(html, rule) {
			t.Errorf("index.html 缺少首屏分组规则 %q", rule)
		}
	}
	// 复位规则必须写在目标高亮之前：两条同权重，调换后目标组不高亮。
	ri := strings.Index(html, `html[data-boot-sect] #set_sect_seg button.on`)
	ti := strings.Index(html, `html[data-boot-sect="common"] #set_sect_seg button[data-sect="common"]`)
	if ri < 0 || ti < 0 || ri > ti {
		t.Error("index.html 的 .on 复位规则必须排在目标组高亮规则之前（同权重靠源序）")
	}

	// 4) setSectSaved() 必须在 goto() 的同步路径里，不能留在 30ms 定时器中。
	gt := funcBody(app, "function goto(v, sect, animate = true) {")
	si := strings.Index(gt, `if (v === "settings")`)
	if si < 0 {
		t.Fatal("goto() 里找不到设置页分支")
	}
	blk := gt[si:]
	setSectAt, timerAt := strings.Index(blk, "setSectSaved();"), strings.Index(blk, "setTimeout(")
	if setSectAt < 0 {
		t.Error("goto() 没有调 setSectSaved()：刷新后分组不会被恢复")
	}
	if timerAt >= 0 && setSectAt > timerAt {
		t.Error("setSectSaved() 又回到 setTimeout 里了：首帧到定时器之间会画一帧「常用」")
	}
	if !strings.Contains(blk, "updateSetBar();") {
		t.Error("分组同步恢复后没有在补量顶栏时重判常驻保存条")
	}

	// 5) 标记必须被撤掉，且要在量 offsetParent 之前撤：首屏规则是 !important，
	//    留着会让 setSectUsable() 把「被首屏藏住」误判成「当前角色没权限」。
	clear := funcBody(app, "function clearBootFlag() {")
	if !strings.Contains(clear, `removeAttribute("data-boot-sect")`) {
		t.Error("clearBootFlag() 没撤 data-boot-sect：之后切组会被首屏 CSS 压住")
	}
	ss := funcBody(app, "function setSect(name, persist = true) {")
	rm, measure := strings.Index(ss, `removeAttribute("data-boot-sect")`), strings.Index(ss, "setSectUsable(cards)")
	if rm < 0 || measure < 0 || rm > measure {
		t.Error("setSect() 必须在 setSectUsable() 量 offsetParent 之前撤掉 data-boot-sect")
	}
}

// TestThemeSwitchNoFlicker 钉住「切主题时整页原子换色，不渐变换色、不重建预览网格」。
//
// 修复前浏览器实测（.e2e/theme-switch-flicker.js）：亮→暗出现 5 个背景色，
// renderSchemes() 的 innerHTML 让 #schemes 记到 1 次 childList 且 7 个预览卡
// 节点身份全变。单测只钉链路，真实 computedStyle / MutationObserver 由浏览器实测覆盖。
func TestThemeSwitchNoFlicker(t *testing.T) {
	appJS, _, err := AssetFile("app.js")
	if err != nil {
		t.Fatal(err)
	}
	app, html := string(appJS), string(indexHTML)

	// 1) 禁过渡的 CSS 必须覆盖所有后代与伪元素，且 transition/animation 都关。
	for _, s := range []string{
		`html[data-theme-switch] *,html[data-theme-switch] *::before,html[data-theme-switch] *::after{`,
		"transition:none!important;animation:none!important}",
	} {
		if !strings.Contains(html, s) {
			t.Errorf("index.html 缺少切主题禁过渡规则 %q", s)
		}
	}
	// 规则要压得住 .view.active.enter 的 fadeUp：靠 !important，不是靠权重。
	guard := blockUntil(html, `html[data-theme-switch] *,`, "}")
	if !strings.Contains(guard, "!important") {
		t.Error("禁过渡规则没写 !important：权重低于 .view.active.enter 等规则，压不住")
	}

	// 2) applyTheme() 必须先挂标记再改 data-mode/data-scheme——同一个 JS 任务里
	//    顺序反了，浏览器就已经按旧样式建好过渡了。
	at := funcBody(app, "function applyTheme() {")
	mi, di := strings.Index(at, "markThemeSwitch();"), strings.Index(at, "r.dataset.mode = themeMode;")
	if mi < 0 || di < 0 || mi > di {
		t.Error("applyTheme() 必须在改 data-mode/scheme 之前调用 markThemeSwitch()")
	}

	// 3) 标记要能自己撤掉，且连点时前一次的定时器不能撤掉后一次的标记。
	ms := funcBody(app, "function markThemeSwitch() {")
	for _, s := range []string{
		`setAttribute("data-theme-switch", "")`,
		"requestAnimationFrame(() => requestAnimationFrame(",
		"if (seq === themeSwitchSeq)",
		`removeAttribute("data-theme-switch")`,
	} {
		if !strings.Contains(ms, s) {
			t.Errorf("markThemeSwitch() 缺少 %q", s)
		}
	}
	if strings.Count(ms, "requestAnimationFrame(") < 2 {
		t.Error("markThemeSwitch() 没有等两帧再撤：样式重算可能还没落地，过渡会补播")
	}

	// 4) renderSchemes() 只能建一次 DOM；之后走增量更新。
	rs := funcBody(app, "function renderSchemes() {")
	// 只允许「首次建 DOM」那一处 innerHTML，且必须被 children.length 守着。
	if strings.Count(rs, "innerHTML =") != 1 {
		t.Errorf("renderSchemes() 里 innerHTML 赋值有 %d 处（应只有首次建 DOM 这一处）", strings.Count(rs, "innerHTML ="))
	}
	for _, s := range []string{
		"if (!host.children.length) host.innerHTML = SCHEMES.map(schemeMarkup).join(\"\");",
		"paintScheme(btn, s[0], s[1]);",
	} {
		if !strings.Contains(rs, s) {
			t.Errorf("renderSchemes() 缺少 %q", s)
		}
	}
	// 增量更新里改文字必须用 textNode.nodeValue（characterData），
	// 用 textContent 会替换文本节点 —— 那是 childList 变更，等于又「重建」了。
	ps := funcBody(app, "function paintScheme(btn, id, name) {")
	if !strings.Contains(ps, ".firstChild.nodeValue") {
		t.Error("paintScheme() 没用 textNode.nodeValue 改色值文本")
	}
	if strings.Contains(ps, "innerHTML") || strings.Contains(ps, "textContent") {
		t.Error("paintScheme() 里出现了 innerHTML/textContent：会触发 childList 变更")
	}
	if !strings.Contains(app, "const SCHEME_DOTS = [") {
		t.Error("app.js 缺少 SCHEME_DOTS：预览色块与变量的对应关系丢了")
	}
}
