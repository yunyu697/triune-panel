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
	"bytes"
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

// 前端脚本作为独立文件内嵌，和 PWA 资源同样的理由：它们必须和当前二进制的
// index.html 严格对应。更重要的是——把脚本从 HTML 里挪出来之后，
// CSP 才能收紧到 script-src 'self'，不再需要 'unsafe-inline'
// （内联 <script> 和内联 onclick 只能靠它放行，而那一条同时也放行了注入的脚本）。
//
// boot.js 在 <head> 同步执行，决定首屏画登录页还是主界面（刷新时不再闪登录页）。
//
//go:embed qr.js app.js boot.js
var scriptAssets embed.FS

// AssetFile 读取内嵌的前端脚本，并返回内容的 ETag。
func AssetFile(name string) (data []byte, etag string, err error) {
	b, err := fs.ReadFile(scriptAssets, name)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(b)
	return b, `"` + hex.EncodeToString(sum[:8]) + `"`, nil
}

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

// EmbeddedVersion 返回内嵌页面的 build 标记版本（如 "v1.4.1"）。
// CLI 与测试用它解释「二进制里到底带的是哪一版界面」。
func EmbeddedVersion() string { return embeddedMarker() }

// EmbeddedPage 返回内嵌页面的副本。
// 调用方拿到的是独立切片，改它不会污染后续响应。
func EmbeddedPage() []byte { return append([]byte(nil), indexHTML...) }

// PageMarker 提取任意页面里的 build 标记；没有标记时返回空串。
func PageMarker(page []byte) string {
	if m := webBuildMarker.FindSubmatch(page); len(m) >= 2 {
		return string(m[1])
	}
	return ""
}

// builtinPageHints 是「这确实是我们自己的内置页」的特征串。
//
// 只收 v1.3.0 之后才存在、且自定义 UI 几乎不可能带的形态：脚本外置后
// 页面必定引用 /assets/app.js；工作台落地后才有 cmdbar / workbench。
// **不要**用 id="nav" 或标题之类的通用特征——那会误伤自定义 UI，
// 把它们当成旧内置页顶掉（见包注释里对自定义 UI 的承诺）。
var builtinPageHints = [][]byte{
	[]byte("/assets/app.js"),
	[]byte(`id="cmdbar"`),
	[]byte(`id="workbench"`),
}

// LooksLikeBuiltinPage 判断一份页面是不是本项目生成的内置页。
func LooksLikeBuiltinPage(page []byte) bool {
	for _, h := range builtinPageHints {
		if bytes.Contains(page, h) {
			return true
		}
	}
	return false
}

// ResolvePage 保证 HTTP 实际发出去的页面不会是上一版。
//
// EmbedIfNeeded 在启动时把 data/web/index.html 覆盖成内嵌版，但那一步可能
// 失败（目录只读、属主不是面板运行用户、只读挂载），而 main 里只记一行日志
// 就继续服务磁盘文件——用户看到的就是「明明升级了，界面还是旧的」。
// 这里做第二道防线，不依赖能不能写盘：
//
//   - 有标记且版本不同 → 服务内嵌页；
//   - 没有标记但明显是内置页（引用了 /assets/app.js 等）→ 服务内嵌页。
//     这一条兜的是更早版本落地的无标记页面：只认「有标记」会把它们
//     永久留在旧界面（写盘失败时两道防线同时失效）；
//   - 其余没有标记的页面一律原样返回：那是用户自己替换的自定义 UI，
//     不能因为缺标记就把它换掉，否则自定义界面会被内置页顶掉、CSP 也会变。
func ResolvePage(disk []byte) []byte {
	if len(disk) == 0 {
		return indexHTML
	}
	if m := webBuildMarker.FindSubmatch(disk); len(m) >= 2 {
		if string(m[1]) != embeddedMarker() {
			return indexHTML
		}
		return disk
	}
	if LooksLikeBuiltinPage(disk) {
		return indexHTML
	}
	return disk
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
