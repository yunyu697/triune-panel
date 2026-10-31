// Package webui 提供内置单页管理界面。
// 若 data/web/index.html 存在（可用于自定义/替换为 Vue 构建产物），优先使用外置文件；
// 否则把嵌入版写入该位置，保证二进制开箱即用。
package webui

import (
	_ "embed"
	"os"
	"path/filepath"
)

//go:embed index.html
var indexHTML []byte

// EmbedIfNeeded 确保数据目录下存在可服务的 UI 文件。
func EmbedIfNeeded(dataDir string) error {
	dir := filepath.Join(dataDir, "web")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(dir, "index.html")
	if _, err := os.Stat(dst); err == nil {
		return nil // 已有外置 UI，不覆盖
	}
	return os.WriteFile(dst, indexHTML, 0o644)
}
