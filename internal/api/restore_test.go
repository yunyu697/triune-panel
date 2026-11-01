// 这份测试故意放在 api_test 外部包里：它需要 import 三个内核实现包来
// 完成注册，而注册是进程级的全局状态。放进 api 包内会顺带改变同包其它
// 测试的可见内核集合，那些测试本来是按「未注册」的前提写的。
package api_test

import (
	"os"
	"path/filepath"
	"testing"

	"triune/internal/api"
	"triune/internal/core"
	"triune/internal/model"
	"triune/internal/proc"
	"triune/internal/store"

	// 注册内核实现（与 cmd/server/main.go 一致），否则 core.All() 是空的，
	// RestoreCores 会什么都不做，测试就成了假通过。
	_ "triune/internal/core/mihomo"
	_ "triune/internal/core/singbox"
	_ "triune/internal/core/xray"
)

// restoreFixture 搭一个能跑 RestoreCores 的最小环境。
//
// fakeBin 是一个会睡一会儿的 shell 脚本——proc.Manager 只看「进程起来了没」，
// 不关心它到底是不是真的内核，所以拿 sleep 冒充足够了。
func restoreFixture(t *testing.T, fakeBin bool) (*api.Server, *proc.Manager, string, string) {
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
	if fakeBin {
		if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := st.SetSetting("bin_singbox", bin); err != nil {
			t.Fatal(err)
		}
	}
	return a, pm, dir, bin
}

// writeCfg 在预期位置放一份渲染产物（模拟「上次应用过配置」）。
func writeCfg(t *testing.T, dir, coreName string) string {
	t.Helper()
	c, err := core.Get(coreName)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "cores", coreName, c.FileName())
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func mkInbound(t *testing.T, a *api.Server, in *model.Inbound) {
	t.Helper()
	if err := a.Store.CreateInbound(in); err != nil {
		t.Fatal(err)
	}
}

// TestRestoreCoresStartsEnabledCore 有启用入站 + 内核没跑 + 配置在位 → 拉起来。
func TestRestoreCoresStartsEnabledCore(t *testing.T) {
	a, pm, dir, _ := restoreFixture(t, true)
	mkInbound(t, a, &model.Inbound{
		Name: "n1", Core: "singbox", Protocol: "vless", Port: 443, Transport: "tcp", Enabled: true,
	})
	writeCfg(t, dir, "singbox")

	a.RestoreCores()

	if running, _ := pm.Status("singbox"); !running {
		t.Fatal("有启用中的入站，singbox 应被恢复启动")
	}
}

// TestRestoreCoresSkipsDisabledAndDraft 停用和草稿都不该触发启动——
// 草稿虽然 enabled 但根本不参与渲染，把它当理由去启动内核是错的。
func TestRestoreCoresSkipsDisabledAndDraft(t *testing.T) {
	a, pm, dir, _ := restoreFixture(t, true)
	mkInbound(t, a, &model.Inbound{Name: "off", Core: "singbox", Protocol: "vless", Port: 1001, Enabled: false})
	mkInbound(t, a, &model.Inbound{Name: "draft", Core: "singbox", Protocol: "vless", Port: 1002, Enabled: true, Draft: true})
	writeCfg(t, dir, "singbox")

	a.RestoreCores()

	if running, _ := pm.Status("singbox"); running {
		t.Fatal("只有停用/草稿入站时不该启动 singbox")
	}
}

// TestRestoreCoresSkipsWhenConfigMissing 没应用过配置就别硬起：
// 内核没有配置文件会直接退出，反而在 core.log 里刷一堆看不懂的错误。
func TestRestoreCoresSkipsWhenConfigMissing(t *testing.T) {
	a, pm, _, _ := restoreFixture(t, true)
	mkInbound(t, a, &model.Inbound{Name: "n1", Core: "singbox", Protocol: "vless", Port: 443, Enabled: true})
	// 故意不写配置文件

	a.RestoreCores()

	if running, _ := pm.Status("singbox"); running {
		t.Fatal("配置不存在时应跳过，不该启动 singbox")
	}
}

// TestRestoreCoresSkipsWhenBinaryMissing 设置里没有 bin_singbox 时跳过。
// 这条路径在生产上很常见：装完面板还没在设置里装内核。
func TestRestoreCoresSkipsWhenBinaryMissing(t *testing.T) {
	a, pm, dir, _ := restoreFixture(t, false) // 不写 bin_singbox
	mkInbound(t, a, &model.Inbound{Name: "n1", Core: "singbox", Protocol: "vless", Port: 443, Enabled: true})
	writeCfg(t, dir, "singbox")

	a.RestoreCores()

	if running, _ := pm.Status("singbox"); running {
		t.Fatal("没配二进制路径时应跳过")
	}
}

// TestRestoreCoresSkipsWhenBinaryNotOnDisk 设置里写了路径但文件不在（装完又删了）。
func TestRestoreCoresSkipsWhenBinaryNotOnDisk(t *testing.T) {
	a, pm, dir, _ := restoreFixture(t, true)
	if err := a.Store.SetSetting("bin_singbox", filepath.Join(dir, "gone")); err != nil {
		t.Fatal(err)
	}
	mkInbound(t, a, &model.Inbound{Name: "n1", Core: "singbox", Protocol: "vless", Port: 443, Enabled: true})
	writeCfg(t, dir, "singbox")

	a.RestoreCores()

	if running, _ := pm.Status("singbox"); running {
		t.Fatal("二进制不存在时应跳过")
	}
}

// TestRestoreCoresAlreadyRunning 已经在跑的内核不该被重启——
// 重启会断掉所有在线连接，而这条路径在「应用配置」里已经走过一遍了。
func TestRestoreCoresAlreadyRunning(t *testing.T) {
	a, pm, dir, bin := restoreFixture(t, true)
	mkInbound(t, a, &model.Inbound{Name: "n1", Core: "singbox", Protocol: "vless", Port: 443, Enabled: true})
	cfg := writeCfg(t, dir, "singbox")

	if err := pm.Start("singbox", bin, cfg, filepath.Join(dir, "cores", "singbox")); err != nil {
		t.Fatal(err)
	}
	_, before := pm.Status("singbox")

	a.RestoreCores()

	running, after := pm.Status("singbox")
	if !running {
		t.Fatal("singbox 应仍在运行")
	}
	if !after.Equal(before) {
		t.Fatal("已运行的内核被重启了：启动时间变了")
	}
}

// TestRestoreCoresNoProcMgr 没有 proc.Manager 时直接返回，不能 panic。
// 面板的某些测试/工具模式就是只开 store 不开内核管理。
func TestRestoreCoresNoProcMgr(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := api.New(st, nil, nil, dir)
	a.RestoreCores() // 不 panic 即通过
}
