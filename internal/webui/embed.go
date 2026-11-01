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
	"crypto/sha256"
	"embed"
	_ "embed"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
)

//go:embed index.html
var indexHTML []byte

// pwaAssets 内嵌的 PWA 静态资源：manifest、service worker、图标。
//
// 这些**不**落盘到 data/web，和 index.html 的处理方式刻意不同。
// index.html 落盘是为了允许用户替换成自己的构建产物（见上方说明）；
// manifest / sw.js / 图标则是二进制的配套文件，必须和当前版本的 UI
// 严格对应——从内嵌副本直接服务，就不存在「升级后 sw.js 还是上一版」
// 这种错位，也不会在用户自定义过 UI 的目录里留下一堆孤儿文件。
//
//go:embed pwa
var pwaAssets embed.FS

// pwaFS 指向内嵌的 pwa 子目录，路径形如 "sw.js"、"icons/icon-192.png"。
var pwaFS = mustSub(pwaAssets, "pwa")

func mustSub(f fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		// dir 是编译期常量，走不到这里；真走到了说明 embed 指令被改坏了，
		// 启动就 panic 比悄悄少一个文件好查。
		panic("webui: 内嵌 pwa 目录缺失: " + err.Error())
	}
	return sub
}

// PWAFile 读取内嵌的 PWA 静态资源，并返回内容的 ETag。
//
// ETag 用内容摘要而不是版本号：图标文件名固定不变，只有内容变了才该让
// 浏览器重新下载。用版本号会让每次发版都白下一次 512 图标。
func PWAFile(name string) (data []byte, etag string, err error) {
	b, err := fs.ReadFile(pwaFS, name)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(b)
	return b, `"` + hex.EncodeToString(sum[:8]) + `"`, nil
}

// webBuildMarker 提取页面头部的 build 标记，如 "v0.8.1"。
// 标记形如 <!-- triune-panel-web/<ver> -->，随版本发布更新；
// 允许标记后跟随其他说明文字，因此只锚定前缀不要求闭合箭头紧邻。
var webBuildMarker = regexp.MustCompile(`<!--\s*triune-panel-web/([^\s>]+)`)

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
