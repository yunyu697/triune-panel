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
	// exited 由 Wait goroutine 在回收后置位。只用 Signal(0) 判活不够：
	// 进程刚死、还没被 Wait 回收时是僵尸，Signal(0) 照样返回 nil，
	// 于是「启动即退出」在健康检查窗口里会被误判成存活。
	exited bool
	mu     sync.Mutex
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
	cmd := exec.Command(binPath, runArgs(name, cfgPath, workDir)...)
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
		p.exited = true
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

// StopAll 停掉所有在管的内核。
//
// exec 换进程映像前必须调用：exec 只换自己，子进程原样留着，而新进程的
// Manager 是空的——那些内核会变成没人认领的孤儿，端口一直占着，
// 之后每次「应用配置」都死在端口预检上（见 reap.go 的说明）。
func (m *Manager) StopAll() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var names []string
	for name := range m.procs {
		names = append(names, name)
		m.stopLocked(name)
	}
	return names
}

func (m *Manager) Restart(name, binPath, cfgPath, workDir string) error {
	return m.Start(name, binPath, cfgPath, workDir)
}

// Status 返回 (运行中, 启动时间或零值, 错误)。
func (m *Manager) Status(name string) (bool, time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusLocked(name)
}

// statusLocked 是 Status 的无锁版本，供已持 m.mu 的调用方使用。
//
// 必须共用同一份判活逻辑：RunningCores 曾经持着 m.mu 又去调 Status，
// 而 Status 内部再 Lock 同一把非重入的 sync.Mutex——一次 /metrics 采集
// 就会把整个内核管理器永久卡死，之后所有「应用 / 停止 / 重启」都阻塞在这里，
// 界面既不报错也没有任何进展。
func (m *Manager) statusLocked(name string) (bool, time.Time) {
	p, ok := m.procs[name]
	if !ok {
		return false, time.Time{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	// 已经 Wait 回收过：确定死了，不必再看 Signal。
	if p.exited || p.cmd.Process == nil {
		return false, time.Time{}
	}
	if p.cmd.Process.Signal(syscall.Signal(0)) != nil {
		return false, time.Time{}
	}
	return true, p.started
}

func (m *Manager) RunningCores() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for name := range m.procs {
		if running, _ := m.statusLocked(name); running {
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
// mihomo 需要显式 -d：否则 home 目录落到 ~/.config/mihomo，
// 证书路径白名单（SAFE_PATHS / home）与面板数据目录不重合。
func runArgs(name, cfgPath, workDir string) []string {
	switch name {
	case "singbox":
		return []string{"run", "-c", cfgPath}
	case "xray":
		return []string{"run", "-c", cfgPath}
	case "mihomo":
		return []string{"-d", workDir, "-f", cfgPath}
	}
	return []string{"-c", cfgPath}
}
