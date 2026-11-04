package api

// 内核切换：把已运行的入站从一个内核迁移到另一个内核。
//
// 注意「切换」只作用于**单条入站**：三个内核本来就是三个独立进程，各自管
// 自己那批入站、可以同时运行。这里做的是把某几条入站的 core 字段改掉，
// 不是让两个内核二选一。
//
// 安全流程（任何一步失败都不会让服务处于不可用状态）：
//  1. 预检：在内存中构造切换后的配置，用目标内核二进制做真实校验（不落盘、不动进程）；
//  2. 校验失败 → HTTP 422，DB 与运行配置零改动；
//  3. 校验通过 → 提交 DB（入站 core 字段）→ 旧内核按新状态收缩（迁走那条从
//     配置里消失、端口随之释放，其它入站只经历这次重启）；旧内核被搬空则直接停；
//  4. 收缩后先检查旧内核剩下的入站是否真的还在监听——不通过就中止，新内核没动过；
//  5. 走标准 apply 流水线启动新内核，再做健康检查（端口占用、启动即崩溃都会暴露）；
//  6. 任何一步失败 → 自动回滚：还原 DB 中的 core 字段，把目标内核与旧内核
//     都恢复成切换前的配置，并返回失败详情。

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"triune/internal/core"
	"triune/internal/firewall"
	"triune/internal/model"
)

type switchReq struct {
	To         string  `json:"to"`          // 目标内核：singbox | xray | mihomo
	InboundIDs []int64 `json:"inbound_ids"` // 要切换的入站；空 = 该内核全部启用入站
	DryRun     bool    `json:"dry_run"`     // true = 只预检，不执行
}

// buildRenderInputFrom 用显式的入站列表（切换后的假想状态）构造渲染输入。
func (a *Server) buildRenderInputFrom(ins []*model.Inbound, coreName string) ([]core.InboundWithClients, error) {
	allClients, err := a.Store.ListClients()
	if err != nil {
		return nil, err
	}
	byID := map[int64]*model.Client{}
	for _, c := range allClients {
		byID[c.ID] = c
	}
	var out []core.InboundWithClients
	for _, in := range ins {
		if in.Core != coreName || !in.Enabled || in.Draft {
			continue
		}
		iw := core.InboundWithClients{Inbound: in}
		ids, _ := a.Store.InboundClientIDs(in.ID)
		for _, id := range ids {
			if c, ok := byID[id]; ok && c.Enabled {
				iw.Clients = append(iw.Clients, c)
			}
		}
		out = append(out, iw)
	}
	return out, nil
}

// POST /api/switch-core
func (a *Server) handleSwitchCore(w http.ResponseWriter, r *http.Request) {
	var req switchReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if _, err := core.Get(req.To); err != nil {
		fail(w, 400, "目标内核无效: "+err.Error())
		return
	}
	// 切到兼容内核（mihomo）等于主动降级：它的协议是 sing-box 的子集且缺
	// vless / reality。反方向（mihomo → sing-box / xray）不受影响。
	if !a.compatCoreAllowed(req.To) {
		fail(w, 400, "mihomo 是兼容内核，不能作为切换目标。要迁走 mihomo 入站，请选 sing-box 或 xray。")
		return
	}
	// 目标内核必须有可用的二进制：否则预检无从校验、新进程也起不来，
	// 切过去只会造成服务静默中断 —— 直接拒绝。
	if bin, _ := a.Pipe.Settings.Get("bin_" + req.To); bin == "" {
		fail(w, 400, fmt.Sprintf("目标内核 %s 的二进制路径未配置，请先到「设置」填写再切换", req.To))
		return
	}

	all, err := a.Store.ListInbounds("")
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	byID := map[int64]*model.Inbound{}
	for _, in := range all {
		byID[in.ID] = in
	}

	// 选定要切换的入站
	var targets []*model.Inbound
	if len(req.InboundIDs) == 0 {
		for _, in := range all {
			if in.Core != req.To && in.Enabled && !in.Draft {
				targets = append(targets, in)
			}
		}
	} else {
		for _, id := range req.InboundIDs {
			in, ok := byID[id]
			if !ok {
				fail(w, 404, fmt.Sprintf("入站 %d 不存在", id))
				return
			}
			if in.Core == req.To {
				fail(w, 400, fmt.Sprintf("入站 %q 已运行在 %s 上", in.Name, req.To))
				return
			}
			if !in.Enabled || in.Draft {
				fail(w, 400, fmt.Sprintf("入站 %q 未启用或仍是草稿，无需切换", in.Name))
				return
			}
			targets = append(targets, in)
		}
	}
	if len(targets) == 0 {
		fail(w, 400, "没有可切换的入站")
		return
	}

	// 假想状态：目标入站的 core 改为目标内核
	prospective := make([]*model.Inbound, 0, len(all))
	for _, in := range all {
		cp := *in
		for _, t := range targets {
			if t.ID == in.ID {
				cp.Core = req.To
			}
		}
		prospective = append(prospective, &cp)
	}

	// 受影响的旧内核集合
	fromCores := map[string]bool{}
	for _, t := range targets {
		fromCores[t.Core] = true
	}
	fromList := make([]string, 0, len(fromCores))
	for k := range fromCores {
		fromList = append(fromList, k)
	}
	sort.Strings(fromList)

	// ---- 1. 预检目标内核（不落盘、不动进程）----
	targetInput, err := a.buildRenderInputFrom(prospective, req.To)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	res, content, verr := a.Pipe.DryRun(req.To, targetInput)
	if verr != nil {
		fail(w, 422, fmt.Sprintf("切换预检失败（运行配置未受任何影响）：%v", verr))
		return
	}
	preview := map[string]any{
		"to":        req.To,
		"from":      fromList,
		"inbounds":  inboundNames(targets),
		"validated": res != nil && res.Validated,
		"hash":      res.Hash,
		"content":   content,
	}
	if req.DryRun {
		writeJSON(w, 200, preview)
		return
	}

	// ---- 2. 提交 DB（先拍还原快照，提交失败可整体回退）----
	origs := make([]*model.Inbound, 0, len(targets))
	for _, t := range targets {
		cp := *byID[t.ID] // byID 指向 ListInbounds 返回的原始对象，未被修改
		origs = append(origs, &cp)
	}
	for _, t := range targets {
		cp := *t
		cp.Core = req.To
		cp.UpdatedAt = time.Now()
		if err := a.Store.UpdateInbound(&cp); err != nil {
			a.restoreCores(origs)
			fail(w, 500, "更新数据库失败，已还原: "+err.Error())
			return
		}
		t.Core = req.To // 让响应里的入站展示与切换结果一致
	}

	// ---- 3. 先收缩旧内核（不再整体 Stop）----
	//
	// 这一步是 v1.2.2 的核心修正。原实现直接 ProcMgr.Stop(from) 把整个旧内核
	// 停掉，于是「迁一条入站」会连带断掉旧内核上所有其它入站，而后续的收缩
	// 只把失败记进 warnings、HTTP 照样返回 200 —— 那些入站就一直是断的，
	// 界面上却全是绿的。
	//
	// 现在把失败点全部前移：旧内核先按切换后的状态收缩（迁走那条从配置里
	// 消失，端口随之释放），其它入站只经历这次重启；收缩或健康检查不成功
	// 就中止切换，新内核一个字节都没动过。只有旧内核被搬空了才真的 Stop。
	for _, from := range fromList {
		input, err := a.buildRenderInput(from)
		if err != nil {
			a.restoreCores(origs)
			fail(w, 500, "读取切换后的入站失败（DB 已还原，新内核未改动）: "+err.Error())
			return
		}
		if len(input) == 0 {
			// 旧内核已经没有入站了：停掉它，释放端口，也省一份内存。
			if a.ProcMgr != nil {
				a.ProcMgr.Stop(from)
			}
			continue
		}
		if _, err := a.applyWithRevision(from, input,
			fmt.Sprintf("shrink before switch to %s", req.To)); err != nil {
			rollErrs := a.rollbackSwitch(origs, fromList, req.To)
			detail := fmt.Sprintf("收缩 %s 失败，切换已中止（新内核未改动）：%v", from, err)
			if len(rollErrs) > 0 {
				detail += " 回滚异常（需人工检查）：" + fmt.Sprint(rollErrs)
			}
			writeJSON(w, 422, map[string]any{"error": detail, "rolled_back": true})
			return
		}
		// 旧内核上剩下的入站必须还活着。只看 apply 是否返回 nil 不够：
		// 配置合法但端口绑不上时内核会「启动成功」，服务实际是断的。
		if err := a.healthCheck(from); err != nil {
			rollErrs := a.rollbackSwitch(origs, fromList, req.To)
			detail := fmt.Sprintf("收缩 %s 后健康检查失败，切换已中止（新内核未改动）：%v", from, err)
			if len(rollErrs) > 0 {
				detail += " 回滚异常（需人工检查）：" + fmt.Sprint(rollErrs)
			}
			writeJSON(w, 422, map[string]any{"error": detail, "rolled_back": true})
			return
		}
	}

	// ---- 4. 启动目标内核（带修订记录）----
	applyRes, aerr := a.applyWithRevision(req.To, targetInput, fmt.Sprintf("switch %v → %s", fromList, req.To))
	if aerr == nil {
		aerr = a.healthCheck(req.To)
	}
	if aerr != nil {
		// ---- 5. 自动回滚：还原 DB，两个内核都恢复成切换前的样子 ----
		rollErrs := a.rollbackSwitch(origs, fromList, req.To)
		detail := fmt.Sprintf("切换到 %s 失败：%v。已自动回滚到 %v。", req.To, aerr, fromList)
		if len(rollErrs) > 0 {
			detail += " 回滚异常（需人工检查）：" + fmt.Sprint(rollErrs)
		}
		hash := ""
		if applyRes != nil {
			hash = applyRes.Hash
		}
		writeJSON(w, 422, map[string]any{
			"error":       detail,
			"rolled_back": true,
			"apply_hash":  hash,
		})
		return
	}
	// 保留这个字段是为了不破坏前端契约。v1.2.2 起收缩失败不再降级成警告，
	// 而是直接中止切换并回滚，所以这里恒为空。
	warnings := []string{}

	writeJSON(w, 200, map[string]any{
		"ok":          true,
		"to":          req.To,
		"from":        fromList,
		"inbounds":    inboundNames(targets),
		"validated":   true,
		"hash":        applyRes.Hash,
		"revision_id": applyRes.RevisionID,
		"warnings":    warnings,
	})
	a.changed(r, AuditSwitchCore, req.To,
		fmt.Sprintf("%d 个入站由 %s 切换到 %s（修订 #%d）",
			len(targets), strings.Join(fromList, "/"), req.To, applyRes.RevisionID))
}

// applyWithRevision 在标准 apply 流水线外补上修订记录（与 handleApply 同构），
// 保证切换/回滚动作全程可审计。
func (a *Server) applyWithRevision(coreName string, input []core.InboundWithClients, note string) (*core.ApplyResult, error) {
	c, err := core.Get(coreName)
	if err != nil {
		return nil, err
	}
	rev := &model.ConfigRevision{Core: coreName, Status: model.RevPending, Note: note}
	if content, rerr := c.Render(input, a.Pipe.RenderOptsFor(c.Name())); rerr == nil {
		rev.Content = string(content)
		rev.Hash = shortHash(content)
	}
	_ = a.Store.CreateRevision(rev)
	res, err := a.Pipe.Apply(coreName, input, note)
	if err != nil {
		_ = a.Store.UpdateRevisionStatus(rev.ID, model.RevFailed, err.Error())
		return res, err
	}
	_ = a.Store.UpdateRevisionStatus(rev.ID, model.RevApplied, "")
	res.RevisionID = rev.ID
	return res, nil
}

// healthCheck 确认内核不仅"活着"，而且真的在监听它该监听的端口。
//
// 只看进程存活是不够的：xray 端口被占、mihomo listener 证书加载失败
// 都只记一条日志、进程继续跑，界面显示"已启用"而实际没有服务。
// 这里逐个入站探测监听状态，未监听直接判失败并带上内核日志尾部。
func (a *Server) healthCheck(coreName string) error {
	if a.ProcMgr == nil {
		return nil
	}
	for i := 0; i < 3; i++ {
		time.Sleep(700 * time.Millisecond)
		if alive, _ := a.ProcMgr.Status(coreName); !alive {
			return fmt.Errorf("内核进程启动后退出（健康检查失败）。最近日志：\n%s", a.coreLogTail(coreName, 15))
		}
	}
	ins, err := a.Store.ListInbounds(coreName)
	if err != nil {
		return nil // 查不到入站时不做端口判定，避免误报
	}
	var missing []string
	for _, in := range ins {
		if !in.Enabled || in.Draft {
			continue
		}
		if !portListening(in) {
			missing = append(missing, fmt.Sprintf("%s (%s:%d/%s)",
				in.Name, orAll(in.Listen), in.Port, firewall.ProtoOf(in.Protocol, in.Transport)))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("内核进程在运行，但以下入站端口没有监听：%s\n常见原因：端口被其它程序占用、证书加载失败、协议参数不合法。\n最近日志：\n%s",
			strings.Join(missing, "、"), a.coreLogTail(coreName, 20))
	}
	return nil
}

// coreLogTail 读取内核日志尾部（与 proc.Manager.Logs 一致的数据源）。
func (a *Server) coreLogTail(coreName string, n int) string {
	logs := a.ProcMgr.Logs(coreName, a.DataDir+"/cores/"+coreName, n)
	return strings.Join(logs, "\n")
}

func orAll(listen string) string {
	if listen == "" {
		return "::"
	}
	return listen
}

// portListening 探测入站端口是否真的处于监听状态。
// TCP 直接拨号；UDP 没有可靠的"拨通"语义，改用独占绑定反证：
// 能绑上说明没有进程在监听它。
func portListening(in *model.Inbound) bool {
	host := in.Listen
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	proto := firewall.ProtoOf(in.Protocol, in.Transport)
	if proto == "udp" {
		addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", host, in.Port))
		if err != nil {
			return true // 地址解析不了就不下结论
		}
		c, err := net.ListenUDP("udp", addr)
		if err != nil {
			return true // 绑不上 = 已被占用 = 有进程在监听
		}
		c.Close()
		return false
	}
	c, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, in.Port), 2*time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// restoreCores 把入站的 core 字段还原为切换前的值。
func (a *Server) restoreCores(origs []*model.Inbound) {
	for _, o := range origs {
		cp := *o
		cp.UpdatedAt = time.Now()
		_ = a.Store.UpdateInbound(&cp)
	}
}

// rollbackSwitch 把切换失败后的现场恢复成切换前的样子。
//
// 顺序有讲究：先恢复目标内核，再恢复旧内核。目标内核可能已经起来并占着
// 迁过去的端口，先让它的配置缩回去（或直接停掉），旧内核才绑得回原端口。
//
// 返回每一步的错误，调用方拼进响应里；空切片表示全部恢复成功。
func (a *Server) rollbackSwitch(origs []*model.Inbound, fromList []string, to string) []string {
	a.restoreCores(origs) // 先还原 DB，后面两次渲染都以还原后的状态为准
	var errs []string
	if err := a.restoreCoreTo(to, "rollback switch: 恢复 "+to+" 到切换前"); err != nil {
		errs = append(errs, fmt.Sprintf("回滚 %s: %v", to, err))
	}
	for _, from := range fromList {
		if err := a.restoreCoreTo(from, "rollback switch: 恢复 "+from+" 到切换前"); err != nil {
			errs = append(errs, fmt.Sprintf("回滚 %s: %v", from, err))
		}
	}
	return errs
}

// restoreCoreTo 按当前 DB 状态把某个内核重新应用一遍（DB 需已还原）。
// 没有入站时直接停进程——空配置的内核留着只会占端口。
func (a *Server) restoreCoreTo(name, note string) error {
	input, err := a.buildRenderInput(name)
	if err != nil {
		return err
	}
	if len(input) == 0 {
		if a.ProcMgr != nil {
			a.ProcMgr.Stop(name)
		}
		return nil
	}
	_, err = a.applyWithRevision(name, input, note)
	return err
}

func inboundNames(ins []*model.Inbound) []string {
	out := make([]string, 0, len(ins))
	for _, in := range ins {
		out = append(out, fmt.Sprintf("#%d %s (%s:%d)", in.ID, in.Name, in.Core, in.Port))
	}
	return out
}
