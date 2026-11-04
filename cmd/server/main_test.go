package main

import (
	"os"
	"path/filepath"
	"testing"

	"triune/internal/authn"
	"triune/internal/store"
)

// TestAbsDataDir：-data 传相对路径时必须变成绝对路径。
//
// 这个转换看着琐碎，但它决定「导入证书的入站能不能起来」：证书路径由
// filepath.Join(dataDir, …) 拼出来存进 DB，而内核的 cmd.Dir 是 cores/<内核>，
// 相对路径在那儿指向不存在的地方。曾经 `serve -data ./data` 起面板，
// 带证书的入站全部报 `open .../fullchain.pem: no such file`。
func TestAbsDataDir(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	// 相对路径 → 绝对，且落在当前工作目录下
	for _, in := range []string{"./data", "data", "./a/b/../data"} {
		got := absDataDir(in)
		if !filepath.IsAbs(got) {
			t.Errorf("absDataDir(%q) = %q，应转成绝对路径", in, got)
		}
		if filepath.Dir(got) != cwd && in == "./data" {
			t.Errorf("absDataDir(%q) = %q，应基于 cwd %q", in, got, cwd)
		}
	}

	// 本来就是绝对路径 → 原样返回（不能多此一举地 Clean 掉用户的选择）
	abs := filepath.Join(cwd, "some-data")
	if got := absDataDir(abs); got != abs {
		t.Errorf("绝对路径应原样返回，%q -> %q", abs, got)
	}
}

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
