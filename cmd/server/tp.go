// tp：面向日常运维的精简命令入口。
//
// 与 cli.go 的分工：cli.go 放具体实现（status / doctor / backup / restore /
// 重置密码），这里只负责命令表、分发与输出约定。**旧命令名一个都没改**，
// tp 只是它们的新入口——install.sh 里的 `reset-password` 调用、
// systemd 的 `serve`、文档里的 `triune-panel status` 全部照旧。
//
// 两条硬规则：
//   - 只读命令直连 SQLite，**面板挂了也能查**。这是 CLI 最有价值的时刻：
//     服务起不来时你唯一能用的就是它，所以查询一律不依赖 daemon。
//   - 任何会改变内核状态的命令都不在这里直接动手（见 cli.go 顶部的说明），
//     必须走面板；P1 接 unix socket 之后才补上这些写操作。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"
)

// tpCmd 一条命令。name 用于匹配（支持唯一前缀），desc 进帮助。
type tpCmd struct {
	name   string
	desc   string
	run    func(args []string)
	hidden bool // 兼容用的旧名，不出现在帮助里
}

// tpCommands 命令表。顺序即帮助里的顺序：先日常，再查询，最后维护。
var tpCommands = []tpCmd{
	{name: "status", desc: "只读汇总：服务 / 入站 / 用户 / 内核 / 证书", run: cmdStatus},
	{name: "start", desc: "启动面板", run: cmdStart},
	{name: "stop", desc: "停止面板", run: cmdStop},
	{name: "restart", desc: "重启面板并等待就绪", run: cmdRestart},
	{name: "logs", desc: "面板日志（journalctl）", run: cmdLogs},
	{name: "update", desc: "升级面板二进制", run: cmdUpdate},

	{name: "in", desc: "入站：ls / show", run: cmdIn},
	{name: "user", desc: "用户：ls / show / link", run: cmdUser},
	{name: "audit", desc: "审计日志", run: cmdAudit},

	{name: "doctor", desc: "环境自检", run: cmdDoctor},
	{name: "backup", desc: "备份数据目录", run: cmdBackup},
	{name: "restore", desc: "从备份恢复", run: cmdRestore},
	{name: "passwd", desc: "重置管理密码", run: cmdResetPassword},

	{name: "version", desc: "打印版本", run: func([]string) { printVersion() }},
	{name: "help", desc: "本帮助", run: cmdHelp},

	// 兼容：老名字继续可用，但不在帮助里占地方
	{name: "reset-password", run: cmdResetPassword, hidden: true},
	{name: "reset", run: cmdResetPassword, hidden: true},
}

// tpMain 分发子命令。args 是 os.Args[1:]。
func tpMain(args []string) {
	if len(args) == 0 {
		// `tp` 不带参数 = 看状态。**绝不在这里启动服务**：把「手滑敲了 tp」
		// 变成「前台起了一个面板」是最坏的一种意外——端口被占时还会甩出
		// 一堆看不懂的错。要启动请用 tp start。
		cmdStatus(nil)
		return
	}
	if strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "-h", "--help":
			tpUsage()
			return
		case "-v", "--version", "-version":
			printVersion()
			return
		}
		// 其余开关一律当作「给 status 的参数」：`tp --data DIR`、`tp --json`
		// 都是最自然的写法，不该报「未知命令 --data」。
		cmdStatus(args)
		return
	}
	c, ok := lookupTP(args[0])
	if !ok {
		fmt.Fprintf(os.Stderr, "未知命令 %q\n\n", args[0])
		tpUsage()
		os.Exit(2)
	}
	c.run(args[1:])
}

// lookupTP 精确匹配优先，否则按唯一前缀匹配。
//
// 前缀缩写是为了少敲几个键（tp u ls = tp user ls），但**有歧义时必须报错**
// 而不是猜一个：`tp st` 同时是 status 和 start，猜错就是"本想看状态，
// 结果启动了一个服务"。
func lookupTP(name string) (tpCmd, bool) {
	var hits []tpCmd
	for _, c := range tpCommands {
		if c.name == name {
			return c, true
		}
		if strings.HasPrefix(c.name, name) {
			hits = append(hits, c)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], true
	case 0:
		return tpCmd{}, false
	}
	names := make([]string, len(hits))
	for i, c := range hits {
		names[i] = c.name
	}
	fmt.Fprintf(os.Stderr, "%q 有歧义，可能是：%s\n", name, strings.Join(names, " / "))
	os.Exit(2)
	return tpCmd{}, false
}

// cmdHelp `tp help` 打短表，`tp help --full` 打带备份加密/异地备份细节的长版。
//
// 长版内容（usage）本来就写好了，只是长到不适合当默认帮助——但它记着
// 「口令丢失 = 归档永久打不开」这类不能省的话，所以留着并给它一个入口。
func cmdHelp(args []string) {
	if len(args) > 0 {
		switch args[0] {
		case "--full", "-full", "full":
			usage()
			return
		}
	}
	tpUsage()
}

func tpUsage() {
	var b strings.Builder
	b.WriteString(`tp — Triune Panel 命令行

用法:
  tp <命令> [参数]

日常:
  tp                     概览（同 tp status）
  tp status [--json]     只读汇总：服务 / 入站 / 用户 / 内核 / 证书
  tp start               启动面板
  tp stop                停止面板（内核会一起停）
  tp restart             重启面板，等到就绪才返回
  tp logs [-f] [-n 200]  面板日志
  tp update [--check]    升级面板（详见 tp update --help）

查询（直连数据库，面板没在跑也能用）:
  tp in ls [--json]      入站列表
  tp in show <id>        入站详情
  tp user ls [--json]    用户列表：配额 / 已用 / 到期
  tp user show <name>    用户详情
  tp user link <name>    订阅链接（三种格式）
  tp audit [--since 7d]  审计日志

维护:
  tp doctor              环境自检（有失败项时退出码 1）
  tp backup [--out DIR]  备份数据目录
  tp restore <归档>      从备份恢复
  tp passwd              重置管理密码

通用参数:
  --data DIR   数据目录；默认 ./data → /opt/triune-panel/data 自动探测
               （也可用环境变量 TRIUNE_DATA）
  --json       机器可读输出（标了 --json 的命令支持）

命令名支持唯一前缀缩写：tp au 等同 tp audit；有歧义时会报错
（tp u 就同时命中 update 和 user，此时必须写全）。
备份加密、异地备份的完整参数：tp help --full
退出码: 0 成功 · 1 执行失败 · 2 用法错误
`)
	fmt.Print(b.String())
}

// ---- 输出与交互辅助 ----

// isTTY 判断标准输出是不是终端。用于决定要不要上色、能不能交互提问。
func isTTY() bool { return term.IsTerminal(int(os.Stdout.Fd())) }

// tpJSON 以 JSON 输出，供脚本取值。
func tpJSON(v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		die(1, "序列化失败: %v", err)
	}
	fmt.Println(string(b))
}

// tpTable 打印一张对齐的表。列宽按显示宽度算（CJK 记两格），
// 不依赖 tab——中文混排时 tab 一定会歪。
func tpTable(headers []string, rows [][]string) {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = dwidth(h)
	}
	for _, r := range rows {
		for i, c := range r {
			if i < len(widths) && dwidth(c) > widths[i] {
				widths[i] = dwidth(c)
			}
		}
	}
	printRow := func(cells []string) {
		var b strings.Builder
		b.WriteString("  ")
		for i, c := range cells {
			if i == len(cells)-1 {
				b.WriteString(c)
				break
			}
			b.WriteString(padLabel(c, widths[i]+2))
		}
		fmt.Println(strings.TrimRight(b.String(), " "))
	}
	printRow(headers)
	for _, r := range rows {
		printRow(r)
	}
	if len(rows) == 0 {
		fmt.Println("  （无）")
	}
}

// tpField 打印「标签 + 值」的一行，标签对齐到 10 格（与 status 一致）。
func tpField(label, format string, a ...any) {
	tpFieldW(label, 10, format, a...)
}

// tpFieldW 同 tpField，但标签列宽由调用方定。
//
// 标签本身比 width 还宽时补不出空格，值会直接贴在标签后面（「Clash / mihomohttp://…」），
// 所以一组标签里有长有短时，先算出最宽的那个再传进来。
func tpFieldW(label string, width int, format string, a ...any) {
	fmt.Printf("  %s%s\n", padLabel(label, width), fmt.Sprintf(format, a...))
}

// tpParse 解析参数，允许开关和位置参数任意穿插。
//
// 标准库 flag 遇到第一个非开关参数就停下，剩下的全丢进 NArg()。于是
// `tp in show 1 --data /opt/triune-panel/data` 里的 --data 会被当成
// 多余的位置参数吃掉，命令回落到自动探测，然后报「未找到数据库，请用
// --data 指定数据目录」——用户明明指定了。这种错误最难自查，
// 所以在入口处把开关重新排到前面。
func tpParse(fs *flag.FlagSet, args []string) error {
	flags := make([]string, 0, len(args))
	pos := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		// 负号开头的纯数字（如 -1）当位置参数，交给命令自己报「id 必须为正」
		if len(a) < 2 || a[0] != '-' || (len(a) > 1 && a[1] >= '0' && a[1] <= '9') {
			pos = append(pos, a)
			continue
		}
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			flags = append(flags, a)
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			// 不认识的开关原样交给 flag，让它自己报错——在这里猜它要不要
			// 取值，只会把错误信息改得更难懂
			flags = append(flags, a)
			continue
		}
		flags = append(flags, a)
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return fs.Parse(append(flags, pos...))
}

// confirm 破坏性操作的二次确认。
//
// 非交互环境（管道、cron）**一律返回 false**：脚本里少写一个 --yes 就
// 静默删数据，比多问一句危险得多。要跳过就显式加 --yes。
func confirm(yes bool, format string, a ...any) bool {
	if yes {
		return true
	}
	if !isTTY() {
		return false
	}
	fmt.Printf(format+" [y/N] ", a...)
	var s string
	_, _ = fmt.Scanln(&s)
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "y" || s == "yes"
}

// invokedAsTP 判断二进制是不是以 tp 这个名字被调用的（安装脚本会软链）。
//
// 这个判断决定「不带参数」的含义：triune-panel 不带参数是启动服务
//（systemd 的 ExecStart 依赖它），tp 不带参数是看状态。
func invokedAsTP() bool {
	return filepath.Base(os.Args[0]) == "tp"
}
