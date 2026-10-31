// Package authn 负责管理面板的凭据存储与校验。
//
// 历史包袱：v0.8.6 及以前密码以**明文**存在 settings.admin_pass。现在改为
// bcrypt 哈希存 settings.admin_pass_hash，明文槽只保留作兼容与迁移：
//   - 登录命中明文槽 → 校验通过后静默改写为哈希并清空明文（用户无感）；
//   - 安装脚本落地的 bootstrap.creds 在首启时直接写成哈希，明文不落库。
//
// admin_pass 与 admin_pass_hash 同时为空 = 出厂默认（admin/admin），
// 此时登录成功会返回 must_change，前端强制改密。
//
// 会话失效：settings.session_epoch 每次改密/重置密码都会换成新的随机值。
// 会话在签发时记下当时的 epoch，请求时比对——不一致即失效。
// 之所以放 DB 而不是进程内变量：CLI 重置密码跑在另一个进程里，
// 只有 DB 才能让运行中的 daemon 立刻感知并踢掉旧会话。
package authn

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"

	"triune/internal/store"
)

// 设置键。
const (
	SettingUser  = "admin_user"
	SettingPlain = "admin_pass"      // 遗留明文槽（只读兼容，写入即清空）
	SettingHash  = "admin_pass_hash" // bcrypt 哈希
	SettingEpoch = "session_epoch"   // 会话纪元
)

// 出厂默认账号密码。
const (
	DefaultUser = "admin"
	DefaultPass = "admin"
)

// MinPasswordLen 密码最小长度（重置与改密共用）。
const MinPasswordLen = 8

// Hash 生成 bcrypt 哈希。
func Hash(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Verify 校验密码。legacy 为 true 表示命中的是遗留明文槽，调用方应调用
// MigrateLegacy 把它升级为哈希。
//
// 出厂默认（两个槽都空）按 admin/admin 校验，保证全新安装可登录。
func Verify(st *store.Store, plain string) (ok, legacy bool, err error) {
	hash, err := st.GetSetting(SettingHash)
	if err != nil {
		return false, false, err
	}
	if hash != "" {
		return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil, false, nil
	}
	old, err := st.GetSetting(SettingPlain)
	if err != nil {
		return false, false, err
	}
	if old == "" {
		return subtle.ConstantTimeCompare([]byte(plain), []byte(DefaultPass)) == 1, false, nil
	}
	return subtle.ConstantTimeCompare([]byte(plain), []byte(old)) == 1, true, nil
}

// SetPassword 设置新密码：写哈希、清空明文槽、递增会话纪元（踢掉所有在线会话）。
func SetPassword(st *store.Store, plain string) error {
	if len(plain) < MinPasswordLen {
		return fmt.Errorf("密码至少 %d 位", MinPasswordLen)
	}
	h, err := Hash(plain)
	if err != nil {
		return err
	}
	if err := st.SetSetting(SettingHash, h); err != nil {
		return err
	}
	if err := st.SetSetting(SettingPlain, ""); err != nil {
		return err
	}
	return BumpEpoch(st)
}

// MigrateLegacy 把遗留明文密码升级为哈希（不递增纪元，避免刚登录就被踢）。
func MigrateLegacy(st *store.Store) error {
	old, err := st.GetSetting(SettingPlain)
	if err != nil {
		return err
	}
	if old == "" {
		return nil
	}
	h, err := Hash(old)
	if err != nil {
		return err
	}
	if err := st.SetSetting(SettingHash, h); err != nil {
		return err
	}
	return st.SetSetting(SettingPlain, "")
}

// IsFactoryDefault 是否仍是出厂默认（从未设置过密码）。
func IsFactoryDefault(st *store.Store) bool {
	hash, _ := st.GetSetting(SettingHash)
	if hash != "" {
		return false
	}
	old, _ := st.GetSetting(SettingPlain)
	return old == ""
}

// User 返回当前管理用户名（未设置时为 admin）。
func User(st *store.Store) string {
	u, _ := st.GetSetting(SettingUser)
	if u == "" {
		return DefaultUser
	}
	return u
}

// Epoch 返回当前会话纪元（未设置时为空串）。
func Epoch(st *store.Store) string {
	v, _ := st.GetSetting(SettingEpoch)
	return v
}

// BumpEpoch 换一个新的会话纪元，使所有已签发的会话失效。
func BumpEpoch(st *store.Store) error {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	return st.SetSetting(SettingEpoch, hex.EncodeToString(b))
}

// Disable2FA 关闭登录二次验证（TOTP 与邮箱两种方式一起关）。
//
// 忘记密码往往伴随丢失 2FA 设备，重置密码时默认一并关闭，否则用户改完
// 密码仍然进不去；保留的场景用 --keep-2fa 显式声明。
func Disable2FA(st *store.Store) error {
	if err := st.SetSetting("auth_2fa_mode", ""); err != nil {
		return err
	}
	return st.SetSetting("totp_secret", "")
}

// RandomPassword 生成 n 位随机密码（大小写字母 + 数字，去掉易混淆的 0O1lI）。
func RandomPassword(n int) (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	if n <= 0 {
		return "", errors.New("长度必须为正")
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i, v := range buf {
		out[i] = alphabet[int(v)%len(alphabet)]
	}
	return string(out), nil
}
