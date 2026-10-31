// Package authn 负责管理面板的账号存储、密码校验与角色判定。
//
// 历史沿革（三阶段，读代码时容易踩）：
//  1. v0.8.6 及以前：密码**明文**存 settings.admin_pass；
//  2. 之后：改为 bcrypt 哈希存 settings.admin_pass_hash，明文槽只留作兼容
//     ——登录命中明文槽则校验通过后静默改写为哈希并清空明文；
//  3. v0.9.6 起：账号迁到 admin_users 表（多管理员 + 角色），settings 里那
//     两个槽由迁移 v4 搬进表里，此后只作为「遗留明文槽」被读取一次。
//
// 出厂默认 = owner 的 pass_hash 为空且遗留明文槽也为空，此时按 admin/admin
// 放行并返回 must_change，前端强制改密。
//
// 会话失效机制：v0.9.6 之前靠 settings.session_epoch（全局纪元，改密即全站
// 踢线）。多管理员之后这是错的——A 改密码不该把 B 踢下线。现在会话记下
// 签发时的**密码哈希指纹**（Fingerprint），请求时与库里的哈希比对：
// 只有「这个人的密码变了 / 账号被删了 / 被停用了」才会失效。
// 之所以存 DB 而不是进程内变量：CLI 重置密码跑在另一个进程里，
// 只有 DB 才能让运行中的 daemon 立刻感知并踢掉旧会话。
package authn

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"triune/internal/store"
)

// 角色。权限从高到低：owner > admin > viewer。
const (
	// RoleOwner 超级管理员：唯一、不可删除/降级/停用，只有它能管理管理员账号。
	RoleOwner = "owner"
	// RoleAdmin 管理员：业务全权限（入站/用户/应用/回滚/设置/证书），不能管账号。
	RoleAdmin = "admin"
	// RoleViewer 只读：能看状态、入站、流量、日志，任何写操作都被拒。
	RoleViewer = "viewer"
)

// ValidRole 角色名是否合法。
func ValidRole(r string) bool {
	return r == RoleOwner || r == RoleAdmin || r == RoleViewer
}

// RoleRank 角色权限等级（未知角色为 0，任何鉴权都不会通过）。
func RoleRank(r string) int {
	switch r {
	case RoleOwner:
		return 3
	case RoleAdmin:
		return 2
	case RoleViewer:
		return 1
	}
	return 0
}

// 设置键。
const (
	SettingUser  = "admin_user"      // 遗留单账号用户名槽（v4 迁移后只读）
	SettingPlain = "admin_pass"      // 遗留明文槽（只读兼容，写入即清空）
	SettingHash  = "admin_pass_hash" // 遗留 bcrypt 哈希槽（v4 迁移后只读）
)

// 出厂默认账号密码。
const (
	DefaultUser = "admin"
	DefaultPass = "admin"
)

// MinPasswordLen 密码最小长度（重置与改密共用）。
const MinPasswordLen = 8

// ErrDisabled 账号已停用。只有密码正确时才会返回，避免变成用户枚举探针。
var ErrDisabled = errors.New("账号已停用，请联系超级管理员")

// dummyHash 用户不存在时用来消耗一次 bcrypt 比较，让响应时间与「用户存在但
// 密码错」一致，避免靠耗时差异枚举用户名。
var dummyHash = []byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy")

// Hash 生成 bcrypt 哈希。
func Hash(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Fingerprint 会话指纹：密码哈希的 sha256 前 8 字节（十六进制 16 位）。
//
// 用它而不是直接存哈希：会话表在内存里，没必要留着完整 bcrypt 串；
// 而且哈希本身每次重设都会变（bcrypt 自带盐），天然满足「改密即失效」。
func Fingerprint(hash string) string {
	sum := sha256.Sum256([]byte("triune-session-v1|" + hash))
	return hex.EncodeToString(sum[:8])
}

// Authenticate 校验用户名与密码。
//
// 返回值：
//   - u 为 nil 且 err 为 nil：用户名或密码错误（调用方统一回「用户名或密码错误」）；
//   - legacy 为 true：命中的是遗留明文槽，调用方应调用 MigrateLegacy 升级为哈希；
//   - err 为 ErrDisabled：密码正确但账号被停用。
func Authenticate(st *store.Store, username, plain string) (u *store.AdminUser, legacy bool, err error) {
	username = strings.TrimSpace(username)
	if username == "" || plain == "" {
		return nil, false, nil
	}
	u, err = st.GetAdminByName(username)
	if err == sql.ErrNoRows {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(plain))
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	ok := false
	if u.PassHash != "" {
		ok = bcrypt.CompareHashAndPassword([]byte(u.PassHash), []byte(plain)) == nil
	} else if u.Role == RoleOwner {
		// 只有 owner 允许「空哈希」：那是出厂默认 / 遗留明文槽的形态。
		// 其它账号空哈希一律拒绝——绝不能让它变成「任意密码都能进」。
		old, err := st.GetSetting(SettingPlain)
		if err != nil {
			return nil, false, err
		}
		if old != "" {
			ok = subtle.ConstantTimeCompare([]byte(plain), []byte(old)) == 1
			legacy = ok
		} else {
			ok = subtle.ConstantTimeCompare([]byte(plain), []byte(DefaultPass)) == 1
		}
	}
	if !ok {
		return nil, false, nil
	}
	if !u.Enabled {
		return u, legacy, ErrDisabled
	}
	return u, legacy, nil
}

// MigrateLegacy 把遗留明文密码升级为 owner 的哈希（不改哈希指纹之外的东西）。
func MigrateLegacy(st *store.Store, u *store.AdminUser) error {
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
	u.PassHash = h
	if err := st.UpdateAdmin(u); err != nil {
		return err
	}
	return st.SetSetting(SettingPlain, "")
}

// SetPasswordFor 设置某个账号的密码（清空遗留明文槽，旧会话因指纹变化失效）。
func SetPasswordFor(st *store.Store, id int64, plain string) error {
	if len(plain) < MinPasswordLen {
		return fmt.Errorf("密码至少 %d 位", MinPasswordLen)
	}
	u, err := st.GetAdmin(id)
	if err != nil {
		return err
	}
	h, err := Hash(plain)
	if err != nil {
		return err
	}
	u.PassHash = h
	if err := st.UpdateAdmin(u); err != nil {
		return err
	}
	// 密码一旦落成哈希，遗留明文槽就再无意义，顺手清掉（CLI 重置时尤其重要：
	// 否则明文槽里的旧密码还能登录）。
	return st.SetSetting(SettingPlain, "")
}

// SetPassword 设置 owner 的密码（CLI 与兼容路径用）。
func SetPassword(st *store.Store, plain string) error {
	u, err := st.OwnerAdmin()
	if err != nil {
		return fmt.Errorf("找不到超级管理员账号: %w", err)
	}
	return SetPasswordFor(st, u.ID, plain)
}

// IsFactoryDefault 是否仍是出厂默认（owner 从未设置过密码）。
func IsFactoryDefault(st *store.Store) bool {
	u, err := st.OwnerAdmin()
	if err != nil {
		return true // 账号表为空：等同于全新库，首登必须强制改密
	}
	if u.PassHash != "" {
		return false
	}
	old, _ := st.GetSetting(SettingPlain)
	return old == ""
}

// User 返回超级管理员用户名（未设置时为 admin）。
func User(st *store.Store) string {
	if u, err := st.OwnerAdmin(); err == nil && u.Username != "" {
		return u.Username
	}
	if v, _ := st.GetSetting(SettingUser); v != "" {
		return v
	}
	return DefaultUser
}

// Disable2FA 关闭登录二次验证（模式与所有账号的 TOTP 密钥一起清）。
//
// 忘记密码往往伴随丢失 2FA 设备，重置密码时默认一并关闭，否则用户改完
// 密码仍然进不去；保留的场景用 --keep-2fa 显式声明。
func Disable2FA(st *store.Store) error {
	if err := st.SetSetting("auth_2fa_mode", ""); err != nil {
		return err
	}
	return st.ClearAll2FA()
}

// 用户名字符集：字母数字与 . _ -，2~32 位。刻意不允许空格与中文——
// 用户名会出现在审计日志与命令行参数里，限制字符集能省掉一堆转义问题。
var usernameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{2,32}$`)

// ValidateUsername 校验用户名格式。
func ValidateUsername(name string) error {
	if !usernameRe.MatchString(name) {
		return errors.New("用户名只能是 2~32 位的字母、数字或 . _ -")
	}
	return nil
}

// CreateUser 新建一个管理员账号（含密码哈希与角色校验）。
func CreateUser(st *store.Store, username, plain, role string) (*store.AdminUser, error) {
	username = strings.TrimSpace(username)
	if err := ValidateUsername(username); err != nil {
		return nil, err
	}
	if !ValidRole(role) {
		return nil, errors.New("角色无效")
	}
	if role == RoleOwner {
		// owner 由迁移唯一播种，不允许通过接口再造一个——否则「唯一超级管理员」
		// 这条不变量会从入口就被破坏。
		return nil, errors.New("超级管理员账号唯一，不能新建")
	}
	if len(plain) < MinPasswordLen {
		return nil, fmt.Errorf("密码至少 %d 位", MinPasswordLen)
	}
	h, err := Hash(plain)
	if err != nil {
		return nil, err
	}
	u := &store.AdminUser{Username: username, PassHash: h, Role: role, Enabled: true}
	if err := st.CreateAdmin(u); err != nil {
		return nil, err
	}
	return u, nil
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
