// Package update 提供组件（面板与三内核二进制）的通用安全更新能力。
//
// 可靠性原则（与配置流水线同构）：
//  1. 任何新二进制先在临时目录完成"可执行验证"（跑 version 命令），
//     再用新二进制对当前渲染配置做真实校验 —— 两关都过才触碰旧文件；
//  2. 旧二进制备份到 data/backups/<组件>/，可回滚到任意历史版本；
//  3. 替换是同目录 rename，原子完成，不存在半写状态；
//  4. 运行中的内核在替换后经健康检查，失败自动回滚旧版本并重启。
package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

func execCmd(bin string, args ...string) *exec.Cmd { return exec.Command(bin, args...) }

// Release GitHub Release 摘要（版本列表用）。
type Release struct {
	Tag    string   `json:"tag"`
	URL    string   `json:"url,omitempty"`
	Sha256 string   `json:"sha256,omitempty"`
	Notes  string   `json:"notes,omitempty"` // Release 说明正文（markdown 原文）
	Assets []string `json:"assets,omitempty"`
	// AssetURLs 资产名 → 下载直链（不序列化：几十个资产塞进 JSON 太吵，
	// 调用方按需取一个即可，见 api 里的 fillPanelAssetURL）。
	AssetURLs   map[string]string `json:"-"`
	Prerelease  bool              `json:"prerelease"`             // 预发布（alpha/beta/rc）
	PublishedAt string            `json:"published_at,omitempty"` // RFC3339，仅用于排序兜底
}

// 版本分支：GitHub Releases 是稳定版与预发布混在一起的列表，
// 「装最新」如果不区分分支，会直接把 alpha 装到生产面板上。
const (
	ChannelAll        = "all"
	ChannelStable     = "stable"
	ChannelPrerelease = "prerelease"
)

// NormalizeChannel 把外部传入的分支名收敛到已知值，空值视为 all。
func NormalizeChannel(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", ChannelAll:
		return ChannelAll, true
	case ChannelStable:
		return ChannelStable, true
	case ChannelPrerelease:
		return ChannelPrerelease, true
	}
	return "", false
}

// FilterChannel 按分支过滤版本列表（保持原有顺序）。
func FilterChannel(rels []Release, channel string) []Release {
	ch, ok := NormalizeChannel(channel)
	if !ok || ch == ChannelAll {
		return rels
	}
	out := make([]Release, 0, len(rels))
	for _, r := range rels {
		if (ch == ChannelPrerelease) == r.Prerelease {
			out = append(out, r)
		}
	}
	return out
}

// parseSemver 从 tag 里取出三段数字与预发布后缀。
// 只认 "1.2.3" / "v1.2.3" / "v1.2.3-alpha.1" 这类写法；
// 认不出（如日期型 tag）返回 ok=false，排序时退化为按发布时间。
func parseSemver(tag string) (nums [3]int, pre string, ok bool) {
	s := strings.TrimSpace(tag)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		pre = s[i+1:]
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return nums, pre, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nums, pre, false
		}
		nums[i] = n
	}
	return nums, pre, true
}

// newerThan 报告 a 是否应排在 b 前面（版本号降序）。
// 同一版本号下正式版优先于预发布（1.15.0 > 1.15.0-alpha.13）。
func newerThan(a, b Release) bool {
	an, ap, aok := parseSemver(a.Tag)
	bn, bp, bok := parseSemver(b.Tag)
	if aok && bok {
		for k := 0; k < 3; k++ {
			if an[k] != bn[k] {
				return an[k] > bn[k]
			}
		}
		if (ap == "") != (bp == "") {
			return ap == ""
		}
		if ap != bp {
			return ap > bp
		}
	}
	if aok != bok {
		return aok
	}
	return a.PublishedAt > b.PublishedAt
}

// sortReleases 按版本号降序重排（认不出的 tag 退化为按发布时间）。
// GitHub 按创建时间返回，补丁回填（v1.14.5 晚于 v1.15.0 发布）会让
// 「第一条」不是最高版本。
func sortReleases(rels []Release) {
	sort.SliceStable(rels, func(i, j int) bool { return newerThan(rels[i], rels[j]) })
}

// ghReleases GitHub API 的 releases 响应子集。
type ghReleases []struct {
	TagName     string `json:"tag_name"`
	Body        string `json:"body"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
	PublishedAt string `json:"published_at"`
	Assets      []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// ListVersions 拉取某组件的可用版本列表（GitHub Releases）。
// apiBase 可替换为镜像；失败时返回错误（调用方决定如何提示）。
func ListVersions(apiBase, repo string) ([]Release, error) {
	if repo == "" {
		return nil, fmt.Errorf("未配置仓库（owner/name）")
	}
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}
	url := fmt.Sprintf("%s/repos/%s/releases?per_page=30", apiBase, repo)
	client := &http.Client{Timeout: 60 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	// GitHub API 强制要求 User-Agent，缺失会被 403/重置
	req.Header.Set("User-Agent", "triune-panel")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("获取版本列表失败: %w（网络受限时可设置 gh_api_base 镜像，或直接用 URL 方式更新）", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("获取版本列表失败: HTTP %d", resp.StatusCode)
	}
	var gh ghReleases
	// 注意上限：sing-box 等仓库的 releases JSON 可达 10MB+，太小会截断报 unexpected EOF
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&gh); err != nil {
		return nil, fmt.Errorf("解析版本列表失败: %w", err)
	}
	out := make([]Release, 0, len(gh))
	for _, r := range gh {
		if r.Draft {
			continue // 草稿对外不可下载，列出来只会让人点出一个 404
		}
		_, pre, ok := parseSemver(r.TagName)
		rel := Release{
			Tag:         r.TagName,
			Notes:       r.Body,
			Prerelease:  r.Prerelease || (ok && pre != ""),
			PublishedAt: r.PublishedAt,
		}
		for _, a := range r.Assets {
			rel.Assets = append(rel.Assets, a.Name)
			if a.BrowserDownloadURL != "" {
				if rel.AssetURLs == nil {
					rel.AssetURLs = map[string]string{}
				}
				rel.AssetURLs[a.Name] = a.BrowserDownloadURL
			}
		}
		out = append(out, rel)
	}
	// GitHub 按创建时间排序，补丁版本回填（如 v1.14.5 晚于 v1.15.0 发布）
	// 会让「第一条」不是最高版本；这里按版本号重排，认不出的退化为按时间。
	sortReleases(out)
	return out, nil
}

// ListIndexVersions 从自定义索引清单读取版本列表（面板任意版本更新的推荐方式）。
// 清单格式：[{"version":"v0.5.1","url":"https://...","sha256":"可选"}]
func ListIndexVersions(indexURL string) ([]Release, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(indexURL)
	if err != nil {
		return nil, fmt.Errorf("获取版本索引失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("获取版本索引失败: HTTP %d", resp.StatusCode)
	}
	var idx []struct {
		Version string `json:"version"`
		URL     string `json:"url"`
		Sha256  string `json:"sha256"`
		Notes   string `json:"notes"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&idx); err != nil {
		return nil, fmt.Errorf("解析版本索引失败: %w", err)
	}
	out := make([]Release, 0, len(idx))
	for _, it := range idx {
		_, pre, ok := parseSemver(it.Version)
		out = append(out, Release{Tag: it.Version, URL: it.URL, Sha256: it.Sha256, Notes: it.Notes,
			Prerelease: ok && pre != ""})
	}
	sortReleases(out)
	return out, nil
}

// AssetURL 按组件与版本号构造官方 Release 资源的下载地址（自动匹配本机 OS/架构）。
func AssetURL(component, tag string) (string, error) {
	goos, goarch := runtime.GOOS, runtime.GOARCH
	switch component {
	case "singbox":
		v := strings.TrimPrefix(tag, "v")
		return fmt.Sprintf("https://github.com/SagerNet/sing-box/releases/download/%s/sing-box-%s-%s-%s.tar.gz",
			tag, v, goos, goarch), nil
	case "xray":
		code, err := xrayCode(goos, goarch)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("https://github.com/XTLS/Xray-core/releases/download/%s/Xray-%s.zip", tag, code), nil
	case "mihomo":
		return fmt.Sprintf("https://github.com/MetaCubeX/mihomo/releases/download/%s/mihomo-%s-%s-%s.gz",
			tag, goos, goarch, tag), nil
	}
	return "", fmt.Errorf("未知组件: %s", component)
}

func xrayCode(goos, goarch string) (string, error) {
	switch {
	case goos == "darwin" && goarch == "amd64":
		return "macos-64", nil
	case goos == "darwin" && goarch == "arm64":
		return "macos-arm64-v8a", nil
	case goos == "linux" && goarch == "amd64":
		return "linux-64", nil
	case goos == "linux" && goarch == "arm64":
		return "linux-arm64-v8a", nil
	case goos == "linux" && goarch == "386":
		return "linux-32", nil
	case goos == "windows" && goarch == "amd64":
		return "windows-64", nil
	}
	return "", fmt.Errorf("xray 无预编译包: %s/%s", goos, goarch)
}

// ApplyMirror 把 GitHub 下载地址替换为镜像前缀（如 https://ghfast.top）。
// mirror 为空时原样返回。
func ApplyMirror(rawURL, mirror string) string {
	if mirror == "" {
		return rawURL
	}
	mirror = strings.TrimRight(mirror, "/")
	if strings.HasPrefix(rawURL, "https://github.com/") {
		return mirror + "/" + rawURL
	}
	return rawURL
}

// Download 下载 url 到 destDir 下的临时文件，返回 (文件路径, sha256, 错误)。
// wantSha256 非空时强校验。
func Download(url, wantSha256, destDir string) (string, string, error) {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", "", err
	}
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return "", "", fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("下载失败: HTTP %d (%s)", resp.StatusCode, url)
	}
	tmp := filepath.Join(destDir, fmt.Sprintf(".dl-%d%s", time.Now().UnixNano(), urlExt(url)))
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", "", err
	}
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, sum), io.LimitReader(resp.Body, 1<<30))
	if err != nil {
		f.Close()
		os.Remove(tmp)
		return "", "", fmt.Errorf("下载写入失败: %w", err)
	}
	f.Close()
	// 防截断：Content-Length 已知时必须完全一致，截断的包会解出坏二进制
	if resp.ContentLength > 0 && n != resp.ContentLength {
		os.Remove(tmp)
		return "", "", fmt.Errorf("下载不完整: 期望 %d 字节实际 %d 字节", resp.ContentLength, n)
	}
	got := hex.EncodeToString(sum.Sum(nil))
	if wantSha256 != "" && !strings.EqualFold(got, wantSha256) {
		os.Remove(tmp)
		return "", got, fmt.Errorf("sha256 不匹配: 期望 %s 实际 %s", wantSha256, got)
	}
	return tmp, got, nil
}

// urlExt 从下载 URL 提取归档扩展名（.tar.gz / .tgz / .zip / .gz / 其他为空）。
// Extract 按扩展名选择解压算法，下载临时文件必须保留它。
func urlExt(rawURL string) string {
	p := strings.ToLower(rawURL)
	if i := strings.Index(p, "?"); i >= 0 {
		p = p[:i]
	}
	switch {
	case strings.HasSuffix(p, ".tar.gz"):
		return ".tar.gz"
	case strings.HasSuffix(p, ".tgz"):
		return ".tgz"
	case strings.HasSuffix(p, ".zip"):
		return ".zip"
	case strings.HasSuffix(p, ".gz"):
		return ".gz"
	}
	return ""
}

// Extract 从压缩包中提取组件二进制到 destDir/<name>。
// 支持 .tar.gz / .zip / 单文件 .gz / 裸二进制（按扩展名判断）。
func Extract(archivePath, name, destDir string) (string, error) {
	base := strings.ToLower(archivePath)
	out := filepath.Join(destDir, name+".new")
	switch {
	case strings.HasSuffix(base, ".tar.gz") || strings.HasSuffix(base, ".tgz"):
		if err := extractTarGz(archivePath, name, out); err != nil {
			return "", err
		}
	case strings.HasSuffix(base, ".zip"):
		if err := extractZip(archivePath, name, out); err != nil {
			return "", err
		}
	case strings.HasSuffix(base, ".gz"):
		if err := extractGz(archivePath, out); err != nil {
			return "", err
		}
	default: // 裸二进制
		if err := copyFile(archivePath, out); err != nil {
			return "", err
		}
	}
	if err := os.Chmod(out, 0o755); err != nil {
		return "", err
	}
	return out, nil
}

func matchMember(base, name string) bool {
	b := filepath.Base(base)
	if b == name || b == name+".exe" {
		return true
	}
	return false
}

func extractTarGz(path, name, out string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("压缩包内未找到 %s", name)
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg || !matchMember(hdr.Name, name) {
			continue
		}
		return writeFile(tr, out)
	}
}

func extractZip(path, name, out string) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !matchMember(f.Name, name) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		return writeFile(rc, out)
	}
	return fmt.Errorf("压缩包内未找到 %s", name)
}

func extractGz(path, out string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	return writeFile(gz, out)
}

func writeFile(r io.Reader, out string) error {
	f, err := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, r)
	return err
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return writeFile(in, dst)
}

// VersionArgs 各组件的版本查询参数。
var VersionArgs = map[string][]string{
	"panel":   {"-version"},
	"singbox": {"version"},
	"xray":    {"version"},
	"mihomo":  {"-v"},
}

// BinaryNames 各组件压缩包内的二进制文件名（与组件标识可能不同）。
var BinaryNames = map[string]string{
	"panel":   "triune-panel",
	"singbox": "sing-box",
	"xray":    "xray",
	"mihomo":  "mihomo",
}

// BinaryVersion 运行二进制的版本命令，返回首行输出（5 秒超时）。
// 这是更新前的可执行性验证：连 version 都跑不起来的二进制一律拒收。
func BinaryVersion(bin string, args []string) (string, error) {
	if len(args) == 0 {
		args = []string{"version"}
	}
	cmd := execCmd(bin, args...)
	var buf strings.Builder
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("无法执行 %s: %w", bin, err)
	}
	timer := time.AfterFunc(5*time.Second, func() { _ = cmd.Process.Kill() })
	defer timer.Stop()
	err := cmd.Wait()
	line := strings.TrimSpace(buf.String())
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	if err != nil {
		if line == "" {
			return "", fmt.Errorf("版本查询失败: %v", err)
		}
		// 某些内核 version 时输出到 stderr 且退出码非 0 —— 只要输出了版本行即认为可执行
	}
	if line == "" {
		return "", fmt.Errorf("%s 无任何输出", bin)
	}
	return line, nil
}

// Replace 原子替换目标二进制：旧文件先备份到 backupDir，再同目录 rename。
// 返回备份文件路径。任一步失败都不影响旧文件。
func Replace(newBin, target, backupDir string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", fmt.Errorf("目标目录不可创建: %w", err)
	}
	backup := ""
	if _, err := os.Stat(target); err == nil {
		if err := os.MkdirAll(backupDir, 0o755); err != nil {
			return "", err
		}
		backup = filepath.Join(backupDir, fmt.Sprintf("%s.bak", time.Now().Format("20060102-150405")))
		if err := copyFile(target, backup); err != nil {
			return "", fmt.Errorf("备份旧二进制失败: %w", err)
		}
	}
	staged := target + ".update-new"
	if err := copyFile(newBin, staged); err != nil {
		return "", fmt.Errorf("写入目标目录失败: %w", err)
	}
	if err := os.Chmod(staged, 0o755); err != nil {
		os.Remove(staged)
		return "", err
	}
	if err := os.Rename(staged, target); err != nil {
		os.Remove(staged)
		return backup, fmt.Errorf("原子替换失败: %w", err)
	}
	return backup, nil
}

// Restore 从备份恢复二进制（回滚）。
func Restore(backup, target string) error {
	if _, err := os.Stat(backup); err != nil {
		return fmt.Errorf("备份不存在: %s", backup)
	}
	staged := target + ".update-new"
	if err := copyFile(backup, staged); err != nil {
		return err
	}
	_ = os.Chmod(staged, 0o755)
	if err := os.Rename(staged, target); err != nil {
		os.Remove(staged)
		return err
	}
	return nil
}

// ListBackups 列出备份目录中的历史版本文件（按时间倒序）。
func ListBackups(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{}, nil
	}
	var out []string
	for i := len(entries) - 1; i >= 0; i-- {
		if !entries[i].IsDir() {
			out = append(out, entries[i].Name())
		}
	}
	return out, nil
}

// SameVersion 判断两个 tag 是否指向同一版本（忽略 v 前缀与首尾空白）。
// CLI 里比较「当前版本」与「目标版本」用，避免 v1.0.2 / 1.0.2 被判成两个版本。
func SameVersion(a, b string) bool {
	norm := func(s string) string {
		return strings.TrimPrefix(strings.TrimSpace(s), "v")
	}
	return norm(a) == norm(b)
}

// Newer 报告版本 a 是否比 b 新（语义化比较；预发布低于同号正式版）。
// 认不出的 tag 退化为「不比对方新」——宁可当成不新，也不要在升级判断上猜。
func Newer(a, b string) bool {
	_, _, aok := parseSemver(a)
	_, _, bok := parseSemver(b)
	if !aok || !bok {
		return false
	}
	return newerThan(Release{Tag: a}, Release{Tag: b})
}
