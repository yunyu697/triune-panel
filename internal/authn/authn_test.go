package authn

import (
	"errors"
	"testing"

	"triune/internal/store"
)

func newStore(t *testing.T) (*store.Store, func()) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return st, func() { st.Close() }
}

// TestFactoryDefault 出厂状态下按 admin/admin 校验，且 must_change 为真。
func TestFactoryDefault(t *testing.T) {
	st, done := newStore(t)
	defer done()

	if !IsFactoryDefault(st) {
		t.Fatal("全新库应判定为出厂默认")
	}
	u, legacy, err := Authenticate(st, DefaultUser, "admin")
	if err != nil || u == nil || legacy {
		t.Fatalf("admin 应校验通过且非 legacy, u=%v legacy=%v err=%v", u, legacy, err)
	}
	if u.Role != RoleOwner {
		t.Fatalf("迁移播种的账号应为 owner，实际 %q", u.Role)
	}
	if u, _, _ := Authenticate(st, DefaultUser, "admin1"); u != nil {
		t.Fatal("错误密码不应通过")
	}
	if u, _, _ := Authenticate(st, "nobody", "admin"); u != nil {
		t.Fatal("不存在的用户不应通过")
	}
}

// TestSetPasswordThenVerify 设置密码后：哈希可校验、明文槽清空、不再是出厂默认。
func TestSetPasswordThenVerify(t *testing.T) {
	st, done := newStore(t)
	defer done()

	if err := SetPassword(st, "N3wPassw0rd"); err != nil {
		t.Fatal(err)
	}
	if IsFactoryDefault(st) {
		t.Fatal("设置密码后不应再是出厂默认")
	}
	if u, legacy, _ := Authenticate(st, DefaultUser, "N3wPassw0rd"); u == nil || legacy {
		t.Fatalf("新密码应校验通过, u=%v legacy=%v", u, legacy)
	}
	if u, _, _ := Authenticate(st, DefaultUser, "admin"); u != nil {
		t.Fatal("出厂密码不应再通过")
	}
	if p, _ := st.GetSetting(SettingPlain); p != "" {
		t.Fatalf("明文槽应为空，实际 %q", p)
	}
	// 哈希落到了账号表，而不是遗留的 settings 槽
	owner, err := st.OwnerAdmin()
	if err != nil {
		t.Fatal(err)
	}
	if owner.PassHash == "" || owner.PassHash == "N3wPassw0rd" {
		t.Fatalf("账号表哈希异常: %q", owner.PassHash)
	}
}

// TestMinLength 密码过短应拒绝。
func TestMinLength(t *testing.T) {
	st, done := newStore(t)
	defer done()
	if err := SetPassword(st, "short"); err == nil {
		t.Fatal("短密码应报错")
	}
}

// TestLegacyMigration 遗留明文密码：登录校验标记 legacy，迁移后转哈希且明文清空。
func TestLegacyMigration(t *testing.T) {
	st, done := newStore(t)
	defer done()

	if err := st.SetSetting(SettingPlain, "old-plain-pass"); err != nil {
		t.Fatal(err)
	}
	if IsFactoryDefault(st) {
		t.Fatal("有明文密码时不算出厂默认")
	}
	u, legacy, err := Authenticate(st, DefaultUser, "old-plain-pass")
	if u == nil || !legacy || err != nil {
		t.Fatalf("应命中明文槽, u=%v legacy=%v err=%v", u, legacy, err)
	}
	if err := MigrateLegacy(st, u); err != nil {
		t.Fatal(err)
	}
	if p, _ := st.GetSetting(SettingPlain); p != "" {
		t.Fatalf("迁移后明文应清空，实际 %q", p)
	}
	u2, legacy2, _ := Authenticate(st, DefaultUser, "old-plain-pass")
	if u2 == nil || legacy2 {
		t.Fatalf("迁移后应走哈希校验, u=%v legacy=%v", u2, legacy2)
	}
}

// TestFingerprintChangesOnPasswordChange 指纹随密码哈希变化——
// 这是「改密踢掉旧会话」的唯一依据，必须锁死。
func TestFingerprintChangesOnPasswordChange(t *testing.T) {
	st, done := newStore(t)
	defer done()

	if err := SetPassword(st, "first-pass-1"); err != nil {
		t.Fatal(err)
	}
	u1, _ := st.OwnerAdmin()
	fp1 := Fingerprint(u1.PassHash)
	if fp1 == "" || fp1 == Fingerprint("") {
		t.Fatalf("指纹异常: %q", fp1)
	}
	if err := SetPassword(st, "second-pass-2"); err != nil {
		t.Fatal(err)
	}
	u2, _ := st.OwnerAdmin()
	if Fingerprint(u2.PassHash) == fp1 {
		t.Fatal("改密后指纹必须变化")
	}
	// 同一哈希稳定（否则每次请求都会把会话判失效）
	if Fingerprint(u2.PassHash) != Fingerprint(u2.PassHash) {
		t.Fatal("同一哈希的指纹应稳定")
	}
}

// TestDisable2FA 关闭二次验证会清掉模式与所有账号的 TOTP 密钥。
func TestDisable2FA(t *testing.T) {
	st, done := newStore(t)
	defer done()

	_ = st.SetSetting("auth_2fa_mode", "totp")
	owner, err := st.OwnerAdmin()
	if err != nil {
		t.Fatal(err)
	}
	owner.TotpSecret = "JBSWY3DPEHPK3PXP"
	if err := st.UpdateAdmin(owner); err != nil {
		t.Fatal(err)
	}
	if err := Disable2FA(st); err != nil {
		t.Fatal(err)
	}
	if m, _ := st.GetSetting("auth_2fa_mode"); m != "" {
		t.Fatalf("auth_2fa_mode 应清空，实际 %q", m)
	}
	owner2, _ := st.OwnerAdmin()
	if owner2.TotpSecret != "" {
		t.Fatalf("账号 TOTP 密钥应清空，实际 %q", owner2.TotpSecret)
	}
}

// TestCreateUser 新建账号：密码独立、角色独立、用户名重复被拒、owner 不能新建。
func TestCreateUser(t *testing.T) {
	st, done := newStore(t)
	defer done()

	u, err := CreateUser(st, "alice", "alice-password", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if u.ID == 0 || u.Role != RoleAdmin || !u.Enabled {
		t.Fatalf("新建账号字段异常: %+v", u)
	}
	if got, _, _ := Authenticate(st, "alice", "alice-password"); got == nil || got.ID != u.ID {
		t.Fatal("新账号应能用自己的密码登录")
	}
	// 各账号密码互不影响
	if got, _, _ := Authenticate(st, "alice", DefaultPass); got != nil {
		t.Fatal("新账号不应接受出厂密码")
	}
	if got, _, _ := Authenticate(st, DefaultUser, "alice-password"); got != nil {
		t.Fatal("owner 不应接受他人密码")
	}

	if _, err := CreateUser(st, "alice", "another-password", RoleViewer); !errors.Is(err, store.ErrUsernameTaken) {
		t.Fatalf("重名应返回 ErrUsernameTaken，实际 %v", err)
	}
	if _, err := CreateUser(st, "root2", "another-password", RoleOwner); err == nil {
		t.Fatal("不应允许新建第二个 owner")
	}
	if _, err := CreateUser(st, "bad name", "another-password", RoleViewer); err == nil {
		t.Fatal("非法用户名应被拒")
	}
	if _, err := CreateUser(st, "bob", "short", RoleViewer); err == nil {
		t.Fatal("短密码应被拒")
	}
}

// TestDisabledAccount 停用后即使密码正确也登不进，且返回 ErrDisabled。
func TestDisabledAccount(t *testing.T) {
	st, done := newStore(t)
	defer done()

	u, err := CreateUser(st, "carol", "carol-password", RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	u.Enabled = false
	if err := st.UpdateAdmin(u); err != nil {
		t.Fatal(err)
	}
	if got, _, err := Authenticate(st, "carol", "carol-password"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("停用账号应返回 ErrDisabled，实际 u=%v err=%v", got, err)
	}
	// 密码错误时仍然是「用户名或密码错误」，不暴露账号存在
	if got, _, err := Authenticate(st, "carol", "wrong-password"); got != nil || err != nil {
		t.Fatalf("密码错误应返回 nil/nil，实际 u=%v err=%v", got, err)
	}
}

// TestRoleRank 角色等级顺序：owner > admin > viewer > 未知。
func TestRoleRank(t *testing.T) {
	if !(RoleRank(RoleOwner) > RoleRank(RoleAdmin) &&
		RoleRank(RoleAdmin) > RoleRank(RoleViewer) &&
		RoleRank(RoleViewer) > RoleRank("nonsense")) {
		t.Fatal("角色等级顺序不对")
	}
	for _, r := range []string{RoleOwner, RoleAdmin, RoleViewer} {
		if !ValidRole(r) {
			t.Fatalf("%s 应是合法角色", r)
		}
	}
	if ValidRole("root") {
		t.Fatal("root 不是合法角色")
	}
}

// TestRandomPassword 随机密码长度正确、字符集不含易混淆字符、两次不重复。
func TestRandomPassword(t *testing.T) {
	p1, err := RandomPassword(16)
	if err != nil {
		t.Fatal(err)
	}
	p2, _ := RandomPassword(16)
	if len(p1) != 16 {
		t.Fatalf("长度应为 16，实际 %d", len(p1))
	}
	if p1 == p2 {
		t.Fatal("两次生成不应相同")
	}
	for _, c := range p1 {
		if c == '0' || c == 'O' || c == '1' || c == 'l' || c == 'I' {
			t.Fatalf("不应包含易混淆字符: %q", p1)
		}
	}
	if _, err := RandomPassword(0); err == nil {
		t.Fatal("长度 0 应报错")
	}
}
