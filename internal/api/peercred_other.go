//go:build !linux && !darwin && !freebsd

package api

import (
	"errors"
	"net"
)

// peerUID 在不支持取对端凭据的平台上直接失败。
//
// 这里刻意 fail closed：取不到身份就拒绝服务，而不是「取不到就放行」。
// 放行意味着任何能连上套接字的进程都能改面板配置，而那正是这套机制要防的事。
// 实际部署只有 Linux，这个分支主要是让 GOOS=windows 的交叉编译能过。
func peerUID(*net.UnixConn) (int, error) {
	return -1, errors.New("当前平台不支持读取 unix 套接字对端凭据")
}
