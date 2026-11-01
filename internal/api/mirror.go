package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"triune/internal/update"
)

// 下载镜像：给「一键安装 / 更新」弹窗提供候选列表与测速。
//
// 为什么要测速而不是写死一个推荐：这些加速站的实际速度完全取决于你所在地区
// 到它的链路，同一天不同人测出来的第一名经常不一样。写死一个「推荐镜像」
// 等于把某个地区的偶然结果当成普遍事实。

const mirrorProbeTimeout = 6 * time.Second

// GET /api/mirrors —— 候选镜像（含「官方地址」）。
func (a *Server) handleMirrors(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"mirrors":    update.MirrorCandidates(),
		"default":    update.OfficialMirror.Base, // 空串 = 官方地址
		"timeout_ms": mirrorProbeTimeout.Milliseconds(),
	})
}

// POST /api/mirrors/test —— 并发测速，按可用 + 延迟排序返回。
// 需要写权限：这个接口会让面板对外发十几个请求，不该让只读账号随便触发。
//
// 请求体三种写法，优先级从高到低：
//
//	{"url": "https://github.com/..."}          直接给完整地址（面板自更新用这个）
//	{"component":"singbox","version":"v1.14.4"} 由服务端按官方规则拼地址
//
// 必须给具体地址而不是固定探针：加速站只转发 release / raw 路径，拿别的东西
// 去测会得到一片 403（见 update/mirror.go 的 probeNote）。
func (a *Server) handleMirrorTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL       string `json:"url"`
		Component string `json:"component"`
		Version   string `json:"version"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	target, err := resolveProbeTarget(req.URL, req.Component, req.Version)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), mirrorProbeTimeout+2*time.Second)
	defer cancel()
	results := update.ProbeMirrors(ctx, target, mirrorProbeTimeout)
	best := ""
	for _, res := range results {
		if res.OK {
			best = res.Base
			break
		}
	}
	writeJSON(w, 200, map[string]any{
		"target":   target,
		"results":  results,
		"best":     best, // 空串有两种含义：官方最快，或者全挂了——看 results 判断
		"ok_count": countOK(results),
	})
}

// resolveProbeTarget 决定测速打哪个地址。
// 只允许 github.com：这个接口会把 URL 交给十几个第三方站点去请求，
// 放开任意地址等于把面板变成「谁都能用的探活器」。
func resolveProbeTarget(rawURL, component, version string) (string, error) {
	if u := strings.TrimSpace(rawURL); u != "" {
		if !strings.HasPrefix(u, "https://github.com/") {
			return "", fmt.Errorf("只能测速 github.com 的下载地址，收到: %s", u)
		}
		return u, nil
	}
	if component == "" || version == "" {
		return "", fmt.Errorf("需要 url，或同时给 component 与 version")
	}
	if component == "panel" {
		return "", fmt.Errorf("面板版本请直接传 url（资产名随平台不同，服务端拼不出来）")
	}
	u, err := update.AssetURL(component, version)
	if err != nil {
		return "", err
	}
	return u, nil
}

func countOK(rs []update.ProbeResult) int {
	n := 0
	for _, r := range rs {
		if r.OK {
			n++
		}
	}
	return n
}
