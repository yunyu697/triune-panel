package store

import (
	"errors"
	"testing"
)

// TestAdminSeededOnFreshDB 全新库迁移后必须有一条 owner，且是出厂默认形态
// （空哈希 = 从未设置过密码），否则首登强制改密这条防线就断了。
func TestAdminSeededOnFreshDB(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	list, err := st.ListAdmins()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("全新库应有且仅有 1 个管理员，实际 %d", len(list))
	}
	u := list[0]
	if u.Username != "admin" || u.Role != "owner" || !u.Enabled || u.PassHash != "" {
		t.Fatalf("播种的 owner 形态不对: %+v", u)
	}
}

// TestAdminMigrationFromLegacySettings 老库升级：settings 里的单账号必须原样
// 搬进 admin_users（用户名与哈希都不能丢，否则升级即锁死面板）。
func TestAdminMigrationFromLegacySettings(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// 模拟 v0.9.5 的库：账号在 settings 里，账号表还是空的
	if err := st.SetSetting("admin_user", "ops-master"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("admin_pass_hash", "$2a$10$abcdefghijklmnopqrstuv"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`DELETE FROM admin_users`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE settings SET value='3' WHERE key='schema_version'`); err != nil {
		t.Fatal(err)
	}
	st.Close()

	// 重新打开 → 迁移 v4 重跑并播种
	st2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()

	u, err := st2.OwnerAdmin()
	if err != nil {
		t.Fatal(err)
	}
	if u.Username != "ops-master" {
		t.Fatalf("用户名应从 settings 搬过来，实际 %q", u.Username)
	}
	if u.PassHash != "$2a$10$abcdefghijklmnopqrstuv" {
		t.Fatalf("密码哈希应原样搬过来，实际 %q", u.PassHash)
	}
	if u.Role != "owner" {
		t.Fatalf("升级后的账号应是 owner，实际 %q", u.Role)
	}
}

// TestAdminCRUD 增删改查 + 用户名唯一 + owner 计数。
func TestAdminCRUD(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	u := &AdminUser{Username: "alice", PassHash: "h1", Role: "admin", Enabled: true}
	if err := st.CreateAdmin(u); err != nil {
		t.Fatal(err)
	}
	if u.ID == 0 {
		t.Fatal("CreateAdmin 应回填 id")
	}
	if err := st.CreateAdmin(&AdminUser{Username: "alice", Role: "viewer"}); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("重名应返回 ErrUsernameTaken，实际 %v", err)
	}

	got, err := st.GetAdminByName("alice")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != u.ID || got.Role != "admin" || !got.Enabled || got.PassHash != "h1" {
		t.Fatalf("按名查询结果不对: %+v", got)
	}

	got.Role = "viewer"
	got.Enabled = false
	got.PassHash = "h2"
	got.TotpSecret = "SECRET"
	if err := st.UpdateAdmin(got); err != nil {
		t.Fatal(err)
	}
	again, _ := st.GetAdmin(u.ID)
	if again.Role != "viewer" || again.Enabled || again.PassHash != "h2" || again.TotpSecret != "SECRET" {
		t.Fatalf("更新未生效: %+v", again)
	}

	// 用户名改成已存在的 → 唯一约束
	again.Username = "admin"
	if err := st.UpdateAdmin(again); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("改名冲突应返回 ErrUsernameTaken，实际 %v", err)
	}

	if err := st.DeleteAdmin(u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetAdmin(u.ID); err == nil {
		t.Fatal("删除后应查不到")
	}
}

// TestCountOwners 只统计「启用中的 owner」——停用的 owner 不算数，
// 否则「最后一个 owner 不能停用」这条守卫会被自己绕过。
func TestCountOwners(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	n, err := st.CountOwners()
	if err != nil || n != 1 {
		t.Fatalf("全新库应有 1 个启用中的 owner，实际 %d err=%v", n, err)
	}
	owner, _ := st.OwnerAdmin()
	owner.Enabled = false
	if err := st.UpdateAdmin(owner); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.CountOwners(); n != 0 {
		t.Fatalf("停用后应为 0，实际 %d", n)
	}
}

// TestTouchAdminLogin 记录上次登录来源与时间，失败不影响登录本身。
func TestTouchAdminLogin(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	owner, _ := st.OwnerAdmin()
	if err := st.TouchAdminLogin(owner.ID, "203.0.113.7"); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetAdmin(owner.ID)
	if got.LastIP != "203.0.113.7" || got.LastLogin == "" {
		t.Fatalf("上次登录未记录: %+v", got)
	}
}
