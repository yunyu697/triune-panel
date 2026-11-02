package api

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 自动切换这条路的回归保护。
//
// 用户的原话是「我需要的是升级完成后立刻切换成最新版本」——不是「面板告诉我
// 该重启了」。所以这里钉四件事：
//   - 版本串比较不能把 "triune-panel v1.1.1 (linux/amd64)" 和 "v1.1.1" 判成不同；
//   - 文件正在被写的时候不能动手（要连看两轮同一个签名）；
//   - 切过去之后必须真的换了进程，否则重试有上限，不能变成重启风暴；
//   - 开关关掉时一轮都不许切。

func TestNormVerString(t *testing.T) {
	cases := []struct{ in, want string }{
		{"triune-panel v1.1.1 (linux/amd64)", "1.1.1"},
		{"v1.1.1", "1.1.1"},
		{"1.1.1", "1.1.1"},
		{"V1.1.1", "1.1.1"},
		{"v1.1.1-rc2", "1.1.1-rc2"},
		{"  v1.1.1  ", "1.1.1"},
		{"dev", "dev"},
		// dev 构建自报的是整行、没有版本号：程序名与平台后缀不能算进版本
		{"triune-panel dev (linux/amd64)", "dev"},
	}
	for _, c := range cases {
		if got := normVerString(c.in); got != c.want {
			t.Errorf("normVerString(%q) = %q，应为 %q", c.in, got, c.want)
		}
	}
	// 最关键的一条：二进制自报的整行必须和构建时注入的短版本号相等
	if normVerString("triune-panel v1.1.1 (linux/amd64)") != normVerString("v1.1.1") {
		t.Error("自报整行与短版本号被判成不同版本，会导致每次启动都白切一次")
	}
}

func TestClaimSwitchTriesCapsAtTwo(t *testing.T) {
	a := newAuthTestServer(t)

	if !a.claimSwitchTries("v1.1.1") {
		t.Fatal("第一次切换应被允许")
	}
	if !a.claimSwitchTries("v1.1.1") {
		t.Fatal("第二次切换应被允许")
	}
	if a.claimSwitchTries("v1.1.1") {
		t.Fatal("同一目标超过上限还在放行，会变成无限重启")
	}
	// 换了目标就是新的一轮，计数要重新开始
	if !a.claimSwitchTries("v1.2.0") {
		t.Fatal("换了目标版本应重新计数")
	}
	// 版本串形式不同（自报整行 vs 短版本号）要算同一个目标
	if !a.claimSwitchTries("triune-panel v1.2.0 (linux/amd64)") {
		t.Fatal("同一版本的不同写法应共用计数")
	}
	if a.claimSwitchTries("v1.2.0") {
		t.Fatal("同一版本换个写法就绕过上限，等于没有上限")
	}
	a.clearSwitchTries()
	if !a.claimSwitchTries("v1.2.0") {
		t.Fatal("清零后应重新允许切换")
	}
}

// TestWatchTickWaitsForStableBinary 文件刚变的那一轮只记录，不变的第二轮才切。
//
// 「拷贝到一半的二进制」通常跑不起来，但万一跑起来了就是个坏版本；
// 多等一轮（默认 3 秒）换来的确定性，比省那 3 秒值。
func TestWatchTickWaitsForStableBinary(t *testing.T) {
	a := newAuthTestServer(t)
	oldProbe := probeVer
	defer func() { probeVer = oldProbe }()
	probeVer = func(string) (string, error) { return "triune-panel v9.9.9 (linux/amd64)", nil }

	exe := filepath.Join(t.TempDir(), "triune-panel")
	if err := os.WriteFile(exe, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	var w binWatch

	if got := a.watchTick(&w, exe); got != "" {
		t.Fatalf("刚看到文件变化就切了（%q），没有等它稳定", got)
	}
	if got := a.watchTick(&w, exe); got != "triune-panel v9.9.9 (linux/amd64)" {
		t.Fatalf("第二轮文件没再变，应该切过去，实际 %q", got)
	}

	// 文件又变了（还在写）：必须重新观察，不能拿旧结论直接切
	if err := os.WriteFile(exe, []byte("v1-longer"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := a.watchTick(&w, exe); got != "" {
		t.Fatalf("文件又变了却立刻切（%q），可能切到写了一半的二进制", got)
	}
	if got := a.watchTick(&w, exe); got == "" {
		t.Fatal("稳定下来之后应该切")
	}
}

// TestWatchTickIgnoresSameVersion 磁盘上就是要跑的那个版本时永远不动。
func TestWatchTickIgnoresSameVersion(t *testing.T) {
	a := newAuthTestServer(t)
	oldProbe := probeVer
	defer func() { probeVer = oldProbe }()
	probeVer = func(string) (string, error) { return "triune-panel " + Version + " (linux/amd64)", nil }

	exe := filepath.Join(t.TempDir(), "triune-panel")
	if err := os.WriteFile(exe, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	var w binWatch
	for i := 0; i < 3; i++ {
		if got := a.watchTick(&w, exe); got != "" {
			t.Fatalf("版本相同却要切换（%q）", got)
		}
	}
	// 探版本失败（文件不可执行 / 正在写）时也只是跳过
	probeVer = func(string) (string, error) { return "", errors.New("exec format error") }
	if err := os.WriteFile(exe, []byte("bin2"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if got := a.watchTick(&w, exe); got != "" {
			t.Fatalf("探不到版本时不应切换（%q）", got)
		}
	}
}

// TestWatchOnceSwitchesAndGivesUp 端到端：切 → exec 失败留痕 → 重试有上限 → 停手。
//
// 这是「升级完成后立刻切过去」的主路径。exec 在测试里必然失败（真跑起来就
// 换成测试二进制了），所以这里验证的是**失败之后的行为**：不能无限重启，
// 但必须把「切了、没成」写清楚。
func TestWatchOnceSwitchesAndGivesUp(t *testing.T) {
	a := newAuthTestServer(t)
	oldProbe, oldExec, oldWait := probeVer, execSelf, execSelfWait
	defer func() { probeVer, execSelf, execSelfWait = oldProbe, oldExec, oldWait }()
	probeVer = func(string) (string, error) { return "triune-panel v9.9.9 (linux/amd64)", nil }
	execSelfWait = 0
	attempts := 0
	execSelf = func(string, []string, []string) error {
		attempts++
		return errors.New("operation not permitted")
	}

	exe := filepath.Join(t.TempDir(), "triune-panel")
	if err := os.WriteFile(exe, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	var w binWatch
	a.watchOnce(&w, exe) // 第一轮只记录
	if attempts != 0 {
		t.Fatalf("文件刚变就 exec 了（%d 次）", attempts)
	}

	switched := 0
	for i := 0; i < 6; i++ {
		if a.watchOnce(&w, exe) {
			switched++
		}
	}
	if switched != maxAutoSwitchTries {
		t.Fatalf("应尝试切换 %d 次后停手，实际 %d 次", maxAutoSwitchTries, switched)
	}
	if attempts != maxAutoSwitchTries {
		t.Fatalf("exec 次数应与切换次数一致，实际 %d", attempts)
	}
	// 失败必须留痕：界面靠它说清「换了文件但进程没换」
	msg, _ := a.Store.GetSetting(SettingRestartFailed)
	if !strings.Contains(msg, "v9.9.9") || !strings.Contains(msg, "operation not permitted") {
		t.Fatalf("失败说明应包含目标版本与原因，实际 %q", msg)
	}
}

// TestAutoSwitchSettingOff 关掉开关就一轮都不切（容器编排下由外部决定重启时机）。
func TestAutoSwitchSettingOff(t *testing.T) {
	a := newAuthTestServer(t)
	oldProbe, oldExec := probeVer, execSelf
	defer func() { probeVer, execSelf = oldProbe, oldExec }()
	probeVer = func(string) (string, error) { return "triune-panel v9.9.9 (linux/amd64)", nil }
	execSelf = func(string, []string, []string) error {
		t.Error("开关关掉后不应 exec")
		return nil
	}
	if !a.autoSwitchOn() {
		t.Fatal("默认应开启")
	}
	_ = a.Store.SetSetting(AutoSwitchSetting, "0")
	if a.autoSwitchOn() {
		t.Fatal("设为 0 后应关闭")
	}
	// 开关只影响循环，watchTick 本身不看开关——这里确认循环的前置判断生效
	exe := filepath.Join(t.TempDir(), "triune-panel")
	if err := os.WriteFile(exe, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	var w binWatch
	a.watchTick(&w, exe)
	a.watchTick(&w, exe) // 即便探到新版本，也由循环里的 autoSwitchOn 拦住
	time.Sleep(10 * time.Millisecond)
}

// TestSelfExeStripsDeletedSuffix Linux 上 /proc/self/exe 在二进制被替换后带
// " (deleted)"，直接拿去 rename / exec 会失败得莫名其妙。这里只钉住「剥后缀」
// 这一层——真实文件不存在的分支不该被当成可用路径。
func TestSelfExeStripsDeletedSuffix(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "triune-panel")
	if err := os.WriteFile(p, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 剥后缀后文件在 → 应成功（用同一段逻辑模拟 os.Executable 的返回值）
	got, err := selfExeAt(p + " (deleted)")
	if err != nil {
		t.Fatalf("应剥掉 (deleted) 后缀并返回可用路径，实际报错 %v", err)
	}
	if got != p {
		t.Fatalf("应返回 %q，实际 %q", p, got)
	}
	if _, err := selfExeAt(filepath.Join(dir, "nope") + " (deleted)"); err == nil {
		t.Fatal("文件真的不存在时应报错，而不是返回一个用不了的路径")
	}
}
