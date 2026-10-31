package store

import (
	"strings"
	"time"
)

// AuditEntry 一条审计记录：谁、从哪、什么时候、做了什么、结果如何。
//
// 时间用本地时间字符串（与面板其余时间展示一致），排序按 id 自增，
// 不依赖字符串比较，避免跨时区/夏令时导致的排序错乱。
type AuditEntry struct {
	ID     int64  `json:"id"`
	Time   string `json:"time"`
	Actor  string `json:"actor"`
	IP     string `json:"ip"`
	Action string `json:"action"`
	Target string `json:"target"`
	Result string `json:"result"`
	Detail string `json:"detail"`
}

// AuditFilter 审计查询条件。零值表示不限。
type AuditFilter struct {
	Limit  int // 默认 100，上限 500
	Offset int
	Prefix string // action 前缀，如 "login" 匹配 login / login.fail
	Result string // ok / fail
	// Since 只取该时间之后的记录，格式与 AuditEntry.Time 一致
	// （"2006-01-02 15:04:05"，本地时区）。登录历史按天回溯时用。
	Since string
}

// 审计结果的两种取值。
const (
	AuditOK   = "ok"
	AuditFail = "fail"
)

// AuditKeepDays 审计日志保留天数（按天清理，避免长期运行把库撑大）。
const AuditKeepDays = 90

// AuditTimeLayout 审计时间格式（本地时区）。查询条件 Since 与展示共用，
// 改一处即可，避免写入与过滤用两种格式导致「明明有记录却查不到」。
const AuditTimeLayout = "2006-01-02 15:04:05"

// AddAudit 写入一条审计。
//
// 注意：调用方必须把「写审计失败」当作可忽略的错误——审计是旁路，
// 不能因为它挂了就让改配置这类主操作失败。
func (s *Store) AddAudit(e AuditEntry) error {
	if e.Time == "" {
		e.Time = time.Now().Format(AuditTimeLayout)
	}
	if e.Result == "" {
		e.Result = AuditOK
	}
	_, err := s.db.Exec(
		`INSERT INTO audit_log(ts,actor,ip,action,target,result,detail) VALUES(?,?,?,?,?,?,?)`,
		e.Time, e.Actor, e.IP, e.Action, e.Target, e.Result, truncRunes(e.Detail, 800))
	return err
}

// ListAudit 按条件查询审计，返回按时间倒序（id 倒序）的记录。
func (s *Store) ListAudit(f AuditFilter) ([]AuditEntry, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	if f.Offset < 0 {
		f.Offset = 0
	}

	var where []string
	var args []any
	if f.Prefix != "" {
		// 前缀匹配用 LIKE，同时转义 % 与 _，避免用户输入被当成通配符
		p := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(f.Prefix)
		where = append(where, `action LIKE ? ESCAPE '\'`)
		args = append(args, p+"%")
	}
	if f.Result != "" {
		where = append(where, `result = ?`)
		args = append(args, f.Result)
	}
	if f.Since != "" {
		where = append(where, `ts >= ?`)
		args = append(args, f.Since)
	}
	q := `SELECT id, ts, actor, ip, action, target, result, detail FROM audit_log`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += ` ORDER BY id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, f.Offset)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]AuditEntry, 0, limit)
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.Time, &e.Actor, &e.IP, &e.Action,
			&e.Target, &e.Result, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PruneAudit 删除早于保留期的记录，返回删除条数。
func (s *Store) PruneAudit(keepDays int) (int64, error) {
	if keepDays <= 0 {
		keepDays = AuditKeepDays
	}
	cutoff := time.Now().AddDate(0, 0, -keepDays).Format(AuditTimeLayout)
	res, err := s.db.Exec(`DELETE FROM audit_log WHERE ts < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ClearAudit 清空全部审计（面板「清空」按钮用）。
func (s *Store) ClearAudit() error {
	_, err := s.db.Exec(`DELETE FROM audit_log`)
	return err
}

// truncRunes 按字符数截断，避免超长详情（例如整段错误输出）把库撑爆。
func truncRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
