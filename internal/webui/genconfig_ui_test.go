package webui

import (
	"strings"
	"testing"
)

// 「绕过节点服务器」是自动带出地址的开关，前端这一层最容易漏的是**参数没进
// buildGenParams**：界面上看得见勾选、配置里也有规则，但保存下来的 params
// 少了这个字段，一周后「重新生成」就悄悄丢掉防回环规则。整条链钉住。
func TestBypassServerOptionWired(t *testing.T) {
	html := string(indexHTML)
	appJS, _, err := AssetFile("app.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(appJS)

	// 1) 开关存在、默认勾选、走 data-act 委托（不能写内联 onclick：CSP 会忽略）
	if !strings.Contains(html, `id="gen_bypass" checked`) {
		t.Error("生成配置页缺少默认开启的 gen_bypass 开关")
	}
	if !strings.Contains(html, `id="gen_bypass"`) ||
		!strings.Contains(html, `data-act="genOptionChanged"`) {
		t.Error("gen_bypass 没有走 data-act 事件委托")
	}
	if !strings.Contains(html, "绕过节点服务器") {
		t.Error("缺少开关说明文案")
	}

	// 2) 参数必须进请求体，且随 params 保存/重新生成
	params := funcBody(src, "function buildGenParams() {")
	if !strings.Contains(params, `body.bypass_server = !!$("gen_bypass").checked`) {
		t.Error("buildGenParams() 没有收集 bypass_server，保存后重新生成会丢掉这个选项")
	}
	if !strings.Contains(src, `params.bypass_server`) {
		t.Error("重新生成没有把保存时的开关状态传回 demoGenConfig")
	}

	// 3) 开关切换后旧结果要失效，否则「下载」拿到的是上一份配置
	invalidate := funcBody(src, "function genOptionChanged() {")
	for _, s := range []string{"genLast = null", "gen_dl", "gen_verify", "gen_save"} {
		if !strings.Contains(invalidate, s) {
			t.Errorf("genOptionChanged() 缺少 %q", s)
		}
	}

	// 4) 生成结果要回显实际直连的地址：TUN 下这是「能不能连上节点」的关键信息
	if !strings.Contains(src, "j.bypass_servers") ||
		!strings.Contains(src, "节点服务器直连") {
		t.Error("生成结果没有显示实际直连的节点服务器地址")
	}

	// 5) 离线演示也要按开关出规则，否则演示页和真实面板长得不一样
	demo := funcBody(src, "function demoGenConfig(core, target, ver, bypass) {")
	for _, s := range []string{
		"  - DOMAIN,demo.example.com,DIRECT",
		`{ domain: ["demo.example.com"], outbound: "direct" }`,
	} {
		if !strings.Contains(demo, s) {
			t.Errorf("demoGenConfig() 缺少直连规则 %q", s)
		}
	}
}

// TestOpenWrtMomoTargetWired OpenWrt-momo 是「目标」，但它绑死了 sing-box 内核
// 与 1.12+ 版本。这条链最容易漏的是：按钮加了、界面还停在 mihomo，请求体
// 发出去直接 400；或者离线演示没有 momo 分支，demo.html 显示的是主路由配置。
func TestOpenWrtMomoTargetWired(t *testing.T) {
	html := string(indexHTML)
	appJS, _, err := AssetFile("app.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(appJS)

	if !strings.Contains(html, `data-v="openwrt-momo"`) {
		t.Error("配置生成页缺 OpenWrt-momo 目标按钮")
	}
	if !strings.Contains(src, `"openwrt-momo": "OpenWrt-momo：`) ||
		!strings.Contains(src, `"openwrt-momo": "OpenWrt-momo"`) {
		t.Error("目标提示或保存配置的目标标签没登记 openwrt-momo")
	}

	sync := funcBody(src, "function syncGenTargetCore() {")
	for _, s := range []string{
		`genTarget === "openwrt-momo"`,
		`genCore = "singbox"`,
		`mihomoBtn.disabled = momo`,
		`genSBVer = "1.12"`,
		`renderSets(); filterSets()`,
	} {
		if !strings.Contains(sync, s) {
			t.Errorf("syncGenTargetCore() 缺少 %q", s)
		}
	}
	// 目标切换必须调用它，并让上一份生成结果失效
	targetClick := src[strings.Index(src, `$("g_target_seg").addEventListener("click"`):]
	targetClick = targetClick[:strings.Index(targetClick, "});")+3]
	for _, s := range []string{"syncGenTargetCore()", "genOptionChanged()"} {
		if !strings.Contains(targetClick, s) {
			t.Errorf("切换目标的事件处理缺少 %q", s)
		}
	}

	// 演示数据必须和后端 genMomo 同形：momo 按 tag 找入站，少一个就起不来
	demo := funcBody(src, "function demoGenConfig(core, target, ver, bypass) {")
	for _, s := range []string{
		`if (target === "openwrt-momo")`,
		`"dns-in"`, `"redirect-in"`, `"tproxy-in"`, `"tun-in"`,
		`"fake-ip-dns-server"`, `auto_route: false`, `interface_name: "momo"`,
	} {
		if !strings.Contains(demo, s) {
			t.Errorf("demoGenConfig() 的 momo 分支缺少 %q", s)
		}
	}
}
