// 这份测试与 restore_test.go 同属 api_test 外部包：/api/switch-core 要走真实的
// 内核注册表与进程管理，而注册是进程级全局状态，放进 api 包内会改变同包其它
// 测试「内核未注册」的前提。
package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"triune/internal/api"
	"triune/internal/core"
	"triune/internal/model"
	"triune/internal/proc"
	"triune/internal/store"
)

// fakeCore 是一个「像内核一样」的假二进制。
//
// 校验参数（check / -test / -t）立刻退出 0；运行参数则按配置文件里的端口
// 真的监听起来 —— healthCheck 会逐个拨号探测入站端口，只 sleep 的假内核
// 会被判成「进程在跑但端口没监听」，成功路径的断言就永远过不了。
//
// 运行阶段必须 exec 过去：proc.Manager.Stop 杀的是主进程，python 如果是
// shell 的子进程就会活下来继续占着端口，下一次 apply 的端口预检必然误报。
const fakeCore = `#!/bin/sh
for a in "$@"; do
	case "$a" in
	check|-test|-t) exit 0 ;;
	esac
done
cfg=""
prev=""
for a in "$@"; do
	case "$prev" in
	-c|-config|-f) cfg="$a" ;;
	esac
	prev="$a"
done
exec python3 -c '
import json, socket, sys, time
doc = json.load(open(sys.argv[1]))
socks = []
for ib in doc.get("inbounds", []):
    for k in ("listen_port", "port"):
        if k in ib:
            s = socket.socket()
            s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            s.bind(("127.0.0.1", int(ib[k])))
            s.listen(8)
            socks.append(s)
time.sleep(120)
' "$cfg"
`

// switchFixture 搭一个能跑 /api/switch-core 的环境：Store + Pipe + ProcMgr 全在，
// 两个内核都配好假二进制。
func switchFixture(t *testing.T) (*api.Server, *proc.Manager, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	pm := proc.NewManager()
	t.Cleanup(func() {
		for _, n := range []string{"singbox", "xray", "mihomo"} {
			pm.Stop(n)
		}
	})
	pipe := &core.PipelineManager{DataDir: dir, ProcMgr: pm, Settings: st}
	a := api.New(st, pipe, pm, dir)

	bin := filepath.Join(dir, "fake-core.sh")
	if err := os.WriteFile(bin, []byte(fakeCore), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"singbox", "xray"} {
		if err := st.SetSetting("bin_"+n, bin); err != nil {
			t.Fatal(err)
		}
	}
	return a, pm, dir
}

func switchLogin(t *testing.T, a *api.Server) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"user": "admin", "pass": "admin"})
	req := httptest.NewRequest("POST", "/api/login", bytes.NewReader(body))
	req.RemoteAddr = "203.0.113.9:1234"
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("登录失败 %d: %s", w.Code, w.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	tok, _ := out["token"].(string)
	if tok == "" {
		t.Fatal("登录没拿到 token")
	}
	return tok
}

func switchReq(t *testing.T, a *api.Server, tok, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "203.0.113.9:1234"
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	return w
}

// TestSwitchAbortsBeforeTouchingTargetWhenShrinkFails
//
// v1.2.2 的核心回归：旧内核收缩失败时必须中止切换，目标内核一个字节都不动。
//
// 原实现先 ProcMgr.Stop(from) 把整个旧内核停掉，再去启动新内核——收缩失败
// 发生在「服务已经断了」之后，而且只记进 warnings、HTTP 照样返回 200。
func TestSwitchAbortsBeforeTouchingTargetWhenShrinkFails(t *testing.T) {
	a, _, dir := switchFixture(t)
	tok := switchLogin(t, a)

	keep := &model.Inbound{Name: "keep", Core: "singbox", Protocol: "socks", Port: 39101,
		Transport: "tcp", TLS: "none", Tag: "keep", Enabled: true, Settings: "{}"}
	stay := &model.Inbound{Name: "stay", Core: "singbox", Protocol: "socks", Port: 39102,
		Transport: "tcp", TLS: "none", Tag: "stay", Enabled: true, Settings: "{}"}
	mkInbound(t, a, keep)
	mkInbound(t, a, stay)

	// 旧内核的校验命令直接失败 → 收缩必然过不去
	bad := filepath.Join(dir, "bad-core.sh")
	if err := os.WriteFile(bad, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.SetSetting("bin_singbox", bad); err != nil {
		t.Fatal(err)
	}

	w := switchReq(t, a, tok, "POST", "/api/switch-core",
		fmt.Sprintf(`{"to":"xray","inbound_ids":[%d]}`, keep.ID))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("收缩失败应 422，实际 %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "收缩") {
		t.Errorf("错误应指明失败在收缩阶段，实际: %s", w.Body.String())
	}

	got, err := a.Store.GetInbound(keep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Core != "singbox" {
		t.Errorf("DB 应还原成 singbox，实际 %q", got.Core)
	}

	// 目标内核根本没被启动过：连配置文件都不该存在
	cfg := filepath.Join(dir, "cores", "xray", "config.json")
	if _, err := os.Stat(cfg); err == nil {
		t.Error("收缩失败时不应触碰目标内核，但它的配置文件已经生成了")
	}
}

// TestSwitchKeepsOldCoreServingItsOtherInbounds
//
// 迁一条入站不能让旧内核上其它入站跟着断——这是「两个内核可以同时运行」
// 在切换路径上的直接体现。断言旧内核进程仍存活，且它的配置里只剩没迁走那条。
func TestSwitchKeepsOldCoreServingItsOtherInbounds(t *testing.T) {
	a, pm, dir := switchFixture(t)
	tok := switchLogin(t, a)

	move := &model.Inbound{Name: "move", Core: "singbox", Protocol: "socks", Port: 39111,
		Transport: "tcp", TLS: "none", Tag: "mv-39111", Enabled: true, Settings: "{}"}
	stay := &model.Inbound{Name: "stay", Core: "singbox", Protocol: "socks", Port: 39112,
		Transport: "tcp", TLS: "none", Tag: "st-39112", Enabled: true, Settings: "{}"}
	mkInbound(t, a, move)
	mkInbound(t, a, stay)

	// 先真的应用一次，让 singbox 跑起来（否则「没被停」无从判断）
	if w := switchReq(t, a, tok, "POST", "/api/apply", `{"core":"singbox"}`); w.Code != http.StatusOK {
		t.Fatalf("apply singbox 失败 %d: %s", w.Code, w.Body.String())
	}
	if running, _ := pm.Status("singbox"); !running {
		t.Fatal("前置条件不成立：singbox 没跑起来")
	}

	w := switchReq(t, a, tok, "POST", "/api/switch-core",
		fmt.Sprintf(`{"to":"xray","inbound_ids":[%d]}`, move.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("切换应成功，实际 %d: %s", w.Code, w.Body.String())
	}

	// 旧内核还承载着 stay，必须继续跑
	if running, _ := pm.Status("singbox"); !running {
		t.Error("旧内核还有其它入站，不该被停掉")
	}
	b, err := os.ReadFile(filepath.Join(dir, "cores", "singbox", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "mv-39111") {
		t.Error("迁走的入站应已从旧内核配置里消失")
	}
	if !strings.Contains(string(b), "st-39112") {
		t.Error("没迁走的入站应仍在旧内核配置里")
	}

	// 新内核接管迁走的那条
	if running, _ := pm.Status("xray"); !running {
		t.Error("目标内核应已启动")
	}
	bx, err := os.ReadFile(filepath.Join(dir, "cores", "xray", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bx), "mv-39111") {
		t.Error("迁走的入站应出现在新内核配置里")
	}
}
