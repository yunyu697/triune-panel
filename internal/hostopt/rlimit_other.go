//go:build !linux && !darwin

package hostopt

import "errors"

// 非 unix 平台没有 RLIMIT_NOFILE 这个可调项：返回明确的「不支持」，
// 让体检页显示原因，而不是给一个点了会静默失败的按钮。
func getNofileImpl() (cur, max uint64, ok bool) { return 0, 0, false }

func setNofileImpl(cur, max uint64) error {
	return errors.New("当前平台不支持调整文件描述符上限")
}
