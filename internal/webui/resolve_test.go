package webui

import (
	"bytes"
	"testing"
)

// TestResolvePageFallsBackForUnmarkedBuiltinPage 兜住更早版本落地的无标记页面。
//
// 只认「带标记且版本不同」是不够的：v1.3.0 之前落地的页面没有 build 标记，
// 若启动时又写盘失败（只读挂载 / 属主不对），两道防线会同时失效，界面永久
// 停在旧版——这正是用户反馈「升级完还是旧界面」的一条完整链路。
func TestResolvePageFallsBackForUnmarkedBuiltinPage(t *testing.T) {
	disk := []byte(`<!DOCTYPE html><html><body>OLD-UI-SENTINEL` +
		`<script src="/assets/app.js"></script></body></html>`)
	got := ResolvePage(disk)
	if bytes.Contains(got, []byte("OLD-UI-SENTINEL")) {
		t.Fatal("无标记的内置页也必须回退到内嵌页，否则写盘失败时界面永远停在旧版")
	}
	if !bytes.Contains(got, []byte("/assets/app.js")) {
		t.Fatal("回退结果应当是内嵌页面")
	}
}

// TestResolvePageKeepsCustomUI 无标记、也不像内置页的，是用户自己的 UI，
// 必须原样返回——否则自定义界面会被内置页顶掉，连宽松 CSP 一起丢。
func TestResolvePageKeepsCustomUI(t *testing.T) {
	custom := []byte(`<!DOCTYPE html><html><body>` +
		`<button onclick="x()">go</button>CUSTOM-UI</body></html>`)
	if got := ResolvePage(custom); !bytes.Equal(got, custom) {
		t.Fatal("无标记的自定义 UI 必须原样返回")
	}
}

// TestResolvePageKeepsCurrentVersion 标记与当前二进制一致时不动它。
func TestResolvePageKeepsCurrentVersion(t *testing.T) {
	cur := append([]byte("<!-- triune-panel-web/"+EmbeddedVersion()+" -->\n"),
		[]byte("<html>CURRENT</html>")...)
	if got := ResolvePage(cur); !bytes.Equal(got, cur) {
		t.Fatal("标记与二进制一致的页面应原样返回")
	}
}

// TestResolvePageEmpty 空页面（读到了但内容为空）也要回退，不能把白页发出去。
func TestResolvePageEmpty(t *testing.T) {
	if got := ResolvePage(nil); !bytes.Equal(got, indexHTML) {
		t.Fatal("空页面应回退内嵌页")
	}
}

// TestEmbeddedPageIsCopy 返回副本，调用方改它不能污染后续响应。
func TestEmbeddedPageIsCopy(t *testing.T) {
	p := EmbeddedPage()
	if len(p) == 0 {
		t.Fatal("内嵌页不应为空")
	}
	first := indexHTML[0]
	p[0] = 'X'
	if indexHTML[0] != first {
		t.Fatal("EmbeddedPage 必须返回副本")
	}
}
