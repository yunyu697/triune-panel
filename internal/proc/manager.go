// Package proc 管理内核子进程：启动、停止、重启、日志环形缓冲。
package proc

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const logRingSize = 500

type procInfo struct {
	cmd     *exec.Cmd
	logFile *os.File
	logBuf  []string
	started time.Time
	mu      sync.Mutex
}

type Manager struct {
	mu    sync.Mutex
	procs map[string]*procInfo
}

func NewManager() *Manager {
	return &Manager{procs: map[string]*procInfo{}}
}

// Start 启动内核进程（若已在运行则先停止）。
func (m *Manager) Start(name, binPath, cfgPath, workDir string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked(name)

	// 统一转绝对路径：内核进程的 cwd 会被切到 workDir，
	// 相对路径会导致内核读不到配置（真实踩过的坑）
	if abs, err := filepath.Abs(binPath); err == nil {
		binPath = abs
	}
	if abs, err := filepath.Abs(cfgPath); err == nil {
		cfgPath = abs
	}
	if abs, err := filepath.Abs(workDir); err == nil {
		workDir = abs
	}

	if _, err := os.Stat(binPath); err != nil {
		return fmt.Errorf("core binary not found: %s", binPath)
	}
	_ = os.MkdirAll(workDir, 0o755)

	logPath := workDir + "/core.log"
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	cmd := exec.Command(binPath, runArgs(name, cfgPath)...)
	cmd.Dir = workDir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("start %s: %w", name, err)
	}
	p := &procInfo{cmd: cmd, logFile: logFile, started: time.Now()}
	go func() {
		_ = cmd.Wait() // 进程退出后保留信息供状态查询
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.logFile != nil {
			p.logFile.Close()
			p.logFile = nil
		}
	}()
	m.procs[name] = p
	return nil
}

func (m *Manager) stopLocked(name string) {
	p, ok := m.procs[name]
	if !ok {
		return
	}
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
		_, _ = p.cmd.Process.Wait()
	}
	p.mu.Lock()
	if p.logFile != nil {
		p.logFile.Close()
		p.logFile = nil
	}
	p.mu.Unlock()
	delete(m.procs, name)
}

func (m *Manager) Stop(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked(name)
}

func (m *Manager) Restart(name, binPath, cfgPath, workDir string) error {
	return m.Start(name, binPath, cfgPath, workDir)
}

// Status 返回 (运行中, 启动时间或零值, 错误)。
func (m *Manager) Status(name string) (bool, time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.procs[name]
	if !ok {
		return false, time.Time{}
	}
	// 进程可能已退出但未从 map 移除（goroutine 只关日志），
	// 用 Signal(0) 探活
	if p.cmd.Process == nil || p.cmd.Process.Signal(syscall.Signal(0)) != nil {
		return false, time.Time{}
	}
	return true, p.started
}

func (m *Manager) RunningCores() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for name := range m.procs {
		if running, _ := m.Status(name); running {
			out = append(out, name)
		}
	}
	return out
}

// Logs 返回某内核最近的运行日志（从 core.log 文件尾部读取）。
func (m *Manager) Logs(name, workDir string, lines int) []string {
	if lines <= 0 {
		lines = 100
	}
	data, err := os.ReadFile(workDir + "/core.log")
	if err != nil {
		return nil
	}
	all := splitLines(string(data))
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return all
}

func splitLines(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '\n' {
			out = append(out, cur)
			cur = ""
		} else {
			cur += string(r)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// runArgs 各内核的运行参数。
func runArgs(name, cfgPath string) []string {
	switch name {
	case "singbox":
		return []string{"run", "-c", cfgPath}
	case "xray":
		return []string{"run", "-c", cfgPath}
	case "mihomo":
		return []string{"-f", cfgPath}
	}
	return []string{"-c", cfgPath}
}
