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
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"triune/internal/proc"
)

// PipelineManager 管理三个内核的配置应用。
type PipelineManager struct {
	DataDir string
	ProcMgr *proc.Manager
	// Settings 提供内核二进制路径等设置（由 store 实现）。
	Settings SettingProvider
	// Version 是面板版本，写进渲染戳（见 RenderStamp）。
	Version string
	// StartGrace 是「启动后观察多久」的窗口，0 用默认值。
	// 内核启动即退出时靠它把「配置已替换但服务没起来」暴露出来，
	// 从而有机会回滚；测试里调小以缩短耗时。
	StartGrace time.Duration
	// applyMu 串行化「应用 / 回滚」。
	//
	// 面板有多个入口会触发这同一件事（应用弹窗、切换内核、启动自愈、
	// 控制套接字），并发跑会出现：A 刚 Stop 完内核，B 也在 Stop/Restart，
	// 两个 Start 交错，最终谁都没守住端口，内核停在半路。串行化最省事，
	// 代价只是并发应用时排队——这种操作本来就该一次只做一件。
	applyMu sync.Mutex
}

// RenderStamp 记录「磁盘上这份配置是哪一版面板渲染出来的」。
//
// 为什么需要它：内核是拿磁盘上的渲染产物启动的，而渲染器本身会随面板升级变化。
// 升级只换二进制，磁盘上那份还是旧渲染器的结果——v1.2.0 修 sing-box REALITY
// 时就栽在这里：面板已经是 1.2.0，内核照旧用 1.1.9 渲染出来的配置启动，
// 用户不去点一次「应用配置」就永远不会好，而界面上所有指标都是绿的。
//
// 所以每次写配置时在旁边记一笔版本号，启动时比对：版本变了才重新渲染。
// **只比版本、不比内容**——比内容会把「保存了但还没应用」的改动和「回滚」
// 一并当成过期配置重新应用，那是另一种事故。
//
// 一个已知的粗糙处：`dev` 构建所有版本都叫 dev，所以改完渲染器重编译、
// 重启后面板认为「配置是同一版渲染的」，不会自愈。开发时手动点一次
// 「应用配置」即可；生产构建都带真实版本号，不受影响。
type RenderStamp struct {
	Version string `json:"version"`
	Hash    string `json:"hash,omitempty"` // 配置内容的短哈希，仅供排障对照
	At      string `json:"at,omitempty"`
}

func (m *PipelineManager) stampPath(name string) string {
	return filepath.Join(m.coreDir(name), ".rendered.json")
}

// RenderStamp 读磁盘上那份配置的渲染戳。
//
// 读不到、读坏了都返回零值。零值的 Version 与任何真实版本都不等，于是会被
// 当成「来历不明，需要重新渲染」——这是安全的默认方向：宁可多渲染一次
// （内容一样时并不会重启内核），也不要留着一份不知道谁渲染的配置。
func (m *PipelineManager) RenderStamp(name string) RenderStamp {
	var s RenderStamp
	b, err := os.ReadFile(m.stampPath(name))
	if err != nil {
		return s
	}
	_ = json.Unmarshal(b, &s)
	return s
}

// MarkRendered 只更新渲染戳，不动配置文件。给「渲染结果与磁盘内容一致」的
// 场景用：那种情况下没有任何东西需要重启，但戳得跟着升上来，否则每次启动
// 都要重渲染一遍。
func (m *PipelineManager) MarkRendered(name string, content []byte) {
	m.writeStamp(name, shortHash(content))
}

func (m *PipelineManager) writeStamp(name, hash string) {
	v := m.Version
	if v == "" {
		v = "dev"
	}
	b, err := json.Marshal(RenderStamp{
		Version: v, Hash: hash, At: time.Now().Format(time.RFC3339),
	})
	if err != nil {
		return
	}
	_ = os.WriteFile(m.stampPath(name), b, 0o600)
}

// shortHash 与 ApplyResult.Hash 同一口径（sha256 前 8 字节）。
func shortHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
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

func (m *PipelineManager) coreDir(name string) string { return filepath.Join(m.DataDir, "cores", name) }
func (m *PipelineManager) backupDir(name string) string {
	return filepath.Join(m.DataDir, "revisions", name)
}

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

// RenderOptsFor 带上该内核独有的全局段（导入的 DNS / 路由）。
func (m *PipelineManager) RenderOptsFor(name string) RenderOpts {
	o := m.renderOpts()
	o.DNSJSON, _ = m.Settings.Get(name + "_dns_json")
	o.RouteJSON, _ = m.Settings.Get(name + "_route_json")
	return o
}

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
	content, err := c.Render(ins, m.RenderOptsFor(name))
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
	hash := shortHash(content)

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
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	c, err := Get(name)
	if err != nil {
		return nil, err
	}
	content, err := c.Render(ins, m.RenderOptsFor(name))
	if err != nil {
		return nil, fmt.Errorf("render: %w", err)
	}
	hash := shortHash(content)

	dir := m.coreDir(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	cfgPath := filepath.Join(dir, c.FileName())

	// 1. 备份当前配置（若存在）
	var oldCfg []byte
	if b, err := os.ReadFile(cfgPath); err == nil {
		oldCfg = b
		_ = os.MkdirAll(m.backupDir(name), 0o755)
		bak := filepath.Join(m.backupDir(name), fmt.Sprintf("%s.%s.bak", time.Now().Format("20060102-150405"), c.Ext()))
		_ = os.WriteFile(bak, b, 0o600)
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

	bin, _ := m.Settings.Get("bin_" + name)
	manage := m.ProcMgr != nil && bin != ""

	// 4. 停旧进程并做端口预检 —— **在替换配置之前**。
	//
	// 端口预检原来放在 rename 之后：预检失败要把刚写进去的新配置再写回来，
	// 多一次可能失败的写盘；更糟的是预检通过、内核却仍因绑定失败而退出的
	// 情况（sing-box 的 check 只校验语法，不 bind socket），旧代码只是把
	// 错误抛给调用方，配置已经被换掉、旧内核已经停了——该内核下的所有节点
	// 一起断，而界面上只说了一句「启动失败」。现在顺序前移，并在下面的
	// 启动阶段补回滚：应用失败必须保持原配置在跑，这是对用户的核心承诺。
	if manage {
		m.ProcMgr.Stop(name)
		if perr := checkPortsFree(ins); perr != nil {
			// 端口被占，但很可能就是「上一代面板」留下的自家内核：
			// exec 换进程映像时子进程不跟着退，崩溃 / 被 SIGKILL 也一样。
			// 认得出是自己的（二进制与配置路径都对上）就先清掉再试一次；
			// 认不出才当作别人的程序，如实报错。
			if killed := proc.ReapOrphans(bin, cfgPath); len(killed) > 0 {
				log.Printf("[core] 端口被上一代面板留下的 %s 进程 %v 占着，已清理并重试", name, killed)
				perr = checkPortsFree(ins)
			}
			if perr != nil {
				os.Remove(tmp)
				// 配置还没替换：磁盘上仍是旧配置，直接把旧内核拉起来。
				_ = m.ProcMgr.Start(name, bin, cfgPath, dir)
				return &ApplyResult{Core: name, Validated: res.validated, Restarted: false, Hash: hash}, perr
			}
		}
	}

	// 5. 原子替换
	if err := os.Rename(tmp, cfgPath); err != nil {
		os.Remove(tmp)
		return nil, fmt.Errorf("atomic rename: %w", err)
	}
	// 5.1 记下「这份配置是哪一版渲染的」。放在替换之后：中间崩了只会丢戳，
	// 下次启动当成来历不明重新渲染一遍，方向是安全的；反过来先写戳再替换，
	// 崩在中间就会把一份旧配置标成新版本，自愈永远不触发。
	m.writeStamp(name, hash)

	// 6. 重启内核，并确认它活过观察窗口
	restarted := false
	if manage {
		if err := m.ProcMgr.Restart(name, bin, cfgPath, dir); err != nil {
			// 配置已经原子替换成功，但进程起不来（二进制不可执行、
			// 工作目录不可写……）。必须回滚，否则该内核下的节点全断。
			return m.revertAfterStartFailure(name, bin, cfgPath, dir, oldCfg, hash, res.validated,
				fmt.Errorf("配置已更新，但 %s 启动失败：%w", name, err))
		}
		if !m.waitAlive(name) {
			// 启动即退出：check 不 bind socket，端口被占 / 证书在运行期
			// 加载失败等都要到这一刻才暴露。
			return m.revertAfterStartFailure(name, bin, cfgPath, dir, oldCfg, hash, res.validated,
				fmt.Errorf("%s 启动后立即退出。最近日志：\n%s", name, m.logTail(name, 15)))
		}
		restarted = true
	}
	return &ApplyResult{Core: name, Validated: res.validated, Restarted: restarted, Hash: hash}, nil
}

// startGrace 返回启动观察窗口。
func (m *PipelineManager) startGrace() time.Duration {
	if m.StartGrace > 0 {
		return m.StartGrace
	}
	return 1500 * time.Millisecond
}

// waitAlive 在观察窗口内持续确认进程还活着。
// 进程在窗口内退出即返回 false；活过窗口返回 true。
func (m *PipelineManager) waitAlive(name string) bool {
	deadline := time.Now().Add(m.startGrace())
	for {
		if alive, _ := m.ProcMgr.Status(name); !alive {
			return false
		}
		if !time.Now().Before(deadline) {
			return true
		}
		time.Sleep(40 * time.Millisecond)
	}
}

func (m *PipelineManager) logTail(name string, n int) string {
	if m.ProcMgr == nil {
		return ""
	}
	return strings.Join(m.ProcMgr.Logs(name, m.coreDir(name), n), "\n")
}

// revertAfterStartFailure 把磁盘配置恢复成上一份，并把旧内核重新拉起来。
//
// 为什么必须做：apply 已经把新配置原子替换上去了，内核进程却没起来。
// 不回滚的话，一次「应用」会把该内核下的所有节点一起停掉，而面板只能
// 报一句「启动失败」——用户看到的正是「内核直接停了」。
//
// 返回的错误里明确写出是否已回滚：调用方据此决定界面提示，不能一律
// 说「运行中的配置未被修改」。
func (m *PipelineManager) revertAfterStartFailure(name, bin, cfgPath, dir string, oldCfg []byte, hash string, validated bool, cause error) (*ApplyResult, error) {
	res := &ApplyResult{Core: name, Validated: validated, Restarted: false, Hash: hash}
	if len(oldCfg) == 0 {
		return res, fmt.Errorf("%w；此前没有可回滚的配置", cause)
	}
	if werr := os.WriteFile(cfgPath, oldCfg, 0o600); werr != nil {
		return res, fmt.Errorf("%w；回滚写入旧配置失败：%v（服务可能已中断，请查看内核日志）", cause, werr)
	}
	// 戳跟着回退：否则下次启动会以为这份旧配置是当前版本渲染的，自愈不会触发。
	m.writeStamp(name, shortHash(oldCfg))
	if m.ProcMgr != nil {
		if serr := m.ProcMgr.Start(name, bin, cfgPath, dir); serr != nil {
			return res, fmt.Errorf("%w；已恢复旧配置，但旧内核也没能重新启动：%v", cause, serr)
		}
	}
	return res, fmt.Errorf("%w；已自动回滚到上一份配置，服务保持可用", cause)
}

// Rollback 把最新备份恢复为运行配置并重启。
func (m *PipelineManager) Rollback(name string) (string, error) {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
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
	// 回滚是用户的明确选择，写当前版本的戳意味着「别再自动重渲染它」——
	// 否则下次启动的自愈会把刚回滚掉的配置又渲染回来，等于回滚无效。
	m.writeStamp(name, shortHash(data))
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

// checkPortsFree 校验入站端口当前是否空闲（必须在旧进程停止之后调用）。
// 内核在端口被占时经常只记一条日志就"启动成功"，所以启动前主动探一遍。
func checkPortsFree(ins []InboundWithClients) error {
	seen := map[string]bool{}
	for _, iw := range ins {
		in := iw.Inbound
		proto := "tcp"
		switch in.Protocol {
		case "hysteria2", "tuic":
			proto = "udp"
		}
		if in.Transport == "udp" {
			proto = "udp"
		}
		host := in.Listen
		if host == "0.0.0.0" || host == "::" {
			host = ""
		}
		key := fmt.Sprintf("%s/%s/%d", proto, host, in.Port)
		if seen[key] {
			continue
		}
		seen[key] = true
		addr := net.JoinHostPort(host, strconv.Itoa(in.Port))
		if proto == "udp" {
			ua, err := net.ResolveUDPAddr("udp", addr)
			if err != nil {
				continue
			}
			c, err := net.ListenUDP("udp", ua)
			if err != nil {
				return fmt.Errorf("端口 %d/UDP 已被其它程序占用，%s 无法启动（%v）。%s",
					in.Port, in.Name, err, portBusyHint(in.Port))
			}
			c.Close()
			continue
		}
		// TCP 先看能不能连上：有程序在监听时连接必然成功。
		// 这一步跨平台可靠（BSD/macOS 的 SO_REUSEADDR 允许对已监听
		// 端口重复 bind，单靠 bind 判不出来）。
		if c, err := net.DialTimeout("tcp", addr, 700*time.Millisecond); err == nil {
			c.Close()
			return fmt.Errorf("端口 %d/TCP 已被其它程序占用（连接成功），%s 无法启动。%s",
				in.Port, in.Name, portBusyHint(in.Port))
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("端口 %d/TCP 已被其它程序占用，%s 无法启动（%v）。%s",
				in.Port, in.Name, err, portBusyHint(in.Port))
		}
		ln.Close()
	}
	return nil
}

// portBusyHint 端口被占时告诉用户下一步做什么。
//
// 光说「被其它程序占用」会让人卡在这儿：面板刚升级过、界面上也没有任何线索。
// 实际上最常见的原因是**面板自己上一代留下的内核**——exec 换进程映像时子进程
// 不跟着退，于是它继续占着端口。这种现在会被自动清掉，所以还报这个错时，
// 基本就是别人的程序了，直接把查法写出来。
func portBusyHint(port int) string {
	return fmt.Sprintf("面板自己残留的内核会被自动清理；仍然占用说明是别的程序，可用 `lsof -i :%d` 查是谁", port)
}
