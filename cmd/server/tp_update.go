// tp update：升级面板自身的二进制。
//
// 与面板里那个「自更新」按钮的区别：Web 端是「exec 重启，起不来就起不来」，
// systemd 会按 Restart=on-failure 反复重试，全失败后只能 SSH 上去手动救。
// CLI 版本能在重启之后继续盯着 /healthz，**不就绪就自动回滚**——
// 一次坏升级把面板搞黑，是这套命令里最值得多写几十行的地方。
//
// 版本解析不需要面板在跑：读 DB 里的 panel_repo / panel_update_index，
// 没有就用官方仓库。所以面板起不来的时候，tp update --file 仍然能救回来。
package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"triune/internal/update"
)

// panelAssetName 本机这个平台在 Release 里的资产名（与 deploy/release.sh 一致）。
func panelAssetName() string {
	return fmt.Sprintf("triune-panel-%s-%s", runtime.GOOS, runtime.GOARCH)
}

// updateUsage 给 `tp update --help` 用的说明。
// 默认的 flag 帮助只列开关名，不说「升级会重启面板、失败会自动回滚」这类
// 关键前提——而这些恰恰是用户按回车之前最该知道的。
func updateUsage() {
	fmt.Print(`用法: tp update [开关]

  不带开关      升级到最新正式版
  --check       只看有没有新版，不下载、不改任何东西

开关:
  --to VER      指定目标版本（如 v1.0.3），可升可降
  --pre         允许安装预发布版本
  --file PATH   用本地二进制升级（离线 / 救砖场景）
  --dry-run     下载并校验，但不替换
  --yes         跳过确认（非交互环境必须加）
  --mirror URL  下载镜像前缀；默认用设置里的 gh_mirror，留空则直连
  --unit NAME   systemd 单元名（默认 triune-panel）
  --wait 15s    重启后等待就绪的时长，超时就回滚
  --data DIR    数据目录

它会做什么:
  1. 取版本列表 → 选目标 → 下载 → 校验 sha256
  2. 跑 ` + "`-version`" + ` 自检，确认拿到的真是面板二进制、版本对得上
  3. 备份现有二进制，原子替换
  4. 重启服务并等 /healthz 就绪；不就绪会自动回滚，并报出失败原因
`)
}

func cmdUpdate(args []string) {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	dataDir := fs.String("data", "", "数据目录（默认自动探测）")
	checkOnly := fs.Bool("check", false, "只检查有没有新版本，不下载")
	to := fs.String("to", "", "指定目标版本，如 v1.0.3（可升可降）")
	pre := fs.Bool("pre", false, "允许预发布版本")
	local := fs.String("file", "", "用本地二进制升级（离线 / 救砖）")
	dryRun := fs.Bool("dry-run", false, "下载并校验，但不替换")
	yes := fs.Bool("yes", false, "跳过确认")
	unit := fs.String("unit", defaultUnit, "systemd 单元名")
	wait := fs.Duration("wait", 15*time.Second, "重启后等待就绪的时长")
	mirror := fs.String("mirror", "", "下载镜像前缀（默认用设置里的 gh_mirror，留空则直连）")
	fs.Usage = updateUsage
	_ = tpParse(fs, args)

	if *local != "" {
		runUpdateFromFile(*local, *dataDir, *unit, *wait, *dryRun, *yes)
		return
	}
	runUpdateOnline(*dataDir, *to, *pre, *checkOnly, *dryRun, *yes, *unit, *wait, *mirror)
}

// ---- 在线升级 ----

// panelReleases 解析面板的可用版本。返回的列表已按版本降序。
//
// 两条来源：panel_update_index（自建索引清单，直接给二进制直链 + sha256）、
// panel_repo / 官方仓库的 GitHub Releases。索引优先，因为它是作者为「任意版本
// 更新」准备的正路。
func panelReleases(dataDir string) (rels []update.Release, source string, err error) {
	st := openExistingStore(dataDir)
	defer st.Close()

	if idx, _ := st.GetSetting("panel_update_index"); idx != "" {
		rs, err := update.ListIndexVersions(idx)
		if err != nil {
			return nil, "index", err
		}
		return rs, "索引清单", nil
	}
	repo, _ := st.GetSetting("panel_repo")
	src := "GitHub（" + repo + "）"
	if repo == "" {
		repo = "yunyu697/triune-panel"
		src = "官方仓库"
	}
	apiBase, _ := st.GetSetting("gh_api_base")
	rs, err := update.ListVersions(apiBase, repo)
	if err != nil {
		return nil, src, err
	}
	return rs, src, nil
}

// pickRelease 选出要装的那个版本。
func pickRelease(rels []update.Release, want string, allowPre bool) (update.Release, error) {
	if want != "" {
		for _, r := range rels {
			if update.SameVersion(r.Tag, want) {
				return r, nil
			}
		}
		return update.Release{}, fmt.Errorf("版本 %s 不在可用列表里（tp update --check 可以看到全部）", want)
	}
	for _, r := range rels {
		if r.Prerelease && !allowPre {
			continue
		}
		return r, nil
	}
	return update.Release{}, fmt.Errorf("没有可用的正式版（要装预发布请加 --pre）")
}

func runUpdateOnline(dataDir, want string, allowPre, checkOnly, dryRun, yes bool, unit string, wait time.Duration, mirror string) {
	dir := resolveDataDir(dataDir)
	rels, source, err := panelReleases(dir)
	if err != nil {
		die(1, "获取版本列表失败（来源：%s）: %v", source, err)
	}
	if len(rels) == 0 {
		die(1, "版本列表是空的（来源：%s）", source)
	}
	target, err := pickRelease(rels, want, allowPre)
	if err != nil {
		die(1, "%v", err)
	}

	latest := rels[0]
	if checkOnly {
		printUpdateCheck(target, latest)
		return
	}
	if update.SameVersion(Version, target.Tag) && want == "" {
		fmt.Printf("  已是最新版本 %s，无需升级。\n", Version)
		return
	}

	// 目标版本比当前旧就明说，别让人以为在升级
	verb := "升级"
	if Version != "dev" && update.Newer(Version, target.Tag) {
		verb = "降级"
	}
	fmt.Printf("将把面板从 %s %s到 %s（来源：%s）\n", Version, verb, target.Tag, source)

	// 取下载地址：索引清单直接给 url，GitHub 要按平台从资产里挑
	url := target.URL
	if url == "" {
		name := panelAssetName()
		url = target.AssetURLs[name]
		if url == "" {
			die(1, "版本 %s 里没有本平台的资产 %s\n  可用资产：%s\n  这台机器的平台是 %s/%s",
				target.Tag, name, strings.Join(target.Assets, ", "), runtime.GOOS, runtime.GOARCH)
		}
	}
	if mirror != "" {
		url = update.ApplyMirror(url, mirror)
	}

	sum := target.Sha256
	if sum == "" {
		sum = fetchSum(target, panelAssetName(), mirror)
	}
	if sum == "" {
		fmt.Println("  ⚠ 没拿到 sha256，跳过校验（索引清单和 sha256sums.txt 都没有）")
	}

	if !confirm(yes, "继续？") {
		die(1, "已取消")
	}

	tmp, err := os.MkdirTemp("", "triune-update-")
	if err != nil {
		die(1, "创建临时目录失败: %v", err)
	}
	defer os.RemoveAll(tmp)

	fmt.Printf("  下载 %s …\n", filepath.Base(strings.Split(url, "?")[0]))
	bin, gotSum, err := update.Download(url, sum, tmp)
	if err != nil {
		die(1, "下载失败: %v\n  网络受限时可以加 --mirror https://ghfast.top 之类的镜像前缀", err)
	}
	fmt.Printf("  校验 sha256 … OK（%s）\n", shortHash(gotSum))

	verifyPanelBinary(bin, target.Tag)

	if dryRun {
		fmt.Printf("\n  ✔ 校验通过，--dry-run 到此为止，未做任何替换。\n")
		return
	}
	installAndRestart(bin, dir, unit, wait, target.Tag)
}

func printUpdateCheck(target, latest update.Release) {
	fmt.Printf("  当前版本  %s\n", Version)
	fmt.Printf("  最新版本  %s", latest.Tag)
	if latest.Prerelease {
		fmt.Print("（预发布）")
	}
	if len(latest.PublishedAt) >= 10 {
		fmt.Printf("  %s", latest.PublishedAt[:10])
	}
	fmt.Println()
	if update.SameVersion(Version, latest.Tag) {
		fmt.Println("\n  已是最新，无需升级。")
		return
	}
	if latest.Tag != target.Tag {
		fmt.Printf("  指定版本  %s\n", target.Tag)
	}
	name := panelAssetName()
	if latest.URL == "" && latest.AssetURLs[name] == "" {
		fmt.Printf("\n  ⚠ 最新版本里没有本平台的资产 %s，无法自动升级。\n", name)
		return
	}
	fmt.Printf("\n  升级：sudo tp update")
	if latest.Prerelease {
		fmt.Printf(" --pre")
	}
	fmt.Println()
}

// fetchSum 从 release 的 sha256sums.txt 里取指定资产的哈希。
// 拿不到就返回空串——由调用方决定是否放行（索引清单可能自带 sha256）。
func fetchSum(rel update.Release, asset string, mirror string) string {
	u := rel.AssetURLs["sha256sums.txt"]
	if u == "" {
		return ""
	}
	if mirror != "" {
		u = update.ApplyMirror(u, mirror)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(u)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ""
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && strings.TrimPrefix(f[1], "*") == asset {
			return f[0]
		}
	}
	return ""
}

// verifyPanelBinary 在替换前确认新文件真的是能跑的面板二进制，且版本对得上。
//
// 这一步比 sha256 更重要：sha256 只能证明「文件没被篡改」，证明不了
// 「这是我该装的东西」——把内核二进制传进 --file 一样能通过校验。
func verifyPanelBinary(bin, wantTag string) {
	line, err := update.BinaryVersion(bin, []string{"-version"})
	if err != nil {
		die(1, "新二进制跑不起来，已拒收（现有环境零改动）: %v", err)
	}
	// 期望形如 "triune-panel v1.0.3 (linux/amd64)"
	if !strings.HasPrefix(line, "triune-panel ") {
		die(1, "这不像面板二进制（版本输出是 %q），已拒收。", line)
	}
	got := strings.TrimSpace(strings.TrimPrefix(line, "triune-panel "))
	if f := strings.Fields(got); len(f) > 0 {
		got = f[0]
	} else {
		got = ""
	}
	if wantTag != "" && !update.SameVersion(got, wantTag) {
		die(1, "二进制自报版本 %q，与目标 %s 不一致，已拒收。", got, wantTag)
	}
	fmt.Printf("  自检 … %s OK\n", line)
}

// installAndRestart 替换二进制 → 重启 → 等就绪，不就绪自动回滚。
func installAndRestart(newBin, dataDir, unit string, wait time.Duration, wantTag string) {
	exe, err := os.Executable()
	if err != nil {
		die(1, "定位当前二进制失败: %v", err)
	}
	// 软链到 /usr/local/bin/tp 时 os.Executable 会解析出真实路径，
	// 这正是要替换的那份，而不是软链本身。
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	backupDir := filepath.Join(dataDir, "backups", "panel")

	fmt.Printf("  备份旧版本 → %s\n", backupDir)
	backup, err := update.Replace(newBin, exe, backupDir)
	if err != nil {
		die(1, "替换失败: %v", err)
	}
	fmt.Println("  替换 … OK")

	if !systemdAvailable(unit) {
		fmt.Printf("\n  ✔ 二进制已替换为 %s，但没找到 systemd 单元 %s，需要你自己重启面板。\n", wantTag, unit)
		return
	}
	if !serviceActive(unit) {
		fmt.Printf("\n  ✔ 二进制已替换为 %s，面板当前没在运行（tp start 启动）。\n", wantTag)
		return
	}

	if err := restartUnit(unit); err != nil {
		rollbackPanel(backup, exe, unit, wantTag, err)
		return
	}
	d, ok := waitReady(unit, wait)
	if !ok {
		rollbackPanel(backup, exe, unit, wantTag,
			fmt.Errorf("%s 内没有就绪（%s 无响应）", wait, healthURL(unit)))
		return
	}
	fmt.Printf("  重启 … %.1fs\n", d.Seconds())
	fmt.Printf("\n  ✔ 已升级到 %s\n", wantTag)
}

// rollbackPanel 升级失败时退回旧二进制并重启。
//
// 回滚本身也可能失败（比如磁盘满），所以两条路都打印清楚：
// 成功就说"已回滚、面板可用"，失败就给出可手抄的命令。
func rollbackPanel(backup, exe, unit, wantTag string, cause error) {
	fmt.Printf("\n  ✘ 升级失败：%v\n", cause)
	if backup == "" {
		die(1, "没有可用的备份文件，需要手动处理。\n  二进制位置：%s", exe)
	}
	fmt.Printf("  正在回滚到旧版本（备份 %s）\n", filepath.Base(backup))
	if err := update.Restore(backup, exe); err != nil {
		die(1, "回滚失败: %v\n  手动恢复：cp %s %s && systemctl restart %s", err, backup, exe, unit)
	}
	if err := restartUnit(unit); err != nil {
		die(1, "回滚后重启失败: %v\n  手动：systemctl restart %s", err, unit)
	}
	if _, ok := waitReady(unit, 15*time.Second); !ok {
		die(1, "回滚后仍未就绪。\n  看日志：tp logs\n  手动恢复：cp %s %s && systemctl restart %s", backup, exe, unit)
	}
	fmt.Printf("  ✔ 已回滚，面板恢复可用。升级失败的原因见：tp logs\n")
	os.Exit(1)
}

func restartUnit(unit string) error {
	out, err := exec.Command("systemctl", "restart", unit).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl restart 失败: %v %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func shortHash(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// ---- 本地文件升级（离线 / 救砖）----

// runUpdateFromFile 用本地二进制升级。这条路不联网、不读版本列表，
// 是面板起不来、又没网时的唯一自救方式。
func runUpdateFromFile(path, dataDir, unit string, wait time.Duration, dryRun, yes bool) {
	info, err := os.Stat(path)
	if err != nil {
		die(1, "找不到文件 %s: %v", path, err)
	}
	if info.IsDir() {
		die(2, "%s 是目录，需要指向二进制文件", path)
	}
	verifyPanelBinary(path, "")
	if dryRun {
		fmt.Println("\n  ✔ --dry-run 到此为止，未做任何替换。")
		return
	}
	label := "本地文件 " + filepath.Base(path)
	if !confirm(yes, "用 %s 替换当前二进制并重启面板？", label) {
		die(1, "已取消")
	}
	installAndRestart(path, resolveDataDir(dataDir), unit, wait, label)
}
