// Package webui 提供内置单页管理界面。
// data/web/index.html 优先使用外置文件（可用于自定义/替换为 Vue 构建产物）；
// 不存在时把嵌入版写入该位置，保证二进制开箱即用。
//
// 升级注意：旧版本（v0.8.0 及以前）落地过的外置 index.html 没有 build 标记，
// 二进制升级后若不覆盖，面板会一直服务旧 UI（表现为"升级后界面还是旧的"）。
// 因此嵌入页面头部维护一个 build 标记注释（<!-- triune-panel-web/<ver> -->），
// 启动时标记缺失或与当前版本不一致即覆盖为嵌入版。
// 想长期自定义 UI 的用户：替换文件时请把头部 build 标记注释改成当前版本号。
package webui

import (
	_ "embed"
	"os"
	"path/filepath"
	"regexp"
)

//go:embed index.html
var indexHTML []byte

// webBuildMarker 提取页面头部的 build 标记，如 "v0.8.1"。
// 标记形如 <!-- triune-panel-web/<ver> -->，随版本发布更新。
var webBuildMarker = regexp.MustCompile(`<!--\s*triune-panel-web/([^\s>]+)\s*-->`)

func embeddedMarker() string {
	if m := webBuildMarker.FindSubmatch(indexHTML); len(m) >= 2 {
		return string(m[1])
	}
	return ""
}

// EmbedIfNeeded 确保数据目录下的 UI 文件与当前二进制的内嵌版本一致：
//   - 文件不存在，或缺少 build 标记（旧版本落地/手工放置），或标记版本
//     与当前嵌入版不同 → 写入/覆盖为嵌入版（否则升级后界面停留在旧版）；
//   - 标记与当前版本一致 → 保留（尊重自定义 UI，前提是自定义者保留了标记）。
func EmbedIfNeeded(dataDir string) error {
	dir := filepath.Join(dataDir, "web")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(dir, "index.html")
	if data, err := os.ReadFile(dst); err == nil {
		if m := webBuildMarker.FindSubmatch(data); len(m) >= 2 && string(m[1]) == embeddedMarker() {
			return nil
		}
	}
	return os.WriteFile(dst, indexHTML, 0o644)
}
