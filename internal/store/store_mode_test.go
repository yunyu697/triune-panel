package store

import (
	"os"
	"path/filepath"
	"testing"
)

// 数据目录与库文件只给运行面板的账号。
//
// 库里存着密码哈希、TOTP 密钥、订阅令牌、SMTP 与 ACME 凭据。原来的
// MkdirAll(0755) 意味着同机任何账号都能读走这些——共享主机与「一台 VPS
// 跑好几个服务」是最常见的部署形态。这里钉住：新建与老目录都要收紧。
func TestOpenTightensDataDirPermissions(t *testing.T) {
	dir := t.TempDir()
	// 模拟旧安装：目录 0755
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if info, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	} else if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("数据目录权限 = %04o，应为 0700", got)
	}
	if info, err := os.Stat(filepath.Join(dir, "triune.db")); err != nil {
		t.Fatalf("库文件不存在: %v", err)
	} else if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("库文件权限 = %04o，应为 0600", got)
	}
}
