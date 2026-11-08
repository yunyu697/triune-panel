// tp ui：把「浏览器到底拿到哪一版界面」一次说清。
//
// 「升级后界面没变」有三层可能：跑着的二进制是旧的、磁盘上的
// data/web/index.html 是旧的、浏览器或反向代理缓存了旧页面。前两层在这里
// 都能直接看清，剩下的就只能是缓存——不用再靠猜。
//
// 只读优先，与 tp 其它查询命令一致：默认不写任何文件；`tp ui reset` 是
// 显式修复动作，会把磁盘页面覆盖成当前二进制内嵌的界面，并先留一份 .bak。
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"triune/internal/webui"
)

type uiReport struct {
	Version      string `json:"version"`
	DataDir      string `json:"data_dir"`
	PagePath     string `json:"page_path"`
	Embedded     string `json:"embedded_marker"`
	DiskMarker   string `json:"disk_marker"`
	DiskBytes    int64  `json:"disk_bytes"`
	DiskModTime  string `json:"disk_mod_time,omitempty"`
	DiskReadable bool   `json:"disk_readable"`
	Served       string `json:"served"`
	Action       string `json:"action"`
}

func cmdUI(args []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "reset", "repair":
			cmdUIReset(args[1:])
			return
		case "status", "show":
			cmdUIStatus(args[1:])
			return
		}
		die(2, "未知动作 %q（可用: tp ui / tp ui reset）", args[0])
	}
	cmdUIStatus(args)
}

func cmdUIStatus(args []string) {
	fs := flag.NewFlagSet("ui", flag.ExitOnError)
	dataDir := fs.String("data", "", "数据目录（默认自动探测）")
	asJSON := fs.Bool("json", false, "以 JSON 输出")
	_ = tpParse(fs, args)

	dir := resolveDataDir(*dataDir)
	path := filepath.Join(dir, "web", "index.html")
	disk, readErr := os.ReadFile(path)
	emb := webui.EmbeddedPage()

	rep := uiReport{
		Version:      Version,
		DataDir:      dir,
		PagePath:     path,
		Embedded:     webui.EmbeddedVersion(),
		DiskReadable: readErr == nil,
	}
	if readErr == nil {
		rep.DiskMarker = webui.PageMarker(disk)
		rep.DiskBytes = int64(len(disk))
		if fi, err := os.Stat(path); err == nil {
			rep.DiskModTime = fi.ModTime().Format("2006-01-02 15:04")
		}
	}

	served := webui.ResolvePage(disk)
	switch {
	case readErr != nil:
		rep.Served = "embedded"
		rep.Action = "磁盘页面读不到，/ 会直接服务内嵌页；若浏览器仍是旧界面，问题在浏览器或反向代理缓存"
	case bytes.Equal(served, disk):
		rep.Served = "disk"
		rep.Action = "实际服务的就是磁盘页面；若界面没变，请强制刷新（Ctrl+Shift+R）或绕开反向代理"
		if rep.DiskMarker == "" {
			rep.Action = "磁盘页面没有 build 标记、也不像内置页，按自定义 UI 原样保留；要恢复内置界面请跑 tp ui reset"
		}
	default:
		rep.Served = "embedded"
		rep.Action = "磁盘页面不是当前版本，服务端已改为发送内嵌页；请强制刷新或绕开反向代理"
	}

	if *asJSON {
		b, _ := json.MarshalIndent(rep, "", "  ")
		fmt.Println(string(b))
		return
	}

	fmt.Printf("版本      %s\n", rep.Version)
	fmt.Printf("数据目录  %s\n", rep.DataDir)
	fmt.Printf("内嵌页面  %s · %s\n", uiOrDash(rep.Embedded), uiBytes(int64(len(emb))))
	if readErr != nil {
		fmt.Printf("磁盘页面  %s（读不到: %v）\n", path, readErr)
	} else {
		fmt.Printf("磁盘页面  %s · %s · %s\n", uiOrDash(rep.DiskMarker), uiBytes(rep.DiskBytes), rep.DiskModTime)
	}
	fmt.Printf("实际服务  %s\n", uiServedLabel(rep.Served))
	fmt.Printf("结论      %s\n", rep.Action)
}

func cmdUIReset(args []string) {
	fs := flag.NewFlagSet("ui reset", flag.ExitOnError)
	dataDir := fs.String("data", "", "数据目录（默认自动探测）")
	yes := fs.Bool("yes", false, "跳过确认（非交互环境必须显式加）")
	_ = tpParse(fs, args)

	dir := resolveDataDir(*dataDir)
	path := filepath.Join(dir, "web", "index.html")
	page := webui.EmbeddedPage()

	before := "（文件不存在）"
	if b, err := os.ReadFile(path); err == nil {
		before = uiOrDash(webui.PageMarker(b))
		if bytes.Equal(b, page) {
			fmt.Printf("磁盘页面已经是内置 %s，无需重写：%s\n", webui.EmbeddedVersion(), path)
			return
		}
		if !confirm(*yes, "把 %s 覆盖为内置界面（原文件另存 .bak）？", path) {
			die(1, "已取消")
		}
		// 覆盖前留一份：用户可能改过自定义界面，找回来比重新写好受得多。
		if err := os.WriteFile(path+".bak", b, 0o644); err != nil {
			die(1, "备份原页面失败: %v", err)
		}
	} else if !confirm(*yes, "在 %s 写入内置界面？", path) {
		die(1, "已取消")
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		die(1, "创建 web 目录失败: %v", err)
	}
	if err := os.WriteFile(path, page, 0o644); err != nil {
		die(1, "写入页面失败: %v", err)
	}

	fmt.Printf("已写入 %s\n", path)
	fmt.Printf("  标记    %s → %s\n", before, uiOrDash(webui.EmbeddedVersion()))
	fmt.Printf("  大小    %s\n", uiBytes(int64(len(page))))
	if _, err := os.Stat(path + ".bak"); err == nil {
		fmt.Printf("  备份    %s.bak\n", path)
	}
	// 响应层用同一个 ResolvePage 判定，所以这一步之后服务端发出的必然是
	// 当前二进制内嵌的界面，不需要重启面板。
	fmt.Println("  无需重启：服务端从下一个请求起就会发送内嵌界面，浏览器强制刷新即可")
}

func uiServedLabel(s string) string {
	if s == "disk" {
		return "磁盘页面"
	}
	return "二进制内嵌页面"
}

func uiOrDash(s string) string {
	if s == "" {
		return "（无标记）"
	}
	return s
}

func uiBytes(n int64) string {
	const kb = 1024
	switch {
	case n >= kb*kb:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(kb*kb))
	case n >= kb:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(kb))
	}
	return fmt.Sprintf("%d B", n)
}
