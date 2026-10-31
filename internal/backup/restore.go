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

// RestoreOptions 恢复参数。
type RestoreOptions struct {
	Archive string    // 归档路径
	DataDir string    // 目标数据目录
	Now     time.Time // 便于测试注入
	// Passphrase 归档为 age 加密时的口令；明文归档忽略此字段。
	// 缺失时返回 ErrPassphraseRequired，调用方据此提示输入。
	Passphrase string
}

// RestoreResult 恢复结果。
type RestoreResult struct {
	Files     int    // 写入的文件数
	Preserved int    // 保留的旧备份份数（backups/ 不参与替换）
	OldDBKept string // 旧数据被移到的目录（成功后已删除，失败时保留用于回滚）
}

// Restore 用归档替换数据目录内容。
//
// 安全约定：
//   - 只接受常规文件条目，拒绝绝对路径与 `..` 穿越（防恶意归档写到目录外）；
//   - `backups/` 不参与替换——恢复历史数据不该把用户的备份清掉；
//   - 先全部解到临时目录并确认 triune.db 存在，再原子换入；换入过程中
//     任何一步失败都尝试把旧内容移回原位。
//
// 注意：调用方必须确保面板已停止。daemon 持有旧 db 的文件句柄，
// 替换后它会继续写被移走的旧 inode，造成"数据分裂"。
func Restore(opts RestoreOptions) (*RestoreResult, error) {
	if opts.Archive == "" || opts.DataDir == "" {
		return nil, fmt.Errorf("归档路径与数据目录都不能为空")
	}
	if _, err := os.Stat(opts.DataDir); err != nil {
		return nil, fmt.Errorf("数据目录不可用: %w", err)
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	stamp := now.Format("20060102-150405")
	tmpDir := filepath.Join(opts.DataDir, ".restore-"+stamp)
	oldDir := filepath.Join(opts.DataDir, ".pre-restore-"+stamp)

	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	files, err := extract(opts.Archive, tmpDir, opts.Passphrase)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "triune.db")); err != nil {
		return nil, fmt.Errorf("归档里没有 triune.db，可能不是 Triune Panel 的备份")
	}

	// 把现有内容（除 backups/ 与临时目录）移到 .pre-restore-<ts>
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		return nil, err
	}
	if _, err := moveEntries(opts.DataDir, oldDir, true); err != nil {
		return nil, fmt.Errorf("暂存旧数据失败（未改动任何文件）: %w", err)
	}
	// 把新内容移入
	if _, err := moveEntries(tmpDir, opts.DataDir, false); err != nil {
		// 回滚：旧内容搬回去
		if _, rbErr := moveEntries(oldDir, opts.DataDir, false); rbErr != nil {
			return nil, fmt.Errorf("写入新数据失败且回滚不完整，旧数据保留在 %s: %v", oldDir, rbErr)
		}
		_ = os.RemoveAll(oldDir)
		return nil, fmt.Errorf("写入新数据失败，已回滚到恢复前状态: %w", err)
	}
	_ = os.RemoveAll(oldDir)

	preserved := 0
	if entries, err := os.ReadDir(filepath.Join(opts.DataDir, "backups")); err == nil {
		preserved = len(entries)
	}
	return &RestoreResult{Files: files, Preserved: preserved}, nil
}

// extract 解包归档到 dst，返回写入的文件数。拒绝路径穿越与非常规文件。
// 归档为 age 加密时先解密再解压（按魔数自动识别，不看扩展名）。
func extract(archive, dst, passphrase string) (int, error) {
	rc, err := openArchive(archive, passphrase)
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	gz, err := gzip.NewReader(rc)
	if err != nil {
		return 0, fmt.Errorf("不是有效的 tar.gz: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	count := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return count, err
		}
		name := filepath.Clean(hdr.Name)
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			return count, fmt.Errorf("归档条目路径非法（疑似路径穿越）: %s", hdr.Name)
		}
		top := strings.SplitN(filepath.ToSlash(name), "/", 2)[0]
		if ExcludeNames[top] {
			continue // backups/ 不参与恢复
		}
		target := filepath.Join(dst, name)
		if !strings.HasPrefix(target, filepath.Clean(dst)+string(os.PathSeparator)) {
			return count, fmt.Errorf("归档条目越界: %s", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return count, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return count, err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return count, err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return count, err
			}
			if err := out.Close(); err != nil {
				return count, err
			}
			count++
		default:
			// 符号链接/设备文件一律跳过：备份里本不该有，出现即异常
			continue
		}
	}
	return count, nil
}

// moveEntries 把 src 下的条目移动到 dst；skipKeep 为真时跳过 backups/
// 与本次恢复产生的临时目录（避免自己搬自己）。
func moveEntries(src, dst string, skipKeep bool) (int, error) {
	entries, err := os.ReadDir(src)
	if err != nil {
		return 0, err
	}
	// 排序保证回滚顺序稳定
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	n := 0
	for _, e := range entries {
		name := e.Name()
		if skipKeep {
			if ExcludeNames[name] {
				continue
			}
			if strings.HasPrefix(name, ".restore-") || strings.HasPrefix(name, ".pre-restore-") {
				continue
			}
		}
		if err := os.Rename(filepath.Join(src, name), filepath.Join(dst, name)); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// dbCheckFiles 做只读 schema 检查时需要一并解出的文件。
//
// 必须带上 -wal：面板的库是 WAL 模式，停服后最后一个事务常常还留在
// triune.db-wal 里没合并回主库。只解 triune.db 会看到「空库」，
// 表现为 no such table: settings——数据其实一点没少。
var dbCheckFiles = map[string]bool{
	"triune.db":     true,
	"triune.db-wal": true,
	"triune.db-shm": true,
}

// ExtractDB 把归档里的 triune.db（连同 -wal/-shm）解到 dir，返回主库路径。
//
// 用途：恢复前只读检查归档的 schema 版本，不必解整包。
// 加密归档需要提供口令。
func ExtractDB(archive, dir, passphrase string) (string, error) {
	rc, err := openArchive(archive, passphrase)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	gz, err := gzip.NewReader(rc)
	if err != nil {
		return "", fmt.Errorf("不是有效的 tar.gz: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	found := ""
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		name := filepath.ToSlash(filepath.Clean(hdr.Name))
		if !dbCheckFiles[name] || hdr.Typeflag != tar.TypeReg {
			continue
		}
		dst := filepath.Join(dir, name)
		out, err := os.Create(dst)
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return "", err
		}
		if err := out.Close(); err != nil {
			return "", err
		}
		if name == "triune.db" {
			found = dst
		}
	}
	if found == "" {
		return "", fmt.Errorf("归档里没有 triune.db")
	}
	return found, nil
}
