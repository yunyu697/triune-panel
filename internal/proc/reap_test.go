package proc

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// 清理「上一代面板留下的内核」这件事的判据与效果。
//
// 现场：面板自更新走 syscall.Exec，只换自己的进程映像，子进程原样留着；
// 新面板的 Manager 是空的，于是那些内核成了孤儿——继续占着入站端口，
// 而每次「应用配置」都死在端口预检上，界面上无解。

func TestSamePath(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "xray")
	if err := os.WriteFile(f, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "xray-link")
	if err := os.Symlink(f, link); err != nil {
		t.Fatal(err)
	}

	if !samePath(f, f) {
		t.Error("同一路径应判等")
	}
	if !samePath(link, f) {
		t.Error("软链与目标应判等：设置里填的常是 /usr/local/bin/xray，报回来的是解析后的真实路径")
	}
	if samePath(f, filepath.Join(dir, "other")) {
		t.Error("不同文件不应判等")
	}
	if samePath("", f) {
		t.Error("空路径不应判等")
	}

	// 相对路径：设置里填的是相对路径时，进程表里报回来的是绝对路径。
	// 不做绝对化就会「明明是自己人却认不出」，清理静默失效。
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(wd) }()
	if !samePath("xray", f) {
		t.Error("相对路径应与其绝对路径判等")
	}
}

func TestIsOurs(t *testing.T) {
	bin := "/opt/triune/bin/xray"
	cfg := "/var/lib/triune/cores/xray/config.json"

	ours := procEntry{PID: 1, Path: bin, Args: []string{bin, "run", "-c", cfg}}
	if !isOurs(ours, bin, cfg) {
		t.Error("二进制与配置都对上，应认作自己人")
	}

	// 同一个二进制、别的配置：用户手工跑同款内核调另一份配置，不能杀
	foreign := procEntry{PID: 2, Path: bin, Args: []string{bin, "run", "-c", "/tmp/other.json"}}
	if isOurs(foreign, bin, cfg) {
		t.Error("配置路径不同，不该认作自己人（会误杀手工起的进程）")
	}

	// 别的二进制、参数里恰好带着我们的配置路径：也不是
	other := procEntry{PID: 3, Path: "/usr/bin/tail", Args: []string{"tail", "-f", cfg}}
	if isOurs(other, bin, cfg) {
		t.Error("二进制不同，不该认作自己人")
	}
}

// TestHelperSleep 是被 ReapOrphans 杀的「内核」。
//
// 不能拿 /bin/sleep 冒充内核：isOurs 要求可执行文件路径与配置里的二进制一致，
// 而 sleep 的 exe 是 /bin/sleep。所以用测试二进制自己当内核——它的 exe 就是
// 我们传进去的那个 binPath，参数里带上配置路径，正好满足判据。
func TestHelperSleep(t *testing.T) {
	if os.Getenv("REAP_HELPER") != "1" {
		t.Skip("只在被 spawn 出来时干活")
	}
	time.Sleep(60 * time.Second)
}

func spawnFakeCore(t *testing.T, cfgPath string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperSleep$", cfgPath)
	cmd.Env = append(os.Environ(), "REAP_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd
}

func waitAlive(t *testing.T, pid int) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if alive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("进程 %d 没起来", pid)
}

func TestReapOrphansKillsOurCore(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	cmd := spawnFakeCore(t, cfg)
	pid := cmd.Process.Pid
	waitAlive(t, pid)

	killed := ReapOrphans(os.Args[0], cfg)
	if len(killed) != 1 || killed[0] != pid {
		t.Fatalf("应清掉 %d，实际 %v", pid, killed)
	}
	if alive(pid) && !reapChild(pid) {
		t.Fatalf("进程 %d 应已被清掉", pid)
	}
}

// 同一个二进制、不同配置的进程必须留着：那是用户自己起的，不是我们的孤儿。
func TestReapOrphansLeavesForeignProcessAlone(t *testing.T) {
	dir := t.TempDir()
	ours := filepath.Join(dir, "ours.json")
	theirs := filepath.Join(dir, "theirs.json")

	mine := spawnFakeCore(t, ours)
	other := spawnFakeCore(t, theirs)
	waitAlive(t, mine.Process.Pid)
	waitAlive(t, other.Process.Pid)

	killed := ReapOrphans(os.Args[0], ours)
	if len(killed) != 1 || killed[0] != mine.Process.Pid {
		t.Fatalf("只应清掉 %d，实际 %v", mine.Process.Pid, killed)
	}
	if !alive(other.Process.Pid) {
		t.Fatal("配置不同的同名进程被误杀了")
	}
}

// 配置路径为空时不做任何事：宁可留着孤儿，也不能拿空串去匹配（空串会命中一切）。
func TestReapOrphansIgnoresEmptyPaths(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	cmd := spawnFakeCore(t, cfg)
	waitAlive(t, cmd.Process.Pid)

	if killed := ReapOrphans("", cfg); len(killed) != 0 {
		t.Fatalf("二进制路径为空时不该动手，实际 %v", killed)
	}
	if killed := ReapOrphans(os.Args[0], ""); len(killed) != 0 {
		t.Fatalf("配置路径为空时不该动手，实际 %v", killed)
	}
	if !alive(cmd.Process.Pid) {
		t.Fatal("进程不该被杀")
	}
	_ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
}
