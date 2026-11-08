package webui

import (
	"regexp"
	"strings"
	"testing"
)

// TestFirstPaintTheme 钉住「刷新第一帧就是用户选的主题，且没有整页淡入」。
//
// 修复前浏览器实测（.e2e/first-paint-flicker.js）：深色 + forest 偏好下，
// app.js 接手前 <html> 仍是 light/azure、body 背景 rgb(244,246,250)，最终
// 变成 rgb(10,18,14)；body 还带 background/color transition，所以是一次
// 肉眼可见的渐变。同时 .view.active 的 fadeUp 让首帧 opacity=0。
// 单测只能钉住「这些环节都在」，真实 computedStyle 由浏览器实测覆盖。
func TestFirstPaintTheme(t *testing.T) {
	appJS, _, err := AssetFile("app.js")
	if err != nil {
		t.Fatalf("读 app.js 失败: %v", err)
	}
	bootJS, _, err := AssetFile("boot.js")
	if err != nil {
		t.Fatalf("读 boot.js 失败: %v", err)
	}
	app, boot, html := string(appJS), string(bootJS), string(indexHTML)

	// 1) 主题必须在首帧前写到 <html>，不能等 body 末尾的 app.js。
	for _, s := range []string{
		`localStorage.getItem("triune_mode")`,
		`localStorage.getItem("triune_scheme")`,
		`setAttribute("data-mode", mode)`,
		`setAttribute("data-scheme", scheme)`,
		`BOOT_MODES.indexOf(m) >= 0`,
		`BOOT_SCHEMES.indexOf(s) >= 0`,
	} {
		if !strings.Contains(boot, s) {
			t.Errorf("boot.js 缺少 %q：主题会等 app.js 才生效，刷新时仍闪", s)
		}
	}
	// 主题对未登录也要生效（登录页同样用这套配色），不能塞在 boot==="app" 分支里。
	mi, ai := strings.Index(boot, `setAttribute("data-mode"`), strings.Index(boot, `if (boot === "app"`)
	if mi < 0 || ai < 0 || mi > ai {
		t.Error("boot.js 的主题设置必须放在登录态判断之前，否则登录页也会闪")
	}

	// 2) 两份白名单必须一致。boot.js 少认一个配色，那个配色就永远在首帧
	//    画不出来，等 app.js 改正时又是一次可见跳变。
	appSchemes := keysIn(blockUntil(app, "const SCHEMES = [", "];"))
	bootSchemes := keysIn(blockUntil(boot, "var BOOT_SCHEMES = [", "];"))
	appModes := modeKeys(blockUntil(app, "const MODE_NAME = ", ";\n"))
	bootModes := keysIn(blockUntil(boot, "var BOOT_MODES = [", "];"))
	if strings.Join(appSchemes, ",") != strings.Join(bootSchemes, ",") {
		t.Errorf("配色清单不一致: app.js=%v boot.js=%v", appSchemes, bootSchemes)
	}
	if strings.Join(appModes, ",") != strings.Join(bootModes, ",") {
		t.Errorf("亮暗清单不一致: app.js=%v boot.js=%v", appModes, bootModes)
	}

	// 3) 旧版 triune_theme 迁移表必须逐字一致：老用户升级后第一次刷新也要
	//    直接画对，不能等 app.js 迁移完再跳一次。
	norm := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	ltApp := blockUntil(app, "const LEGACY_THEME = {", "};")
	ltBoot := blockUntil(boot, "var BOOT_LEGACY_THEME = {", "};")
	if ltApp == "" || ltBoot == "" || norm(ltApp) != norm(ltBoot) {
		t.Errorf("旧版主题迁移表不一致:\n app.js: %s\n boot.js: %s", ltApp, ltBoot)
	}

	// 4) 入场动画只能由 goto() 加 .enter 触发；挂在 .view.active 上会让
	//    HTML 写死的首页在刷新时从 opacity:0 淡入。
	for _, s := range []string{
		`.view.active{display:block}`,
		`.view.active.enter{animation:fadeUp`,
		`@media (max-width:960px){.view.active.enter{animation:none}}`,
		`html[data-boot] .view{animation:none!important}`,
	} {
		if !strings.Contains(html, s) {
			t.Errorf("index.html 缺少 %q", s)
		}
	}
	if strings.Contains(html, ".view.active{display:block;animation:") {
		t.Error(".view.active 仍自带入场动画：刷新首帧 opacity=0，整页淡入")
	}

	// 5) goto() 区分「用户切页」与「刷新恢复」，后者必须显式关掉动画。
	gt := funcBody(app, "function goto(v, sect, animate = true) {")
	if gt == "" {
		t.Fatal("app.js 里找不到带 animate 参数的 goto()")
	}
	if !strings.Contains(gt, `el.classList.toggle("enter", !!animate && on)`) {
		t.Error("goto() 没有按 animate 参数加 .enter：刷新恢复仍会播入场动画")
	}
	rv := funcBody(app, "function restoreView() {")
	if !strings.Contains(rv, "goto(v, undefined, false);") {
		t.Error("restoreView() 没关掉入场动画：刷新仍会淡入一下")
	}
}

// blockUntil 取常量字面量的一段，供 keysIn / 迁移表比对使用。
func blockUntil(src, sig, end string) string {
	i := strings.Index(src, sig)
	if i < 0 {
		return ""
	}
	rest := src[i+len(sig):]
	if j := strings.Index(rest, end); j >= 0 {
		return rest[:j]
	}
	return rest
}

// modeKeys 抽 { light: "浅色", dark: "深色" } 里的标识符键。
func modeKeys(block string) []string {
	re := regexp.MustCompile(`([a-z]+)\s*:`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(block, -1) {
		out = append(out, m[1])
	}
	return out
}
