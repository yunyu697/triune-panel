//go:build !linux && !darwin

package backup

// 其它平台没有 syscall.Statfs 的同一套字段；容量治理只做「占用」不做
// 「剩余」降级，保证交叉编译不会因为磁盘统计而挂掉。
func diskUsage(_ string, _ []string) (free, total int64) { return 0, 0 }
