package api

// demo 页的类别表是 systemClasses 的第二份手抄件。
//
// demo.html 必须离线可用，所以它不能去拉 /api/export/catalog，只能在 app.js 里
// 内联一份。手抄件就会漂——v2.1.5 刚加上 apple-push 这一类，Go 侧有了、JS 侧没有，
// 演示页的「关键服务保障」于是缺了 Apple 推送这一栏，而且没人会去点开验。
//
// 这里把漂移变成红灯：任何一侧新增/删除类别而没同步另一侧，测试直接失败。
// 与其要求「记得两边一起改」，不如让漏改的地方响一声。

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// demoClassBlock 从 app.js 里截出 DEMO_CATALOG.classes 那一段。
func demoClassBlock(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../webui/app.js")
	if err != nil {
		t.Fatalf("读 app.js: %v", err)
	}
	s := string(b)
	i := strings.Index(s, "DEMO_CATALOG.classes = [")
	if i < 0 {
		t.Fatal("app.js 里找不到 DEMO_CATALOG.classes，演示页的类别表被删了或改了写法")
	}
	j := strings.Index(s[i:], "];")
	if j < 0 {
		t.Fatal("DEMO_CATALOG.classes 没有闭合（找不到 ];）")
	}
	// 只看到结束标记之后再往前走一个 `];` 的长度，避免把后面的代码卷进来。
	end := i + j + len("];")
	// 但保守一点：截到最后一个 `}` 为止的类别条目即可。
	body := s[i:end]
	if k := strings.LastIndex(body, "];"); k > 0 {
		body = body[:k]
	}
	return body
}

var demoKeyRe = regexp.MustCompile(`key:"([^"]+)"`)

func TestDemoCatalogClassesMatchBackend(t *testing.T) {
	block := demoClassBlock(t)
	inDemo := map[string]bool{}
	for _, m := range demoKeyRe.FindAllStringSubmatch(block, -1) {
		inDemo[m[1]] = true
	}
	if len(inDemo) == 0 {
		t.Fatalf("演示页类别表解析不出 key，格式可能变了：\n%s", block)
	}

	inGo := map[string]bool{}
	for _, c := range systemClasses {
		inGo[c.Key] = true
		if !inDemo[c.Key] {
			t.Errorf("后端有类别 %q（%s），演示页的 DEMO_CATALOG.classes 里没有——"+
				"离线 demo 的覆盖提示会漏掉这一类", c.Key, c.Name)
		}
	}
	for k := range inDemo {
		if !inGo[k] {
			t.Errorf("演示页有类别 %q，后端 systemClasses 里没有（多半是改了 key 没同步）", k)
		}
	}
}
