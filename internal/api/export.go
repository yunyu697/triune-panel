package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"triune/internal/link"

	"gopkg.in/yaml.v3"
)

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
		for _, n := range nodes {
			if l := link.GenLink(n); l != "" {
				lines = append(lines, l)
			}
		}
		if len(lines) == 0 {
			lines = []string{"# 该入站暂无有效关联用户：请先在「用户」中创建用户并勾选本入站"}
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(strings.Join(lines, "\n")))
	}
}
