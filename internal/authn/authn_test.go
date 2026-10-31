package authn

import (
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
	if ok, legacy, _ := Verify(st, "admin"); !ok || legacy {
		t.Fatalf("admin 应校验通过且非 legacy, ok=%v legacy=%v", ok, legacy)
	}
	if ok, _, _ := Verify(st, "admin1"); ok {
		t.Fatal("错误密码不应通过")
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
	if ok, legacy, _ := Verify(st, "N3wPassw0rd"); !ok || legacy {
		t.Fatalf("新密码应校验通过, ok=%v legacy=%v", ok, legacy)
	}
	if ok, _, _ := Verify(st, "admin"); ok {
		t.Fatal("出厂密码不应再通过")
	}
	if p, _ := st.GetSetting(SettingPlain); p != "" {
		t.Fatalf("明文槽应为空，实际 %q", p)
	}
	if h, _ := st.GetSetting(SettingHash); h == "" || h == "N3wPassw0rd" {
		t.Fatalf("哈希槽异常: %q", h)
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
	ok, legacy, _ := Verify(st, "old-plain-pass")
	if !ok || !legacy {
		t.Fatalf("应命中明文槽, ok=%v legacy=%v", ok, legacy)
	}
	if err := MigrateLegacy(st); err != nil {
		t.Fatal(err)
	}
	if p, _ := st.GetSetting(SettingPlain); p != "" {
		t.Fatalf("迁移后明文应清空，实际 %q", p)
	}
	ok2, legacy2, _ := Verify(st, "old-plain-pass")
	if !ok2 || legacy2 {
		t.Fatalf("迁移后应走哈希校验, ok=%v legacy=%v", ok2, legacy2)
	}
}

// TestEpochBump 改密会换纪元，重置密码后旧纪元与新纪元不同。
func TestEpochBump(t *testing.T) {
	st, done := newStore(t)
	defer done()

	if e := Epoch(st); e != "" {
		t.Fatalf("初始纪元应为空，实际 %q", e)
	}
	if err := SetPassword(st, "first-pass-1"); err != nil {
		t.Fatal(err)
	}
	e1 := Epoch(st)
	if e1 == "" {
		t.Fatal("改密后应生成纪元")
	}
	if err := SetPassword(st, "second-pass-2"); err != nil {
		t.Fatal(err)
	}
	if e2 := Epoch(st); e2 == e1 {
		t.Fatal("再次改密应更换纪元")
	}
}

// TestDisable2FA 关闭二次验证会清掉模式与 TOTP 密钥。
func TestDisable2FA(t *testing.T) {
	st, done := newStore(t)
	defer done()

	_ = st.SetSetting("auth_2fa_mode", "totp")
	_ = st.SetSetting("totp_secret", "JBSWY3DPEHPK3PXP")
	if err := Disable2FA(st); err != nil {
		t.Fatal(err)
	}
	if m, _ := st.GetSetting("auth_2fa_mode"); m != "" {
		t.Fatalf("auth_2fa_mode 应清空，实际 %q", m)
	}
	if s, _ := st.GetSetting("totp_secret"); s != "" {
		t.Fatalf("totp_secret 应清空，实际 %q", s)
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
