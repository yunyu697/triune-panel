package webui

import (
	"strings"
	"testing"
)

// 事件委托的两条硬约束。都是「不报错、只表现为功能静默失灵」的类型，
// 靠人眼审代码很难发现，所以钉在测试里。

// TestDelegatedEventsGuardSelf 所有委托事件都必须经过 actFrom()。
//
// 遮罩层（.overlay）挂着 data-act="closeModal" data-self="1"，含义是
// 「只有事件发生在它自己身上才算」。data-self 的判断原先只写在 click 里，
// input/change 各写了一份不带判断的版本，于是输入框里敲一个字，input 事件
// 冒泡到遮罩被当成点了空白区，弹窗当场关闭——**所有对话框都无法编辑**，
// 控制台一声不响。
func TestDelegatedEventsGuardSelf(t *testing.T) {
	js, _, err := AssetFile("app.js")
	if err != nil {
		t.Fatalf("读 app.js 失败: %v", err)
	}
	src := string(js)

	// 守卫函数本身不能丢
	i := strings.Index(src, "function actFrom(")
	if i < 0 {
		t.Fatal("app.js 里找不到 actFrom()——委托事件的 data-self 守卫没了")
	}
	body := src[i:]
	if j := strings.Index(body, "\n}"); j > 0 {
		body = body[:j]
	}
	if !strings.Contains(body, `dataset.self === "1"`) {
		t.Error("actFrom() 里没有 data-self 判断：遮罩内部控件的事件会被当成点击空白区")
	}

	// 三个事件必须共用同一个派发函数，且这个函数里同时有 actFrom 与 eventOf。
	// 各写一份的结果就是「漏守卫」（v1.1.5）或「同一控件派发三次」（通知连弹三条）。
	h := strings.Index(src, "function onDelegated(")
	if h < 0 {
		t.Fatal("app.js 里找不到 onDelegated()——委托事件的统一入口没了")
	}
	hbody := src[h:]
	if j := strings.Index(hbody, "\n}"); j > 0 {
		hbody = hbody[:j]
	}
	for _, need := range []struct{ name, why string }{
		{"actFrom(", "没有走 actFrom：弹窗里的输入/选择会冒泡到遮罩并关掉弹窗"},
		{"eventOf(", "没有走 eventOf：select / checkbox 会连发 click+input+change，动作执行三次"},
	} {
		if !strings.Contains(hbody, need.name) {
			t.Errorf("onDelegated() %s", need.why)
		}
	}

	for _, evt := range []string{"click", "change", "input"} {
		reg := `document.addEventListener("` + evt + `", onDelegated)`
		if !strings.Contains(src, reg) {
			t.Errorf("app.js 里没有统一的 %s 委托注册（应为 %s）", evt, reg)
		}
	}

	// eventOf 必须按控件类型分流，否则 select/checkbox 会被派发三次。
	e := strings.Index(src, "function eventOf(")
	if e < 0 {
		t.Fatal("app.js 里找不到 eventOf()——同一控件会派发三次")
	}
	ebody := src[e:]
	if j := strings.Index(ebody, "\n}"); j > 0 {
		ebody = ebody[:j]
	}
	for _, need := range []string{`"SELECT"`, `"change"`, `"input"`} {
		if !strings.Contains(ebody, need) {
			t.Errorf("eventOf() 缺少 %s 分支：select / 输入框会被派发多次或一次都不派发", need)
		}
	}
}

// TestNoTemplateLiteralLeakInHTML index.html 是静态 HTML，不是模板字符串。
// 里面出现 ${...} 只可能是「本想写在 app.js 的表达式被误贴进来」，结果是一段
// 永远不会被求值的字面量：data-args 解析失败、按钮点了没反应。
// v1.1.4 的 diff 弹窗「确认无误，应用」就是这么坏的。
func TestNoTemplateLiteralLeakInHTML(t *testing.T) {
	for n, line := range strings.Split(string(indexHTML), "\n") {
		if strings.Contains(line, "${") {
			t.Errorf("index.html:%d 出现模板字符串占位符（静态 HTML 里不会被求值）: %s",
				n+1, strings.TrimSpace(line))
		}
	}
}
