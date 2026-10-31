package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"triune/internal/store"
)

// RFC 6238 附录 B 测试向量（SHA1，密钥 "12345678901234567890"）。
// 基准 8 位结果去掉前两位即为 6 位形式。
func TestTOTPKnownVectors(t *testing.T) {
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" // base32("12345678901234567890")
	key, err := decodeSecret(secret)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		unix int64
		want string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1234567890, "005924"},
		{2000000000, "279037"},
	}
	for _, c := range cases {
		got := totpAt(key, time.Unix(c.unix, 0))
		if got != c.want {
			t.Errorf("T=%d: want %s, got %s", c.unix, c.want, got)
		}
	}
}

func TestTOTPVerifyWindow(t *testing.T) {
	secret, err := generateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	key, _ := decodeSecret(secret)
	now := time.Now()
	cur := totpAt(key, now)
	prev := totpAt(key, now.Add(-totpPeriod))
	next := totpAt(key, now.Add(totpPeriod))
	if !verifyTOTP(secret, cur) {
		t.Error("当前周期口令应通过")
	}
	if !verifyTOTP(secret, prev) || !verifyTOTP(secret, next) {
		t.Error("相邻周期口令应通过（±1 窗口）")
	}
	if verifyTOTP(secret, totpAt(key, now.Add(-3*totpPeriod))) {
		t.Error("超出窗口的口令应拒绝")
	}
	if verifyTOTP(secret, "abcdef") || verifyTOTP(secret, "12345") || verifyTOTP(secret, "") {
		t.Error("格式非法的口令应拒绝")
	}
}

func TestMaskEmail(t *testing.T) {
	cases := map[string]string{
		"admin@example.com": "ad***@example.com",
		"a@b.com":           "a***@b.com",
		"broken":            "broken",
	}
	for in, want := range cases {
		if got := maskEmail(in); got != want {
			t.Errorf("maskEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEmailCodeState(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := New(st, nil, nil, dir)

	a.emailCode.code = "123456"
	a.emailCode.exp = time.Now().Add(time.Minute)
	if err := a.checkEmailCode("wrong"); err == nil {
		t.Error("错误验证码应失败")
	}
	if err := a.checkEmailCode(" 123456 "); err != nil {
		t.Errorf("带空格的正确验证码应通过: %v", err)
	}
	if err := a.checkEmailCode("123456"); err == nil {
		t.Error("验证码应一次性消费")
	}

	// 过期
	a.emailCode.code = "654321"
	a.emailCode.exp = time.Now().Add(-time.Second)
	if err := a.checkEmailCode("654321"); err == nil {
		t.Error("过期验证码应失败")
	}

	// 尝试次数限制
	a.emailCode.code = "111111"
	a.emailCode.exp = time.Now().Add(time.Minute)
	a.emailCode.tries = emailCodeMaxTries
	if err := a.checkEmailCode("111111"); err == nil {
		t.Error("超过尝试次数上限应失败")
	}
}

func postLogin(a *Server, body map[string]string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/login", bytes.NewReader(b))
	req.RemoteAddr = "10.9.9.9:12345"
	w := httptest.NewRecorder()
	a.handleLogin(w, req)
	return w
}

func TestLoginTOTPFlow(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := New(st, nil, nil, dir)

	secret, _ := generateTOTPSecret()
	// TOTP 密钥是账号级的（admin_users.totp_secret），不再是 settings 全局槽
	owner, err := st.OwnerAdmin()
	if err != nil {
		t.Fatal(err)
	}
	owner.TotpSecret = secret
	if err := st.UpdateAdmin(owner); err != nil {
		t.Fatal(err)
	}
	_ = st.SetSetting("auth_2fa_mode", "totp")
	key, _ := decodeSecret(secret)
	code := totpAt(key, time.Now())

	// 第一步：只有账密 → need_2fa
	w := postLogin(a, map[string]string{"user": "admin", "pass": "admin"})
	if w.Code != 200 {
		t.Fatalf("step1: want 200, got %d", w.Code)
	}
	var r1 map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &r1)
	if r1["need_2fa"] != "totp" {
		t.Fatalf("step1: want need_2fa=totp, got %v", r1)
	}

	// 第二步：错误口令 → 401；正确口令 → token
	if w = postLogin(a, map[string]string{"user": "admin", "pass": "admin", "code": "000000"}); w.Code != 401 {
		t.Fatalf("wrong code: want 401, got %d", w.Code)
	}
	if w = postLogin(a, map[string]string{"user": "admin", "pass": "admin", "code": code}); w.Code != 200 {
		t.Fatalf("right code: want 200, got %d (%s)", w.Code, w.Body.String())
	}
	var r2 map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &r2)
	if r2["token"] == "" {
		t.Fatal("right code: want token")
	}

	// 账密错误仍然 401（且不泄露 2FA 状态）
	if w = postLogin(a, map[string]string{"user": "admin", "pass": "bad"}); w.Code != 401 {
		t.Fatalf("bad pass: want 401, got %d", w.Code)
	}
}

func TestLoginEmailFlow(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := New(st, nil, nil, dir)
	_ = st.SetSetting("auth_2fa_mode", "email")
	_ = st.SetSetting("auth_email", "me@example.com")

	// 第一步（无 SMTP，发送失败）→ 500 带明确错误
	w := postLogin(a, map[string]string{"user": "admin", "pass": "admin"})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("step1 without smtp: want 500, got %d (%s)", w.Code, w.Body.String())
	}

	// 模拟验证码已发送（直接注入状态，跳过真实 SMTP）
	a.emailCode.mu.Lock()
	a.emailCode.code = "246810"
	a.emailCode.exp = time.Now().Add(time.Minute)
	a.emailCode.tries = 0
	a.emailCode.mu.Unlock()

	if w = postLogin(a, map[string]string{"user": "admin", "pass": "admin", "code": "246810"}); w.Code != 200 {
		t.Fatalf("right code: want 200, got %d (%s)", w.Code, w.Body.String())
	}
	// 一次性：再用同一个码应失败
	if w = postLogin(a, map[string]string{"user": "admin", "pass": "admin", "code": "246810"}); w.Code != 401 {
		t.Fatalf("replayed code: want 401, got %d", w.Code)
	}
}
