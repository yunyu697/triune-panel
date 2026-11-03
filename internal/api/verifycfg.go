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
//
// dir 是本函数选定的可写工作目录：mihomo 在解析配置前会先初始化 home
// （缺省 $HOME/.config/mihomo），rootfs 只读时会在这一步失败，报出来的
// 却是「配置文件有问题」。显式 -d 把 home 钉在临时目录里，校验结果才只
// 反映配置本身。sing-box / xray 同样把 HOME 指过去：它们不写 home 是常态，
// 但真写的时候不该因为 /root 只读而误判。
func verifyCmd(core, bin, file, dir string) *exec.Cmd {
	var cmd *exec.Cmd
	switch core {
	case "singbox":
		cmd = exec.Command(bin, "check", "-c", file)
	case "mihomo":
		cmd = exec.Command(bin, "-t", "-d", dir, "-f", file)
	case "xray":
		cmd = exec.Command(bin, "-test", "-config", file)
	}
	if cmd != nil {
		cmd.Env = envWithHome(dir)
	}
	return cmd
}

// envWithHome 覆盖 HOME / XDG_CACHE_HOME 到可写目录。
// 保留其余环境（路径、代理变量对内核自检仍有意义）。
func envWithHome(dir string) []string {
	drop := map[string]bool{"HOME": true, "XDG_CACHE_HOME": true, "XDG_CONFIG_HOME": true}
	out := make([]string, 0, len(os.Environ())+3)
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i > 0 && drop[kv[:i]] {
			continue
		}
		out = append(out, kv)
	}
	return append(out,
		"HOME="+dir,
		"XDG_CACHE_HOME="+filepath.Join(dir, ".cache"),
		"XDG_CONFIG_HOME="+filepath.Join(dir, ".config"),
	)
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

	// 配置文件写在 cores/<内核> 下而不是系统临时目录：mihomo 的 home 就是
	// 这个目录，缺 GeoSite.dat / MMDB 时会联网下载并缓存到这里。
	// 用一次性临时目录的话每次校验都要重下一遍（实测 9 秒，且断网即失败）。
	dir := filepath.Join(a.Pipe.DataDir, "cores", q.Core)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail(w, 500, err.Error())
		return
	}
	f, err := os.CreateTemp(dir, ".verify-*"+verifyExt(q.Core))
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	file := f.Name()
	defer os.Remove(file)
	_, werr := f.WriteString(q.Content)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		if werr == nil {
			werr = cerr
		}
		fail(w, 500, werr.Error())
		return
	}

	// 30s：首次校验若缺 GeoSite.dat / MMDB，内核会当场下载（实测约 9s）。
	// 给 10s 会把「网慢」误报成「配置有问题」。
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	cmd := verifyCmd(q.Core, bin, file, dir)
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
		resp["output"] = "校验超时（30s），内核可能卡在加载阶段或正在下载 geo 数据。"
	}
	// 失败时区分「环境问题」与「配置问题」：把只读文件系统/权限报成
	// 「内核拒绝这份配置」，会让人去改一份本来没问题的配置。
	if err != nil {
		if h := envHint(text); h != "" {
			resp["hint"] = h
		}
	}
	writeJSON(w, 200, resp)
}

// envHint 识别「内核没跑起来」类错误，返回一句人话。
func envHint(text string) string {
	low := strings.ToLower(text)
	switch {
	case strings.Contains(low, "read-only file system"):
		return "这是执行环境问题，不是配置内容错误：内核要写的工作目录在只读文件系统上。" +
			"面板已把内核 home 指到数据目录下的 cores/<内核>；若仍报错，说明数据目录本身是只读挂载，" +
			"请把 --data 指到可写位置（如 /opt/triune-panel/data）。"
	case strings.Contains(low, "permission denied"):
		return "这是权限问题，不是配置内容错误：内核无法写入自己的工作目录。" +
			"检查数据目录 cores/<内核> 的属主与权限，或面板是否以无写权限的用户运行。"
	case strings.Contains(low, "no space left on device"):
		return "磁盘已满，内核无法创建缓存文件。清出空间后重试。"
	}
	return ""
}
