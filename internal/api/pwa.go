package api

import (
	"bytes"
	"net/http"
	"path"
	"time"

	"triune/internal/webui"
)

// PWA 静态资源（manifest / service worker / 图标）。
//
// 为什么这些必须挂在站点根路径上，而不是收进 /static/ 之类的子目录：
//
//   - service worker 的作用域由**它自己的 URL** 决定。/static/sw.js 最多
//     只能管到 /static/ 下的请求，管不到面板页面本身，等于白装。
//   - manifest 的 scope 缺省是相对于自身路径的，同理放子目录会把「已安装
//     的 App」限制在子目录里，主界面打不开。
//
// 免认证：这几个文件里没有任何凭据，而且浏览器一进页面就要拉 manifest，
// 放在登录墙后面会导致未登录时安装提示直接消失。

// pwaContentType 按扩展名给 PWA 资源定 Content-Type。
//
// manifest 的规范类型是 application/manifest+json。写成 application/json
// 浏览器也认，但 Chrome 对清单的类型校验越来越严，按规范给更稳。
func pwaContentType(name string) string {
	switch path.Ext(name) {
	case ".webmanifest":
		return "application/manifest+json"
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".png":
		return "image/png"
	case ".svg":
		return "image/svg+xml"
	}
	return "application/octet-stream"
}

// handlePWA 从内嵌副本提供 PWA 资源。name 为空表示走 /icons/{file} 路由，
// 实际文件名从路径参数取。
func (a *Server) handlePWA(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 注意用局部变量：name 是闭包捕获的，同一个 handler 实例服务所有
		// /icons/{file} 请求，就地改写它会让后续请求全部返回第一次那个文件。
		file := name
		if file == "" {
			file = "icons/" + r.PathValue("file")
		}
		data, etag, err := webui.PWAFile(file)
		if err != nil {
			// 找不到就是 404，别让它落进「/」的兜底里返回一个 HTML 页面——
			// 浏览器拿到 Content-Type: text/html 的图标会静默失败，很难查。
			http.NotFound(w, r)
			return
		}
		h := w.Header()
		h.Set("Content-Type", pwaContentType(file))
		h.Set("ETag", etag)
		// 文件内容跟着二进制走，但 URL 不变，所以每次都让浏览器拿 ETag 来问
		// 一句：没变就 304，省下整个文件；变了立刻拿到新的，不会被缓存卡住。
		h.Set("Cache-Control", "no-cache")
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		// ServeContent 处理 Range 与 If-Range；modtime 给零值即不下发
		// Last-Modified，避免和 ETag 两套校验器打架。
		http.ServeContent(w, r, file, time.Time{}, bytes.NewReader(data))
	}
}
