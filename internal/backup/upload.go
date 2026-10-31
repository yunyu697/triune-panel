package backup

// 备份归档上传到异地（rclone）。
//
// 为什么不自己实现 S3：S3 兼容存储的实现差异很大（AWS / R2 / B2 / MinIO /
// 阿里云 OSS 的签名细节、分片、region 探测各不相同），而 rclone 已经把这些
// 全部处理好了，还顺带支持 WebDAV / SFTP / 对象存储以外的后端。
// 本项目的既有做法也是「外部工具在就调用、不在就明确降级」
// （见 internal/firewall 对 ufw / firewall-cmd 的处理），这里保持一致。
//
// 关键设计：**上传后必须回读校验**。备份这东西最怕"看起来传上去了"——
// 网络抖动导致的截断在 rclone 退出码里不一定体现，所以传完再用
// `rclone lsjson` 读回远端对象的字节数，与本地归档比对，不一致就报错。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DefaultRcloneBin 默认的 rclone 可执行文件名（走 PATH）。
const DefaultRcloneBin = "rclone"

// UploadTimeout 单次上传（含回读校验与远端轮转）的总超时。
// 备份归档通常几十 MB，慢网也够用；设上限是为了别把 systemd timer 挂死。
const UploadTimeout = 30 * time.Minute

// UploadOptions 上传参数。
type UploadOptions struct {
	// Remote 是 rclone 的「远端:目录」写法，例如：
	//   r2:triune-backups        Cloudflare R2
	//   b2:my-bucket/triune      Backblaze B2
	//   s3:my-bucket/backups     S3 兼容
	//   gdrive:triune            Google Drive
	Remote string
	// RcloneBin 可指定 rclone 路径（PATH 里没有时用），空则用 DefaultRcloneBin。
	RcloneBin string
	// Keep 远端保留份数；<=0 表示不在远端做轮转（只上传，不删任何东西）。
	Keep int
}

// UploadResult 上传结果。
type UploadResult struct {
	RemotePath string   // 远端完整路径，如 r2:triune-backups/triune-data-20261101-030000.tar.gz.age
	Bytes      int64    // 回读校验得到的远端字节数
	Removed    []string // 远端被轮转删除的旧归档
}

// Upload 把归档上传到远端，回读校验，并按 Keep 清理远端旧归档。
func Upload(ctx context.Context, archive string, opt UploadOptions) (*UploadResult, error) {
	if strings.TrimSpace(opt.Remote) == "" {
		return nil, fmt.Errorf("未指定远端")
	}
	fi, err := os.Stat(archive)
	if err != nil {
		return nil, fmt.Errorf("归档不可用: %w", err)
	}
	bin, err := resolveRclone(opt.RcloneBin)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, UploadTimeout)
	defer cancel()

	remote := strings.TrimRight(strings.TrimSpace(opt.Remote), "/")
	base := filepath.Base(archive)
	remotePath := remote + "/" + base

	// ① 上传
	if out, err := runRclone(ctx, bin, "copy", archive, remote); err != nil {
		return nil, fmt.Errorf("上传失败: %v%s", err, tail(out))
	}

	// ② 回读校验：远端字节数必须与本地一致
	got, err := remoteSize(ctx, bin, remotePath)
	if err != nil {
		return nil, fmt.Errorf("上传后校验失败（无法确认远端文件）: %w", err)
	}
	if got != fi.Size() {
		return nil, fmt.Errorf("上传后校验失败：远端 %d 字节，本地 %d 字节，归档可能被截断", got, fi.Size())
	}

	res := &UploadResult{RemotePath: remotePath, Bytes: got}

	// ③ 远端轮转（只删本包命名的归档，不碰远端目录里的其他东西）
	if opt.Keep > 0 {
		removed, err := rotateRemote(ctx, bin, remote, opt.Keep)
		res.Removed = removed
		if err != nil {
			// 清理失败不影响"备份已经安全落地"这个事实，交给调用方提示
			return res, fmt.Errorf("远端旧归档清理失败: %w", err)
		}
	}
	return res, nil
}

// resolveRclone 定位 rclone 可执行文件，给出可操作的错误提示。
func resolveRclone(bin string) (string, error) {
	if bin == "" {
		bin = DefaultRcloneBin
	}
	p, err := exec.LookPath(bin)
	if err != nil {
		return "", fmt.Errorf("未找到 rclone（异地备份需要它）。\n" +
			"  安装: curl https://rclone.org/install.sh | sudo bash\n" +
			"  配置: sudo rclone config（root 身份运行，配置读 /root/.config/rclone/rclone.conf）\n" +
			"  已装在非标准路径时可用 --rclone /path/to/rclone 指定")
	}
	return p, nil
}

// remoteSize 用 lsjson 读回远端对象的字节数。
func remoteSize(ctx context.Context, bin, remotePath string) (int64, error) {
	out, err := runRclone(ctx, bin, "lsjson", remotePath)
	if err != nil {
		return 0, fmt.Errorf("%v%s", err, tail(out))
	}
	var items []struct {
		Size int64 `json:"Size"`
	}
	if err := json.Unmarshal(out, &items); err != nil {
		return 0, fmt.Errorf("无法解析 rclone 输出: %w", err)
	}
	if len(items) != 1 {
		return 0, fmt.Errorf("远端应只有 1 个对象，实际 %d 个", len(items))
	}
	return items[0].Size, nil
}

// rotateRemote 远端只保留最新 keep 份本包命名的归档。
//
// 只处理 triune-data-*.tar.gz[.age]，远端目录里其他文件一律不碰——
// 用户很可能把备份传进一个共用目录，误删别人的东西是不可接受的。
func rotateRemote(ctx context.Context, bin, remote string, keep int) ([]string, error) {
	out, err := runRclone(ctx, bin, "lsf", remote, "--files-only")
	if err != nil {
		return nil, fmt.Errorf("%v%s", err, tail(out))
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		n := strings.TrimSpace(line)
		if n == "" || strings.Contains(n, "/") {
			continue // 只看顶层文件
		}
		if isArchiveName(n) {
			names = append(names, n)
		}
	}
	if len(names) <= keep {
		return nil, nil
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	var removed []string
	for _, n := range names[keep:] {
		if out, err := runRclone(ctx, bin, "deletefile", remote+"/"+n); err != nil {
			return removed, fmt.Errorf("删除 %s 失败: %v%s", n, err, tail(out))
		}
		removed = append(removed, n)
	}
	return removed, nil
}

// runRclone 执行一次 rclone，返回 stdout。
func runRclone(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return out, fmt.Errorf("%w: %s", err, msg)
		}
		return out, err
	}
	return out, nil
}

// tail 截取命令输出末尾，用于错误信息（rclone 的报错常常在最后几行）。
func tail(b []byte) string {
	s := strings.TrimSpace(string(b))
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	return "\n  " + strings.Join(lines, "\n  ")
}
