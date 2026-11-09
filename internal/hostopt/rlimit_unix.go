//go:build linux || darwin

package hostopt

import "golang.org/x/sys/unix"

// 文件描述符上限是唯一一个「不需要 root、可以在进程内瞬间修好」的体检项：
// 面板 fork 的内核进程会继承面板的 RLIMIT_NOFILE，所以把面板自己的 soft
// 提上去，新启动的 xray / sing-box 就跟着拿到同一个上限。
//
// 只动 Cur 不动 Max：Max 一旦降下去，非 root 进程就再也提不回来。

func getNofileImpl() (cur, max uint64, ok bool) {
	var l unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &l); err != nil {
		return 0, 0, false
	}
	return l.Cur, l.Max, true
}

func setNofileImpl(cur, max uint64) error {
	return unix.Setrlimit(unix.RLIMIT_NOFILE, &unix.Rlimit{Cur: cur, Max: max})
}
