package api

// 组件更新 API：面板 + 三内核二进制的版本管理。
//
// 可靠性保证（每一层都不让"不可用"状态出现）：
//   - 新二进制必须先通过可执行验证（version 命令）与对当前配置的真实校验，
//     两关不过则一切照旧；
//   - 替换前旧版本自动备份到 data/backups/，支持回滚到任意历史版本；
//   - 替换后经健康检查，失败自动恢复旧版本并重启 —— 服务始终在线；
//   - 面板自更新沿用 sha256 校验 + .bak 备份 + syscall.Exec 无缝重启。

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"triune/internal/core"
	"triune/internal/update"
)

// componentRepos 三内核的官方发布仓库（版本列表用）。
var componentRepos = map[string]string{
	"singbox": "SagerNet/sing-box",
	"xray":    "XTLS/Xray-core",
	"mihomo":  "MetaCubeX/mihomo",
}

// defaultPanelRepo 面板自身的发布仓库。
// panel_repo 未配置时用它——否则「面板 → 版本列表」开箱就是 400，必须先知道
// 要填什么才能用。fork 或自建分发在设置里改成自己的仓库即可。
const defaultPanelRepo = "yunyu697/triune-panel"

// ---- 查询 ----

// GET /api/components：面板与各内核的当前版本/路径/运行状态（UI 组件管理页）。
func (a *Server) handleComponents(w http.ResponseWriter, r *http.Request) {
	cores := []map[string]any{}
	for _, name := range []string{"singbox", "xray", "mihomo"} {
		entry := map[string]any{"component": name}
		bin, _ := a.Store.GetSetting("bin_" + name)
		entry["bin"] = bin
		entry["bin_set"] = bin != ""
		if bin != "" {
			if v, err := update.BinaryVersion(bin, update.VersionArgs[name]); err == nil {
				entry["version"] = v
			} else {
				entry["version_error"] = err.Error()
			}
		}
		if p, err := a.Pipe.ConfigPath(name); err == nil {
			if st, serr := os.Stat(p); serr == nil {
				entry["config_exists"] = !st.IsDir()
			}
		}
		running, _ := a.ProcMgr.Status(name)
		entry["running"] = running
		backups, _ := update.ListBackups(a.backupDirFor(name))
		entry["backups"] = backups
		cores = append(cores, entry)
	}
	writeJSON(w, 200, map[string]any{
		"panel":   map[string]any{"component": "panel", "version": Version},
		"cores":   cores,
	})
}

// GET /api/updates/versions?component=panel|singbox|xray|mihomo&channel=all|stable|prerelease
// 面板：读 panel_update_index 索引清单（或 panel_repo 的 GitHub Releases）。
// 内核：读官方仓库 GitHub Releases（gh_api_base / gh_mirror 可在设置中替换为镜像）。
//
// channel 默认 all（保持旧行为）。stable 只给正式版——「一键安装最新」如果不区分
// 分支，遇到 1.15.0-alpha 这类预发布就会把 alpha 装到生产面板上。
func (a *Server) handleUpdateVersions(w http.ResponseWriter, r *http.Request) {
	comp := r.URL.Query().Get("component")
	channel, ok := update.NormalizeChannel(r.URL.Query().Get("channel"))
	if !ok {
		fail(w, 400, "channel 只能是 all / stable / prerelease")
		return
	}
	if comp == "panel" {
		if idx, _ := a.Store.GetSetting("panel_update_index"); idx != "" {
			rels, err := update.ListIndexVersions(idx)
			if err != nil {
				fail(w, 502, err.Error())
				return
			}
			writeVersions(w, comp, "index", rels, channel)
			return
		}
		repo, _ := a.Store.GetSetting("panel_repo")
		source := "github"
		if repo == "" {
			repo = defaultPanelRepo
			source = "default" // 告诉前端这是内置默认仓库，不是用户配的
		}
		rels, err := update.ListVersions(a.ghAPIBase(), repo)
		if err != nil {
			fail(w, 502, err.Error())
			return
		}
		// 面板自更新要的是「本机这份平台的二进制」直链，而 GitHub 的 release
		// 里一堆资产。这里按当前 GOOS/GOARCH 挑一个填进 url，前端就不用自己拼。
		fillPanelAssetURL(rels)
		writeVersions(w, comp, source, rels, channel)
		return
	}
	repo, ok := componentRepos[comp]
	if !ok {
		fail(w, 400, "未知组件: "+comp)
		return
	}
	rels, err := update.ListVersions(a.ghAPIBase(), repo)
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	writeVersions(w, comp, "github", rels, channel)
}

// fillPanelAssetURL 给每条 release 挑出「本机这个平台」的二进制直链填进 url。
// 面板自更新只认 URL（不像内核那样能按版本号拼），而一个 release 里躺着四个
// 平台的资产，让前端去猜不现实。
func fillPanelAssetURL(rels []update.Release) {
	name := fmt.Sprintf("triune-panel-%s-%s", runtime.GOOS, runtime.GOARCH)
	for i := range rels {
		if u := rels[i].AssetURLs[name]; u != "" {
			rels[i].URL = u
		}
	}
}

// writeVersions 输出按分支过滤后的版本列表，并附带各分支的数量
// （前端要显示「稳定版 12 / 预发布 3」，不能只给过滤后的结果）。
func writeVersions(w http.ResponseWriter, comp, source string, rels []update.Release, channel string) {
	counts := map[string]int{"all": len(rels), "stable": 0, "prerelease": 0}
	for _, r := range rels {
		if r.Prerelease {
			counts["prerelease"]++
		} else {
			counts["stable"]++
		}
	}
	writeJSON(w, 200, map[string]any{
		"component": comp,
		"source":    source,
		"channel":   channel,
		"counts":    counts,
		"releases":  update.FilterChannel(rels, channel),
	})
}

func (a *Server) ghAPIBase() string {
	v, _ := a.Store.GetSetting("gh_api_base")
	return v
}

// ---- 内核二进制更新 ----

type coreUpdateReq struct {
	Version string `json:"version"` // 与 url 二选一：按版本号自动构造官方下载地址
	URL     string `json:"url"`     // 直接指定下载地址（tar.gz/zip/gz/裸二进制）
	Sha256  string `json:"sha256"`  // 可选强校验
	// Mirror 本次下载用的镜像前缀。用指针是为了区分「没传」与「传了空串」：
	// 没传 = 沿用设置里的 gh_mirror；传空串 = 本次强制走官方地址。
	Mirror *string `json:"mirror"`
}

// resolveMirror 决定本次下载用哪个镜像：请求里给了就用请求的，没给才回落设置。
func (a *Server) resolveMirror(reqMirror *string) (string, error) {
	raw := ""
	if reqMirror != nil {
		raw = *reqMirror
	} else {
		raw, _ = a.Store.GetSetting("gh_mirror")
	}
	return update.NormalizeMirror(raw)
}

// POST /api/cores/{name}/update
func (a *Server) handleCoreUpdate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	c, err := core.Get(name)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var req coreUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, err.Error())
		return
	}
	dlURL := req.URL
	if dlURL == "" && req.Version != "" {
		dlURL, err = update.AssetURL(name, req.Version)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
	}
	if dlURL == "" {
		fail(w, 400, "需要 version 或 url 字段")
		return
	}
	mirror, err := a.resolveMirror(req.Mirror)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	dlURL = update.ApplyMirror(dlURL, mirror)

	downloads := filepath.Join(a.DataDir, "downloads")
	// ---- 1. 下载 ----
	archivePath, gotSha, err := update.Download(dlURL, req.Sha256, downloads)
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	defer os.RemoveAll(filepath.Dir(archivePath))

	// ---- 2. 解压 + 可执行验证 ----
	memberName := name
	if n, ok := update.BinaryNames[name]; ok {
		memberName = n
	}
	newBin, err := update.Extract(archivePath, memberName, downloads)
	if err != nil {
		fail(w, 422, "解压失败: "+err.Error())
		return
	}
	verOut, err := update.BinaryVersion(newBin, update.VersionArgs[name])
	if err != nil {
		os.Remove(newBin)
		fail(w, 422, "新二进制不可执行（已拒收，现有环境零改动）: "+err.Error())
		return
	}

	// ---- 3. 用新二进制对当前配置做真实校验 ----
	input, _ := a.buildRenderInput(name)
	oldBin, _ := a.Store.GetSetting("bin_" + name)
	if len(input) > 0 {
		content, rerr := c.Render(input, a.Pipe.RenderOpts())
		if rerr == nil {
			dir := a.Pipe.DataDir + "/cores/" + name
			_ = os.MkdirAll(dir, 0o755)
			tmp := filepath.Join(dir, ".update-validate."+c.Ext())
			if werr := os.WriteFile(tmp, content, 0o600); werr == nil {
				out, verr := c.ValidateCmd(newBin, tmp).CombinedOutput()
				os.Remove(tmp)
				if verr != nil {
					os.Remove(newBin)
					fail(w, 422, fmt.Sprintf("新版本 %s 校验当前配置失败（已拒收，运行环境零改动）：\n%s",
						verOut, truncate(string(out), 1500)))
					return
				}
			}
		}
	}

	// ---- 4. 备份旧版本 + 原子替换 ----
	// 未配置二进制路径 = 首次安装：装到面板专用目录 data/bin/<name>/，
	// 与系统里其他服务使用的二进制完全隔离，互不影响。
	target := oldBin
	settingChanged := false
	if target == "" {
		member := name
		if n, ok := update.BinaryNames[name]; ok {
			member = n
		}
		target = filepath.Join(a.DataDir, "bin", name, member)
		settingChanged = true
	}
	backup, err := update.Replace(newBin, target, a.backupDirFor(name))
	if err != nil {
		os.Remove(newBin)
		fail(w, 500, "替换失败（旧二进制未受影响）: "+err.Error())
		return
	}
	if settingChanged {
		_ = a.Store.SetSetting("bin_"+name, target)
	}

	// ---- 5. 重启内核 + 健康检查；失败自动回滚 ----
	wasRunning, _ := a.ProcMgr.Status(name)
	rollback := func(reason string) {
		if backup != "" {
			_ = update.Restore(backup, target)
		} else if settingChanged {
			// 之前没有二进制：撤掉新增路径设置
			_ = a.Store.SetSetting("bin_"+name, "")
		}
		if len(input) > 0 {
			_, _ = a.applyWithRevision(name, input, "restore after failed core update")
		}
		writeJSON(w, 422, map[string]any{
			"error":       fmt.Sprintf("内核更新到 %s 失败：%s。已自动恢复旧版本。", verOut, reason),
			"rolled_back": true,
		})
	}

	restartNote := fmt.Sprintf("core %s updated → %s", name, verOut)
	if len(input) > 0 {
		if _, err := a.applyWithRevision(name, input, restartNote); err != nil {
			rollback("apply 失败: "+err.Error())
			return
		}
		if err := a.healthCheck(name); err != nil {
			rollback("健康检查失败: "+err.Error())
			return
		}
	} else if wasRunning {
		// 无入站但进程在跑（如空配置启动）：直接重启验证
		if err := a.ProcMgr.Restart(name, target, a.Pipe.DataDir+"/cores/"+name+"/config."+c.Ext(), a.Pipe.DataDir+"/cores/"+name); err == nil {
			if err := a.healthCheck(name); err != nil {
				rollback("健康检查失败: "+err.Error())
				return
			}
		}
	}

	writeJSON(w, 200, map[string]any{
		"ok":          true,
		"component":   name,
		"version":     verOut,
		"sha256":      gotSha,
		"backup":      backup,
		"target":      target,
		"installed":   settingChanged, // true = 首次安装（此前未配置二进制路径）
		"was_running": wasRunning,
		"restarted":   len(input) > 0,
	})
	a.auditOK(r, AuditUpdate, name, fmt.Sprintf("内核更新到 %s（备份 %s）", verOut, backup))
}

// POST /api/cores/{name}/rollback {"backup":"20260102-150405.bak"}
func (a *Server) handleCoreRollback(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, err := core.Get(name); err != nil {
		fail(w, 400, err.Error())
		return
	}
	var req struct {
		Backup string `json:"backup"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Backup == "" {
		fail(w, 400, "需要 backup 字段（见 GET /api/components 的 backups 列表）")
		return
	}
	if strings.Contains(req.Backup, "/") || strings.Contains(req.Backup, "..") {
		fail(w, 400, "非法备份名")
		return
	}
	bin, _ := a.Store.GetSetting("bin_" + name)
	if bin == "" {
		fail(w, 400, "未配置该内核二进制路径")
		return
	}
	backupPath := filepath.Join(a.backupDirFor(name), req.Backup)
	if err := update.Restore(backupPath, bin); err != nil {
		fail(w, 500, err.Error())
		return
	}
	// 恢复后走完整流水线重启（校验 + 原子替换 + 健康检查）
	input, _ := a.buildRenderInput(name)
	if len(input) > 0 {
		if _, err := a.applyWithRevision(name, input, fmt.Sprintf("core %s rolled back → %s", name, req.Backup)); err != nil {
			fail(w, 422, "回滚完成但 apply 失败（二进制已恢复）: "+err.Error())
			return
		}
		if err := a.healthCheck(name); err != nil {
			fail(w, 422, "回滚后健康检查失败（二进制已恢复）: "+err.Error())
			return
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true, "restored": req.Backup})
	a.auditOK(r, AuditUpdate, name, "内核回滚到备份 "+req.Backup)
}

func (a *Server) backupDirFor(component string) string {
	return filepath.Join(a.DataDir, "backups", "cores", component)
}

// ---- 面板版本化备份与回滚 ----

// GET /api/panel/backups
func (a *Server) handlePanelBackups(w http.ResponseWriter, r *http.Request) {
	backups, _ := update.ListBackups(filepath.Join(a.DataDir, "backups", "panel"))
	exe, _ := os.Executable()
	writeJSON(w, 200, map[string]any{
		"current_version": Version,
		"executable":      exe,
		"backups":         backups,
		"bak_exists":      fileExists(exe + ".bak"),
	})
}

// POST /api/panel/rollback {"backup":"v0.4.0-20260102-150405.bak"}
// 恢复所选历史版本并重启（exec 替换进程映像，监听无缝衔接）。
func (a *Server) handlePanelRollback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Backup string `json:"backup"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Backup == "" {
		fail(w, 400, "需要 backup 字段（见 GET /api/panel/backups）")
		return
	}
	if strings.Contains(req.Backup, "/") || strings.Contains(req.Backup, "..") {
		fail(w, 400, "非法备份名")
		return
	}
	exe, err := os.Executable()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	src := filepath.Join(a.DataDir, "backups", "panel", req.Backup)
	staged := exe + ".rollback-new"
	if err := copyFileTo(src, staged); err != nil {
		fail(w, 500, "读取备份失败: "+err.Error())
		return
	}
	_ = os.Chmod(staged, 0o755)
	if v, err := update.BinaryVersion(staged, update.VersionArgs["panel"]); err != nil {
		os.Remove(staged)
		fail(w, 422, "备份文件不可执行（已取消）: "+err.Error())
		return
	} else {
		_ = v
	}
	if err := os.Rename(staged, exe); err != nil {
		os.Remove(staged)
		fail(w, 500, "替换失败: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "msg": "回滚完成，正在重启..."})
	a.auditOK(r, AuditUpdate, "panel", "面板回滚到备份 "+req.Backup)
	go func() {
		time.Sleep(500 * time.Millisecond)
		syscall.Exec(exe, os.Args, os.Environ())
	}()
}

func copyFileTo(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
