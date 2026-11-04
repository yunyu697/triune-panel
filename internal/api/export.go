package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"triune/internal/link"
	"triune/internal/model"

	"gopkg.in/yaml.v3"
)

// exportNotice 返回「这份导出连不上」的原因；入站正常时返回空串。
//
// 导出的是「客户端该怎么连」，不是「服务端在监听」。入站停用 / 草稿时端口
// 根本没有监听，可链接和 Clash 配置看起来完全正常——于是排查会一路跑到客户端、
// 防火墙、REALITY 参数上去（真实案例：3x-ui JSON 导入的入站忘了打开启用开关，
// 导出的链接在 Clash Verge 里怎么都连不上，而同一份参数手工核对全对）。
//
// 所以导出产物必须自带这句话：订阅端点（/sub/<token>）早就跳过停用入站了，
// 这条手工导出路径以前什么都不说。
func exportNotice(in *model.Inbound) string {
	switch {
	case in.Draft:
		return fmt.Sprintf("该入站仍是草稿，未参与配置渲染；端口 %d 没有监听，这份配置连不上。"+
			"先取消草稿并启用，再「应用配置」。", in.Port)
	case !in.Enabled:
		return fmt.Sprintf("该入站当前已停用；端口 %d 没有监听，这份配置连不上。"+
			"先打开启用开关，再「应用配置」。", in.Port)
	}
	return ""
}

// handleInboundExport 入站导出：GET /api/inbounds/{id}/export?format=links|clash|singbox|meta
// 为该入站关联的所有有效用户生成可分享的节点配置；草稿/停用用户跳过。
func (a *Server) handleInboundExport(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 400, "bad id")
		return
	}
	in, err := a.Store.GetInbound(id)
	if err != nil {
		fail(w, 404, "入站不存在")
		return
	}

	host := a.publicHost(r)
	notice := exportNotice(in)
	var nodes []*link.Node
	clients, _ := a.Store.ListClients()
	for _, c := range clients {
		if !c.Enabled || c.ExpiresAt != nil && c.ExpiresAt.Before(timeNow()) {
			continue
		}
		ids, _ := a.Store.ClientInboundIDs(c.ID)
		associated := false
		for _, cid := range ids {
			if cid == id {
				associated = true
				break
			}
		}
		if !associated {
			continue
		}
		n := a.inboundToNode(in, c, host)
		if c.Name != "" {
			n.Name = in.Name + "-" + c.Name // 同一入站多用户时用名称区分
		}
		nodes = append(nodes, n)
	}

	name := in.Name
	if name == "" {
		name = fmt.Sprintf("inbound-%d", in.ID)
	}
	safeName := strings.ReplaceAll(strings.ReplaceAll(name, "/", "_"), " ", "_")

	switch r.URL.Query().Get("format") {
	case "clash":
		proxies := []any{}
		names := []string{}
		for _, n := range nodes {
			if p, err := link.ToClash(n); err == nil {
				proxies = append(proxies, p)
				names = append(names, p["name"].(string))
			}
		}
		doc := map[string]any{
			"port": 7890, "socks-port": 7891, "allow-lan": false,
			"mode": "rule", "log-level": "info",
			"proxy-groups": []any{map[string]any{
				"name": "PROXY", "type": "select", "proxies": names,
			}},
			"rules":   []any{"MATCH,PROXY"},
			"proxies": proxies,
		}
		out, _ := yaml.Marshal(doc)
		// YAML 支持注释：把「这条入站没在监听」写在文件头上，
		// 配置跟着文件走，比只在界面上提示更难被忽略。
		if notice != "" {
			out = append([]byte("# ⚠ "+notice+"\n"), out...)
		}
		w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s-clash.yaml", safeName))
		_, _ = w.Write(out)
	case "singbox":
		outbounds := []any{}
		for _, n := range nodes {
			if o, err := link.ToSingboxOutbound(n); err == nil {
				outbounds = append(outbounds, o)
			}
		}
		doc := map[string]any{
			"outbounds": append(outbounds, map[string]any{"type": "direct", "tag": "direct"}),
		}
		out, _ := json.MarshalIndent(doc, "", "  ")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s-singbox.json", safeName))
		_, _ = w.Write(out)
	case "meta":
		out, _ := json.MarshalIndent(in, "", "  ")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s-inbound.json", safeName))
		_, _ = w.Write(out)
	default: // links：纯文本分享链接，每行一条
		var lines []string
		// 注释行在客户端里会被跳过（现有「暂无有效用户」也是这么写的），
		// 所以既能说明问题，又不会污染导入结果。
		if notice != "" {
			lines = append(lines, "# ⚠ "+notice)
		}
		for _, n := range nodes {
			if l := link.GenLink(n); l != "" {
				lines = append(lines, l)
			}
		}
		if len(nodes) == 0 {
			lines = append(lines, "# 该入站暂无有效关联用户：请先在「用户」中创建用户并勾选本入站")
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(strings.Join(lines, "\n")))
	}
}
