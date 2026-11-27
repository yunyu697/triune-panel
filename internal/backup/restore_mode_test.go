package backup

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 恢复出来的文件必须是「只有运行面板的账号能读写」。
//
// 归档里的 mode 不可信：它可能是从 0644 的旧安装打出来的，而这棵树里有
// 数据库（密码哈希、TOTP 密钥、订阅令牌）与证书私钥。之前恢复时硬编码
// 0644/0755，等于把备份里最敏感的东西恢复成同机任何账号都能读。
func TestRestoreTightensPermissions(t *testing.T) {
	src := t.TempDir()
	// 模拟旧安装：文件 0644、目录 0755
	mustWrite(t, filepath.Join(src, "triune.db"), "db-bytes")
	if err := os.Chmod(filepath.Join(src, "triune.db"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(src, "certs", "a.com", "key.pem"), "PRIVATE")
	if err := os.Chmod(filepath.Join(src, "certs"), 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := Create(Options{DataDir: src, Now: time.Date(2026, 11, 28, 1, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}

	dst := t.TempDir()
	if _, err := Restore(RestoreOptions{Archive: res.Path, DataDir: dst,
		Now: time.Date(2026, 11, 28, 1, 5, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{"triune.db", filepath.Join("certs", "a.com", "key.pem")} {
		st, err := os.Stat(filepath.Join(dst, p))
		if err != nil {
			t.Fatalf("%s 未恢复: %v", p, err)
		}
		if got := st.Mode().Perm(); got != 0o600 {
			t.Errorf("%s 权限 = %04o，应为 0600（同机其他账号不能读）", p, got)
		}
	}
	if st, err := os.Stat(filepath.Join(dst, "certs")); err != nil {
		t.Fatal(err)
	} else if got := st.Mode().Perm(); got != 0o700 {
		t.Errorf("恢复出来的目录权限 = %04o，应为 0700", got)
	}
	if st, err := os.Stat(dst); err != nil {
		t.Fatal(err)
	} else if got := st.Mode().Perm(); got != 0o700 {
		t.Errorf("数据目录权限 = %04o，应为 0700", got)
	}
}
