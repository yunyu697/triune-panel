// Package webui 提供内置单页管理界面。
//
// 页面只有一个来源：**二进制内嵌的 index.html**。data/web/index.html 不参与
// 渲染，除非它显式声明为自定义界面（见 CustomUIMarker）。
//
// 为什么把这件事做绝：v1.5.0 之前页面是「磁盘优先」的，升级链路上叠了四层
// 判断——启动落盘、build 标记比对、启动覆盖、响应层回退。每层都在给上一层
// 兜底，而任意一层失效，用户就会看到「明明升级了、界面还是旧的」，且升级
// 多少次都不变（旧页面始终躺在磁盘上，判据又恰好认不出它）。
//
// 把页面钉死在二进制里之后，这个失效模式在结构上不存在：换了二进制就是换了
// 界面，没有中间状态。代价是自定义 UI 需要显式声明，这是有意的取舍——
// 隐式约定换来的是一类查不出来的故障，不值得。
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
// 这些**不**落盘，和下面页面「唯一来源是二进制」是同一个道理：manifest /
// sw.js / 图标必须和当前版本的 UI 严格对应，从内嵌副本直接服务就不存在
// 「升级后 sw.js 还是上一版」这种错位，也不会在数据目录里留下孤儿文件。
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

// webBuildMarker 提取页面头部的 build 标记，如 "v1.5.0"。
// 标记形如 <!-- triune-panel-web/<ver> -->，随版本发布更新。
//
// 它现在只用于「说明二进制里带的是哪一版界面」（tp ui 的输出、
// release.sh 的版本一致性校验），**不再参与渲染决策**——渲染只认内嵌页。
var webBuildMarker = regexp.MustCompile(`<!--\s*triune-panel-web/([^\s>]+)`)

func embeddedMarker() string {
	if m := webBuildMarker.FindSubmatch(indexHTML); len(m) >= 2 {
		return string(m[1])
	}
	return ""
}

// EmbeddedVersion 返回内嵌页面的 build 标记版本（如 "v1.5.0"）。
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

// CustomUIMarker 是「这份页面是我自己做的，请用它」的显式声明。
//
// 放在页面里（HTML 注释即可）就把 data/web/index.html 变成自定义界面：
// 面板原样服务它，并放宽 CSP 以允许内联脚本与内联事件属性。
//
// 用显式声明而不是「文件存在就算」或「没有标记就算」，是因为后两者都会让
// 一个历史遗留文件悄悄接管界面，而那正是「升级后界面没变」的根源。
const CustomUIMarker = "triune-panel-web/custom"

// IsCustomUI 判断页面是否被显式声明为自定义 UI。
func IsCustomUI(page []byte) bool { return bytes.Contains(page, []byte(CustomUIMarker)) }

// ServedPage 返回面板实际要发出的页面，以及它是不是自定义界面。
//
// 这是**唯一**的页面来源判定，API 层与 tp ui 都调它，保证「命令说什么、
// 服务端就做什么」。两个分支之外没有第三种可能，所以不存在「旧页面残留
// 导致界面不更新」这种情况。
func ServedPage(dataDir string) (page []byte, custom bool) {
	if disk, err := os.ReadFile(filepath.Join(dataDir, "web", "index.html")); err == nil {
		if IsCustomUI(disk) {
			return disk, true
		}
	}
	// 内嵌页是编译期常量，只读；调用方不得原地修改。
	return indexHTML, false
}
