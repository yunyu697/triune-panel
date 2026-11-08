//go:build !linux

package hostopt

// compiledReport 是非 Linux 二进制的生产探测入口：只返回兜底报告，
// 绝不读取 /proc，也绝不暴露应用能力（产品约束：默认绝不修改系统）。
//
// 与 detect_linux.go 成对存在，保证 CI 的 darwin/amd64、darwin/arm64 等
// 平台 go build ./... 全过，且这些平台上面板永远只是「只读展示」。
func compiledReport(dataDir string) Report { return unsupportedReport(dataDir) }

// 非 Linux 没有 /proc/sys，也没有 Linux 版 unix.Utsname，内核回退直接放弃。
func kernelReleaseFallback() (string, bool) { return "", false }
