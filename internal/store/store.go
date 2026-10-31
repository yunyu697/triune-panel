// Package store 提供基于 SQLite 的持久化。
//
// 迁移策略（痛点根治的关键之一）：
//   - migrations 为版本化、前向兼容的 SQL 列表，只增列不删列、不改类型；
//   - schema_version 记录在 settings 表，启动时按序补跑；
//   - 用户数据存于 JSON 文本列，面板升级新增字段不影响旧数据。
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"triune/internal/model"
)

type Store struct {
	db *sql.DB
}

// migrations 每个元素代表一个版本的 DDL；永远 append，不修改历史条目。
var migrations = []string{
	// v1: 初始 schema
	`
	CREATE TABLE IF NOT EXISTS settings (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL DEFAULT ''
	);
	CREATE TABLE IF NOT EXISTS inbounds (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		name       TEXT NOT NULL,
		core       TEXT NOT NULL,
		protocol   TEXT NOT NULL,
		listen     TEXT NOT NULL DEFAULT '',
		port       INTEGER NOT NULL DEFAULT 0,
		tag        TEXT NOT NULL DEFAULT '',
		transport  TEXT NOT NULL DEFAULT 'tcp',
		tls        TEXT NOT NULL DEFAULT 'none',
		settings   TEXT NOT NULL DEFAULT '{}',
		enabled    INTEGER NOT NULL DEFAULT 1,
		draft      INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE IF NOT EXISTS clients (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		name       TEXT NOT NULL,
		credential TEXT NOT NULL,
		sub_token  TEXT NOT NULL UNIQUE,
		quota_gb   REAL NOT NULL DEFAULT 0,
		expires_at DATETIME,
		enabled    INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE IF NOT EXISTS client_inbounds (
		client_id  INTEGER NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
		inbound_id INTEGER NOT NULL REFERENCES inbounds(id) ON DELETE CASCADE,
		PRIMARY KEY (client_id, inbound_id)
	);
	CREATE TABLE IF NOT EXISTS external_nodes (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		name       TEXT NOT NULL,
		raw_link   TEXT NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE IF NOT EXISTS config_revisions (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		core       TEXT NOT NULL,
		content    TEXT NOT NULL,
		hash       TEXT NOT NULL,
		status     TEXT NOT NULL,
		note       TEXT NOT NULL DEFAULT '',
		detail     TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_inbounds_core ON inbounds(core);
	CREATE INDEX IF NOT EXISTS idx_revisions_core ON config_revisions(core, id);
	`,
	// v2: 流量统计
	//
	// rate_limit_mbps 是历史遗留列：当初以为可以给内核做每用户限速，实际三个
	// 内核都没有这个能力（见 model.Client 注释）。字段已从代码里摘掉，但迁移
	// 条目必须原样保留——schema 版本号就是迁移条目数，删一条会让已升级的库
	// 认为自己版本更高而跳过后续迁移。列留在库里，恒为 0，不参与读写。
	`
	ALTER TABLE clients ADD COLUMN rate_limit_mbps INTEGER NOT NULL DEFAULT 0;
	CREATE TABLE IF NOT EXISTS traffic_counters (
		client_id  INTEGER PRIMARY KEY REFERENCES clients(id) ON DELETE CASCADE,
		up_bytes   INTEGER NOT NULL DEFAULT 0,
		down_bytes INTEGER NOT NULL DEFAULT 0,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE IF NOT EXISTS traffic_daily (
		client_id  INTEGER NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
		day        TEXT NOT NULL,
		up_bytes   INTEGER NOT NULL DEFAULT 0,
		down_bytes INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (client_id, day)
	);
	`,
	// 后续版本在此追加，永远 append，不改历史条目。
	// v3: 审计日志（谁在什么时候改了什么）
	`
	CREATE TABLE IF NOT EXISTS audit_log (
		id     INTEGER PRIMARY KEY AUTOINCREMENT,
		ts     TEXT NOT NULL DEFAULT '',
		actor  TEXT NOT NULL DEFAULT '',
		ip     TEXT NOT NULL DEFAULT '',
		action TEXT NOT NULL DEFAULT '',
		target TEXT NOT NULL DEFAULT '',
		result TEXT NOT NULL DEFAULT 'ok',
		detail TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX IF NOT EXISTS idx_audit_ts ON audit_log(ts);
	CREATE INDEX IF NOT EXISTS idx_audit_action ON audit_log(action);
	`,
	// v4: 多管理员账号
	//
	// 播种语句直接从 settings 的旧单账号槽搬过来，让老库升级后原来的
	// 用户名/密码照常可用（哈希原样搬，用户无感）；全新库 settings 里没有
	// 这两个键，于是得到 admin + 空哈希，也就是「出厂默认、首登强制改密」。
	// 用 WHERE NOT EXISTS 兜住「迁移重跑」的情况（版本号丢失的存量库）。
	`
	CREATE TABLE IF NOT EXISTS admin_users (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		username    TEXT NOT NULL UNIQUE,
		pass_hash   TEXT NOT NULL DEFAULT '',
		role        TEXT NOT NULL DEFAULT 'admin',
		enabled     INTEGER NOT NULL DEFAULT 1,
		totp_secret TEXT NOT NULL DEFAULT '',
		last_ip     TEXT NOT NULL DEFAULT '',
		last_login  TEXT NOT NULL DEFAULT '',
		created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	INSERT INTO admin_users(username,pass_hash,role,enabled)
	SELECT COALESCE(NULLIF((SELECT value FROM settings WHERE key='admin_user'),''),'admin'),
	       COALESCE((SELECT value FROM settings WHERE key='admin_pass_hash'),''),
	       'owner',1
	WHERE NOT EXISTS (SELECT 1 FROM admin_users);
	`,
}

func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "triune.db")+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite 单写者，避免 busy
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	var ver int
	if err := s.db.QueryRow(`SELECT value FROM settings WHERE key='schema_version'`).Scan(&ver); err != nil {
		ver = 0
		// settings 表不存在 → 全新库，先跑 v1 建表
		if _, probe := s.db.Exec(`SELECT 1 FROM settings LIMIT 1`); probe != nil {
			if _, err2 := s.db.Exec(migrations[0]); err2 != nil {
				return fmt.Errorf("initial migration: %w", err2)
			}
		}
	}
	for i := ver; i < len(migrations); i++ {
		if err := s.execMigration(migrations[i]); err != nil {
			return fmt.Errorf("migration v%d: %w", i+1, err)
		}
	}
	// 用 upsert 保证版本号一定落库（此前 insert-or-ignore 在全新库上失败后
	// 版本号丢失，会导致重启后迁移重跑、ADD COLUMN 报 duplicate column）
	if _, err := s.db.Exec(`INSERT INTO settings(key,value) VALUES('schema_version',?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, len(migrations)); err != nil {
		return err
	}
	return nil
}

// execMigration 逐条执行迁移语句。
// SQLite 不支持 ADD COLUMN IF NOT EXISTS，对 ALTER TABLE ... ADD COLUMN
// 做"列已存在则跳过"，以兼容版本号丢失的存量库（修复前版本创建）。
func (s *Store) execMigration(sqlText string) error {
	for _, stmt := range strings.Split(sqlText, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" || strings.HasPrefix(stmt, "--") {
			continue
		}
		if t, col, ok := parseAlterAddColumn(stmt); ok {
			exists, err := s.columnExists(t, col)
			if err != nil {
				return err
			}
			if exists {
				continue
			}
		}
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

var alterRe = regexp.MustCompile(`(?is)^ALTER\s+TABLE\s+(\w+)\s+ADD\s+COLUMN\s+(\w+)`)

func parseAlterAddColumn(stmt string) (table, col string, ok bool) {
	m := alterRe.FindStringSubmatch(stmt)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

func (s *Store) columnExists(table, col string) (bool, error) {
	rows, err := s.db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if name == col {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *Store) Close() error { return s.db.Close() }

// ---- Settings ----

func (s *Store) GetSetting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// Get 实现 core.SettingProvider 接口。
func (s *Store) Get(key string) (string, error) { return s.GetSetting(key) }

// ---- Inbounds ----

func (s *Store) CreateInbound(in *model.Inbound) error {
	res, err := s.db.Exec(`INSERT INTO inbounds(name,core,protocol,listen,port,tag,transport,tls,settings,enabled,draft)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		in.Name, in.Core, in.Protocol, in.Listen, in.Port, in.Tag, in.Transport, in.TLS, in.Settings,
		b2i(in.Enabled), b2i(in.Draft))
	if err != nil {
		return err
	}
	in.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) UpdateInbound(in *model.Inbound) error {
	_, err := s.db.Exec(`UPDATE inbounds SET name=?,core=?,protocol=?,listen=?,port=?,tag=?,transport=?,tls=?,settings=?,enabled=?,draft=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		in.Name, in.Core, in.Protocol, in.Listen, in.Port, in.Tag, in.Transport, in.TLS, in.Settings,
		b2i(in.Enabled), b2i(in.Draft), in.ID)
	return err
}

func (s *Store) DeleteInbound(id int64) error {
	_, err := s.db.Exec(`DELETE FROM inbounds WHERE id=?`, id)
	return err
}

func scanInbound(row interface{ Scan(...any) error }) (*model.Inbound, error) {
	var in model.Inbound
	var enabled, draft int
	err := row.Scan(&in.ID, &in.Name, &in.Core, &in.Protocol, &in.Listen, &in.Port, &in.Tag,
		&in.Transport, &in.TLS, &in.Settings, &enabled, &draft, &in.CreatedAt, &in.UpdatedAt)
	if err != nil {
		return nil, err
	}
	in.Enabled, in.Draft = enabled == 1, draft == 1
	return &in, nil
}

const inboundCols = `id,name,core,protocol,listen,port,tag,transport,tls,settings,enabled,draft,created_at,updated_at`

func (s *Store) ListInbounds(core string) ([]*model.Inbound, error) {
	q := `SELECT ` + inboundCols + ` FROM inbounds`
	var args []any
	if core != "" {
		q += ` WHERE core=?`
		args = append(args, core)
	}
	q += ` ORDER BY id`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Inbound
	for rows.Next() {
		in, err := scanInbound(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

func (s *Store) GetInbound(id int64) (*model.Inbound, error) {
	in, err := scanInbound(s.db.QueryRow(`SELECT `+inboundCols+` FROM inbounds WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("inbound %d not found", id)
	}
	return in, err
}

// ---- Clients ----

func (s *Store) CreateClient(c *model.Client) error {
	res, err := s.db.Exec(`INSERT INTO clients(name,credential,sub_token,quota_gb,expires_at,enabled) VALUES(?,?,?,?,?,?)`,
		c.Name, c.Credential, c.SubToken, c.QuotaGB, c.ExpiresAt, b2i(c.Enabled))
	if err != nil {
		return err
	}
	c.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) UpdateClient(c *model.Client) error {
	_, err := s.db.Exec(`UPDATE clients SET name=?,credential=?,sub_token=?,quota_gb=?,expires_at=?,enabled=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		c.Name, c.Credential, c.SubToken, c.QuotaGB, c.ExpiresAt, b2i(c.Enabled), c.ID)
	return err
}

func (s *Store) DeleteClient(id int64) error {
	_, err := s.db.Exec(`DELETE FROM clients WHERE id=?`, id)
	return err
}

func scanClient(row interface{ Scan(...any) error }) (*model.Client, error) {
	var c model.Client
	var enabled int
	var exp sql.NullTime
	if err := row.Scan(&c.ID, &c.Name, &c.Credential, &c.SubToken, &c.QuotaGB, &exp, &enabled, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	if exp.Valid {
		t := exp.Time
		c.ExpiresAt = &t
	}
	c.Enabled = enabled == 1
	return &c, nil
}

// clientCols 不含 rate_limit_mbps：那是已废弃的历史列（见 model.Client 注释）。
const clientCols = `id,name,credential,sub_token,quota_gb,expires_at,enabled,created_at,updated_at`

func (s *Store) ListClients() ([]*model.Client, error) {
	rows, err := s.db.Query(`SELECT ` + clientCols + ` FROM clients ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Client
	for rows.Next() {
		c, err := scanClient(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetClient(id int64) (*model.Client, error) {
	c, err := scanClient(s.db.QueryRow(`SELECT `+clientCols+` FROM clients WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("client %d not found", id)
	}
	return c, err
}

func (s *Store) GetClientByToken(token string) (*model.Client, error) {
	c, err := scanClient(s.db.QueryRow(`SELECT `+clientCols+` FROM clients WHERE sub_token=?`, token))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("client not found")
	}
	return c, err
}

// SetClientInbounds 重设用户的入站关联（全量替换）。
func (s *Store) SetClientInbounds(clientID int64, inboundIDs []int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM client_inbounds WHERE client_id=?`, clientID); err != nil {
		return err
	}
	for _, id := range inboundIDs {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO client_inbounds(client_id,inbound_id) VALUES(?,?)`, clientID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ClientInboundIDs(clientID int64) ([]int64, error) {
	rows, err := s.db.Query(`SELECT inbound_id FROM client_inbounds WHERE client_id=?`, clientID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// InboundClientIDs 返回关联到某入站的所有用户。
func (s *Store) InboundClientIDs(inboundID int64) ([]int64, error) {
	rows, err := s.db.Query(`SELECT client_id FROM client_inbounds WHERE inbound_id=?`, inboundID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ---- External nodes ----

func (s *Store) CreateExternalNode(n *model.ExternalNode) error {
	res, err := s.db.Exec(`INSERT INTO external_nodes(name,raw_link) VALUES(?,?)`, n.Name, n.RawLink)
	if err != nil {
		return err
	}
	n.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) ListExternalNodes() ([]*model.ExternalNode, error) {
	rows, err := s.db.Query(`SELECT id,name,raw_link,created_at FROM external_nodes ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.ExternalNode
	for rows.Next() {
		var n model.ExternalNode
		if err := rows.Scan(&n.ID, &n.Name, &n.RawLink, &n.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &n)
	}
	return out, rows.Err()
}

func (s *Store) DeleteExternalNode(id int64) error {
	_, err := s.db.Exec(`DELETE FROM external_nodes WHERE id=?`, id)
	return err
}

// ---- Revisions ----

func (s *Store) CreateRevision(r *model.ConfigRevision) error {
	res, err := s.db.Exec(`INSERT INTO config_revisions(core,content,hash,status,note,detail) VALUES(?,?,?,?,?,?)`,
		r.Core, r.Content, r.Hash, r.Status, r.Note, r.Detail)
	if err != nil {
		return err
	}
	r.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) UpdateRevisionStatus(id int64, status, detail string) error {
	_, err := s.db.Exec(`UPDATE config_revisions SET status=?,detail=? WHERE id=?`, status, detail, id)
	return err
}

func (s *Store) ListRevisions(core string, limit int) ([]*model.ConfigRevision, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT id,core,content,hash,status,note,detail,created_at FROM config_revisions`
	var args []any
	if core != "" {
		q += ` WHERE core=?`
		args = append(args, core)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.ConfigRevision
	for rows.Next() {
		var r model.ConfigRevision
		if err := rows.Scan(&r.ID, &r.Core, &r.Content, &r.Hash, &r.Status, &r.Note, &r.Detail, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// ---- Traffic ----

// AddTraffic 累计某用户的流量增量（总量表 + 按日表）。
func (s *Store) AddTraffic(clientID int64, upDelta, downDelta int64) error {
	day := time.Now().Format("2006-01-02")
	if _, err := s.db.Exec(`INSERT INTO traffic_counters(client_id,up_bytes,down_bytes) VALUES(?,?,?)
		ON CONFLICT(client_id) DO UPDATE SET
			up_bytes=up_bytes+excluded.up_bytes,
			down_bytes=down_bytes+excluded.down_bytes,
			updated_at=CURRENT_TIMESTAMP`, clientID, upDelta, downDelta); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO traffic_daily(client_id,day,up_bytes,down_bytes) VALUES(?,?,?,?)
		ON CONFLICT(client_id,day) DO UPDATE SET
			up_bytes=up_bytes+excluded.up_bytes,
			down_bytes=down_bytes+excluded.down_bytes`, clientID, day, upDelta, downDelta)
	return err
}

// TrafficRow 单用户累计流量。
type TrafficRow struct {
	ClientID  int64  `json:"client_id"`
	UpBytes   int64  `json:"up_bytes"`
	DownBytes int64  `json:"down_bytes"`
	UpdatedAt string `json:"updated_at"`
}

func (s *Store) GetTrafficAll() (map[int64]*TrafficRow, error) {
	rows, err := s.db.Query(`SELECT client_id,up_bytes,down_bytes,updated_at FROM traffic_counters`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]*TrafficRow{}
	for rows.Next() {
		var r TrafficRow
		if err := rows.Scan(&r.ClientID, &r.UpBytes, &r.DownBytes, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out[r.ClientID] = &r
	}
	return out, rows.Err()
}

// DailyPoint 单日流量点。
type DailyPoint struct {
	Day       string `json:"day"`
	UpBytes   int64  `json:"up"`
	DownBytes int64  `json:"down"`
}

// GetTrafficDaily 近 days 天的按日流量（可选按用户过滤，clientID=0 表示全部）。
// 返回按日期升序、补零的完整序列，前端可直接画图。
func (s *Store) GetTrafficDaily(clientID int64, days int) ([]DailyPoint, error) {
	if days <= 0 || days > 365 {
		days = 30
	}
	startDay := time.Now().AddDate(0, 0, -(days - 1)).Format("2006-01-02")

	q := `SELECT day, SUM(up_bytes), SUM(down_bytes) FROM traffic_daily
		WHERE day >= ?`
	args := []any{startDay}
	if clientID > 0 {
		q += ` AND client_id = ?`
		args = append(args, clientID)
	}
	q += ` GROUP BY day ORDER BY day`

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byDay := map[string][2]int64{}
	for rows.Next() {
		var day string
		var up, down int64
		if err := rows.Scan(&day, &up, &down); err != nil {
			return nil, err
		}
		byDay[day] = [2]int64{up, down}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]DailyPoint, 0, days)
	base, _ := time.ParseInLocation("2006-01-02", startDay, time.Local)
	for i := 0; i < days; i++ {
		d := base.AddDate(0, 0, i).Format("2006-01-02")
		p := DailyPoint{Day: d}
		if v, ok := byDay[d]; ok {
			p.UpBytes, p.DownBytes = v[0], v[1]
		}
		out = append(out, p)
	}
	return out, nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Ping 轻量探活：只确认连接与查询通路可用，不做任何写操作或校验，
// 供 /healthz 这类高频探测使用（quick_check 太重，不能挂在探活上）。
func (s *Store) Ping() error {
	var one int
	return s.db.QueryRow(`SELECT 1`).Scan(&one)
}

// IntegrityCheck 执行 SQLite 的 quick_check，供 `triune-panel doctor` 自检使用。
func (s *Store) IntegrityCheck() error {
	var out string
	if err := s.db.QueryRow(`PRAGMA quick_check`).Scan(&out); err != nil {
		return err
	}
	if strings.ToLower(strings.TrimSpace(out)) != "ok" {
		return fmt.Errorf("quick_check 未通过: %s", out)
	}
	return nil
}

// PeekSchemaVersion 只读读取指定数据库文件的 schema 版本。
//
// 用途：恢复备份前先确认归档里的库版本与本二进制是否兼容——
// 用新版面板写的库拿旧二进制打开会缺列、甚至写坏数据，必须提前拦住。
// 以 mode=ro 打开，不会触发迁移、也不会修改归档内容。
func PeekSchemaVersion(dbPath string) (int, error) {
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=busy_timeout(2000)")
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var ver int
	if err := db.QueryRow(`SELECT value FROM settings WHERE key='schema_version'`).Scan(&ver); err != nil {
		return 0, fmt.Errorf("读取 schema_version 失败: %w", err)
	}
	return ver, nil
}

// SchemaVersion 返回当前二进制期望的 schema 版本（migrations 条数）。
func SchemaVersion() int { return len(migrations) }
