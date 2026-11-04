package api

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"triune/internal/core"
	"triune/internal/model"
	"triune/internal/proc"
	"triune/internal/store"
)

// healNote 是「版本变化后自动重渲染」写进配置历史的那条修订的说明。
// 用户过一阵发现配置自己变了，能在「配置历史」里查到是面板升级干的，
// 而不是以为有人动过服务器。
const healNote = "auto: 面板版本变化后重新渲染"

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
// 非 draft 的入站，它就该在跑。
//
// 配置文件默认用上一次渲染的产物，**不重新渲染**——渲染是「应用配置」的职责。
// 唯一的例外是面板版本变了（见 healStaleRender）：那种情况下磁盘上那份配置
// 是旧渲染器写的，不重做的话新版的行为永远不生效。
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

		// 面板升级过就把配置重做一遍。它内部走的是完整的「应用」流水线
		//（渲染 → 校验 → 原子替换 → 重启），成功即已启动，不必再启一次。
		if a.healStaleRender(name, cfgPath) {
			continue
		}

		if running, _ := a.ProcMgr.Status(name); running {
			continue
		}
		workDir := filepath.Join(a.Pipe.DataDir, "cores", name)
		// 上一代面板留下的内核可能还活着并占着端口：exec 换进程映像时子进程
		// 不跟着退，崩溃 / 被 SIGKILL 也一样。认得出是自己家的就先清掉，
		// 否则新进程起得来但绑不上端口，界面显示「已启用」而实际连不上。
		if killed := proc.ReapOrphans(bin, cfgPath); len(killed) > 0 {
			log.Printf("[core] 清理 %s 的残留进程 %v（上一代面板留下的，占着入站端口）", name, killed)
		}
		if err := a.ProcMgr.Start(name, bin, cfgPath, workDir); err != nil {
			log.Printf("[core] 启动 %s 失败: %v", name, err)
			continue
		}
		log.Printf("[core] 已恢复 %s（配置 %s）", name, cfgPath)
	}
}

// healStaleRender 在面板版本变化后，用当前渲染器把磁盘上的配置重做一遍。
//
// 返回 true 表示已经重新渲染并应用（内含重启），调用方不必再启动一次。
//
// 为什么必须自动做：升级只替换二进制，磁盘上的渲染产物不会自己更新。
// v1.2.0 修 sing-box REALITY 时，用户升级完看到的是「面板写着 1.2.0、节点仍然
// 连不上」——唯一的解法是有人记得去点一次「应用配置」，而当时界面上所有指标
// 都是绿的，没有任何线索指向这件事。渲染器变了，就该由面板自己把配置重做一遍。
//
// 为什么只比版本、不比内容：内容不一致有三种来源，只有第一种该在启动时处理——
//   - 渲染器变了（本次升级）→ 该重做；
//   - 用户保存了改动但还没点「应用配置」→ 不该在启动时悄悄生效；
//   - 用户回滚过配置（回滚只换文件、不动数据库）→ 更不该被覆盖。
//
// 版本号能把第一种单独摘出来，内容比对做不到。
//
// 任何一步失败都只记日志并返回 false：磁盘上那份旧配置是能用的，让它继续跑。
// 「升级」绝不能变成「节点全断」。
func (a *Server) healStaleRender(name, cfgPath string) bool {
	st := a.Pipe.RenderStamp(name)
	if st.Version == Version {
		return false
	}
	from := st.Version
	if from == "" {
		from = "无记录"
	}

	c, err := core.Get(name)
	if err != nil {
		log.Printf("[core] %s: %v", name, err)
		return false
	}
	input, err := a.buildRenderInput(name)
	if err != nil {
		log.Printf("[core] %s 的配置由 %s 渲染，但读不出当前入站，沿用旧配置: %v", name, from, err)
		return false
	}
	content, err := c.Render(input, a.Pipe.RenderOptsFor(name))
	if err != nil {
		log.Printf("[core] %s 用 %s 重新渲染失败，沿用磁盘上的旧配置（由 %s 渲染）: %v",
			name, Version, from, err)
		return false
	}

	// 渲染器没变，内容一字不差：把戳补上就行，正在跑的进程一下都不用动。
	// 这条分支让「升了个跟渲染无关的小版本」不产生任何重启。
	if old, rerr := os.ReadFile(cfgPath); rerr == nil && bytes.Equal(old, content) {
		a.Pipe.MarkRendered(name, content)
		log.Printf("[core] %s 的配置与 %s 渲染的结果一致，只更新渲染戳", name, Version)
		return false
	}

	rev := &model.ConfigRevision{
		Core: name, Status: model.RevPending, Note: healNote,
		Content: string(content), Hash: shortHash(content),
	}
	_ = a.Store.CreateRevision(rev)

	res, err := a.Pipe.Apply(name, input, healNote)
	if err != nil {
		_ = a.Store.UpdateRevisionStatus(rev.ID, model.RevFailed, err.Error())
		a.auditAs("system", nil, AuditApply, name, store.AuditFail,
			"面板版本变化后自动重新渲染失败："+err.Error())
		log.Printf("[core] %s 升级后自动重新渲染失败，沿用磁盘上的旧配置（由 %s 渲染）: %v",
			name, from, err)
		return false
	}
	_ = a.Store.UpdateRevisionStatus(rev.ID, model.RevApplied, "")
	a.auditAs("system", nil, AuditApply, name, store.AuditOK,
		fmt.Sprintf("面板升到 %s，磁盘上的配置由 %s 渲染，已自动重新渲染并应用", Version, from))
	log.Printf("[core] %s 的配置由 %s 渲染，已按 %s 重新渲染并应用", name, from, Version)
	return res.Restarted
}
