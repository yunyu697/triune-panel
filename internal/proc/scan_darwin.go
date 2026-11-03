//go:build darwin

package proc

import (
	"bytes"
	"encoding/binary"

	"golang.org/x/sys/unix"
)

// scanProcs 在 macOS 上走 sysctl：kern.proc.all 列出所有进程，
// kern.procargs2 取每个进程的可执行路径与完整参数。
//
// 为什么不用 ps：面板是常驻进程，清理只在启动/应用配置时跑一次，但 ps 的输出
// 会被终端宽度截断、还得解析人类可读的文本；sysctl 给的是原始字节，路径与
// 参数都是完整的。也考虑过 lsof，但它在最小系统上不一定装。
func scanProcs() []procEntry {
	all, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil
	}
	out := make([]procEntry, 0, len(all))
	for _, k := range all {
		pid := int(k.Proc.P_pid)
		if pid <= 0 {
			continue
		}
		raw, err := unix.SysctlRaw("kern.procargs2", pid)
		if err != nil {
			// 别的用户的进程读不到，跳过就好
			continue
		}
		path, args := parseProcArgs(raw)
		if path == "" {
			continue
		}
		out = append(out, procEntry{PID: pid, Path: path, Args: args})
	}
	return out
}

// parseProcArgs 解析 kern.procargs2 返回的那块字节。
//
// 布局：int32 argc | 可执行文件路径 NUL | 若干对齐用的 NUL | argv[0..argc-1] 各以 NUL 结尾 | 环境变量
//
// argc 是小端 int32，不是 uint32——但按位读进来再当 int 用，两者在正常取值下没区别。
func parseProcArgs(raw []byte) (string, []string) {
	if len(raw) < 4 {
		return "", nil
	}
	argc := int(binary.LittleEndian.Uint32(raw[:4]))
	b := raw[4:]
	i := bytes.IndexByte(b, 0)
	if i < 0 {
		return "", nil
	}
	path := string(b[:i])
	b = b[i+1:]
	// 路径后面补的 NUL 是 4 字节对齐用的，数量不定，全跳掉
	for len(b) > 0 && b[0] == 0 {
		b = b[1:]
	}
	args := make([]string, 0, argc)
	for k := 0; k < argc && len(b) > 0; k++ {
		j := bytes.IndexByte(b, 0)
		if j < 0 {
			break
		}
		args = append(args, string(b[:j]))
		b = b[j+1:]
	}
	return path, args
}
