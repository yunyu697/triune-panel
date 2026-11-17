package api

// 报障用的两样东西：能在设备上跑的规则体检脚本，和面板侧的诊断摘要。
//
// 这两件事的共同目的是把「用户描述现象 → 维护者猜类别 → 来回问配置」
// 压成一次复制粘贴。所以两者都刻意做成**纯文本**：好贴、好存、好 diff，
// 不需要用户找附件或截图。

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"triune/internal/store"
	"triune/internal/update"
)

// GET /api/export/healthcheck：下载规则体检脚本
func (a *Server) handleHealthCheckScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="tp-check.sh"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, healthCheckScript())
}

// GET /api/diagnose/summary：纯文本诊断摘要（报障时整段复制）
func (a *Server) handleDiagnoseSummary(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	line := func(format string, args ...any) {
		fmt.Fprintf(&b, format+"\n", args...)
	}

	line("triune-panel 诊断摘要")
	line("生成时间   %s", time.Now().Format("2006-01-02 15:04:05 -0700"))
	line("面板版本   %s", Version)
	line("运行平台   %s/%s（%s）", runtime.GOOS, runtime.GOARCH, runtime.Version())
	line("数据库     schema v%d", store.SchemaVersion())
	line("数据目录   %s", a.DataDir)
	line("")

	// 访问入口按本次请求算：面板没有把监听地址持久化，这里给用户实际用的那个，
	// 排障时才知道该从哪个地址复现。
	line("本次请求")
	line("  访问地址   %s", r.Host)
	line("  客户端 IP  %s", a.clientIP(r))
	line("  登录角色   %s", a.roleOf(r))
	line("")

	line("内核")
	for _, name := range []string{"mihomo", "singbox", "xray"} {
		bin, _ := a.Store.GetSetting("bin_" + name)
		if bin == "" {
			line("  %-8s 未设置二进制", name)
			continue
		}
		verTxt := "版本未知"
		if v, err := update.BinaryVersion(bin, update.VersionArgs[name]); err == nil {
			verTxt = v
		}
		running := "未知"
		if a.ProcMgr != nil {
			if on, _ := a.ProcMgr.Status(name); on {
				running = "运行中"
			} else {
				running = "已停止"
			}
		}
		line("  %-8s %s  %s  %s", name, running, verTxt, bin)
		line("           %s", describeConfigFile(a, name))
	}
	line("")

	line("内置系统规则")
	line("  强制直连       %d 条", len(systemDirectRules))
	line("  强制代理域名   %d 条", len(systemProxyDomains))
	line("  强制代理 IP 段 %d 条", len(systemProxyCIDRs))
	line("  直连端口       %s", joinInts(systemDirectPorts))
	line("  私网/特殊段    %d 条", len(privateCIDRs))
	line("")

	// 保存的配置：报「有几份落后」比报「有几份」有用得多——落后意味着
	// 那台设备上跑的东西缺失了当前版本新增的规则。
	if cfgs, err := a.Store.ListSavedConfigs(); err == nil && len(cfgs) > 0 {
		stale := 0
		for _, c := range cfgs {
			if d := driftOf(c.RuleSnapshot); d.Stale {
				stale++
			}
		}
		line("保存的配置 %d 份（其中 %d 份落后于当前版本，建议重新生成）", len(cfgs), stale)
		c := cfgs[0]
		line("  最近一份   %s  %s / %s  生成于 %s  %s",
			c.Name, c.Core, orDash(c.Target), orDash(c.PanelVersion), c.UpdatedAt)
		line("")
	}

	line("如果某个服务不通，把这段连同体检脚本的输出一起发出来。")

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, b.String())
}

// describeConfigFile 一行说清「内核读的那个配置文件」的状态。
//
// 带上内容 hash 前 8 位是有意的：用户说「配置改了但没生效」时，
// 拿这个值和他本地那份一比，立刻能分清是没落盘、没重载，还是看错了文件。
func describeConfigFile(a *Server, core string) string {
	if a.Pipe == nil {
		return "配置路径未知"
	}
	p, err := a.Pipe.ConfigPath(core)
	if err != nil || p == "" {
		return "配置路径未知"
	}
	if _, err := os.Stat(p); err != nil {
		return "配置不存在  " + p
	}
	head := "配置"
	if data, rerr := os.ReadFile(p); rerr == nil {
		sum := sha256.Sum256(data)
		head = fmt.Sprintf("配置 %d 字节 sha %s", len(data), hex.EncodeToString(sum[:])[:8])
	}
	return fmt.Sprintf("%s  %s", head, p)
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func joinInts(v []int) string {
	if len(v) == 0 {
		return "—"
	}
	parts := make([]string, 0, len(v))
	for _, n := range v {
		parts = append(parts, fmt.Sprint(n))
	}
	return strings.Join(parts, " / ")
}
