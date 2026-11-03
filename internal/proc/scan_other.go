//go:build !linux && !darwin

package proc

// scanProcs 在不支持的平台上直接返回空：清理孤儿是「能做就做」的兜底，
// 取不到进程列表就当作没有孤儿，绝不能让面板因此起不来。
// 实际部署只有 Linux 与 macOS，这个分支主要是让 GOOS=windows 的交叉编译能过。
func scanProcs() []procEntry { return nil }
