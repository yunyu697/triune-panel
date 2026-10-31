package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// AdminUser 一个面板管理员账号。
//
// v0.9.6 之前面板只有一个账号，凭据存在 settings（admin_user / admin_pass_hash），
// 于是审计日志里的 Actor 永远是同一个人——「谁改的」这件事实际上无法追溯。
// 现在账号落在独立的 admin_users 表，会话与审计都带真实身份。
//
// PassHash / TotpSecret 是机密，永不进入 JSON 响应（json:"-"），
// 序列化时漏一个字段就等于把哈希和 2FA 密钥发给前端。
type AdminUser struct {
	ID         int64     `json:"id"`
	Username   string    `json:"username"`
	PassHash   string    `json:"-"`
	Role       string    `json:"role"`
	Enabled    bool      `json:"enabled"`
	TotpSecret string    `json:"-"`
	LastIP     string    `json:"last_ip"`
	LastLogin  string    `json:"last_login"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// ErrUsernameTaken 用户名已被占用（UNIQUE 约束）。
var ErrUsernameTaken = errors.New("用户名已存在")

const adminCols = `id,username,pass_hash,role,enabled,totp_secret,last_ip,last_login,created_at,updated_at`

func scanAdmin(row interface{ Scan(...any) error }) (*AdminUser, error) {
	var u AdminUser
	var enabled int
	if err := row.Scan(&u.ID, &u.Username, &u.PassHash, &u.Role, &enabled,
		&u.TotpSecret, &u.LastIP, &u.LastLogin, &u.CreatedAt, &u.UpdatedAt); err != nil {
		return nil, err
	}
	u.Enabled = enabled == 1
	return &u, nil
}

// isUniqueErr 判断是否命中唯一约束。modernc 驱动没有导出错误码，
// 只能按错误文本判断；把判断收敛在这里，避免各处字符串比较。
func isUniqueErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") || strings.Contains(msg, "constraint failed")
}

// ListAdmins 按 id 升序返回全部管理员。
func (s *Store) ListAdmins() ([]*AdminUser, error) {
	rows, err := s.db.Query(`SELECT ` + adminCols + ` FROM admin_users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AdminUser
	for rows.Next() {
		u, err := scanAdmin(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// GetAdmin 按 id 取管理员。
func (s *Store) GetAdmin(id int64) (*AdminUser, error) {
	u, err := scanAdmin(s.db.QueryRow(`SELECT `+adminCols+` FROM admin_users WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("管理员 %d 不存在", id)
	}
	return u, err
}

// GetAdminByName 按用户名取管理员（登录用）。
func (s *Store) GetAdminByName(name string) (*AdminUser, error) {
	u, err := scanAdmin(s.db.QueryRow(`SELECT `+adminCols+` FROM admin_users WHERE username=?`, name))
	if err == sql.ErrNoRows {
		return nil, sql.ErrNoRows
	}
	return u, err
}

// OwnerAdmin 返回超级管理员（角色 owner 中 id 最小的那个）。
//
// 「owner 只有一条」靠 API 层保证；这里取最小 id 是为了在历史数据异常时
// 也能稳定返回一个确定的结果，而不是随机挑一个。
func (s *Store) OwnerAdmin() (*AdminUser, error) {
	u, err := scanAdmin(s.db.QueryRow(`SELECT ` + adminCols + ` FROM admin_users WHERE role='owner' ORDER BY id LIMIT 1`))
	if err == sql.ErrNoRows {
		return nil, sql.ErrNoRows
	}
	return u, err
}

// CountOwners 统计仍处于启用状态的超级管理员数量。
//
// 删除/停用/降级前必须检查：把最后一个 owner 弄掉，面板就永久失去
// 管理管理员的能力，只能靠命令行救回来。
func (s *Store) CountOwners() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM admin_users WHERE role='owner' AND enabled=1`).Scan(&n)
	return n, err
}

// CreateAdmin 新建管理员。用户名重复返回 ErrUsernameTaken。
func (s *Store) CreateAdmin(u *AdminUser) error {
	res, err := s.db.Exec(`INSERT INTO admin_users(username,pass_hash,role,enabled,totp_secret) VALUES(?,?,?,?,?)`,
		u.Username, u.PassHash, u.Role, b2i(u.Enabled), u.TotpSecret)
	if err != nil {
		if isUniqueErr(err) {
			return ErrUsernameTaken
		}
		return err
	}
	u.ID, _ = res.LastInsertId()
	return nil
}

// UpdateAdmin 更新可写字段（用户名/角色/启用/密码哈希/2FA 密钥）。
//
// 与 inbound/client 不同，这里刻意保持「整行写入」语义：调用方总是先从库里
// 读出完整记录再改需要的字段（见 api 层的 updateAdmin），不存在前端只提交
// 部分字段的情况——管理员记录本来就不是表单整体提交的对象。
func (s *Store) UpdateAdmin(u *AdminUser) error {
	_, err := s.db.Exec(`UPDATE admin_users SET username=?,pass_hash=?,role=?,enabled=?,totp_secret=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		u.Username, u.PassHash, u.Role, b2i(u.Enabled), u.TotpSecret, u.ID)
	if err != nil && isUniqueErr(err) {
		return ErrUsernameTaken
	}
	return err
}

// TouchAdminLogin 记录最近一次成功登录的来源与时间。
// 写失败只影响「上次登录」这一条展示信息，不影响登录本身。
func (s *Store) TouchAdminLogin(id int64, ip string) error {
	_, err := s.db.Exec(`UPDATE admin_users SET last_ip=?,last_login=? WHERE id=?`,
		ip, time.Now().Format("2006-01-02 15:04:05"), id)
	return err
}

// DeleteAdmin 删除管理员。
func (s *Store) DeleteAdmin(id int64) error {
	_, err := s.db.Exec(`DELETE FROM admin_users WHERE id=?`, id)
	return err
}

// ClearAll2FA 关闭全部管理员的 TOTP 并清空密钥（CLI 重置密码时用）。
func (s *Store) ClearAll2FA() error {
	_, err := s.db.Exec(`UPDATE admin_users SET totp_secret='', updated_at=CURRENT_TIMESTAMP`)
	return err
}
