package api

import (
	"encoding/json"
	"errors"
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

// exportSkip 一个没能导出的节点及原因。
//
// 以前转换失败是 `if err == nil` 直接跳过的，什么痕迹都不留。这造成过一个
// 很难查的现场：入站是 vless + xhttp（3x-ui 的常见组合），而 mihomo 的客户端
// 节点里没有 xhttp 这种传输，转换必然失败——于是导出的 YAML 里 `proxies` 是
// 空数组，`proxy-groups` 里那个组也是空的。mihomo 对空组是**硬拒绝**：
//
//	proxy group[0]: PROXY: `use` or `proxies` missing
//	configuration file ... test failed
//
// 用户拿到的是一个连加载都加载不了的文件，文件里一个字的原因都没有，界面上
// 也只说「已下载」。所以现在把失败的节点和原因都记下来，写进产物与界面。
type exportSkip struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// skipReason 把转换错误压成一句人话。
// "传输无法导出为客户端节点: mihomo 不支持 xhttp 传输的客户端节点" 里前半句
// 是哨兵值，对用户没有信息量。
func skipReason(err error) string {
	msg := err.Error()
	if errors.Is(err, link.ErrTransport) {
		if i := strings.Index(msg, ": "); i >= 0 {
			return msg[i+2:]
		}
	}
	return msg
}

// convertAll 逐个转换，失败的收集起来而不是静默跳过。
func convertAll(nodes []*link.Node, conv func(*link.Node) (map[string]any, error)) ([]map[string]any, []exportSkip) {
	items := make([]map[string]any, 0, len(nodes))
	skipped := make([]exportSkip, 0)
	for _, n := range nodes {
		item, err := conv(n)
		if err != nil {
			skipped = append(skipped, exportSkip{Name: n.Name, Reason: skipReason(err)})
			continue
		}
		items = append(items, item)
	}
	return items, skipped
}

// exportHeader 生成产物文件头的警告注释。
//
// YAML / 纯文本支持注释，客户端会跳过——既能说明问题，又不污染内容。
// 这些话说的是「为什么这份配置连不上」，不说清楚，用户只能去猜。
func exportHeader(notice string, skipped []exportSkip, empty string) string {
	var b strings.Builder
	add := func(s string) {
		if s == "" {
			return
		}
		b.WriteString("# ⚠ " + s + "\n")
	}
	add(notice)
	for _, s := range skipped {
		add(fmt.Sprintf("节点「%s」没能导出：%s", s.Name, s.Reason))
	}
	add(empty)
	return b.String()
}

// emptyClashNote 说明「这份 Clash 配置里为什么没有节点」。
//
// 没有节点时 PROXY 组里只剩 DIRECT 占位：不给占位的话 mihomo 会拒收整份
// 配置（`use` or `proxies` missing），用户看到的是「配置文件用不了」，
// 而真正的原因是上游一个节点都没导出成功。
func emptyClashNote(nodeCount, proxyCount int, skipped []exportSkip) string {
	if proxyCount > 0 {
		return ""
	}
	switch {
	case len(skipped) > 0:
		return fmt.Sprintf("%d 个节点都没能转成 mihomo 节点（原因见上），"+
			"所以 PROXY 组里只有 DIRECT——这份配置能加载，但等于直连。"+
			"换一种传输方式，或者改用 xray 系的客户端。", len(skipped))
	case nodeCount == 0:
		return "这条入站下没有有效用户，所以一个节点都导不出来。" +
			"先到「用户」里建一个用户并勾选本入站，再导出。PROXY 组里只有 DIRECT。"
	}
	return ""
}

// handleInboundExport 入站导出：GET /api/inbounds/{id}/export?format=links|clash|singbox|meta|summary
// 为该入站关联的所有有效用户生成可分享的节点配置；草稿/停用用户跳过。
//
// summary 只回 JSON 结论（几个节点、哪些导不出来、入站在不在监听），
// 供导出弹窗在**下载之前**就把问题摊开——比事后看文件头有效得多。
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
	case "summary":
		_, clashSkip := convertAll(nodes, link.ToClash)
		_, sbSkip := convertAll(nodes, link.ToSingboxOutbound)
		writeJSON(w, 200, map[string]any{
			"nodes":   len(nodes),
			"notice":  notice,
			"clash":   map[string]any{"skipped": clashSkip},
			"singbox": map[string]any{"skipped": sbSkip},
		})
	case "clash":
		items, skipped := convertAll(nodes, link.ToClash)
		proxies := make([]any, 0, len(items))
		names := make([]string, 0, len(items))
		for _, p := range items {
			proxies = append(proxies, p)
			names = append(names, p["name"].(string))
		}
		if len(names) == 0 {
			// 空组会被 mihomo 整份拒收；给个占位让它至少能加载，真因写在文件头
			names = []string{"DIRECT"}
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
		// YAML 支持注释：把「为什么连不上」写在文件头上，配置跟着文件走，
		// 比只在界面上提示更难被忽略。
		head := exportHeader(notice, skipped, emptyClashNote(len(nodes), len(proxies), skipped))
		w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s-clash.yaml", safeName))
		_, _ = w.Write(append([]byte(head), out...))
	case "singbox":
		items, _ := convertAll(nodes, link.ToSingboxOutbound)
		outbounds := make([]any, 0, len(items)+1)
		for _, o := range items {
			outbounds = append(outbounds, o)
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
