package api

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"triune/internal/proc"
)

// exec 换的只是本进程的映像，**子进程原样留着**。不停内核就切，三个内核会
// 变成没人认领的孤儿：继续监听入站端口，而新面板的 Manager 是空的。
// 之后每次「应用配置」都死在端口预检上——
//
//	端口 24443/TCP 已被其它程序占用（连接成功），hk-01 无法启动
//
// 用户看到的就是「升级之后入站端口依然被占用」，而且界面上无解：
// 停不掉（内存里没有），也起不来新的。
//
// 所以这条用例钉住的是顺序：**停内核必须发生在 exec 之前**。
func TestRestartSelfStopsCoresBeforeExec(t *testing.T) {
	a := newAuthTestServer(t)
	a.ProcMgr = proc.NewManager()

	dir := t.TempDir()
	bin := filepath.Join(dir, "xray")
	// 冒充内核：ProcMgr 会以 `xray run -c <cfg>` 拉起它，脚本忽略参数睡够就行
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfg, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.ProcMgr.Start("xray", bin, cfg, dir); err != nil {
		t.Fatal(err)
	}
	if running, _ := a.ProcMgr.Status("xray"); !running {
		t.Fatal("前提不成立：内核没起来")
	}

	oldExec, oldWait := execSelf, execSelfWait
	defer func() { execSelf, execSelfWait = oldExec, oldWait }()
	execSelfWait = time.Millisecond

	coreRunningAtExec := true
	execSelf = func(string, []string, []string) error {
		coreRunningAtExec, _ = a.ProcMgr.Status("xray")
		return errors.New("exec 被沙箱挡掉了")
	}
	a.restartSelf("/nonexistent/triune-panel", "更新到 v9.9.9")

	if coreRunningAtExec {
		t.Fatal("exec 之前必须先停内核：exec 不换子进程，留着就是占端口的孤儿")
	}
	if running, _ := a.ProcMgr.Status("xray"); running {
		t.Fatal("exec 之后内核不该还在跑")
	}
}

// exec 成功那条路上进程映像会被换掉，restartSelf 不会返回；
// 这里只能验证「停内核」这一步不依赖 exec 的结果。
func TestRestartSelfStopsCoresEvenWhenExecSucceeds(t *testing.T) {
	a := newAuthTestServer(t)
	a.ProcMgr = proc.NewManager()

	dir := t.TempDir()
	bin := filepath.Join(dir, "singbox")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfg, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.ProcMgr.Start("singbox", bin, cfg, dir); err != nil {
		t.Fatal(err)
	}

	oldExec, oldWait := execSelf, execSelfWait
	defer func() { execSelf, execSelfWait = oldExec, oldWait }()
	execSelfWait = time.Millisecond
	execSelf = func(string, []string, []string) error { return nil }

	a.restartSelf("/nonexistent/triune-panel", "更新到 v9.9.9")

	if running, _ := a.ProcMgr.Status("singbox"); running {
		t.Fatal("exec 之前就该停掉内核")
	}
}
