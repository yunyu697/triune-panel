package api

// 出站 / 路由 / DNS 的只读视图。
//
// 面板是**服务端**面板：入站是给客户端连的，进来的流量只能从本机网卡出去，
// 所以出站永远是 direct（xray 的 freedom / sing-box 的 direct / mihomo 不写
// proxies 时的隐式 DIRECT），没有"选出口"这个语义。本页不提供配置能力，
// 只回答两个在排障时真正会问的问题：
//
//  1. 现在真正生效的出站有哪些？通常是 1 个；只有当导入的路由里出现
//     block 规则时才会多一个 blackhole，否则那条规则会让整份配置校验失败。
//  2. 路由规则是谁写的、按什么顺序生效？面板的统计 API 规则必须最前
//     （否则读不到流量计数），导入的规则一律追加在后面。
//
// 渲染用的是与 apply 完全相同的一路径（Core.Render + RenderOptsFor），
// 但只渲染不落盘、不校验、不重启——看页面不该有任何副作用。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"gopkg.in/yaml.v3"

	"triune/internal/core"
	"triune/internal/model"
)

type outboundItem struct {
	Tag    string `json:"tag"`
	Type   string `json:"type"`
	Source string `json:"source"` // builtin | imported
	Note   string `json:"note,omitempty"`
}

type routeRuleItem struct {
	Index    int    `json:"index"`
	Source   string `json:"source"` // panel | imported
	Outbound string `json:"outbound"`
	Summary  string `json:"summary"`
	Raw      any    `json:"raw"`
}

type outboundView struct {
	Core          string          `json:"core"`
	InboundCount  int             `json:"inbound_count"`
	Outbounds     []outboundItem  `json:"outbounds"`
	RouteRules    []routeRuleItem `json:"route_rules"`
	DNS           any             `json:"dns"`
	DNSImported   bool            `json:"dns_imported"`
	RouteImported bool            `json:"route_imported"`
	Notes         []string        `json:"notes"`
	// 渲染失败时不为空：例如某条入站用了目标内核不支持的传输。
	// 这时候前端展示原因，比返回一个 500 更有用 —— 这本来就是用来查问题的页面。
	RenderError string `json:"render_error,omitempty"`
}

// GET /api/outbounds?core=xray|singbox|mihomo
func (a *Server) handleOutbounds(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("core"))
	if name == "" {
		name = a.runningCoreName()
	}
	c, err := core.Get(name)
	if err != nil {
		fail(w, 400, "内核无效："+err.Error())
		return
	}
	ins, err := a.buildRenderInput(name)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	// Pipe 在生产环境一定存在；测试或极早期启动时可能为 nil，
	// 这时候退化成「没有全局段」的渲染结果，不该 panic。
	opts := core.RenderOpts{}
	if a.Pipe != nil {
		opts = a.Pipe.RenderOptsFor(name)
	}
	view := outboundView{
		Core:          name,
		InboundCount:  len(ins),
		Outbounds:     []outboundItem{},
		RouteRules:    []routeRuleItem{},
		DNSImported:   opts.DNSJSON != "",
		RouteImported: opts.RouteJSON != "",
	}
	content, rerr := c.Render(ins, opts)
	if rerr != nil {
		view.RenderError = rerr.Error()
		view.Notes = outboundNotes(name, view)
		writeJSON(w, 200, view)
		return
	}
	doc, derr := decodeConfigDoc(name, content)
	if derr != nil {
		view.RenderError = "解析渲染结果失败：" + derr.Error()
		view.Notes = outboundNotes(name, view)
		writeJSON(w, 200, view)
		return
	}
	view.Outbounds = outboundsOf(name, doc)
	view.RouteRules = routeRulesOf(name, doc)
	view.DNS = doc["dns"]
	view.Notes = outboundNotes(name, view)
	writeJSON(w, 200, view)
}

// runningCoreName 优先取正在跑的内核；一个都没跑时退回 xray
// （它是 3x-ui 迁移过来的用户最常见的那个）。
func (a *Server) runningCoreName() string {
	if a.ProcMgr != nil {
		for _, c := range core.All() {
			if ok, _ := a.ProcMgr.Status(c.Name()); ok {
				return c.Name()
			}
		}
	}
	return model.CoreXray
}

// decodeConfigDoc 把渲染结果解成通用文档：xray / sing-box 是 JSON，
// mihomo 是 YAML。
func decodeConfigDoc(name string, content []byte) (map[string]any, error) {
	var doc map[string]any
	if name == model.CoreMihomo {
		if err := yaml.Unmarshal(content, &doc); err != nil {
			return nil, err
		}
		return doc, nil
	}
	if err := json.Unmarshal(content, &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// outboundsOf 抽出出站列表。
//
// mihomo 的服务端渲染只写 listeners、不写 proxies —— 内核在没有 proxies 时
// 默认 DIRECT，所以这里补一条"隐式"记录，免得页面上显示「出站 0 条」让人
// 以为配置坏了。
func outboundsOf(name string, doc map[string]any) []outboundItem {
	out := []outboundItem{}
	switch name {
	case model.CoreMihomo:
		for _, it := range asAnyList(doc["proxies"]) {
			m := asAnyMap(it)
			tag, _ := m["name"].(string)
			typ, _ := m["type"].(string)
			out = append(out, outboundItem{Tag: tag, Type: typ, Source: "imported"})
		}
		if len(out) == 0 {
			out = append(out, outboundItem{
				Tag: "DIRECT", Type: "direct（隐式）", Source: "builtin",
				Note: "服务端模式不写 proxies，未匹配规则的流量直接走本机网络",
			})
		}
		return out
	case model.CoreSingBox:
		for _, it := range asAnyList(doc["outbounds"]) {
			m := asAnyMap(it)
			tag, _ := m["tag"].(string)
			typ, _ := m["type"].(string)
			out = append(out, outboundItem{Tag: tag, Type: typ, Source: outboundSource(tag)})
		}
	default: // xray
		for _, it := range asAnyList(doc["outbounds"]) {
			m := asAnyMap(it)
			tag, _ := m["tag"].(string)
			proto, _ := m["protocol"].(string)
			out = append(out, outboundItem{Tag: tag, Type: proto, Source: outboundSource(tag)})
		}
	}
	return out
}

// outboundSource 判断出站是面板内置的还是导入配置带来的。
// 面板只会自己生成这两个 tag，其余必然是导入的。
func outboundSource(tag string) string {
	switch strings.ToUpper(tag) {
	case "DIRECT", "BLOCK", "REJECT":
		return "builtin"
	}
	return "imported"
}

// routeRulesOf 抽出路由规则并标注来源。
func routeRulesOf(name string, doc map[string]any) []routeRuleItem {
	var list []any
	switch name {
	case model.CoreSingBox:
		list = asAnyList(asAnyMap(doc["route"])["rules"])
	case model.CoreMihomo:
		list = asAnyList(doc["rules"])
	default: // xray
		list = asAnyList(asAnyMap(doc["routing"])["rules"])
	}
	out := []routeRuleItem{}
	for i, it := range list {
		item := routeRuleItem{Index: i + 1, Raw: it}
		// mihomo 的 rules 是 "DOMAIN-SUFFIX,x.com,DIRECT" 这样的字符串
		if s, ok := it.(string); ok {
			item.Summary = s
			parts := strings.Split(s, ",")
			if len(parts) > 1 {
				item.Outbound = parts[len(parts)-1]
			}
			item.Source = "imported"
			out = append(out, item)
			continue
		}
		m := asAnyMap(it)
		if name == model.CoreXray {
			item.Outbound, _ = m["outboundTag"].(string)
			// 统计 API 的分流：inboundTag 含 "api"，出站 tag 也是 "api"
			if containsAny(asAnyList(m["inboundTag"]), "api") {
				item.Source = "panel"
				item.Summary = "inboundTag=api → " + item.Outbound
				out = append(out, item)
				continue
			}
		} else {
			if act, _ := m["action"].(string); act != "" {
				item.Outbound = "action=" + act
			} else {
				item.Outbound, _ = m["outbound"].(string)
			}
		}
		item.Source = "imported"
		item.Summary = summarizeRule(m)
		if item.Outbound != "" {
			item.Summary += " → " + item.Outbound
		}
		out = append(out, item)
	}
	return out
}

// ruleFieldOrder 决定摘要里字段的显示顺序：匹配条件在前，动作在后。
var ruleFieldOrder = []string{
	"domain", "domain_suffix", "domain_keyword", "domain_regex",
	"ip", "ip_cidr", "rule_set", "geosite", "geoip",
	"inbound", "inboundTag", "protocol", "network", "port", "source",
}

func summarizeRule(m map[string]any) string {
	var parts []string
	for _, k := range ruleFieldOrder {
		v, ok := m[k]
		if !ok || v == nil {
			continue
		}
		parts = append(parts, k+"="+shortValue(v))
	}
	if len(parts) == 0 {
		return "（无条件匹配）"
	}
	return strings.Join(parts, " · ")
}

// shortValue 把字段值压成一行：数组最多列 3 个，多的用省略号。
func shortValue(v any) string {
	if list, ok := v.([]any); ok {
		var ss []string
		for i, x := range list {
			if i >= 3 {
				ss = append(ss, "…")
				break
			}
			ss = append(ss, fmt.Sprint(x))
		}
		return strings.Join(ss, ",")
	}
	return fmt.Sprint(v)
}

func outboundNotes(name string, v outboundView) []string {
	notes := []string{}
	if name == model.CoreMihomo {
		notes = append(notes, "mihomo 用 listeners 做服务端，不写 proxies：所有入站流量直接走本机网络出口。")
	} else {
		notes = append(notes, "服务端出站固定为 direct（freedom）：入站流量只能从本机出去，面板不提供上游代理 / 链式代理。")
	}
	switch {
	case v.RouteImported:
		notes = append(notes, "路由规则来自导入的 3x-ui / xray 配置，追加在面板统计规则之后——统计规则必须最前，否则读不到流量计数。")
	case len(v.RouteRules) > 0:
		notes = append(notes, "路由规则只有面板自己写的统计 API 分流，没有额外的导入规则。")
	default:
		notes = append(notes, "没有任何路由规则：全部流量直接出网。")
	}
	if !v.DNSImported {
		notes = append(notes, "没有导入 DNS 段，域名解析走内核默认（系统 DNS）。")
	}
	if len(v.Outbounds) > 1 {
		notes = append(notes, "出站多于一条，说明导入的路由里有 block 规则，面板为此补了 blackhole 出站。")
	}
	return notes
}

func asAnyMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func asAnyList(v any) []any {
	if l, ok := v.([]any); ok {
		return l
	}
	return nil
}

func containsAny(list []any, want string) bool {
	for _, it := range list {
		if s, _ := it.(string); s == want {
			return true
		}
	}
	return false
}
