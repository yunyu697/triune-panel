//go:build linux

package proc

import (
	"os"
	"strconv"
	"strings"
)

// scanProcs 走 /proc 列进程：exe 是解析后的真实路径，cmdline 是 NUL 分隔的完整参数。
// 两者都不经过 shell、不截断，比调 ps 稳。
func scanProcs() []procEntry {
	d, err := os.Open("/proc")
	if err != nil {
		return nil
	}
	names, err := d.Readdirnames(-1)
	d.Close()
	if err != nil {
		return nil
	}
	out := make([]procEntry, 0, len(names))
	for _, name := range names {
		pid, err := strconv.Atoi(name)
		if err != nil || pid <= 0 {
			continue
		}
		path, err := os.Readlink("/proc/" + name + "/exe")
		if err != nil {
			continue
		}
		// 二进制被替换后内核会在路径后面补 " (deleted)"
		path = strings.TrimSuffix(path, " (deleted)")
		raw, err := os.ReadFile("/proc/" + name + "/cmdline")
		if err != nil {
			continue
		}
		args := splitNul(string(raw))
		out = append(out, procEntry{PID: pid, Path: path, Args: args})
	}
	return out
}

func splitNul(s string) []string {
	s = strings.TrimRight(s, "\x00")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\x00")
}
