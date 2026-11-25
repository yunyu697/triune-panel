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
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"triune/internal/core"
	"triune/internal/model"
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

// SettingGHToken 面板访问 GitHub 用的 Token（空 = 匿名）。
//
// 匿名额度是 60 次/小时且**按出口 IP** 计：同一 NAT 后面的所有设备、机器上
// 跑着的其它 GitHub 工具共用这一份，用光之后面板与内核的版本列表会一起失效，
// 报错却只有一句 HTTP 403。Token 是 5000 次/小时、按账号计。
//
// 与 DNS 凭据同级对待：只落库、只回占位符，不回显明文。
const SettingGHToken = "gh_token"

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
		// Pipe / ProcMgr 判空：正常运行时一定装了，但这个接口是设置页一进来就调的，
		// 一旦哪天在没装管道的环境里被调到，panic 会把整个请求打断（页面整块空白），
		// 而这里少给一个字段只是少显示一个状态而已。
		if a.Pipe != nil {
			if p, err := a.Pipe.ConfigPath(name); err == nil {
				if st, serr := os.Stat(p); serr == nil {
					entry["config_exists"] = !st.IsDir()
				}
			}
		}
		if a.ProcMgr != nil {
			running, _ := a.ProcMgr.Status(name)
			entry["running"] = running
		}
		backups, _ := update.ListBackups(a.backupDirFor(name))
		entry["backups"] = backups
		cores = append(cores, entry)
	}
	writeJSON(w, 200, map[string]any{
		// 面板自己的可执行文件路径：组件表有一列「路径」，面板那行不能空着。
		// 取不到就留空，前端显示「—」，不为一行路径让整个接口失败。
		"panel": map[string]any{"component": "panel", "version": Version, "bin": panelExe()},
		"cores": cores,
	})
}

// panelExe 返回面板自身的可执行文件路径；取不到返回空串。
func panelExe() string {
	exe, err := selfExe()
	if err != nil {
		return ""
	}
	return exe
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
		rels, via, err := a.listVersions(repo)
		if err != nil {
			fail(w, 502, err.Error())
			return
		}
		if via == "atom" {
			source = "atom"
		}
		// 面板自更新要的是「本机这份平台的二进制」直链，而 GitHub 的 release
		// 里一堆资产。这里按当前 GOOS/GOARCH 挑一个填进 url，前端就不用自己拼。
		fillPanelAssetURL(rels, repo, via)
		writeVersions(w, comp, source, rels, channel)
		return
	}
	repo, ok := componentRepos[comp]
	if !ok {
		fail(w, 400, "未知组件: "+comp)
		return
	}
	rels, via, err := a.listVersions(repo)
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	markMihomoIncompat(comp, rels)
	writeVersions(w, comp, via, rels, channel)
}

// listVersions 拉版本列表，配额耗尽时自动改走不计配额的 releases 订阅。
//
// 返回的第二个值是**实际用到的通道**（github / atom），前端据此提示「这份列表
// 没有校验值」。两条都失败时返回配额那条错误并附上订阅失败的原因：如果只说
// 「订阅失败」，用户会以为换个通道就能好。
func (a *Server) listVersions(repo string) ([]update.Release, string, error) {
	rels, err := update.ListVersionsToken(a.ghAPIBase(), repo, a.ghToken())
	if err == nil {
		return rels, "github", nil
	}
	if !update.IsRateLimited(err) {
		return nil, "", err
	}
	atom, aerr := update.ListVersionsAtom("", repo)
	if aerr != nil {
		return nil, "", fmt.Errorf("%w；releases 订阅通道也不可用：%v", err, aerr)
	}
	log.Printf("[update] %s 的接口配额已用完，版本列表改读 releases 订阅", repo)
	return atom, "atom", nil
}

// ghToken 面板访问 GitHub 用的 Token（可空）。
// 空 = 匿名 60 次/小时；填了 = 5000 次/小时。只从设置读，不回显明文。
func (a *Server) ghToken() string {
	v, _ := a.Store.GetSetting(SettingGHToken)
	return strings.TrimSpace(v)
}

// markMihomoIncompat 给版本列表打上「装上之后 mihomo 客户端连不上」的标记。
//
// 判据与诊断页的「客户端兼容性」是同一条版本线（xrayTooNewForOldRealityClients），
// 两处必须一起改——一边说 26.7.11 之后不行、另一边还推荐升级，等于换了一个
// 地方骗人。
//
// 标记打在**版本**上而不是「当前配置有没有 REALITY」上：用户是在选版本的
// 那一刻决定要不要升的，等装完再去诊断页看结论，故障已经发生了。详情由前端
// 在选中该版本时展开（它知道用户选的是哪一条）。
func markMihomoIncompat(component string, rels []update.Release) {
	if component != model.CoreXray {
		return // sing-box / mihomo 没有 minClientVer 这套机制
	}
	for i := range rels {
		rels[i].MihomoIncompatible = xrayTooNewForOldRealityClients(rels[i].Tag)
	}
}

// fillPanelAssetURL 给每条 release 挑出「本机这个平台」的二进制直链填进 url。
// 面板自更新只认 URL（不像内核那样能按版本号拼），而一个 release 里躺着四个
// 平台的资产，让前端去猜不现实。
//
// via=atom 时 API 没给资产清单，只能按命名规律拼：面板资产的四个名字是我们
// 自己在 release 脚本里定的（triune-panel-<os>-<arch>），所以拼得出来。这也是
// 「哪些需求能不靠配额满足」的分界线——名字由我方决定的可派生，第三方内核的
// 资产名不可派生（它们的命名随版本/构建方式变）。
func fillPanelAssetURL(rels []update.Release, repo, via string) {
	name := fmt.Sprintf("triune-panel-%s-%s", runtime.GOOS, runtime.GOARCH)
	for i := range rels {
		if u := rels[i].AssetURLs[name]; u != "" {
			rels[i].URL = u
			continue
		}
		if via == "atom" && rels[i].Tag != "" {
			rels[i].URL = fmt.Sprintf("https://github.com/%s/releases/download/%s/%s",
				repo, url.PathEscape(rels[i].Tag), name)
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
	// browserDLURL 是**未经镜像改写**的官方下载地址。只有按 version 构造的
	// URL 才对应 GitHub release 里的资产；用户直接给的自定义 URL 没有对应
	// release，也就没有 API 直连兜底。
	browserDLURL := ""
	if dlURL == "" && req.Version != "" {
		dlURL, err = update.AssetURL(name, req.Version)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		browserDLURL = dlURL
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
	primaryURL := update.ApplyMirror(dlURL, mirror) // 镜像前缀只拼在主地址上
	// 惰性解析兜底地址：正常下载不额外拉一次 releases API（见 resolveCoreAssetAPIURL）。
	resolveFallback := func() string {
		return a.resolveCoreAssetAPIURL(name, req.Version, browserDLURL)
	}

	downloads := filepath.Join(a.DataDir, "downloads")
	// ---- 1. 下载 ----
	archivePath, gotSha, usedFallback, err := update.DownloadWithFallback(primaryURL, resolveFallback, req.Sha256, downloads)
	if usedFallback {
		log.Printf("[update] %s 主地址下载失败，已改走 GitHub API asset 直连通道", name)
	}
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
		content, rerr := c.Render(input, a.Pipe.RenderOptsFor(c.Name()))
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
			rollback("apply 失败: " + err.Error())
			return
		}
		if err := a.healthCheck(name); err != nil {
			rollback("健康检查失败: " + err.Error())
			return
		}
	} else if wasRunning {
		// 无入站但进程在跑（如空配置启动）：直接重启验证
		if err := a.ProcMgr.Restart(name, target, a.Pipe.DataDir+"/cores/"+name+"/config."+c.Ext(), a.Pipe.DataDir+"/cores/"+name); err == nil {
			if err := a.healthCheck(name); err != nil {
				rollback("健康检查失败: " + err.Error())
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

// resolveCoreAssetAPIURL 惰性解析内核资产的 GitHub API asset 直连兜底地址。
// 只在主下载地址失败时由 DownloadWithFallback 调用，避免给每次正常更新平白
// 多一次 releases API 往返（尤其 sing-box 的 releases JSON 可达数 MB）。
//
// browserDLURL 是未经镜像改写的官方下载地址：用它在该 release 的 AssetURLs 里
// 反查资产名，再取同名资产的 AssetAPIURLs。找不到 release / 资产就返回空串
// （不做兜底，保持既有行为）；本方法失败也静默——要报的是主地址原来的错。
func (a *Server) resolveCoreAssetAPIURL(component, version, browserDLURL string) string {
	repo, ok := componentRepos[component]
	if !ok || version == "" || browserDLURL == "" {
		return ""
	}
	rels, err := update.ListVersions(a.ghAPIBase(), repo)
	if err != nil {
		return ""
	}
	for _, r := range rels {
		if !update.SameVersion(r.Tag, version) {
			continue
		}
		for name, u := range r.AssetURLs {
			if u == browserDLURL {
				return r.AssetAPIURLs[name]
			}
		}
		return "" // 找到 release 但没有与主地址对应的资产
	}
	return ""
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
	exe, _ := selfExe()
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
	exe, err := selfExe()
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
	targetVer, verr := update.BinaryVersion(staged, update.VersionArgs["panel"])
	if verr != nil {
		os.Remove(staged)
		fail(w, 422, "备份文件不可执行（已取消）: "+verr.Error())
		return
	}
	if err := os.Rename(staged, exe); err != nil {
		os.Remove(staged)
		fail(w, 500, "替换失败: "+err.Error())
		return
	}
	// 回滚和自更新一样是 exec 换进程，内存里的会话表照样清空。签发口径必须
	// 与 handleSelfUpdate 一致，否则「回滚完掉回登录页」就是必然——这正是
	// 只给一条路开窄口子时会漏掉的那一半。
	resumeTok, rerr := a.issueUpdateResume(a.currentUser(r))
	if rerr != nil {
		log.Printf("[rollback] 签发接力凭证失败（回滚照常进行，重启后需重新登录）: %v", rerr)
	}
	out := map[string]any{
		"ok": true, "msg": "回滚完成，正在重启...",
		"new_version": targetVer,   // 前端据此判定「新进程回来了」
	}
	if resumeTok != "" {
		out["resume_token"] = resumeTok
	}
	writeJSON(w, 200, out)
	a.auditOK(r, AuditUpdate, "panel", "面板回滚到备份 "+req.Backup)
	// 与自更新共用同一套重启逻辑：exec 失败必须留下痕迹，不能静默继续跑旧映像
	go a.restartSelf(exe, "回滚到 "+strings.TrimSuffix(req.Backup, ".bak"))
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
