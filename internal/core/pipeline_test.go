package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"triune/internal/model"
)

// fakeCore 测试用内核：渲染内容可控，校验命令可指定成功或失败。
type fakeCore struct {
	name    string
	content string
	badCmd  string // "true" / "false"
}

func (f *fakeCore) Name() string     { return f.name }
func (f *fakeCore) Ext() string      { return "json" }
func (f *fakeCore) FileName() string { return "config.json" }
func (f *fakeCore) Render(_ []InboundWithClients, _ RenderOpts) ([]byte, error) {
	return []byte(f.content), nil
}
func (f *fakeCore) ValidateCmd(_, _ string) *exec.Cmd {
	return exec.Command(f.badCmd)
}

// mapSettings 测试用设置源。
type mapSettings map[string]string

func (m mapSettings) Get(k string) (string, error) { return m[k], nil }

// newPipeline 注册一个假内核并返回流水线。名字唯一，避免测试间互相覆盖 registry。
func newPipeline(t *testing.T, name, content, bin string, validate bool) (*PipelineManager, string) {
	t.Helper()
	cmd := "true"
	if !validate {
		cmd = "false"
	}
	Register(&fakeCore{name: name, content: content, badCmd: cmd})

	dir := t.TempDir()
	settings := mapSettings{}
	if bin != "" {
		settings["bin_"+name] = bin
	}
	return &PipelineManager{DataDir: dir, Settings: settings}, dir
}

func cfgPathOf(t *testing.T, m *PipelineManager, name string) string {
	t.Helper()
	p, err := m.ConfigPath(name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestApplyWithoutBinary 未配置内核二进制时跳过校验，但仍应落盘。
func TestApplyWithoutBinary(t *testing.T) {
	m, _ := newPipeline(t, "fake-nobin", `{"a":1}`, "", true)

	res, err := m.Apply("fake-nobin", nil, "test")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Validated {
		t.Fatal("未配置二进制时 Validated 应为 false")
	}
	if res.Hash == "" {
		t.Fatal("应返回内容哈希")
	}
	data, err := os.ReadFile(cfgPathOf(t, m, "fake-nobin"))
	if err != nil {
		t.Fatalf("配置未落盘: %v", err)
	}
	if string(data) != `{"a":1}` {
		t.Fatalf("配置内容不对: %q", data)
	}
}

// TestApplyValidateFailKeepsRunningConfig 是本系统最核心的安全保证：
// 校验失败时，运行中的配置文件必须一个字节都不变。
func TestApplyValidateFailKeepsRunningConfig(t *testing.T) {
	const oldCfg = `{"old":true}`
	m, _ := newPipeline(t, "fake-bad", `{"new":true}`, "/bin/echo", false)
	cfg := cfgPathOf(t, m, "fake-bad")

	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte(oldCfg), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := m.Apply("fake-bad", nil, "test"); err == nil {
		t.Fatal("校验失败时必须返回错误")
	}

	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != oldCfg {
		t.Fatalf("运行中配置被改动了！want %q, got %q", oldCfg, data)
	}
	// 临时文件不能残留（残留会被内核误读或下次 apply 干扰）
	if _, err := os.Stat(cfg + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("失败后不应残留 .tmp 文件")
	}
	if _, err := os.Stat(filepath.Join(m.coreDir("fake-bad"), ".validate.json")); !os.IsNotExist(err) {
		t.Fatal("失败后不应残留校验临时文件")
	}
}

// TestApplyBacksUpPrevious 第二次应用前应把旧配置备份下来，可被 Rollback 取回。
func TestApplyBacksUpPrevious(t *testing.T) {
	m, _ := newPipeline(t, "fake-ok", `{"v":1}`, "/bin/echo", true)

	if _, err := m.Apply("fake-ok", nil, "first"); err != nil {
		t.Fatalf("第一次 apply: %v", err)
	}
	cfg := cfgPathOf(t, m, "fake-ok")
	if data, _ := os.ReadFile(cfg); string(data) != `{"v":1}` {
		t.Fatalf("第一次内容不对: %q", data)
	}

	// 换内容再应用一次
	Register(&fakeCore{name: "fake-ok", content: `{"v":2}`, badCmd: "true"})
	res, err := m.Apply("fake-ok", nil, "second")
	if err != nil {
		t.Fatalf("第二次 apply: %v", err)
	}
	if !res.Validated {
		t.Fatal("配置了二进制且校验通过，Validated 应为 true")
	}
	if data, _ := os.ReadFile(cfg); string(data) != `{"v":2}` {
		t.Fatalf("第二次内容不对: %q", data)
	}

	// 备份目录里应能找到上一次的内容
	entries, err := os.ReadDir(m.backupDir("fake-ok"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("没有生成备份: %v", err)
	}
	found := false
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json.bak") {
			t.Fatalf("备份文件名格式不对: %s", e.Name())
		}
		data, _ := os.ReadFile(filepath.Join(m.backupDir("fake-ok"), e.Name()))
		if string(data) == `{"v":1}` {
			found = true
		}
	}
	if !found {
		t.Fatal("备份里没有上一次的配置内容")
	}
}

// TestDryRunWritesNothing 预检不得落盘。
func TestDryRunWritesNothing(t *testing.T) {
	m, _ := newPipeline(t, "fake-dry", `{"dry":1}`, "/bin/echo", true)

	res, content, err := m.DryRun("fake-dry", nil)
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	if content != `{"dry":1}` {
		t.Fatalf("DryRun 内容不对: %q", content)
	}
	if res == nil || !res.Validated {
		t.Fatalf("DryRun 结果不对: %+v", res)
	}
	if _, err := os.Stat(cfgPathOf(t, m, "fake-dry")); !os.IsNotExist(err) {
		t.Fatal("DryRun 不应写出运行时配置文件")
	}
}

func TestRollback(t *testing.T) {
	m, _ := newPipeline(t, "fake-rb", `{"v":1}`, "/bin/echo", true)
	if _, err := m.Apply("fake-rb", nil, "first"); err != nil {
		t.Fatal(err)
	}
	Register(&fakeCore{name: "fake-rb", content: `{"v":2}`, badCmd: "true"})
	if _, err := m.Apply("fake-rb", nil, "second"); err != nil {
		t.Fatal(err)
	}

	cfg := cfgPathOf(t, m, "fake-rb")
	if data, _ := os.ReadFile(cfg); string(data) != `{"v":2}` {
		t.Fatalf("回滚前应为 v2, got %q", data)
	}

	from, err := m.Rollback("fake-rb")
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if from == "" {
		t.Fatal("应返回备份路径")
	}
	if data, _ := os.ReadFile(cfg); string(data) != `{"v":1}` {
		t.Fatalf("回滚后应为 v1, got %q", data)
	}
}

func TestRollbackWithoutBackups(t *testing.T) {
	m, _ := newPipeline(t, "fake-nobak", `{}`, "", true)
	if _, err := m.Rollback("fake-nobak"); err == nil {
		t.Fatal("没有备份时应报错，而不是静默成功")
	}
}

func TestApplyUnknownCore(t *testing.T) {
	m := &PipelineManager{DataDir: t.TempDir(), Settings: mapSettings{}}
	if _, err := m.Apply("no-such-core", nil, "x"); err == nil {
		t.Fatal("未知内核应报错")
	}
	if _, _, err := m.DryRun("no-such-core", nil); err == nil {
		t.Fatal("未知内核 DryRun 应报错")
	}
}

// TestAllSkipsUnregistered 未注册内核不应以 nil 元素出现在 All() 里
// （否则调用方 range 后取 Name() 会空指针 panic）。
func TestAllSkipsUnregistered(t *testing.T) {
	for _, c := range All() {
		if c == nil {
			t.Fatal("All() 返回了 nil 元素")
		}
		if c.Name() == "" {
			t.Fatal("内核 Name() 为空")
		}
	}
	// 本包测试里没有 import 任何实现包，registry 里只有本测试注册的假内核，
	// 所以固定顺序的三内核都不应出现。
	for _, c := range All() {
		switch c.Name() {
		case model.CoreSingBox, model.CoreXray, model.CoreMihomo:
			t.Fatalf("未 import 实现包却出现了 %s", c.Name())
		}
	}
}
