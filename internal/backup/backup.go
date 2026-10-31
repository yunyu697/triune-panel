// Package backup 实现数据目录的打包备份与轮转。
//
// 与安装脚本的每日备份（systemd timer 里 tar czf）语义一致，但用纯 Go 实现，
// 不依赖系统 tar（精简容器/Windows 上也能跑），并且**排除备份目录自身**——
// 否则备份会把自己打进自己，越滚越大。
package backup

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DefaultKeep 默认保留份数，与安装脚本的 KEEP=7 保持一致。
const DefaultKeep = 7

// ExcludeNames 打包时跳过的顶层条目。
var ExcludeNames = map[string]bool{
	"backups":    true, // 备份目录自身
	"panel.sock": true, // 未来 CLI 的 unix socket
}

// Options 备份参数。
type Options struct {
	DataDir string    // 待备份的数据目录
	OutDir  string    // 输出目录（空则 <DataDir>/backups）
	Keep    int       // 轮转保留份数（<=0 用 DefaultKeep）
	Now     time.Time // 便于测试注入
}

// Result 备份结果。
type Result struct {
	Path    string   // 生成的归档路径
	Size    int64    // 字节数
	Removed []string // 轮转删除的旧归档
}

// Create 打包数据目录为 tar.gz 并按 Keep 轮转旧归档。
func Create(opts Options) (*Result, error) {
	if opts.DataDir == "" {
		return nil, fmt.Errorf("数据目录为空")
	}
	if _, err := os.Stat(opts.DataDir); err != nil {
		return nil, fmt.Errorf("数据目录不可用: %w", err)
	}
	outDir := opts.OutDir
	if outDir == "" {
		outDir = filepath.Join(opts.DataDir, "backups")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}
	keep := opts.Keep
	if keep <= 0 {
		keep = DefaultKeep
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	name := "triune-data-" + now.Format("20060102-150405") + ".tar.gz"
	dst := filepath.Join(outDir, name)

	if err := writeArchive(opts.DataDir, dst, outDir, now); err != nil {
		_ = os.Remove(dst)
		return nil, err
	}
	fi, err := os.Stat(dst)
	if err != nil {
		return nil, err
	}
	removed, err := Rotate(outDir, keep)
	if err != nil {
		// 轮转失败不影响备份本身，交给调用方提示
		return &Result{Path: dst, Size: fi.Size()}, err
	}
	return &Result{Path: dst, Size: fi.Size(), Removed: removed}, nil
}

// writeArchive 把 srcDir 内容写入 dst（归档内路径相对 srcDir）。
// outDir 若位于 srcDir 内（自定义 --out 时常见），整个目录跳过——否则
// 归档会把自己读进去，表现为 archive/tar: write too long。
func writeArchive(srcDir, dst, outDir string, now time.Time) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()

	dstAbs, _ := filepath.Abs(dst)
	outAbs, _ := filepath.Abs(outDir)

	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil || rel == "." {
			return nil
		}
		abs, _ := filepath.Abs(path)
		if abs == dstAbs {
			return nil
		}
		if abs == outAbs && info.IsDir() {
			return filepath.SkipDir
		}
		// 跳过顶层排除项（backups/、panel.sock）
		top := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
		if ExcludeNames[top] {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil // 跳过 socket/设备等非常规文件
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		hdr.ModTime = info.ModTime()
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(tw, src)
		return err
	})
}

// Rotate 只保留最新的 keep 份 triune-data-*.tar.gz，返回被删除的路径。
// 文件名带时间戳，按名字倒序即按时间倒序。
func Rotate(dir string, keep int) ([]string, error) {
	if keep <= 0 {
		keep = DefaultKeep
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasPrefix(n, "triune-data-") && strings.HasSuffix(n, ".tar.gz") {
			names = append(names, n)
		}
	}
	if len(names) <= keep {
		return nil, nil
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	var removed []string
	for _, n := range names[keep:] {
		p := filepath.Join(dir, n)
		if err := os.Remove(p); err != nil {
			return removed, err
		}
		removed = append(removed, p)
	}
	return removed, nil
}
