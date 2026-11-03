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
func TestNoInlineGridColumns(t *testing.T) {
	// 先把 CSS 注释整段去掉：注释里为了说明问题会引用旧写法，不该算违规。
	css := string(indexHTML)
	for {
		i := strings.Index(css, "/*")
		if i < 0 {
			break
		}
		j := strings.Index(css[i:], "*/")
		if j < 0 {
			css = css[:i]
			break
		}
		css = css[:i] + css[i+j+2:]
	}
	for _, line := range strings.Split(css, "\n") {
		if strings.Contains(line, "grid-template-columns") && strings.Contains(line, "style=") {
			t.Errorf("index.html 用内联 grid-template-columns 写死了列数（媒体查询盖不掉）: %s",
				strings.TrimSpace(line))
		}
	}
}
