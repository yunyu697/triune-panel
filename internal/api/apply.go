package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"

	"triune/internal/core"
	"triune/internal/link"
	"triune/internal/model"
)

// ---- import ----

// handleImport 反向导入分享链接。
// mode=external: 仅聚合进订阅（最安全，不生成任何本地配置）
// mode=inbound:  生成本地入站草稿（draft=true，需手动启用并 apply）
// handleImport 导入分享链接：**只做外部节点**（进订阅聚合）。
//
// 这里刻意不再提供「链接 → 本地入站」：分享链接是客户端视角的快照，
// 没有 Reality 私钥、没有证书、没有多用户，反推出来的服务端必然缺东西，
// 表现为「导入成功、一启用就报错」。要建服务端请用 /api/inbounds/import-json
// （3x-ui / xray 的服务端原始定义）。
func (a *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, err.Error())
		return
	}
	nodes, errs := link.ParseMany(req.Text)
	if len(nodes) == 0 {
		fail(w, 400, "没有可解析的链接: "+joinErrs(errs))
		return
	}

	created, skipped, failed := a.addExternalNodes(nodes, "")
	writeJSON(w, 200, map[string]any{
		"created": created, "skipped": skipped, "failed": failed,
		"parse_errors": errStrings(errs),
	})
	if len(created)+len(skipped) > 0 {
		a.auditOK(r, AuditImport, "external",
			fmt.Sprintf("导入 %d 条外部节点（跳过 %d 条重复，失败 %d）",
				len(created), len(skipped), len(failed)))
	}
}

// ---- apply pipeline ----

// buildRenderInput 汇总某内核所有启用且非草稿的入站及用户。
func (a *Server) buildRenderInput(coreName string) ([]core.InboundWithClients, error) {
	ins, err := a.Store.ListInbounds(coreName)
	if err != nil {
		return nil, err
	}
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
		if !in.Enabled || in.Draft {
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

func (a *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Core string `json:"core"`
		Note string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, err.Error())
		return
	}
	input, err := a.buildRenderInput(req.Core)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if req.Note == "" {
		req.Note = "apply via api"
	}
	// 先渲染落一条 pending 修订
	c, err := core.Get(req.Core)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	content, rerr := c.Render(input, a.Pipe.RenderOptsFor(c.Name()))
	rev := &model.ConfigRevision{Core: req.Core, Status: model.RevPending, Note: req.Note}
	if rerr == nil {
		rev.Content = string(content)
		rev.Hash = shortHash(content)
	} else {
		rev.Detail = rerr.Error()
	}
	_ = a.Store.CreateRevision(rev)

	res, err := a.Pipe.Apply(req.Core, input, req.Note)
	if err != nil {
		_ = a.Store.UpdateRevisionStatus(rev.ID, model.RevFailed, err.Error())
		a.auditFail(r, AuditApply, req.Core, "应用失败（运行中配置未变）："+err.Error())
		// 运行中配置未受影响 —— 这是本系统的安全保证
		writeJSON(w, 422, map[string]any{"error": err.Error(), "revision_id": rev.ID, "hint": "运行中的配置未被修改；可用 rollback 恢复历史备份"})
		return
	}
	_ = a.Store.UpdateRevisionStatus(rev.ID, model.RevApplied, "")
	res.RevisionID = rev.ID
	// 启动后短暂观察：端口占用、证书缺失、协议参数错误等"启动即退出"
	// 的问题在这里暴露，而不是界面上显示成功、实际没有服务在监听。
	if res.Restarted {
		if herr := a.healthCheck(req.Core); herr != nil {
			_ = a.Store.UpdateRevisionStatus(rev.ID, model.RevFailed, herr.Error())
			a.auditFail(r, AuditApply, req.Core, "内核启动后退出："+herr.Error())
			writeJSON(w, 422, map[string]any{
				"error":       herr.Error(),
				"revision_id": rev.ID,
				"hint":        "配置已写入，但内核进程没能存活；修正上方错误后重新应用即可",
			})
			return
		}
	}
	a.changed(r, AuditApply, req.Core, fmt.Sprintf("应用成功，修订 #%d", rev.ID))
	writeJSON(w, 200, res)
}

func (a *Server) handleDryRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Core string `json:"core"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, err.Error())
		return
	}
	input, err := a.buildRenderInput(req.Core)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	res, content, err := a.Pipe.DryRun(req.Core, input)
	if err != nil {
		fail(w, 422, err.Error())
		return
	}
	if res == nil {
		res = &core.ApplyResult{Core: req.Core}
	}
	writeJSON(w, 200, map[string]any{"result": res, "content": content})
}

func (a *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Core string `json:"core"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, err.Error())
		return
	}
	bak, err := a.Pipe.Rollback(req.Core)
	if err != nil {
		a.auditFail(r, AuditRollback, req.Core, "回滚失败："+err.Error())
		fail(w, 422, err.Error())
		return
	}
	a.changed(r, AuditRollback, req.Core, "回滚到备份 "+bak)
	writeJSON(w, 200, map[string]string{"restored_from": bak})
}

func (a *Server) listRevisions(w http.ResponseWriter, r *http.Request) {
	coreName := r.URL.Query().Get("core")
	limit := atoiDefault(r.URL.Query().Get("limit"), 30)
	revs, err := a.Store.ListRevisions(coreName, limit)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if revs == nil {
		revs = []*model.ConfigRevision{}
	}
	writeJSON(w, 200, revs)
}

func (a *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	coreName := r.URL.Query().Get("core")
	lines := atoiDefault(r.URL.Query().Get("lines"), 100)
	dir := filepath.Join(a.DataDir, "cores", coreName)
	writeJSON(w, 200, map[string]any{"logs": a.ProcMgr.Logs(coreName, dir, lines)})
}
