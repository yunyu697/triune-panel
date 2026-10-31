package api

// 内核切换：把已运行的入站从一个内核迁移到另一个内核。
//
// 安全流程（任何一步失败都不会让服务处于不可用状态）：
//  1. 预检：在内存中构造切换后的配置，用目标内核二进制做真实校验（不落盘、不动进程）；
//  2. 校验失败 → HTTP 422，DB 与运行配置零改动；
//  3. 校验通过 → 提交 DB（入站 core 字段）→ 停旧内核进程（释放端口）→
//     走标准 apply 流水线启动新内核；
//  4. 健康检查：确认新进程持续存活（端口占用、启动即崩溃等都会在此暴露）；
//  5. 健康检查失败 → 自动回滚：还原 DB 中的 core 字段，重新 apply 旧内核
//     （旧配置此前已验证可运行），并返回失败详情；
//  6. 切换成功后，若原内核仍承载其他入站，则重新 apply 原内核使其配置同步收缩。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"triune/internal/core"
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
		"to":          req.To,
		"from":        fromList,
		"inbounds":    inboundNames(targets),
		"validated":   res != nil && res.Validated,
		"hash":        res.Hash,
		"content":     content,
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
	}

	// ---- 3. 停旧内核进程（释放端口）----
	for _, from := range fromList {
		a.ProcMgr.Stop(from)
	}

	// ---- 4. apply 新内核（带修订记录）----
	applyRes, aerr := a.applyWithRevision(req.To, targetInput, fmt.Sprintf("switch %v → %s", fromList, req.To))
	if aerr == nil {
		aerr = a.healthCheck(req.To)
	}
	if aerr != nil {
		// ---- 5. 自动回滚：还原 DB + 重启旧内核 ----
		a.restoreCores(origs)
		rollErrs := []string{}
		for _, from := range fromList {
			input, err := a.buildRenderInput(from)
			if err != nil {
				rollErrs = append(rollErrs, err.Error())
				continue
			}
			if _, err := a.applyWithRevision(from, input, fmt.Sprintf("rollback after failed switch to %s", req.To)); err != nil {
				rollErrs = append(rollErrs, fmt.Sprintf("回滚 %s: %v", from, err))
			}
		}
		detail := fmt.Sprintf("切换到 %s 失败：%v。已自动回滚到 %v。", req.To, aerr, fromList)
		if len(rollErrs) > 0 {
			detail += " 回滚异常（需人工检查）：" + fmt.Sprint(rollErrs)
		}
		writeJSON(w, 422, map[string]any{
			"error":       detail,
			"rolled_back": true,
			"apply_hash":  applyRes.Hash,
		})
		return
	}

	// ---- 6. 原内核仍承载其他入站 → 重新 apply 使配置收缩 ----
	warnings := []string{}
	for _, from := range fromList {
		input, err := a.buildRenderInput(from)
		if err != nil {
			warnings = append(warnings, err.Error())
			continue
		}
		if len(input) > 0 {
			if _, err := a.applyWithRevision(from, input, fmt.Sprintf("shrink after switch to %s", req.To)); err != nil {
				warnings = append(warnings, fmt.Sprintf("%s 收缩配置失败: %v", from, err))
			}
		}
	}

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
}

// applyWithRevision 在标准 apply 流水线外补上修订记录（与 handleApply 同构），
// 保证切换/回滚动作全程可审计。
func (a *Server) applyWithRevision(coreName string, input []core.InboundWithClients, note string) (*core.ApplyResult, error) {
	c, err := core.Get(coreName)
	if err != nil {
		return nil, err
	}
	rev := &model.ConfigRevision{Core: coreName, Status: model.RevPending, Note: note}
	if content, rerr := c.Render(input, a.Pipe.RenderOpts()); rerr == nil {
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

// healthCheck 确认内核进程在短暂观察窗口内持续存活。
// 端口被占、配置运行期错误等"启动即退出"的问题都会在这里暴露。
func (a *Server) healthCheck(coreName string) error {
	if a.ProcMgr == nil {
		return nil
	}
	for i := 0; i < 3; i++ {
		time.Sleep(700 * time.Millisecond)
		if alive, _ := a.ProcMgr.Status(coreName); !alive {
			logs := a.ProcMgr.Logs(coreName, a.DataDir+"/cores/"+coreName, 15)
			detail := ""
			for _, l := range logs {
				detail += l + "\n"
			}
			return fmt.Errorf("内核进程启动后退出（健康检查失败）。最近日志：\n%s", detail)
		}
	}
	return nil
}

// restoreCores 把入站的 core 字段还原为切换前的值。
func (a *Server) restoreCores(origs []*model.Inbound) {
	for _, o := range origs {
		cp := *o
		cp.UpdatedAt = time.Now()
		_ = a.Store.UpdateInbound(&cp)
	}
}

func inboundNames(ins []*model.Inbound) []string {
	out := make([]string, 0, len(ins))
	for _, in := range ins {
		out = append(out, fmt.Sprintf("#%d %s (%s:%d)", in.ID, in.Name, in.Core, in.Port))
	}
	return out
}
