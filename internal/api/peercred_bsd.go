//go:build darwin || freebsd

package api

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerUID 取对端进程的 uid（macOS / FreeBSD）。
//
// 这两个平台没有 SO_PEERCRED，对应的是 LOCAL_PEERCRED，取回来是一个
// xucred 结构。语义与 Linux 一致：由内核在 connect 时记录并回答，对端
// 无法伪造。主要用途是本地开发（macOS 上跑面板 + tp），生产是 Linux。
func peerUID(c *net.UnixConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return -1, err
	}
	var uid int
	var opErr error
	if err := raw.Control(func(fd uintptr) {
		cred, e := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if e != nil {
			opErr = e
			return
		}
		uid = int(cred.Uid)
	}); err != nil {
		return -1, err
	}
	return uid, opErr
}
