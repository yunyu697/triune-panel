package proc

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeStub(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "stub-core")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestRunningCoresDoesNotDeadlock 回归：RunningCores 曾持着 m.mu 又调
// Status()，而 Status 内部再锁同一把非重入的 sync.Mutex——一次 /metrics
// 采集就会把整个内核管理器永久卡死，之后所有「应用 / 停止 / 重启」都阻塞。
func TestRunningCoresDoesNotDeadlock(t *testing.T) {
	dir := t.TempDir()
	bin := writeStub(t, dir, "exec sleep 30")
	m := NewManager()
	defer m.StopAll()
	if err := m.Start("singbox", bin, filepath.Join(dir, "config.json"), dir); err != nil {
		t.Fatal(err)
	}

	done := make(chan []string, 1)
	go func() { done <- m.RunningCores() }()
	select {
	case got := <-done:
		if len(got) != 1 || got[0] != "singbox" {
			t.Fatalf("RunningCores = %v，应为 [singbox]", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunningCores 死锁：持锁后重复加锁")
	}
}

// TestStatusFalseAfterExit 启动即退出的进程必须判为不存活。
// 只靠 Signal(0) 不行：进程刚死、还没被 Wait 回收时是僵尸，Signal(0)
// 照样成功，健康检查会把「已死」看成「活着」。
func TestStatusFalseAfterExit(t *testing.T) {
	dir := t.TempDir()
	bin := writeStub(t, dir, "exit 0")
	m := NewManager()
	defer m.StopAll()
	if err := m.Start("singbox", bin, filepath.Join(dir, "config.json"), dir); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if alive, _ := m.Status("singbox"); !alive {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("已退出的进程仍被判为存活")
}
