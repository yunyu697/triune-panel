package api

import (
	"log"
	"os"
	"path/filepath"

	"triune/internal/core"
)

// RestoreCores 启动时把「该跑但没在跑」的内核拉起来。
//
// 为什么需要它：内核是面板的子进程，而 systemd 默认的 KillMode=control-group
// 会把整个 cgroup 一起杀掉。所以 `systemctl restart triune-panel`
//（以及 `tp restart`、走 systemd 的 `tp update`）会把三个内核一并带走；
// 服务器重启更是全断。而内核的运行状态只存在 proc.Manager 的内存里，
// 没有任何地方做「开机自启」——不主动恢复的话，重启后节点会一直断到
// 有人进面板手动点一次「应用配置」。
//
// 判定口径与启动时的防火墙端口对账保持一致：某个内核只要有 enabled 且
// 非 draft 的入站，它就该在跑。配置文件用上一次渲染的产物，**不重新渲染**——
// 渲染是「应用配置」的职责，启动阶段不该顺手改运行中的配置。
//
// 任何一步失败都只记日志、不返回错误：内核起不来不该让面板也起不来，
// 否则就成了「节点挂了 → 面板也进不去 → 没法排查」。
func (a *Server) RestoreCores() {
	if a.ProcMgr == nil || a.Pipe == nil {
		return
	}
	for _, c := range core.All() {
		name := c.Name()
		ins, err := a.Store.ListInbounds(name)
		if err != nil {
			log.Printf("[core] 读取 %s 入站失败: %v", name, err)
			continue
		}
		want := false
		for _, in := range ins {
			if in.Enabled && !in.Draft {
				want = true
				break
			}
		}
		if !want {
			continue
		}
		if running, _ := a.ProcMgr.Status(name); running {
			continue
		}
		bin, _ := a.Store.GetSetting("bin_" + name)
		if bin == "" {
			log.Printf("[core] %s 有启用中的入站，但没配二进制路径，跳过", name)
			continue
		}
		if _, err := os.Stat(bin); err != nil {
			log.Printf("[core] %s 的二进制 %s 不存在，跳过", name, bin)
			continue
		}
		cfgPath, err := a.Pipe.ConfigPath(name)
		if err != nil {
			log.Printf("[core] 取 %s 的配置路径失败: %v", name, err)
			continue
		}
		if _, err := os.Stat(cfgPath); err != nil {
			log.Printf("[core] %s 的配置 %s 不存在（还没应用过配置？），跳过", name, cfgPath)
			continue
		}
		workDir := filepath.Join(a.Pipe.DataDir, "cores", name)
		if err := a.ProcMgr.Start(name, bin, cfgPath, workDir); err != nil {
			log.Printf("[core] 启动 %s 失败: %v", name, err)
			continue
		}
		log.Printf("[core] 已恢复 %s（配置 %s）", name, cfgPath)
	}
}
