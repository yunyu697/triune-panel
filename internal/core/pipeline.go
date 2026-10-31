package core

// 应用流水线：渲染 → 校验 → 原子替换 → 记录修订。
//
// 防配置失效的四道防线：
//  1. 校验不过 → 临时文件直接删除，运行中配置零改动；
//  2. 原子替换用 os.Rename，不存在半写状态；
//  3. 每次替换前自动备份到 revisions 目录，随时可回滚；
//  4. 每次渲染的完整内容作为 ConfigRevision 存 DB，全程可审计。

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"triune/internal/proc"
)

// PipelineManager 管理三个内核的配置应用。
type PipelineManager struct {
	DataDir string
	ProcMgr *proc.Manager
	// Settings 提供内核二进制路径等设置（由 store 实现）。
	Settings SettingProvider
}

// SettingProvider 设置读取接口。
type SettingProvider interface {
	Get(key string) (string, error)
}

// ApplyResult 应用结果。
type ApplyResult struct {
	RevisionID int64  `json:"revision_id"`
	Core       string `json:"core"`
	Validated  bool   `json:"validated"` // false = 未找到内核二进制，跳过校验
	Restarted  bool   `json:"restarted"`
	Hash       string `json:"hash"`
}

func (m *PipelineManager) coreDir(name string) string   { return filepath.Join(m.DataDir, "cores", name) }
func (m *PipelineManager) backupDir(name string) string { return filepath.Join(m.DataDir, "revisions", name) }

// ConfigPath 返回某内核的运行时配置文件路径。
func (m *PipelineManager) ConfigPath(name string) (string, error) {
	c, err := Get(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(m.coreDir(name), c.FileName()), nil
}

// RenderOpts 导出给 API 层复用（渲染预览等场景）。
func (m *PipelineManager) RenderOpts() RenderOpts { return m.renderOpts() }

// renderOpts 从设置中组装全局渲染选项。
func (m *PipelineManager) renderOpts() RenderOpts {
	en, _ := m.Settings.Get("stats_enabled")
	secret, _ := m.Settings.Get("stats_secret")
	if secret == "" {
		secret = "triune-stats"
	}
	return RenderOpts{StatsEnabled: en == "1", StatsSecret: secret}
}

// DryRun 渲染并校验，但不落盘、不重启 —— 用于导入草稿预检。
func (m *PipelineManager) DryRun(name string, ins []InboundWithClients) (*ApplyResult, string, error) {
	c, err := Get(name)
	if err != nil {
		return nil, "", err
	}
	content, err := c.Render(ins, m.renderOpts())
	if err != nil {
		return nil, "", fmt.Errorf("render: %w", err)
	}
	res := m.validate(name, c, content)
	return &ApplyResult{Core: name, Validated: res.validated, Hash: res.hash}, string(content), res.err
}

type validateResult struct {
	validated bool
	hash      string
	err       error
}

func (m *PipelineManager) validate(name string, c Core, content []byte) validateResult {
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:8])

	bin, _ := m.Settings.Get("bin_" + name)
	if bin == "" {
		return validateResult{validated: false, hash: hash} // 未配置二进制：跳过校验
	}
	_ = os.MkdirAll(m.coreDir(name), 0o755)
	tmp := filepath.Join(m.coreDir(name), ".validate."+c.Ext())
	if err := os.WriteFile(tmp, content, 0o600); err != nil {
		return validateResult{validated: true, hash: hash, err: fmt.Errorf("write temp: %w", err)}
	}
	defer os.Remove(tmp)

	out, err := c.ValidateCmd(bin, tmp).CombinedOutput()
	if err != nil {
		return validateResult{validated: true, hash: hash, err: fmt.Errorf("%s 校验失败: %v\n%s", name, err, truncate(string(out), 4000))}
	}
	return validateResult{validated: true, hash: hash}
}

// Apply 执行完整流水线。任何一步失败都不会影响正在运行的配置。
func (m *PipelineManager) Apply(name string, ins []InboundWithClients, note string) (*ApplyResult, error) {
	c, err := Get(name)
	if err != nil {
		return nil, err
	}
	content, err := c.Render(ins, m.renderOpts())
	if err != nil {
		return nil, fmt.Errorf("render: %w", err)
	}
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:8])

	dir := m.coreDir(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	cfgPath := filepath.Join(dir, c.FileName())

	// 1. 备份当前配置（若存在）
	if old, err := os.ReadFile(cfgPath); err == nil {
		_ = os.MkdirAll(m.backupDir(name), 0o755)
		bak := filepath.Join(m.backupDir(name), fmt.Sprintf("%s.%s.bak", time.Now().Format("20060102-150405"), c.Ext()))
		_ = os.WriteFile(bak, old, 0o600)
	}

	// 2. 写临时文件
	tmp := cfgPath + ".tmp"
	if err := os.WriteFile(tmp, content, 0o600); err != nil {
		return nil, fmt.Errorf("write temp: %w", err)
	}

	// 3. 校验；失败则删除临时文件并返回 —— 运行配置未动
	res := m.validate(name, c, content)
	if res.err != nil {
		os.Remove(tmp)
		return &ApplyResult{Core: name, Validated: true, Hash: hash}, res.err
	}

	// 4. 原子替换
	if err := os.Rename(tmp, cfgPath); err != nil {
		os.Remove(tmp)
		return nil, fmt.Errorf("atomic rename: %w", err)
	}

	// 5. 重启内核进程
	restarted := false
	if m.ProcMgr != nil {
		if bin, _ := m.Settings.Get("bin_" + name); bin != "" {
			if err := m.ProcMgr.Restart(name, bin, cfgPath, m.coreDir(name)); err == nil {
				restarted = true
			}
		}
	}
	return &ApplyResult{Core: name, Validated: res.validated, Restarted: restarted, Hash: hash}, nil
}

// Rollback 把最新备份恢复为运行配置并重启。
func (m *PipelineManager) Rollback(name string) (string, error) {
	c, err := Get(name)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(m.backupDir(name))
	if err != nil || len(entries) == 0 {
		return "", fmt.Errorf("no backups for %s", name)
	}
	latest := entries[len(entries)-1].Name()
	src := filepath.Join(m.backupDir(name), latest)
	data, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}
	cfgPath := filepath.Join(m.coreDir(name), c.FileName())
	tmp := cfgPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, cfgPath); err != nil {
		return "", err
	}
	if bin, _ := m.Settings.Get("bin_" + name); bin != "" && m.ProcMgr != nil {
		_ = m.ProcMgr.Restart(name, bin, cfgPath, m.coreDir(name))
	}
	return src, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n...(truncated)"
}
