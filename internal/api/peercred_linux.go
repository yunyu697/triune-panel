//go:build linux

package api

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerUID 取对端进程的 uid。
//
// Linux 用 SO_PEERCRED：内核在 connect(2) 那一刻就把对端的 uid/gid/pid 记在
// socket 上，之后由内核负责回答，对端既不能伪造也不能事后更换。这是本地
// 套接字上唯一可信的身份来源——别去读 /proc 反查，那中间有 TOCTOU 窗口。
func peerUID(c *net.UnixConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return -1, err
	}
	var uid int
	var opErr error
	if err := raw.Control(func(fd uintptr) {
		cred, e := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
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
