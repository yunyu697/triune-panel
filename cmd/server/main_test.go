package main

import (
	"os"
	"path/filepath"
	"testing"

	"triune/internal/store"
)

// TestApplyBootstrapCreds：全新安装消费初始凭据；已有密码时不覆盖；文件一律删除。
func TestApplyBootstrapCreds(t *testing.T) {
	writeCreds := func(dir, content string) {
		if err := os.WriteFile(filepath.Join(dir, "bootstrap.creds"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// 场景 1：全新安装 → 应用初始凭据，文件删除
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	writeCreds(dir, "admin_user=admin\nadmin_pass=Rand0mPass16ch\n")
	applyBootstrapCreds(dir, st)
	if p, _ := st.GetSetting("admin_pass"); p != "Rand0mPass16ch" {
		t.Fatalf("scenario1: want pass applied, got %q", p)
	}
	if _, err := os.Stat(filepath.Join(dir, "bootstrap.creds")); !os.IsNotExist(err) {
		t.Fatalf("scenario1: bootstrap.creds should be removed")
	}
	st.Close()

	// 场景 2：密码已自定义 → 忽略且不覆盖，文件删除
	dir2 := t.TempDir()
	st2, _ := store.Open(dir2)
	defer st2.Close()
	if err := st2.SetSetting("admin_pass", "real-password"); err != nil {
		t.Fatal(err)
	}
	writeCreds(dir2, "admin_pass=leaked-old\n")
	applyBootstrapCreds(dir2, st2)
	if p, _ := st2.GetSetting("admin_pass"); p != "real-password" {
		t.Fatalf("scenario2: real password must survive, got %q", p)
	}
	if _, err := os.Stat(filepath.Join(dir2, "bootstrap.creds")); !os.IsNotExist(err) {
		t.Fatalf("scenario2: bootstrap.creds should be removed")
	}

	// 场景 3：无文件 → 无副作用
	dir3 := t.TempDir()
	st3, _ := store.Open(dir3)
	defer st3.Close()
	applyBootstrapCreds(dir3, st3) // 不应 panic 或报错
	if u, _ := st3.GetSetting("admin_user"); u != "" {
		t.Fatalf("scenario3: want untouched, got %q", u)
	}
}
