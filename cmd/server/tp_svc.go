// tp 的服务控制命令：start / stop / restart / logs。
//
// 这一组**只做 systemd 的包装**，不自己 fork 后台进程。CLI 托管守护进程的
// 结局通常是：终端一关进程就没了、崩溃没人拉起、pid 文件与真实进程对不上。
// 没有 systemd 就明确说清楚，让人自己决定怎么跑。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// defaultUnit systemd 单元名。install.sh 装的就是这个。
const defaultUnit = "triune-panel"

// systemdAvailable 判断这台机器上有没有可用的 systemd 单元。
//
// 三层判断缺一不可：systemctl 存在（容器里可能装了但没跑）、PID 1 是 systemd
//（/run/systemd/system 存在）、单元文件确实注册了。只看第一层会在容器里
// 给出误导性的报错。
func systemdAvailable(unit string) bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return false
	}
	return exec.Command("systemctl", "cat", unit).Run() == nil
}

// requireSystemd 拿不到 systemd 时给出可执行的替代方案，而不是一句"不支持"。
func requireSystemd(unit string) {
	if systemdAvailable(unit) {
		return
	}
	die(1, `没有找到 systemd 单元 %[1]s，无法自动控制面板进程。

  可能的情况：
    · 面板是手动前台运行的（终端里跑 triune-panel serve）——用那个终端 Ctrl-C
    · 装在容器里（没有 systemd）——用容器的启停命令
    · 单元名不叫 %[1]s——用 --unit <名字> 指定

  手动启动：triune-panel serve -addr :10010 -data /opt/triune-panel/data`, unit)
}

// requireRoot 提前拦住权限问题。
//
// 不自动 sudo：在脚本里悄悄提权比报错更危险，而且 sudo 可能要求交互输密码，
// 卡在半路的表现是一行看不懂的 systemctl 报错。
func requireRoot(verb string) {
	if os.Geteuid() == 0 {
		return
	}
	die(1, "需要 root 权限：sudo tp %s", verb)
}

// runSystemctl 执行并回显 systemctl 输出。失败时把 stderr 原样带出来——
// systemd 的报错（比如 unit 被 mask、依赖失败）比我们自己编的话有用得多。
func runSystemctl(args ...string) {
	out, err := exec.Command("systemctl", args...).CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		die(1, "systemctl %s 失败: %v\n%s", strings.Join(args, " "), err, s)
	}
	if s != "" {
		fmt.Println("  " + s)
	}
}

// serviceActive 问 systemd 这个单元在不在跑。
func serviceActive(unit string) bool {
	return exec.Command("systemctl", "is-active", "--quiet", unit).Run() == nil
}

// panelAddr 从单元文件的 ExecStart 里把 -addr 抠出来。
//
// 面板监听的端口是可配的（install.sh 允许改），写死 10010 会让改了端口的
// 用户在 restart 之后永远等不到"就绪"。抠不到就退回默认值，不影响启停。
func panelAddr(unit string) string {
	out, err := exec.Command("systemctl", "show", "-p", "ExecStart", "--value", unit).Output()
	if err != nil {
		return ":10010"
	}
	fields := strings.Fields(string(out))
	for i, f := range fields {
		if f == "-addr" && i+1 < len(fields) {
			return strings.Trim(fields[i+1], ";")
		}
		if v, ok := strings.CutPrefix(f, "-addr="); ok {
			return strings.Trim(v, ";")
		}
	}
	return ":10010"
}

// healthURL 把监听地址转成探活 URL。":10010" / "0.0.0.0:10010" 一律走回环。
func healthURL(unit string) string {
	addr := panelAddr(unit)
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://127.0.0.1:10010/healthz"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz"
}

// waitReady 轮询 /healthz 直到就绪或超时。
//
// 这个等待是必需的：systemctl restart 返回时进程只是"起来了"，HTTP 端口
// 往往还没开始监听。不等待的话，用户敲完 restart 立刻敲 status，看到的
// 还是"未运行"，然后以为命令失败了。
func waitReady(unit string, timeout time.Duration) (time.Duration, bool) {
	url := healthURL(unit)
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	start := time.Now()
	for time.Since(start) < timeout {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return time.Since(start), true
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return time.Since(start), false
}

func cmdStart(args []string) {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	unit := fs.String("unit", defaultUnit, "systemd 单元名")
	wait := fs.Duration("wait", 10*time.Second, "等待就绪的时长")
	_ = tpParse(fs, args)

	requireSystemd(*unit)
	requireRoot("start")
	if serviceActive(*unit) {
		fmt.Println("  面板已在运行（无需操作）")
		printReady(*unit)
		return
	}
	runSystemctl("start", *unit)
	reportReady(*unit, *wait)
}

func cmdStop(args []string) {
	fs := flag.NewFlagSet("stop", flag.ExitOnError)
	unit := fs.String("unit", defaultUnit, "systemd 单元名")
	_ = tpParse(fs, args)

	requireSystemd(*unit)
	requireRoot("stop")
	if !serviceActive(*unit) {
		fmt.Println("  面板未在运行（无需操作）")
		return
	}
	runSystemctl("stop", *unit)
	if serviceActive(*unit) {
		die(1, "停止后面板仍在运行，请检查：systemctl status %s", *unit)
	}
	fmt.Println("  ✔ 已停止")
	fmt.Println("    内核进程随面板一起停（systemd 的 cgroup 默认行为），节点会断流。")
}

func cmdRestart(args []string) {
	fs := flag.NewFlagSet("restart", flag.ExitOnError)
	unit := fs.String("unit", defaultUnit, "systemd 单元名")
	wait := fs.Duration("wait", 10*time.Second, "等待就绪的时长")
	_ = tpParse(fs, args)

	requireSystemd(*unit)
	requireRoot("restart")
	runSystemctl("restart", *unit)
	reportReady(*unit, *wait)
}

// reportReady 打印重启结果。超时返回退出码 1，方便脚本判断。
func reportReady(unit string, wait time.Duration) {
	d, ok := waitReady(unit, wait)
	if !ok {
		die(1, "面板已启动，但 %s 内没有就绪（%s 无响应）。\n  看日志：tp logs\n  常见原因：端口被占、数据目录权限、配置损坏",
			wait, healthURL(unit))
	}
	fmt.Printf("  ✔ 已就绪（%.1fs）· %s\n", d.Seconds(), panelVersion(unit))
}

// printReady 已在运行时只报版本，不重复等待。
func printReady(unit string) {
	if v := panelVersion(unit); v != "" {
		fmt.Printf("    当前版本 %s\n", v)
	}
}

// panelVersion 从 /api/public/info 取版本号（免认证）。
// 取不到不算失败——服务可能在但接口变了，不该让 tp start 因此报错。
func panelVersion(unit string) string {
	url := strings.Replace(healthURL(unit), "/healthz", "/api/public/info", 1)
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get(url)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var v struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return ""
	}
	return v.Version
}

func cmdLogs(args []string) {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	unit := fs.String("unit", defaultUnit, "systemd 单元名")
	lines := fs.Int("n", 200, "显示最近多少行")
	follow := fs.Bool("f", false, "持续跟踪（Ctrl-C 退出）")
	_ = tpParse(fs, args)

	if _, err := exec.LookPath("journalctl"); err != nil {
		die(1, "没有 journalctl，无法读取服务日志。\n  面板是手动前台运行的？日志就在那个终端里。")
	}
	if !systemdAvailable(*unit) {
		die(1, "没有找到 systemd 单元 %s，无法读取日志。\n  面板是手动前台运行的？日志就在那个终端里。", *unit)
	}
	argv := []string{"-u", *unit, "-n", fmt.Sprint(*lines), "--no-pager"}
	if *follow {
		argv = append(argv, "-f")
	}
	cmd := exec.Command("journalctl", argv...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		// Ctrl-C 退出时 journalctl 返回非零，不当成错误
		if *follow {
			return
		}
		die(1, "journalctl 失败: %v", err)
	}
}
