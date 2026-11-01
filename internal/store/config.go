package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ==================== 保存的客户端配置 ====================

// SavedConfig 用户手动保存的一份客户端配置。
//
// Params 是生成时的完整请求（core/target/scenario/inbound_ids/sets/custom_rules…），
// 存它是为了「重新生成」：入站或用户改了以后，用当时的参数重跑一遍，而不是
// 让用户凭记忆把选项再点一次。
type SavedConfig struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Core        string `json:"core"`
	CoreVersion string `json:"core_version"`
	Target      string `json:"target"`
	Scenario    string `json:"scenario"`
	Note        string `json:"note"`
	Params      string `json:"params"`  // 生成参数 JSON
	Content     string `json:"content"` // 列表接口不返回，避免一次拉几 MB
	Size        int    `json:"size"`
	Hash        string `json:"hash"` // 内容 sha256 前 16 位，用于判断重新生成后是否变了
	CreatedBy   string `json:"created_by"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// ErrConfigNameTaken 同一内核下重名。不禁止跨内核同名（mihomo 与 sing-box
// 各存一份同名的很正常），所以唯一性由调用方按 (core,name) 判断。
var ErrConfigNameTaken = errors.New("同名配置已存在")

// ErrTagNameTaken 标签重名。
var ErrTagNameTaken = errors.New("同名标签已存在")

// savedConfigCols 与 scanSavedConfig 顺序耦合，改一处必须改另一处。
const savedConfigCols = `id,name,core,core_version,target,scenario,note,params,content,size,hash,created_by,created_at,updated_at`

func scanSavedConfig(sc interface{ Scan(...any) error }) (*SavedConfig, error) {
	var c SavedConfig
	err := sc.Scan(&c.ID, &c.Name, &c.Core, &c.CoreVersion, &c.Target, &c.Scenario,
		&c.Note, &c.Params, &c.Content, &c.Size, &c.Hash,
		&c.CreatedBy, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// ListSavedConfigs 返回全部配置的**元数据**（不含 content）。
//
// 配置动辄几十 KB，列表页一次拉十几份就是几百 KB，还要为每份都做一次
// JSON 编码——把 content 留在详情接口里更合适。Size 与 Hash 足够列表展示
// 「多大」「有没有变」。
func (s *Store) ListSavedConfigs() ([]*SavedConfig, error) {
	rows, err := s.db.Query(`SELECT ` + savedConfigCols + ` FROM saved_configs ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SavedConfig
	for rows.Next() {
		c, err := scanSavedConfig(rows)
		if err != nil {
			return nil, err
		}
		c.Content = "" // 列表不返回正文
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetSavedConfig 取完整记录（含 content）。
func (s *Store) GetSavedConfig(id int64) (*SavedConfig, error) {
	row := s.db.QueryRow(`SELECT `+savedConfigCols+` FROM saved_configs WHERE id=?`, id)
	return scanSavedConfig(row)
}

// CreateSavedConfig 保存一份配置。
//
// 不拦重名：同一份配置改几次存几版是正常用法（改端口前后各存一份来对比）。
// 真需要唯一性时用 FindSavedConfigByName。
func (s *Store) CreateSavedConfig(c *SavedConfig) error {
	now := time.Now().Format(AuditTimeLayout)
	c.CreatedAt, c.UpdatedAt = now, now
	res, err := s.db.Exec(`INSERT INTO saved_configs
		(name,core,core_version,target,scenario,note,params,content,size,hash,created_by,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.Name, c.Core, c.CoreVersion, c.Target, c.Scenario, c.Note,
		c.Params, c.Content, len(c.Content), c.Hash, c.CreatedBy, c.CreatedAt, c.UpdatedAt)
	if err != nil {
		return err
	}
	c.ID, _ = res.LastInsertId()
	c.Size = len(c.Content)
	return nil
}

// UpdateSavedConfig 改名 / 改备注 / 覆盖内容（重新生成后覆盖）。
// 零值字段保持原样，避免部分更新时把没传的列清空。
func (s *Store) UpdateSavedConfig(c *SavedConfig) error {
	c.UpdatedAt = time.Now().Format(AuditTimeLayout)
	if c.Content != "" {
		c.Size = len(c.Content)
	}
	_, err := s.db.Exec(`UPDATE saved_configs SET
		name=COALESCE(NULLIF(?,''),name),
		note=?,
		content=CASE WHEN ?='' THEN content ELSE ? END,
		size=CASE WHEN ?='' THEN size ELSE ? END,
		hash=CASE WHEN ?='' THEN hash ELSE ? END,
		core_version=CASE WHEN ?='' THEN core_version ELSE ? END,
		params=CASE WHEN ?='' THEN params ELSE ? END,
		updated_at=?
		WHERE id=?`,
		c.Name, c.Note,
		c.Content, c.Content, c.Content, len(c.Content),
		c.Hash, c.Hash,
		c.CoreVersion, c.CoreVersion,
		c.Params, c.Params,
		c.UpdatedAt, c.ID)
	return err
}

// DeleteSavedConfig 删除一份配置。
func (s *Store) DeleteSavedConfig(id int64) error {
	_, err := s.db.Exec(`DELETE FROM saved_configs WHERE id=?`, id)
	return err
}

// CountSavedConfigs 当前保存份数（用于配额提示）。
func (s *Store) CountSavedConfigs() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM saved_configs`).Scan(&n)
	return n, err
}

// ==================== 自定义规则标签 ====================

// RuleTag 一组可复用的自定义规则。
//
// 存成多行文本而不是 JSON 数组：用户看到的、编辑的就是这几行，来回转换
// 只会制造「格式对不对」的疑问，而这里没有任何结构化需求。
type RuleTag struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Rules     string `json:"rules"` // 每行一条
	Count     int    `json:"count"` // 行数，列表展示用
	CreatedBy string `json:"created_by"`
	CreatedAt string `json:"created_at"`
}

const ruleTagCols = `id,name,rules,created_by,created_at`

func scanRuleTag(sc interface{ Scan(...any) error }) (*RuleTag, error) {
	var t RuleTag
	if err := sc.Scan(&t.ID, &t.Name, &t.Rules, &t.CreatedBy, &t.CreatedAt); err != nil {
		return nil, err
	}
	t.Count = CountRuleLines(t.Rules)
	return &t, nil
}

// CountRuleLines 非空行数。
func CountRuleLines(rules string) int {
	n := 0
	for _, line := range strings.Split(rules, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// ListRuleTags 按名字排序返回全部标签。
func (s *Store) ListRuleTags() ([]*RuleTag, error) {
	rows, err := s.db.Query(`SELECT ` + ruleTagCols + ` FROM rule_tags ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*RuleTag
	for rows.Next() {
		t, err := scanRuleTag(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetRuleTag 按 id 取标签。
func (s *Store) GetRuleTag(id int64) (*RuleTag, error) {
	return scanRuleTag(s.db.QueryRow(`SELECT `+ruleTagCols+` FROM rule_tags WHERE id=?`, id))
}

// CreateRuleTag 新建标签。重名返回 ErrTagNameTaken。
func (s *Store) CreateRuleTag(t *RuleTag) error {
	t.CreatedAt = time.Now().Format(AuditTimeLayout)
	res, err := s.db.Exec(`INSERT INTO rule_tags(name,rules,created_by,created_at) VALUES (?,?,?,?)`,
		t.Name, t.Rules, t.CreatedBy, t.CreatedAt)
	if err != nil {
		if isUniqueErr(err) {
			return ErrTagNameTaken
		}
		return err
	}
	t.ID, _ = res.LastInsertId()
	t.Count = CountRuleLines(t.Rules)
	return nil
}

// UpdateRuleTag 改名 / 改内容。
func (s *Store) UpdateRuleTag(t *RuleTag) error {
	_, err := s.db.Exec(`UPDATE rule_tags SET name=?, rules=? WHERE id=?`, t.Name, t.Rules, t.ID)
	if err != nil {
		if isUniqueErr(err) {
			return ErrTagNameTaken
		}
		return err
	}
	t.Count = CountRuleLines(t.Rules)
	return nil
}

// DeleteRuleTag 删除标签。只删标签本身，不影响任何已生成配置。
func (s *Store) DeleteRuleTag(id int64) error {
	_, err := s.db.Exec(`DELETE FROM rule_tags WHERE id=?`, id)
	return err
}

// errNoRows 供上层判断「不存在」，避免让 API 层 import database/sql。
func ErrNotFound(err error) bool { return errors.Is(err, sql.ErrNoRows) }
