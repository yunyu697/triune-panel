//go:build linux || darwin

package backup

import "syscall"

// diskUsage 取挂载点剩余/总空间。数据目录不存在时（例如还没初始化）退到
// 已扫到的备份根目录，都取不到就返回 0——前端会把 0 显示成「未知」，
// 不能因为拿不到磁盘信息就让整个盘点失败。
func diskUsage(dataDir string, roots []string) (free, total int64) {
	for _, dir := range append([]string{dataDir}, roots...) {
		if dir == "" {
			continue
		}
		var st syscall.Statfs_t
		if err := syscall.Statfs(dir, &st); err != nil {
			continue
		}
		return int64(st.Bavail) * int64(st.Bsize), int64(st.Blocks) * int64(st.Bsize)
	}
	return 0, 0
}
