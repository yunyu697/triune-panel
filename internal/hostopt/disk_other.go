//go:build !linux && !darwin

package hostopt

import "errors"

// 非 Linux/macOS 平台没有 Statfs，系统概览里磁盘一项降级为「读不到」。
func diskFreeImpl(string) (total, free uint64, err error) {
	return 0, 0, errors.New("当前平台不支持读取磁盘容量")
}
