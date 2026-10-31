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
	// Passphrase 非空时用 age 口令加密，归档名加 .age 后缀。
	// 备份里含密码哈希、全部用户凭据与订阅令牌，落到对象存储/网盘上
	// 不加密等于把面板钥匙一起交出去。
	Passphrase string
}

// Result 备份结果。
type Result struct {
	Path      string   // 生成的归档路径
	Size      int64    // 字节数
	Removed   []string // 轮转删除的旧归档
	Encrypted bool     // 是否加密
	// WALBytes > 0 表示打包时 triune.db-wal 里还有未合并的事务。
	// 归档本身是完整的（WAL 一并打了进去，恢复时 SQLite 会重放），
	// 但若面板正在运行，直接拷文件可能拿到不一致的快照——据此提示用户停服备份。
	WALBytes int64
}

// Create 打包数据目录为 tar.gz（可选 age 加密）并按 Keep 轮转旧归档。
func Create(opts Options) (*Result, error) {
	if opts.DataDir == "" {
		return nil, fmt.Errorf("数据目录为空")
	}
	if _, err := os.Stat(opts.DataDir); err != nil {
		return nil, fmt.Errorf("数据目录不可用: %w", err)
	}
	// 先校验口令长度再开工：写了一半才发现口令不合格，会留下半个密文文件
	if opts.Passphrase != "" && len([]rune(opts.Passphrase)) < MinPassphraseLen {
		return nil, fmt.Errorf("口令太短：至少 %d 个字符", MinPassphraseLen)
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
	if opts.Passphrase != "" {
		name += EncryptedSuffix
	}
	dst := filepath.Join(outDir, name)

	if err := writeArchive(opts.DataDir, dst, outDir, opts.Passphrase); err != nil {
		_ = os.Remove(dst)
		return nil, err
	}
	fi, err := os.Stat(dst)
	if err != nil {
		return nil, err
	}
	res := &Result{Path: dst, Size: fi.Size(), Encrypted: opts.Passphrase != ""}
	if wal, err := os.Stat(filepath.Join(opts.DataDir, "triune.db-wal")); err == nil {
		res.WALBytes = wal.Size()
	}
	removed, err := Rotate(outDir, keep)
	if err != nil {
		// 轮转失败不影响备份本身，交给调用方提示
		return res, err
	}
	res.Removed = removed
	return res, nil
}

// writeArchive 把 srcDir 内容写入 dst（归档内路径相对 srcDir）。
// outDir 若位于 srcDir 内（自定义 --out 时常见），整个目录跳过——否则
// 归档会把自己读进去，表现为 archive/tar: write too long。
//
// 写链路：tar → gzip →（可选 age 加密）→ 文件。关闭顺序必须反过来逐层
// 关闭并检查错误——加密层的 Close 才写出最后一个分块与完整性标签，
// 用 defer 忽略错误会得到"看起来成功但解不开"的备份。
func writeArchive(srcDir, dst, outDir string, passphrase string) (err error) {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}

	var sink io.Writer = f
	var enc io.WriteCloser
	if passphrase != "" {
		enc, err = EncryptWriter(f, passphrase)
		if err != nil {
			f.Close()
			return err
		}
		sink = enc
	}

	gz := gzip.NewWriter(sink)
	tw := tar.NewWriter(gz)

	dstAbs, _ := filepath.Abs(dst)
	outAbs, _ := filepath.Abs(outDir)

	walkErr := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
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

	// 逐层关闭：即使 Walk 出错也要关，让已写入的部分成为合法（可诊断的）
	// 归档/密文，而不是留下截断文件。
	if e := tw.Close(); walkErr == nil {
		walkErr = e
	}
	if e := gz.Close(); walkErr == nil {
		walkErr = e
	}
	if enc != nil {
		if e := enc.Close(); walkErr == nil {
			walkErr = e
		}
	}
	if e := f.Close(); walkErr == nil {
		walkErr = e
	}
	return walkErr
}

// Rotate 只保留最新的 keep 份 triune-data-*.tar.gz[.age]，返回被删除的路径。
// 文件名带时间戳，按名字倒序即按时间倒序；明文与加密归档混存时同样按时间排序。
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
		if isArchiveName(e.Name()) {
			names = append(names, e.Name())
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

// isArchiveName 判断是否为本包生成的归档（含加密版）。
func isArchiveName(n string) bool {
	if !strings.HasPrefix(n, "triune-data-") {
		return false
	}
	return strings.HasSuffix(n, ".tar.gz") || strings.HasSuffix(n, ".tar.gz"+EncryptedSuffix)
}
