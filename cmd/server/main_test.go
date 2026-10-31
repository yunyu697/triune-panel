package main

import (
	"os"
	"path/filepath"
	"testing"

	"triune/internal/authn"
	"triune/internal/store"
)

// TestApplyBootstrapCreds：全新安装消费初始凭据；已有密码时不覆盖；文件一律删除。
// 密码落库后是 bcrypt 哈希（存在 admin_users 里），明文槽必须为空。
func TestApplyBootstrapCreds(t *testing.T) {
	writeCreds := func(dir, content string) {
		if err := os.WriteFile(filepath.Join(dir, "bootstrap.creds"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// 场景 1：全新安装 → 应用初始凭据（哈希入库），文件删除
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	writeCreds(dir, "admin_user=ops\nadmin_pass=Rand0mPass16ch\n")
	applyBootstrapCreds(dir, st)
	if u, _, _ := authn.Authenticate(st, "ops", "Rand0mPass16ch"); u == nil {
		t.Fatal("scenario1: 初始密码应可校验通过")
	}
	if p, _ := st.GetSetting(authn.SettingPlain); p != "" {
		t.Fatalf("scenario1: 明文槽应为空，实际 %q", p)
	}
	owner, err := st.OwnerAdmin()
	if err != nil {
		t.Fatal(err)
	}
	if owner.Username != "ops" {
		t.Fatalf("scenario1: 用户名应改成 ops，实际 %q", owner.Username)
	}
	if owner.PassHash == "" {
		t.Fatal("scenario1: 应写入 bcrypt 哈希")
	}
	if authn.IsFactoryDefault(st) {
		t.Fatal("scenario1: 应用凭据后不应再是出厂默认")
	}
	if _, err := os.Stat(filepath.Join(dir, "bootstrap.creds")); !os.IsNotExist(err) {
		t.Fatalf("scenario1: bootstrap.creds should be removed")
	}
	st.Close()

	// 场景 2：密码已自定义 → 忽略且不覆盖，文件删除
	dir2 := t.TempDir()
	st2, _ := store.Open(dir2)
	defer st2.Close()
	if err := authn.SetPassword(st2, "real-password"); err != nil {
		t.Fatal(err)
	}
	writeCreds(dir2, "admin_pass=leaked-old\n")
	applyBootstrapCreds(dir2, st2)
	if u, _, _ := authn.Authenticate(st2, "admin", "real-password"); u == nil {
		t.Fatal("scenario2: 真实密码必须继续有效")
	}
	if u, _, _ := authn.Authenticate(st2, "admin", "leaked-old"); u != nil {
		t.Fatal("scenario2: 初始凭据不得覆盖真实密码")
	}
	if _, err := os.Stat(filepath.Join(dir2, "bootstrap.creds")); !os.IsNotExist(err) {
		t.Fatalf("scenario2: bootstrap.creds should be removed")
	}

	// 场景 3：无文件 → 无副作用
	dir3 := t.TempDir()
	st3, _ := store.Open(dir3)
	defer st3.Close()
	applyBootstrapCreds(dir3, st3) // 不应 panic 或报错
	owner3, _ := st3.OwnerAdmin()
	if owner3.Username != "admin" || owner3.PassHash != "" {
		t.Fatalf("scenario3: want untouched, got %+v", owner3)
	}
}
