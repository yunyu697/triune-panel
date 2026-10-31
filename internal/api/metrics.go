package api

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"triune/internal/model"
)

// MetricsTokenSetting 是 /metrics 的访问令牌（设置页可生成）。
//
// 为什么不复用会话 token：Prometheus 的 scrape 配置里放不了「先登录再取 token」
// 这套流程。也刻意不做「本机免认证」——面板常挂在 nginx 后面，那样等于任何能
// 碰到反代的人都能拉指标。
const MetricsTokenSetting = "metrics_token"

// metricsContentType 是 Prometheus 文本格式的标准 Content-Type（带版本号）。
const metricsContentType = "text/plain; version=0.0.4; charset=utf-8"

// handleMetrics 输出 Prometheus 文本格式指标。
//
// 默认关闭：没配 metrics_token 就返回 403，避免升级后悄悄多出一个
// 「谁知道地址谁就能看用户列表和流量」的接口。
func (a *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	want, _ := a.Store.GetSetting(MetricsTokenSetting)
	want = strings.TrimSpace(want)
	if want == "" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, "metrics 未启用。请在「设置 → 基础设置 → 指标访问令牌」生成一个令牌后重试。\n")
		return
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if got == "" {
		got = r.URL.Query().Get("token") // 兜底：有些采集器不方便加 header
	}
	// 定长比较，避免按字节逐位试探
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
		fail(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	w.Header().Set("Content-Type", metricsContentType)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(a.renderMetrics()))
}

// renderMetrics 采集并渲染全部指标。
//
// 任何一路取数失败都只影响对应指标（打 _up=0 或整段跳过），不能让整个
// 端点 500——监控最怕的就是「面板出问题时连指标都拉不到」。
func (a *Server) renderMetrics() string {
	m := &metricsWriter{}

	m.family("triune_up", "gauge", "面板进程存活（1=运行中）")
	m.sample("triune_up", 1)

	m.family("triune_build_info", "gauge", "构建信息，值恒为 1")
	m.sample("triune_build_info", 1, "version", Version, "go_version", runtime.Version())

	m.family("triune_uptime_seconds", "gauge", "进程运行时长（秒）")
	m.sample("triune_uptime_seconds", time.Since(startedAt).Seconds())

	dbUp := 0.0
	if a.Store != nil && a.Store.Ping() == nil {
		dbUp = 1
	}
	m.family("triune_db_up", "gauge", "数据库可查询（1=正常）")
	m.sample("triune_db_up", dbUp)

	m.family("triune_go_goroutines", "gauge", "当前 goroutine 数")
	m.sample("triune_go_goroutines", float64(runtime.NumGoroutine()))

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	m.family("triune_go_heap_alloc_bytes", "gauge", "堆上活跃对象占用字节数")
	m.sample("triune_go_heap_alloc_bytes", float64(ms.HeapAlloc))

	a.metricsClients(m)
	a.metricsInbounds(m)
	a.metricsCores(m)
	a.metricsCerts(m)
	a.metricsLive(m)

	return m.String()
}

func (a *Server) metricsClients(m *metricsWriter) {
	cs, err := a.Store.ListClients()
	if err != nil {
		return
	}
	traffic, _ := a.Store.GetTrafficAll()

	m.family("triune_clients_total", "gauge", "用户数（按启用状态分组）")
	enabledN, disabledN := 0, 0
	for _, c := range cs {
		if c.Enabled {
			enabledN++
		} else {
			disabledN++
		}
	}
	m.sample("triune_clients_total", float64(enabledN), "enabled", "true")
	m.sample("triune_clients_total", float64(disabledN), "enabled", "false")

	if len(cs) == 0 {
		return
	}
	m.family("triune_client_enabled", "gauge", "该用户是否启用（1=启用）")
	m.family("triune_client_traffic_bytes", "counter", "该用户累计流量（字节）")
	m.family("triune_client_quota_bytes", "gauge", "该用户配额（字节，0=不限量）")
	m.family("triune_client_expires_timestamp_seconds", "gauge", "该用户到期时间（Unix 秒，仅对设置了到期的用户输出）")

	// 排序保证输出稳定，便于 diff 和测试
	sorted := make([]*model.Client, len(cs))
	copy(sorted, cs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	type row struct {
		name    string
		enabled float64
		up      int64
		down    int64
		quotaB  float64
		exp     *time.Time
	}
	rows := make([]row, 0, len(sorted))
	for _, c := range sorted {
		var up, down int64
		if t, ok := traffic[c.ID]; ok {
			up, down = t.UpBytes, t.DownBytes
		}
		en := 0.0
		if c.Enabled {
			en = 1
		}
		rows = append(rows, row{
			name: c.Name, enabled: en, up: up, down: down,
			quotaB: c.QuotaGB * 1024 * 1024 * 1024, exp: c.ExpiresAt,
		})
	}

	// 按家族逐个输出：同一家族的所有样本必须连续，不能按用户交错
	// （否则 `enabled{a1}` 后面跟了别的家族，再出现 `enabled{a2}`，
	// 严格的解析器会判格式错误）。
	for _, r := range rows {
		m.sample("triune_client_enabled", r.enabled, "client", r.name)
	}
	for _, r := range rows {
		m.sample("triune_client_traffic_bytes", float64(r.up), "client", r.name, "direction", "uplink")
	}
	for _, r := range rows {
		m.sample("triune_client_traffic_bytes", float64(r.down), "client", r.name, "direction", "downlink")
	}
	for _, r := range rows {
		m.sample("triune_client_quota_bytes", r.quotaB, "client", r.name)
	}
	for _, r := range rows {
		if r.exp != nil {
			m.sample("triune_client_expires_timestamp_seconds", float64(r.exp.Unix()), "client", r.name)
		}
	}
}

func (a *Server) metricsInbounds(m *metricsWriter) {
	ins, err := a.Store.ListInbounds("")
	if err != nil {
		return
	}
	type key struct{ core, enabled string }
	counts := map[key]int{}
	for _, in := range ins {
		k := key{core: in.Core, enabled: "false"}
		if in.Enabled && !in.Draft {
			k.enabled = "true"
		}
		counts[k]++
	}
	m.family("triune_inbounds_total", "gauge", "入站数（草稿计入 enabled=\"false\"）")
	keys := make([]key, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].core != keys[j].core {
			return keys[i].core < keys[j].core
		}
		return keys[i].enabled < keys[j].enabled
	})
	for _, k := range keys {
		m.sample("triune_inbounds_total", float64(counts[k]), "core", k.core, "enabled", k.enabled)
	}
}

func (a *Server) metricsCores(m *metricsWriter) {
	if a.ProcMgr == nil {
		return
	}
	running := map[string]bool{}
	for _, n := range a.ProcMgr.RunningCores() {
		running[n] = true
	}
	names := make([]string, 0, len(running))
	for n := range running {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return
	}
	m.family("triune_core_running", "gauge", "内核进程是否在运行（1=运行中）")
	for _, n := range names {
		m.sample("triune_core_running", 1, "core", n)
	}
}

func (a *Server) metricsCerts(m *metricsWriter) {
	if a.CertMgr == nil {
		return
	}
	list := a.CertMgr.Status()
	if len(list) == 0 {
		return
	}
	now := timeNow()
	m.family("triune_cert_expiry_timestamp_seconds", "gauge", "证书到期时间（Unix 秒）")
	m.family("triune_cert_valid", "gauge", "证书是否在有效期内（1=有效）")
	type row struct {
		domain string
		expiry time.Time
	}
	rows := make([]row, 0, len(list))
	for _, c := range list {
		domain, _ := c["domain"].(string)
		if domain == "" {
			continue
		}
		na, ok := c["not_after"].(time.Time)
		if !ok {
			continue
		}
		rows = append(rows, row{domain: domain, expiry: na})
	}
	// 同家族连续输出（理由同 metricsClients）
	for _, r := range rows {
		m.sample("triune_cert_expiry_timestamp_seconds", float64(r.expiry.Unix()), "domain", r.domain)
	}
	for _, r := range rows {
		valid := 0.0
		if r.expiry.After(now) {
			valid = 1
		}
		m.sample("triune_cert_valid", valid, "domain", r.domain)
	}
}

func (a *Server) metricsLive(m *metricsWriter) {
	if a.Coll == nil {
		return
	}
	live := a.Coll.Live()
	m.family("triune_traffic_up_bytes_per_second", "gauge", "最近一个采样周期的总上行速率（字节/秒）")
	m.family("triune_traffic_down_bytes_per_second", "gauge", "最近一个采样周期的总下行速率（字节/秒）")
	m.sample("triune_traffic_up_bytes_per_second", asFloat(live["up_bps"]))
	m.sample("triune_traffic_down_bytes_per_second", asFloat(live["down_bps"]))
}

// modelClientView 已不再需要：直接复用 model.Client。

func asFloat(v any) float64 {
	switch n := v.(type) {
	case int64:
		return float64(n)
	case int:
		return float64(n)
	case float64:
		return n
	}
	return 0
}

// ---- Prometheus 文本格式 ----

type metricsWriter struct {
	b       strings.Builder
	meta    map[string]familyMeta // 家族名 -> HELP/TYPE
	emitted map[string]bool       // 表头是否已输出
}

type familyMeta struct{ typ, help string }

// family 登记家族的 HELP/TYPE，真正输出推迟到第一次 sample。
//
// Prometheus 文本格式要求「同一家族的 HELP/TYPE 与样本必须连成一组」。
// 如果先一次性把所有表头写完再写样本，样本就会跟自己的表头被别的家族隔开，
// 严格的解析器会判为格式错误。
func (m *metricsWriter) family(name, typ, help string) {
	if m.meta == nil {
		m.meta = map[string]familyMeta{}
	}
	m.meta[name] = familyMeta{typ: typ, help: help}
}

// sample 输出一条样本。labels 是交替的 键,值 列表。
func (m *metricsWriter) sample(name string, v float64, labels ...string) {
	if m.emitted == nil {
		m.emitted = map[string]bool{}
	}
	if !m.emitted[name] {
		m.emitted[name] = true
		if meta, ok := m.meta[name]; ok {
			fmt.Fprintf(&m.b, "# HELP %s %s\n# TYPE %s %s\n", name, escapeHelp(meta.help), name, meta.typ)
		}
	}
	m.b.WriteString(name)
	if len(labels) >= 2 {
		m.b.WriteByte('{')
		for i := 0; i+1 < len(labels); i += 2 {
			if i > 0 {
				m.b.WriteByte(',')
			}
			m.b.WriteString(labels[i])
			m.b.WriteString(`="`)
			m.b.WriteString(escapeLabel(labels[i+1]))
			m.b.WriteByte('"')
		}
		m.b.WriteByte('}')
	}
	m.b.WriteByte(' ')
	m.b.WriteString(formatFloat(v))
	m.b.WriteByte('\n')
}

func (m *metricsWriter) String() string { return m.b.String() }

// escapeLabel 转义标签值。
//
// 这不是可选项：客户端名字由用户输入，一个叫 `x"} 1\ntriune_fake 999`
// 的用户不转义就能往指标里塞任意行，伪造出根本不存在的序列。
func escapeLabel(s string) string {
	if !strings.ContainsAny(s, "\\\"\n") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// escapeHelp 转义 HELP 文本（这里不需要转义引号，但反斜杠与换行会破坏格式）。
func escapeHelp(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, "\n", `\n`)
}

// formatFloat 用定点表示，避免大整数被写成 1.073741824e+10。
func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
