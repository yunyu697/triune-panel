// tp ui：说清「浏览器到底会拿到哪一版界面」。
//
// v1.5.0 起页面只有一个来源——二进制内嵌页；磁盘上的 data/web/index.html
// 只有显式声明 custom 时才参与渲染。所以这里不再需要比较「磁盘标记 vs 内嵌
// 标记」，只需要回答两件事：二进制带的是哪一版、谁在服务页面。
//
// 只读优先，与 tp 其它查询命令一致：默认不写任何文件；`tp ui reset` 是显式
// 修复动作，把磁盘上的页面移走（先留 .bak），让界面回到二进制内嵌版。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"triune/internal/webui"
)

type uiReport struct {
	Version     string `json:"version"`
	DataDir     string `json:"data_dir"`
	PagePath    string `json:"page_path"`
	Embedded    string `json:"embedded_marker"`
	DiskPresent bool   `json:"disk_present"`
	DiskMarker  string `json:"disk_marker,omitempty"`
	DiskBytes   int64  `json:"disk_bytes,omitempty"`
	DiskModTime string `json:"disk_mod_time,omitempty"`
	DiskCustom  bool   `json:"disk_custom"`
	DiskInUse   bool   `json:"disk_in_use"`
	Served      string `json:"served"`
	Action      string `json:"action"`
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
	page, custom := webui.ServedPage(dir)

	rep := uiReport{
		Version:  Version,
		DataDir:  dir,
		PagePath: path,
		Embedded: webui.EmbeddedVersion(),
	}
	if disk, err := os.ReadFile(path); err == nil {
		rep.DiskPresent = true
		rep.DiskMarker = webui.PageMarker(disk)
		rep.DiskBytes = int64(len(disk))
		rep.DiskCustom = webui.IsCustomUI(disk)
		rep.DiskInUse = custom
		if fi, err := os.Stat(path); err == nil {
			rep.DiskModTime = fi.ModTime().Format("2006-01-02 15:04")
		}
	}

	switch {
	case custom:
		rep.Served = "custom"
		rep.Action = "正在服务 data/web/index.html 里的自定义界面；想回到内置界面请跑 tp ui reset"
	case rep.DiskPresent:
		rep.Served = "embedded"
		rep.Action = "磁盘上的页面未声明 custom，已不参与渲染（可以删：tp ui reset）；界面跟随二进制版本"
	default:
		rep.Served = "embedded"
		rep.Action = "界面跟随二进制版本，升级后必然更新；若浏览器仍是旧界面，请强制刷新或绕开反向代理"
	}

	if *asJSON {
		b, _ := json.MarshalIndent(rep, "", "  ")
		fmt.Println(string(b))
		return
	}

	fmt.Printf("版本      %s\n", rep.Version)
	fmt.Printf("数据目录  %s\n", rep.DataDir)
	fmt.Printf("内嵌页面  %s · %s\n", uiOrDash(rep.Embedded), uiBytes(int64(len(page))))
	if !rep.DiskPresent {
		fmt.Printf("磁盘页面  （不存在）\n")
	} else {
		label := uiOrDash(rep.DiskMarker)
		if rep.DiskCustom {
			label = "custom"
		}
		suffix := "未参与渲染"
		if rep.DiskInUse {
			suffix = "正在使用"
		}
		fmt.Printf("磁盘页面  %s · %s · %s（%s）\n", label, uiBytes(rep.DiskBytes), rep.DiskModTime, suffix)
	}
	fmt.Printf("页面来源  %s\n", uiServedLabel(rep.Served))
	fmt.Printf("结论      %s\n", rep.Action)
}

func cmdUIReset(args []string) {
	fs := flag.NewFlagSet("ui reset", flag.ExitOnError)
	dataDir := fs.String("data", "", "数据目录（默认自动探测）")
	yes := fs.Bool("yes", false, "跳过确认（非交互环境必须显式加）")
	_ = tpParse(fs, args)

	dir := resolveDataDir(*dataDir)
	path := filepath.Join(dir, "web", "index.html")
	disk, err := os.ReadFile(path)
	if err != nil {
		fmt.Printf("磁盘上没有 %s，界面就是二进制内嵌的 %s，无需处理\n", path, webui.EmbeddedVersion())
		return
	}

	if webui.IsCustomUI(disk) {
		if !confirm(*yes, "%s 是正在使用的自定义界面，确认移除（另存 .bak）？", path) {
			die(1, "已取消")
		}
	} else if !confirm(*yes, "清理已不参与渲染的残留页面 %s（另存 .bak）？", path) {
		die(1, "已取消")
	}

	// 先备份再删：万一那真是用户改过、只是忘了加 custom 标记的界面，
	// 从 .bak 里能原样捞回来。
	if err := os.WriteFile(path+".bak", disk, 0o644); err != nil {
		die(1, "备份原页面失败: %v", err)
	}
	if err := os.Remove(path); err != nil {
		die(1, "移除页面失败: %v", err)
	}

	fmt.Printf("已移除 %s\n", path)
	fmt.Printf("  备份      %s.bak\n", path)
	fmt.Printf("  当前服务  二进制内嵌界面 %s（无需重启）\n", webui.EmbeddedVersion())
	fmt.Println("想以后继续用自定义界面：把文件放回去，并在里面写上 " + webui.CustomUIMarker)
}

func uiServedLabel(s string) string {
	if s == "custom" {
		return "磁盘上的自定义界面（custom）"
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
