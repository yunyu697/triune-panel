//go:build linux

package hostopt

import (
	"bytes"

	"golang.org/x/sys/unix"
)

// compiledReport 是编译为 Linux 时的生产探测入口，对应下列只读路径：
//
//	/proc/sys/net/ipv4/tcp_congestion_control
//	/proc/sys/net/ipv4/tcp_available_congestion_control
//	/proc/sys/net/core/default_qdisc
//	/proc/version、/proc/net/route、/proc/1/cgroup、/proc/vz
//	/sys/module/tcp_bbr、/.dockerenv、/.containerenv
//
// 真正的 /proc 只读逻辑放在无 build tag 的 probe.go（全部用 os.ReadFile/os.Stat，
// 不调 shell）：这样 macOS 开发机上的单测也能用桩覆盖完整 Linux 逻辑；而本文件
// 用 build tag 保证「只有 Linux 二进制才会真正去读 /proc」。
func compiledReport(dataDir string) Report { return linuxReport(dataDir) }

// kernelReleaseFallback 在 /proc/version 读不到时用 unix.Uname 取内核版本：
// 纯 Go 系统调用，不依赖 uname 命令（产品约束：别用 shell 取内核信息）。
func kernelReleaseFallback() (string, bool) {
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		return "", false
	}
	b := u.Release[:]
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b), true
}
