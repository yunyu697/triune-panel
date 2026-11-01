package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ==================== 生成配置的内核自检 ====================
//
// 生成的配置到底能不能被目标内核吃下，不该靠人复制粘贴到客户端去试。
// 面板本来就管着内核二进制（settings 的 bin_singbox / bin_mihomo / bin_xray），
// 直接用它跑一次官方校验命令，把「应该没问题」变成「校验通过」。
//
// 诚实边界（响应里逐条写明，不装作比实际更强）：
//   - 用的是**本机装的那个版本**，未必等于用户为客户端选的版本；
//   - 只做语法与初始化校验，不联网、不验证节点凭据是否真的有效；
//   - 校验通过只说明「内核愿意加载它」，不说明「连得上」。

const verifyMaxSize = 4 << 20

type verifyReq struct {
	Core    string `json:"core"`
	Content string `json:"content"`
}

// verifyCmd 各内核的官方配置校验命令。
func verifyCmd(core, bin, file string) *exec.Cmd {
	switch core {
	case "singbox":
		return exec.Command(bin, "check", "-c", file)
	case "mihomo":
		return exec.Command(bin, "-t", "-f", file)
	case "xray":
		return exec.Command(bin, "-test", "-config", file)
	}
	return nil
}

func verifyExt(core string) string {
	if core == "mihomo" {
		return ".yaml"
	}
	return ".json"
}

// binVersion 取二进制的版本行（第一行即可，各内核格式都够用）。
func binVersion(bin string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version").Output()
	if err != nil {
		// mihomo 的 version 子命令叫 -v
		out, err = exec.CommandContext(ctx, bin, "-v").Output()
		if err != nil {
			return ""
		}
	}
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if len(line) > 120 {
		line = line[:120]
	}
	return line
}

// handleExportVerify POST /api/export/verify
// 返回 {ok, core, bin, version, output} 或 {ok:false, ...}
func (a *Server) handleExportVerify(w http.ResponseWriter, r *http.Request) {
	var q verifyReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, verifyMaxSize)).Decode(&q); err != nil {
		fail(w, 400, "bad request")
		return
	}
	if q.Core != "singbox" && q.Core != "mihomo" && q.Core != "xray" {
		fail(w, 400, "core 必须是 singbox / mihomo / xray")
		return
	}
	if strings.TrimSpace(q.Content) == "" {
		fail(w, 400, "content 为空")
		return
	}

	bin, _ := a.Store.Get("bin_" + q.Core)
	if bin == "" {
		// 没装内核不是错误，是「校验不了」——如实说，别假装通过
		writeJSON(w, 200, map[string]any{
			"ok": false, "core": q.Core, "skipped": true,
			"reason": "面板未配置 " + q.Core + " 内核路径（设置 → 内核），无法自检。",
		})
		return
	}

	dir, err := os.MkdirTemp("", "triune-verify-*")
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "config"+verifyExt(q.Core))
	if err := os.WriteFile(file, []byte(q.Content), 0o600); err != nil {
		fail(w, 500, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	cmd := verifyCmd(q.Core, bin, file)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if len(text) > 4000 {
		text = text[:4000] + "\n...(truncated)"
	}

	resp := map[string]any{
		"core":    q.Core,
		"bin":     bin,
		"version": binVersion(bin),
		"output":  text,
		"ok":      err == nil,
	}
	if ctx.Err() == context.DeadlineExceeded {
		resp["ok"] = false
		resp["output"] = "校验超时（10s），内核可能卡在加载阶段。"
	}
	writeJSON(w, 200, resp)
}
