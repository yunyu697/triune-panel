package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"triune/internal/link"
	"triune/internal/model"

	"gopkg.in/yaml.v3"
)

// ==================== 规则集目录与场景预设 ====================

type ruleSetDef struct {
	ID       string `json:"id"`       // geosite-cn / geoip-cn / geosite-netflix ...
	Name     string `json:"name"`     // 显示名
	Dir      string `json:"-"`        // geosite | geoip（决定 URL 模板）
	Behavior string `json:"behavior"` // domain | ipcidr
	Group    string `json:"group"`    // UI 分组：基础 / 流媒体 / AI / 广告 / 平台
}

type setPolicy struct {
	ID     string `json:"id"`
	Policy string `json:"policy"` // DIRECT | PROXY | REJECT
}

type scenarioDef struct {
	ID    string      `json:"id"`
	Name  string      `json:"name"`
	Desc  string      `json:"desc"`
	Sets  []setPolicy `json:"sets"`
	Rules []string    `json:"rules"` // 场景内置的自定义规则（clash 语法）
}

// ruleCatalog 规则集目录（geosite 类目名与 v2fly domain-list-community / MetaCubeX / SagerNet 对齐）
var ruleCatalog = []ruleSetDef{
	{ID: "geosite-cn", Name: "中国大陆域名", Dir: "geosite", Behavior: "domain", Group: "基础分流"},
	{ID: "geoip-cn", Name: "中国大陆 IP", Dir: "geoip", Behavior: "ipcidr", Group: "基础分流"},
	{ID: "geosite-private", Name: "局域网/私有域名", Dir: "geosite", Behavior: "domain", Group: "基础分流"},
	{ID: "geosite-category-ads-all", Name: "广告域名（通用）", Dir: "geosite", Behavior: "domain", Group: "广告拦截"},
	{ID: "geosite-netflix", Name: "Netflix", Dir: "geosite", Behavior: "domain", Group: "流媒体"},
	{ID: "geosite-disney", Name: "Disney+", Dir: "geosite", Behavior: "domain", Group: "流媒体"},
	{ID: "geosite-youtube", Name: "YouTube", Dir: "geosite", Behavior: "domain", Group: "流媒体"},
	{ID: "geosite-tiktok", Name: "TikTok", Dir: "geosite", Behavior: "domain", Group: "流媒体"},
	{ID: "geosite-spotify", Name: "Spotify", Dir: "geosite", Behavior: "domain", Group: "流媒体"},
	{ID: "geosite-openai", Name: "OpenAI / ChatGPT", Dir: "geosite", Behavior: "domain", Group: "AI 服务"},
	{ID: "geosite-bing", Name: "Bing / Copilot", Dir: "geosite", Behavior: "domain", Group: "AI 服务"},
	{ID: "geosite-category-games", Name: "游戏平台（Steam/PSN 等）", Dir: "geosite", Behavior: "domain", Group: "游戏"},
	{ID: "geosite-steam", Name: "Steam", Dir: "geosite", Behavior: "domain", Group: "游戏"},
	{ID: "geosite-telegram", Name: "Telegram", Dir: "geosite", Behavior: "domain", Group: "平台直连/代理"},
	{ID: "geosite-google", Name: "Google", Dir: "geosite", Behavior: "domain", Group: "平台直连/代理"},
	{ID: "geosite-apple", Name: "Apple", Dir: "geosite", Behavior: "domain", Group: "平台直连/代理"},
	{ID: "geosite-microsoft", Name: "Microsoft", Dir: "geosite", Behavior: "domain", Group: "平台直连/代理"},
	{ID: "geosite-gfw", Name: "GFW 列表", Dir: "geosite", Behavior: "domain", Group: "平台直连/代理"},
}

var ruleSetIndex = func() map[string]ruleSetDef {
	m := map[string]ruleSetDef{}
	for _, d := range ruleCatalog {
		m[d.ID] = d
	}
	return m
}()

// scenarioPresets 场景预设（最常用的几种）
var scenarioPresets = []scenarioDef{
	{
		ID: "balanced", Name: "均衡日常", Desc: "国内直连、广告拦截、常规外网走代理，适合日常使用",
		Sets: []setPolicy{
			{ID: "geosite-category-ads-all", Policy: "REJECT"},
			{ID: "geosite-cn", Policy: "DIRECT"},
			{ID: "geoip-cn", Policy: "DIRECT"},
			{ID: "geosite-private", Policy: "DIRECT"},
			{ID: "geosite-apple", Policy: "DIRECT"},
			{ID: "geosite-microsoft", Policy: "DIRECT"},
		},
	},
	{
		ID: "streaming", Name: "流媒体优先", Desc: "在均衡基础上，Netflix/Disney+/YouTube/TikTok 等强制走代理",
		Sets: []setPolicy{
			{ID: "geosite-category-ads-all", Policy: "REJECT"},
			{ID: "geosite-netflix", Policy: "PROXY"},
			{ID: "geosite-disney", Policy: "PROXY"},
			{ID: "geosite-youtube", Policy: "PROXY"},
			{ID: "geosite-tiktok", Policy: "PROXY"},
			{ID: "geosite-spotify", Policy: "PROXY"},
			{ID: "geosite-cn", Policy: "DIRECT"},
			{ID: "geoip-cn", Policy: "DIRECT"},
			{ID: "geosite-private", Policy: "DIRECT"},
			{ID: "geosite-apple", Policy: "DIRECT"},
			{ID: "geosite-microsoft", Policy: "DIRECT"},
		},
	},
	{
		ID: "gaming", Name: "游戏加速", Desc: "Steam/游戏平台走代理降低延迟，其余国内直连",
		Sets: []setPolicy{
			{ID: "geosite-category-ads-all", Policy: "REJECT"},
			{ID: "geosite-category-games", Policy: "PROXY"},
			{ID: "geosite-steam", Policy: "PROXY"},
			{ID: "geosite-cn", Policy: "DIRECT"},
			{ID: "geoip-cn", Policy: "DIRECT"},
			{ID: "geosite-private", Policy: "DIRECT"},
		},
	},
	{
		ID: "ai", Name: "AI 服务", Desc: "OpenAI / Bing Copilot 等走代理（保证区域可用性），其余国内直连",
		Sets: []setPolicy{
			{ID: "geosite-category-ads-all", Policy: "REJECT"},
			{ID: "geosite-openai", Policy: "PROXY"},
			{ID: "geosite-bing", Policy: "PROXY"},
			{ID: "geosite-google", Policy: "PROXY"},
			{ID: "geosite-cn", Policy: "DIRECT"},
			{ID: "geoip-cn", Policy: "DIRECT"},
			{ID: "geosite-private", Policy: "DIRECT"},
			{ID: "geosite-apple", Policy: "DIRECT"},
			{ID: "geosite-microsoft", Policy: "DIRECT"},
		},
	},
	{
		ID: "privacy", Name: "隐私优先", Desc: "广告/跟踪全拦截，除国内必需外全部走代理",
		Sets: []setPolicy{
			{ID: "geosite-category-ads-all", Policy: "REJECT"},
			{ID: "geosite-cn", Policy: "DIRECT"},
			{ID: "geoip-cn", Policy: "DIRECT"},
			{ID: "geosite-private", Policy: "DIRECT"},
		},
		Rules: []string{"DOMAIN-KEYWORD,analytics,REJECT", "DOMAIN-KEYWORD,tracker,REJECT"},
	},
	{
		ID: "cn-max", Name: "国内直连优先", Desc: "仅 GFW 列表走代理，其余全部直连（可用性最高）",
		Sets: []setPolicy{
			{ID: "geosite-gfw", Policy: "PROXY"},
			{ID: "geosite-cn", Policy: "DIRECT"},
			{ID: "geoip-cn", Policy: "DIRECT"},
			{ID: "geosite-private", Policy: "DIRECT"},
		},
	},
}

// ==================== 生成请求/响应 ====================

type customSetReq struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	Behavior string `json:"behavior"` // domain | ipcidr | classical
	Policy   string `json:"policy"`
	Interval int    `json:"interval"` // 秒，默认 86400
}

type genReq struct {
	Core        string         `json:"core"` // mihomo | singbox
	Target      string         `json:"target"`
	InboundIDs  []int64        `json:"inbound_ids"`
	Scenario    string         `json:"scenario"`
	Sets        []setPolicy    `json:"sets"`
	CustomRules []string       `json:"custom_rules"`
	CustomSets  []customSetReq `json:"custom_sets"`
}

// handleExportCatalog GET /api/export/catalog：规则集目录 + 场景预设
func (a *Server) handleExportCatalog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"sets":      ruleCatalog,
		"scenarios": scenarioPresets,
	})
}

// handleExportConfig POST /api/export/config：生成完整客户端配置
// 返回 {filename, content, warnings}
func (a *Server) handleExportConfig(w http.ResponseWriter, r *http.Request) {
	var q genReq
	if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
		fail(w, 400, "bad request")
		return
	}
	if q.Core != "mihomo" && q.Core != "singbox" {
		fail(w, 400, "core 必须是 mihomo 或 singbox")
		return
	}
	if q.Target != "client" && q.Target != "main-router" && q.Target != "side-router" {
		fail(w, 400, "target 必须是 client / main-router / side-router")
		return
	}
	if len(q.InboundIDs) == 0 {
		fail(w, 400, "请至少选择一个入站")
		return
	}

	// 收集节点（入站 × 有效用户）
	host := a.publicHost(r)
	ins, _ := a.Store.ListInbounds("")
	byID := map[int64]*model.Inbound{}
	for _, in := range ins {
		byID[in.ID] = in
	}
	clients, _ := a.Store.ListClients()
	type pair struct {
		in *model.Inbound
		c  *model.Client
	}
	var pairs []pair
	for _, id := range q.InboundIDs {
		in, ok := byID[id]
		if !ok {
			continue
		}
		for _, c := range clients {
			if !c.Enabled || c.ExpiresAt != nil && c.ExpiresAt.Before(timeNow()) {
				continue
			}
			ids, _ := a.Store.ClientInboundIDs(c.ID)
			for _, cid := range ids {
				if cid == id {
					pairs = append(pairs, pair{in, c})
					break
				}
			}
		}
	}
	if len(pairs) == 0 {
		fail(w, 422, "所选入站下没有有效用户（用户需已创建、启用且勾选该入站）")
		return
	}
	var nodes []*link.Node
	for _, p := range pairs {
		n := a.inboundToNode(p.in, p.c, host)
		n.Name = p.in.Name + "-" + p.c.Name
		nodes = append(nodes, n)
	}

	// 解析规则集选择 + 自定义规则集
	sets, warns := normalizeSets(q)
	var g *generator
	if q.Core == "mihomo" {
		g = genMihomo(q.Target, nodes, sets, q.CustomRules, &warns)
	} else {
		g = genSingbox(q.Target, nodes, sets, q.CustomRules, &warns)
	}

	var content string
	ct := "application/json"
	if q.Core == "mihomo" {
		body, _ := yaml.Marshal(g.doc)
		content = g.header + string(body)
		ct = "text/yaml"
	} else {
		out, _ := json.MarshalIndent(g.doc, "", "  ")
		content = string(out)
	}
	safeName := "config-" + q.Target + "-" + q.Scenario
	if safeName == "config--" {
		safeName = "config"
	}
	writeJSON(w, 200, map[string]any{
		"filename":     fmt.Sprintf("%s-%s.%s", safeName, q.Core, map[string]string{"mihomo": "yaml", "singbox": "json"}[q.Core]),
		"content":      content,
		"content_type": ct,
		"warnings":     warns,
	})
}

// ==================== 规则集归一化 ====================

type normSet struct {
	Tag      string // provider/rule_set 标识
	URL      string
	Behavior string // domain | ipcidr | classical
	Policy   string // DIRECT | PROXY | REJECT
	Format   string // mrs | srs | yaml ...（singbox 用 binary/source）
	Interval int    // 更新间隔（秒）
}

func mihomoSetURL(dir, name string) string {
	return fmt.Sprintf("https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/meta/geo/%s/%s-%s.mrs", dir, dir, name)
}
func singboxSetURL(dir, name string) string {
	if dir == "geoip" {
		return "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/" + name + ".srs"
	}
	return "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/" + name + ".srs"
}

func normalizeSets(q genReq) ([]normSet, []string) {
	var warns []string
	seen := map[string]bool{}
	var out []normSet
	add := func(s normSet) {
		if seen[s.Tag] {
			return
		}
		seen[s.Tag] = true
		out = append(out, s)
	}
	policy := func(p string) string {
		switch p {
		case "DIRECT", "PROXY", "REJECT":
			return p
		}
		return "PROXY"
	}
	for _, s := range q.Sets {
		def, ok := ruleSetIndex[s.ID]
		if !ok {
			warns = append(warns, "未知规则集已跳过: "+s.ID)
			continue
		}
		if q.Core == "mihomo" {
			add(normSet{Tag: def.ID, URL: mihomoSetURL(def.Dir, strings.TrimPrefix(strings.TrimPrefix(def.ID, "geosite-"), "geoip-")),
				Behavior: def.Behavior, Policy: policy(s.Policy), Format: "mrs", Interval: 86400})
		} else {
			add(normSet{Tag: def.ID, URL: singboxSetURL(def.Dir, strings.TrimPrefix(strings.TrimPrefix(def.ID, "geosite-"), "geoip-")),
				Behavior: def.Behavior, Policy: policy(s.Policy), Format: "binary", Interval: 86400})
		}
	}
	for _, cs := range q.CustomSets {
		if cs.URL == "" || cs.Name == "" {
			warns = append(warns, "自定义规则集缺少名称或 URL，已跳过: "+cs.Name)
			continue
		}
		beh := cs.Behavior
		if beh != "domain" && beh != "ipcidr" && beh != "classical" {
			beh = "domain"
		}
		interval := cs.Interval
		if interval <= 0 {
			interval = 86400
		}
		tag := "custom-" + strings.ReplaceAll(strings.ReplaceAll(cs.Name, "/", "_"), " ", "_")
		lu := strings.ToLower(cs.URL)
		fmt := "yaml"
		if q.Core == "singbox" {
			if strings.HasSuffix(lu, ".srs") {
				fmt = "binary"
			}
		} else {
			switch {
			case strings.HasSuffix(lu, ".mrs"):
				fmt = "mrs"
			case strings.HasSuffix(lu, ".txt"), strings.HasSuffix(lu, ".list"):
				fmt = "text"
			}
		}
		if q.Core == "singbox" && !strings.HasSuffix(strings.ToLower(cs.URL), ".srs") {
			warns = append(warns, "sing-box 自定义规则集建议使用 .srs 格式: "+cs.Name)
		}
		add(normSet{Tag: tag, URL: cs.URL, Behavior: beh, Policy: policy(cs.Policy), Format: fmt, Interval: interval})
	}
	// 排序保证稳定输出：REJECT → PROXY → DIRECT
	rank := map[string]int{"REJECT": 0, "PROXY": 1, "DIRECT": 2}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Policy] < rank[out[j].Policy] })
	return out, warns
}

// ==================== 生成器公共结构 ====================

type generator struct {
	doc    map[string]any
	header string
}

// parseCustomRule 解析 clash 风格自定义规则：TYPE,VALUE,POLICY[,no-resolve]
// 支持 DOMAIN / DOMAIN-SUFFIX / DOMAIN-KEYWORD / IP-CIDR / IP-CIDR6 / GEOSITE / GEOIP
func parseCustomRule(line string) (typ, val, pol string, ok bool) {
	parts := strings.Split(strings.TrimSpace(line), ",")
	if len(parts) < 3 {
		return "", "", "", false
	}
	typ = strings.ToUpper(strings.TrimSpace(parts[0]))
	pol = strings.ToUpper(strings.TrimSpace(parts[len(parts)-1]))
	switch pol {
	case "DIRECT", "PROXY", "REJECT":
	default:
		return "", "", "", false
	}
	body := parts[1 : len(parts)-1]
	val = strings.TrimSpace(strings.Join(body, ","))
	switch typ {
	case "DOMAIN", "DOMAIN-SUFFIX", "DOMAIN-KEYWORD", "IP-CIDR", "IP-CIDR6", "GEOSITE", "GEOIP":
		if val == "" {
			return "", "", "", false
		}
		return typ, val, pol, true
	}
	return "", "", "", false
}

// geoProviderFor GEOSITE/GEOIP 自定义规则需要的动态 provider
func geoProviderFor(core, typ, val, pol string, sets *[]normSet) {
	tag := strings.ToLower(typ) + "-" + val
	for _, s := range *sets {
		if s.Tag == tag {
			return
		}
	}
	dir := "geosite"
	if typ == "GEOIP" {
		dir = "geoip"
	}
	var s normSet
	beh := map[string]string{"geosite": "domain", "geoip": "ipcidr"}[dir]
	if core == "mihomo" {
		s = normSet{Tag: tag, URL: mihomoSetURL(dir, val), Behavior: beh, Policy: pol, Format: "mrs", Interval: 86400}
	} else {
		s = normSet{Tag: tag, URL: singboxSetURL(dir, val), Behavior: beh, Policy: pol, Format: "binary", Interval: 86400}
	}
	*sets = append(*sets, s)
}

var privateCIDRs = []string{
	"192.168.0.0/16", "10.0.0.0/8", "172.16.0.0/12",
	"127.0.0.0/8", "169.254.0.0/16", "::1/128", "fc00::/7", "fe80::/10",
}

var fakeIPFilter = []string{
	"*.lan", "*.local", "+.msftconnecttest.com", "+.msftncsi.com",
	"+.stun.*", "+.srv.nintendo.net", "+.stun.playstation.net",
	"xbox.*.microsoft.com", "+.xboxlive.com", "time.*.com", "ntp.*.com",
}

// targetNotes 各目标的使用说明（YAML 头注释 / UI 提示）
func targetNote(target string) string {
	switch target {
	case "main-router":
		return "主路由模式：TUN 接管全机流量，DNS 劫持 :53，局域网设备无需任何配置。\n重启路由前建议先在单设备上验证配置。"
	case "side-router":
		return "旁路由模式：将本机 IP 设为局域网设备的网关与 DNS 即可接管；\n主路由无需改动（关闭主路由 DHCP 冲突即可并存）。"
	default:
		return "客户端模式：默认使用混合端口 7890（HTTP/SOCKS），配合系统代理或浏览器插件使用。"
	}
}

// ==================== mihomo 生成器 ====================

func genMihomo(target string, nodes []*link.Node, sets []normSet, customRules []string, warns *[]string) *generator {
	router := target != "client"

	proxies := []any{}
	names := []string{}
	for _, n := range nodes {
		if p, err := link.ToClash(n); err == nil {
			proxies = append(proxies, p)
			names = append(names, p["name"].(string))
		}
	}

	// 规则：自定义 → 规则集 → 私网 → MATCH（normalizeSets 已按 REJECT>PROXY>DIRECT 排序）
	rules := []any{}
	for _, line := range customRules {
		typ, val, pol, ok := parseCustomRule(line)
		if !ok {
			*warns = append(*warns, "无法解析的自定义规则已跳过: "+line)
			continue
		}
		if typ == "GEOSITE" || typ == "GEOIP" {
			geoProviderFor("mihomo", typ, val, pol, &sets)
			rules = append(rules, fmt.Sprintf("RULE-SET,geosite-%s,%s", val, pol))
			if typ == "GEOIP" {
				rules[len(rules)-1] = fmt.Sprintf("RULE-SET,geoip-%s,%s", val, pol)
			}
			continue
		}
		rules = append(rules, fmt.Sprintf("%s,%s,%s", typ, val, pol))
	}
	for _, s := range sets {
		rules = append(rules, fmt.Sprintf("RULE-SET,%s,%s", s.Tag, s.Policy))
	}
	for _, c := range privateCIDRs {
		rules = append(rules, fmt.Sprintf("IP-CIDR,%s,DIRECT,no-resolve", c))
	}
	rules = append(rules, "MATCH,PROXY")

	providers := map[string]any{}
	for _, s := range sets {
		providers[s.Tag] = map[string]any{
			"type":     "http",
			"behavior": s.Behavior,
			"format":   s.Format,
			"url":      s.URL,
			"path":     "./rules/" + s.Tag + ".mrs",
			"interval": s.Interval,
		}
	}

	groups := []any{
		map[string]any{"name": "PROXY", "type": "select",
			"proxies":     append([]any{"Auto"}, toAnySlice(names)...),
			"disable-udp": false},
		map[string]any{"name": "Auto", "type": "url-test",
			"proxies": toAnySlice(names), "url": "http://www.gstatic.com/generate_204",
			"interval": 300, "tolerance": 50},
	}

	tun := map[string]any{"enable": false}
	if router {
		tun = map[string]any{
			"enable": true, "stack": "mixed",
			"auto-route": true, "auto-redirect": true,
			"auto-detect-interface": true,
			"dns-hijack":            []any{"any:53"},
		}
	}

	dns := map[string]any{
		"enable":             true,
		"ipv6":               false,
		"enhanced-mode":      map[bool]string{true: "fake-ip", false: "redir-host"}[router],
		"fake-ip-range":      "198.18.0.1/16",
		"fake-ip-filter":     toAnySlice(fakeIPFilter),
		"nameserver":         toAnySlice([]string{"https://223.5.5.5/dns-query", "https://doh.pub/dns-query"}),
		"default-nameserver": toAnySlice([]string{"223.5.5.5", "119.29.29.29"}),
	}
	if router {
		dns["listen"] = "0.0.0.0:53"
	}

	doc := map[string]any{
		"mode":                "rule",
		"log-level":           map[bool]string{true: "info", false: "warning"}[router],
		"allow-lan":           router,
		"bind-address":        "*",
		"mixed-port":          7890,
		"external-controller": "127.0.0.1:9090",
		"unified-delay":       true,
		"tcp-concurrent":      true,
		"tun":                 tun,
		"dns":                 dns,
		"proxies":             proxies,
		"proxy-groups":        groups,
		"rule-providers":      providers,
		"rules":               rules,
	}
	if router {
		doc["sniffer"] = map[string]any{
			"enable": true, "parse-pure-ip": true, "override-destination": false,
			"sniff": map[string]any{"HTTP": map[string]any{"ports": []any{80, "8080-8880"}}, "TLS": map[string]any{"ports": []any{443, 8443}}},
		}
	}

	note := strings.ReplaceAll(targetNote(target), "\n", "\n# ")
	header := "# Triune Panel 生成 | mihomo | " + target + "\n# " + note + "\n\n"
	return &generator{doc: doc, header: header}
}

// ==================== sing-box 生成器 ====================

func genSingbox(target string, nodes []*link.Node, sets []normSet, customRules []string, warns *[]string) *generator {
	router := target != "client"

	outbounds := []any{}
	nodeTags := []string{}
	for _, n := range nodes {
		if o, err := link.ToSingboxOutbound(n); err == nil {
			outbounds = append(outbounds, o)
			nodeTags = append(nodeTags, o["tag"].(string))
		}
	}
	outbounds = append(outbounds,
		map[string]any{"type": "urltest", "tag": "Auto", "outbounds": toAnySlice(nodeTags),
			"url": "http://www.gstatic.com/generate_204", "interval": "5m"},
		map[string]any{"type": "selector", "tag": "PROXY",
			"outbounds": append([]any{"Auto"}, toAnySlice(nodeTags)...), "default": "Auto"},
		map[string]any{"type": "direct", "tag": "direct"},
	)

	// 路由规则：DNS 劫持 → 自定义 → 规则集 → 私网
	routeRules := []any{
		map[string]any{"protocol": "dns", "action": "hijack-dns"},
	}
	// 返回规则的出站字段：REJECT 用 action（block 出站已在 1.13 移除）
	ruleReject := func() map[string]any { return map[string]any{"action": "reject"} }
	_ = ruleReject
	outboundOf := func(p string) string {
		switch p {
		case "DIRECT":
			return "direct"
		case "REJECT":
			return ""
		}
		return "PROXY"
	}
	setOut := func(m map[string]any, pol string) map[string]any {
		if pol == "REJECT" {
			m["action"] = "reject"
		} else {
			m["outbound"] = outboundOf(pol)
		}
		return m
	}
	for _, line := range customRules {
		typ, val, pol, ok := parseCustomRule(line)
		if !ok {
			*warns = append(*warns, "无法解析的自定义规则已跳过: "+line)
			continue
		}
		switch typ {
		case "DOMAIN":
			routeRules = append(routeRules, setOut(map[string]any{"domain": []any{val}}, pol))
		case "DOMAIN-SUFFIX":
			routeRules = append(routeRules, setOut(map[string]any{"domain_suffix": []any{val}}, pol))
		case "DOMAIN-KEYWORD":
			routeRules = append(routeRules, setOut(map[string]any{"domain_keyword": []any{val}}, pol))
		case "IP-CIDR", "IP-CIDR6":
			if !strings.Contains(val, "/") {
				val += map[bool]string{true: "/128", false: "/32"}[strings.Contains(val, ":")]
			}
			routeRules = append(routeRules, setOut(map[string]any{"ip_cidr": []any{val}}, pol))
		case "GEOSITE", "GEOIP":
			geoProviderFor("singbox", typ, val, pol, &sets)
			routeRules = append(routeRules, setOut(map[string]any{"rule_set": []any{"geosite-" + val}}, pol))
		}
	}
	for _, s := range sets {
		routeRules = append(routeRules, setOut(map[string]any{"rule_set": []any{s.Tag}}, s.Policy))
	}
	routeRules = append(routeRules, map[string]any{"ip_cidr": toAnySlice(privateCIDRs), "outbound": "direct"})

	ruleSets := []any{}
	for _, s := range sets {
		ruleSets = append(ruleSets, map[string]any{
			"type": "remote", "tag": s.Tag, "format": s.Format,
			"url": s.URL, "download_detour": "direct", "update_interval": fmt.Sprintf("%ds", s.Interval),
		})
	}

	// DNS：国内域名走本地，其余走代理侧 DoH
	dnsRules := []any{map[string]any{"rule_set": []any{"geosite-cn"}, "server": "local"}}
	if hasSet(sets, "geosite-private") {
		dnsRules = append(dnsRules, map[string]any{"rule_set": []any{"geosite-private"}, "server": "local"})
	}
	dns := map[string]any{
		// sing-box 1.12+ 新 DNS server 格式（旧 address 格式已在 1.14 移除）
		"servers": []any{
			map[string]any{"type": "https", "tag": "remote", "server": "8.8.8.8", "detour": "PROXY"},
			map[string]any{"type": "udp", "tag": "local", "server": "223.5.5.5", "detour": "direct"},
		},
		"rules": dnsRules,
		"final": "remote",
	}

	inbounds := []any{}
	if router {
		inbounds = append(inbounds, map[string]any{
			"type": "tun", "tag": "tun-in",
			"address":      []any{"172.19.0.1/30"},
			"auto_route":   true,
			"strict_route": true,
			"stack":        "mixed",
		})
	}
	inbounds = append(inbounds, map[string]any{
		"type": "mixed", "tag": "mixed-in",
		"listen":      map[bool]string{true: "0.0.0.0", false: "127.0.0.1"}[router],
		"listen_port": 7890,
	})

	doc := map[string]any{
		"log":       map[string]any{"level": map[bool]string{true: "info", false: "warn"}[router], "timestamp": true},
		"dns":       dns,
		"inbounds":  inbounds,
		"outbounds": outbounds,
		"route": map[string]any{
			"rules":                   routeRules,
			"rule_set":                ruleSets,
			"final":                   "PROXY",
			"auto_detect_interface":   true,
			"default_domain_resolver": map[string]any{"server": "local"},
		},
	}

	// JSON 无注释；说明放 UI 提示与 README
	return &generator{doc: doc}
}

func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func hasSet(sets []normSet, tag string) bool {
	for _, s := range sets {
		if s.Tag == tag {
			return true
		}
	}
	return false
}
