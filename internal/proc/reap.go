package proc

// 「上一代面板留下的内核」清理。
//
// 内核是面板的子进程，而面板换进程有两条路：
//
//   - systemd restart：默认 KillMode=control-group，整个 cgroup 一起杀，内核跟着走，没问题；
//   - syscall.Exec（自更新、二进制守望者、一键脚本换了文件、docker cp）：
//     **只换进程映像，子进程原样留着**。新面板的 Manager 是空的——那些内核
//     成了没人认领的孤儿，而它们的运行状态本来就只存在内存里。
//
// 孤儿会一直监听入站端口。之后每次「应用配置」都死在端口预检：
//
//	端口 24443/TCP 已被其它程序占用（连接成功），hk-01 无法启动
//
// 面板自己停不掉它（内存里没有这个进程），界面上也删不掉，完全无解。
// 用户看到的就是「升级之后入站端口依然被占用」。
//
// 所以这里做两件事：认出来，杀掉。判定口径故意收紧到**二进制路径与配置路径
// 同时对上**——只有我们自己起的那个进程才满足这两条。只看端口、只看进程名
// 都可能误杀（用户完全可能手工跑同一个内核去调另一份配置）。

import (
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// procEntry 是一个正在运行的进程：可执行文件路径与完整参数。
type procEntry struct {
	PID  int
	Path string
	Args []string
}

// ReapOrphans 杀掉「跑我们的内核二进制、用我们的配置」的残留进程，返回被杀掉的 pid。
//
// 认不出、或杀不掉，都只是返回空——清理失败不该让调用方跟着失败。
func ReapOrphans(binPath, cfgPath string) []int {
	if binPath == "" || cfgPath == "" {
		return nil
	}
	self := os.Getpid()
	var killed []int
	for _, e := range scanProcs() {
		if e.PID == self || e.PID <= 0 {
			continue
		}
		if !isOurs(e, binPath, cfgPath) {
			continue
		}
		if killAndWait(e.PID) {
			killed = append(killed, e.PID)
		}
	}
	return killed
}

// isOurs 判断这个进程是不是「我们自己起的那个内核」。
//
// 两个条件都要满足：可执行文件就是配置里那个二进制，参数里出现我们这份配置的路径。
// 只有一条都不够——同一个内核二进制可以被用户手工跑起来调别的配置，
// 那不该被我们杀掉。
func isOurs(e procEntry, binPath, cfgPath string) bool {
	if !samePath(e.Path, binPath) {
		return false
	}
	for _, a := range e.Args {
		if samePath(a, cfgPath) {
			return true
		}
	}
	return false
}

// samePath 比较两个路径是否指同一个文件。
//
// 三件事都得处理，少一件就会「明明是自己人却认不出」：
//   - 设置里填的可能是**相对路径**（面板的 cwd 是启动目录），而 /proc 与
//     sysctl 报回来的永远是绝对路径；
//   - 可能是软链（/usr/local/bin/xray 指向版本化目录）；
//   - 文件可能已经被删（替换二进制的那一刻），解析会失败。
func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	// 先各自补成绝对路径，否则 EvalSymlinks 对相对路径只会原样返回相对路径，
	// 两边永远比不相等。
	if abs, err := filepath.Abs(a); err == nil {
		a = abs
	}
	if abs, err := filepath.Abs(b); err == nil {
		b = abs
	}
	if a == b {
		return true
	}
	ra, ea := filepath.EvalSymlinks(a)
	rb, eb := filepath.EvalSymlinks(b)
	if ea == nil && eb == nil {
		return ra == rb
	}
	// 解析不了（文件已被删/无权访问）时退回绝对路径比较
	return filepath.Clean(a) == filepath.Clean(b)
}

// killAndWait 先礼后兵：SIGTERM 等 2 秒，不走再 SIGKILL。
// 返回「信号确实送达」——EPERM 之类送不进去才算失败。
//
// 刻意**不**返回「进程已经消失」。被杀的子进程会变成僵尸（父进程还没收尸），
// 僵尸仍然在进程表里、kill(pid, 0) 也仍然成功，但它早就把端口松开了。
// 调用方真正关心的是端口能不能绑上，那件事交给它的端口预检去判。
// 这里要是按「进程消失」下结论，就会把「已经杀掉了」误报成失败，
// 于是调用方放弃重试，端口明明已经空出来却还在报占用。
func killAndWait(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	ok := p.Signal(syscall.SIGTERM) == nil
	if !waitGone(pid, 2*time.Second) {
		if err := p.Signal(syscall.SIGKILL); err == nil {
			ok = true
		}
		waitGone(pid, time.Second)
	}
	return ok
}

// waitGone 在 timeout 内反复探活，顺带收尸。返回进程是否已经不在了。
//
// 注意局限：**别人家的僵尸**在这里看不出已经死了（Wait4 返回 ECHILD、
// kill(pid,0) 仍然成功）。所以这个函数的返回值只能用来「决定还要不要发
// SIGKILL」，不能用来判断清理成功与否——见 killAndWait 的说明。
func waitGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if reapChild(pid) {
			return true
		}
		if !alive(pid) {
			return true
		}
		if time.Now().After(deadline) {
			return !alive(pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// reapChild 非阻塞收一个已经退出的子进程。不是自己的子进程时返回 false。
func reapChild(pid int) bool {
	var ws syscall.WaitStatus
	got, err := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
	return err == nil && got == pid
}

// alive 探活。注意：僵尸也算「活着」，所以调用方要先用 reapChild 收尸。
func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
