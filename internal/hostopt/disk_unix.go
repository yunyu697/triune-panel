//go:build linux || darwin

package hostopt

import "golang.org/x/sys/unix"

// diskFreeImpl 返回 path 所在文件系统的总容量与可用容量（字节）。
// 只做 Statfs，不创建任何文件；dataDir 不存在时返回错误，由调用方降级成 Warn。
func diskFreeImpl(path string) (total, free uint64, err error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize)
	return uint64(st.Blocks) * bs, uint64(st.Bavail) * bs, nil
}
