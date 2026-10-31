// Package ops 后台运维循环：
//   - 流量采样：从各内核统计 API 读取每用户上下行计数，计算增量入库；
//   - 自动禁用：到期 / 超额用户自动停用，并对受影响内核走 apply 流水线
//     （校验→原子替换，失败不影响运行配置）。
package ops

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"triune/internal/core"
	"triune/internal/model"
	"triune/internal/store"
)

type Collector struct {
	Store    *store.Store
	Pipe     *core.PipelineManager
	Interval time.Duration
	// Alert 流量告警回调（由 main 注入 api.SendPanelAlert），
	// 用户用量越过告警阈值/配额时触发；nil 表示不告警。
	Alert func(subject, body string)

	mu        sync.Mutex
	connLast  map[string][2]int64       // clash 连接计数基线: connID -> [up,down]
	userLast  map[string]map[string]int // xray 计数基线: core -> "name:dir" -> total
	disabled  map[int64]bool            // 本轮已处理，避免重复
	notified  map[int64]int             // clientID -> 已告警级别（0 无 / 1 阈值 / 2 超额）
	tickUp    int64                     // 本采样周期累计上行增量（实时速率用）
	tickDown  int64                     // 本采样周期累计下行增量
	liveUp    float64                   // 实时上行速率（字节/秒）
	liveDown  float64                   // 实时下行速率（字节/秒）
	liveAt    time.Time                 // 最近一次速率更新时间
	stopCh    chan struct{}
}

func NewCollector(st *store.Store, pipe *core.PipelineManager) *Collector {
	return &Collector{
		Store:    st,
		Pipe:     pipe,
		Interval: 30 * time.Second,
		connLast: map[string][2]int64{},
		userLast: map[string]map[string]int{},
		disabled: map[int64]bool{},
		notified: map[int64]int{},
		stopCh:   make(chan struct{}),
	}
}

func (c *Collector) Stop() { close(c.stopCh) }

// Run 阻塞运行；每 tick 采样流量 + 巡检到期/配额。
func (c *Collector) Run() {
	ticker := time.NewTicker(c.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-c.stopCh:
			return
		case <-ticker.C:
			c.sampleAll()
			c.enforceLimits()
		}
	}
}

// nameToClient 建立用户名 → 客户端映射（内核统计 API 以用户名/邮箱为键）。
func (c *Collector) nameToClient() map[string]*model.Client {
	out := map[string]*model.Client{}
	cs, err := c.Store.ListClients()
	if err != nil {
		return out
	}
	for _, cl := range cs {
		out[cl.Name] = cl
	}
	return out
}

func (c *Collector) sampleAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	nameMap := c.nameToClient()

	// ---- sing-box / mihomo: clash 兼容 /connections ----
	for _, name := range []string{model.CoreSingBox, model.CoreMihomo} {
		port, _ := c.Store.GetSetting("stats_port_" + name)
		if port == "" {
			if name == model.CoreSingBox {
				port = "9090"
			} else {
				port = "9091"
			}
		}
		secret, _ := c.Store.GetSetting("stats_secret")
		en, _ := c.Store.GetSetting("stats_enabled")
		if en != "1" {
			continue
		}
		if deltas, err := c.sampleClash(name, port, secret); err == nil {
			c.recordDeltas(deltas, nameMap)
		}
	}

	// ---- xray: statsquery CLI ----
	if en, _ := c.Store.GetSetting("stats_enabled"); en == "1" {
		if deltas, err := c.sampleXray(); err == nil {
			c.recordDeltas(deltas, nameMap)
		}
	}

	// 本周期结束：由增量计算实时速率（字节/秒）
	secs := c.Interval.Seconds()
	c.liveUp = float64(c.tickUp) / secs
	c.liveDown = float64(c.tickDown) / secs
	c.liveAt = time.Now()
	c.tickUp, c.tickDown = 0, 0
}

// Live 返回实时速率（最近一个采样周期均值，单位字节/秒）。
func (c *Collector) Live() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	at := ""
	if !c.liveAt.IsZero() {
		at = c.liveAt.Format(time.RFC3339)
	}
	return map[string]any{
		"up_bps":     c.liveUp,
		"down_bps":   c.liveDown,
		"interval":   int(c.Interval.Seconds()),
		"updated_at": at,
	}
}

// clashConn /connections 响应结构（sing-box 与 mihomo 兼容）。
type clashConn struct {
	ID       string `json:"id"`
	Upload   int64  `json:"upload"`
	Download int64  `json:"download"`
	Metadata struct {
		InboundUser string `json:"inboundUser"`
		User        string `json:"user"`
		InboundName string `json:"inboundName"`
	} `json:"metadata"`
}

func (c *Collector) sampleClash(coreName, port, secret string) (map[string][2]int64, error) {
	body, err := httpGet(fmt.Sprintf("http://127.0.0.1:%s/connections", port), secret)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Connections []clashConn `json:"connections"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	deltas := map[string][2]int64{}
	for _, conn := range resp.Connections {
		seen[conn.ID] = true
		user := conn.Metadata.InboundUser
		if user == "" {
			user = conn.Metadata.User
		}
		if user == "" {
			continue // 无用户归属（如 mixed 匿名连接）计入 total
		}
		last, ok := c.connLast[coreName+"/"+conn.ID]
		var du, dd int64
		if ok && conn.Upload >= last[0] && conn.Download >= last[1] {
			du, dd = conn.Upload-last[0], conn.Download-last[1]
		} else if !ok {
			// 新连接：只入账本次采样前已有的部分不可知，从当前值起算
			du, dd = 0, 0
		}
		c.connLast[coreName+"/"+conn.ID] = [2]int64{conn.Upload, conn.Download}
		if du > 0 || dd > 0 {
			d := deltas[user]
			deltas[user] = [2]int64{d[0] + du, d[1] + dd}
		}
	}
	// 清理已关闭连接的基线
	for key := range c.connLast {
		if strings.HasPrefix(key, coreName+"/") && !seen[strings.TrimPrefix(key, coreName+"/")] {
			delete(c.connLast, key)
		}
	}
	return deltas, nil
}

func (c *Collector) sampleXray() (map[string][2]int64, error) {
	bin, _ := c.Store.GetSetting("bin_xray")
	if bin == "" {
		return nil, fmt.Errorf("xray binary not set")
	}
	out, err := exec.Command(bin, "api", "statsquery", "--server=127.0.0.1:10085").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("statsquery: %v: %s", err, truncate(string(out), 500))
	}
	var resp struct {
		Stat []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"stat"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, err
	}
	if c.userLast["xray"] == nil {
		c.userLast["xray"] = map[string]int{}
	}
	baseline := c.userLast["xray"]
	deltas := map[string][2]int64{}
	for _, st := range resp.Stat {
		// user>>><email>>>>traffic>>>uplink|downlink
		parts := strings.Split(st.Name, ">>>")
		if len(parts) != 4 || parts[0] != "user" || parts[2] != "traffic" {
			continue
		}
		var val int
		fmt.Sscanf(st.Value, "%d", &val)
		key := parts[1] + ":" + parts[3]
		prev := baseline[key]
		baseline[key] = val
		if val < prev {
			continue // 内核重启后计数清零：丢弃负增量
		}
		user := parts[1]
		dir := parts[3]
		d := deltas[user]
		if dir == "uplink" {
			deltas[user] = [2]int64{d[0] + int64(val-prev), d[1]}
		} else {
			deltas[user] = [2]int64{d[0], d[1] + int64(val-prev)}
		}
	}
	return deltas, nil
}

func (c *Collector) recordDeltas(deltas map[string][2]int64, nameMap map[string]*model.Client) {
	for name, d := range deltas {
		c.tickUp += d[0] // 实时速率按总增量计（含未关联用户的流量）
		c.tickDown += d[1]
		cl, ok := nameMap[name]
		if !ok || d[0] == 0 && d[1] == 0 {
			continue
		}
		if err := c.Store.AddTraffic(cl.ID, d[0], d[1]); err != nil {
			log.Printf("[ops] add traffic for %s: %v", name, err)
		}
	}
}

// enforceLimits 到期 / 配额巡检：触发即停用 + 自动 apply 受影响内核。
func (c *Collector) enforceLimits() {
	cs, err := c.Store.ListClients()
	if err != nil {
		return
	}
	traffic, _ := c.Store.GetTrafficAll()
	affected := map[string]bool{} // core -> 需要重新 apply

	for _, cl := range cs {
		if !cl.Enabled || c.disabled[cl.ID] {
			continue
		}
		// 流量阈值告警（越过阈值或配额时邮件提醒，同一级别只发一次）
		c.alertQuota(cl, traffic)

		reason := ""
		if cl.ExpiresAt != nil && cl.ExpiresAt.Before(time.Now()) {
			reason = "已到期"
		} else if cl.QuotaGB > 0 {
			var used int64
			if t, ok := traffic[cl.ID]; ok {
				used = t.UpBytes + t.DownBytes
			}
			if float64(used) >= cl.QuotaGB*1024*1024*1024 {
				reason = "流量已达配额"
			}
		}
		if reason == "" {
			continue
		}
		cl.Enabled = false
		if err := c.Store.UpdateClient(cl); err != nil {
			log.Printf("[ops] disable client %s: %v", cl.Name, err)
			continue
		}
		c.disabled[cl.ID] = true
		log.Printf("[ops] 用户 %s 已自动停用（%s）", cl.Name, reason)
		// 找到该用户关联的入站 → 标记受影响内核
		if ids, err := c.Store.ClientInboundIDs(cl.ID); err == nil {
			for _, id := range ids {
				if in, err := c.Store.GetInbound(id); err == nil {
					affected[in.Core] = true
				}
			}
		}
	}
	for coreName := range affected {
		input, err := buildRenderInput(c.Store, coreName)
		if err != nil {
			continue
		}
		// 记录修订（与 API 层 apply 一致，保证自动操作可审计）
		rev := &model.ConfigRevision{Core: coreName, Status: model.RevPending, Note: "auto: 用户停用后重新应用"}
		if cc, err := core.Get(coreName); err == nil {
			if content, rerr := cc.Render(input, c.Pipe.RenderOpts()); rerr == nil {
				rev.Content = string(content)
				rev.Hash = shortHash(content)
			} else {
				rev.Detail = rerr.Error()
			}
		}
		_ = c.Store.CreateRevision(rev)

		if _, err := c.Pipe.Apply(coreName, input, "auto: 用户停用后重新应用"); err != nil {
			_ = c.Store.UpdateRevisionStatus(rev.ID, model.RevFailed, err.Error())
			log.Printf("[ops] auto-apply %s: %v（运行配置未受影响）", coreName, err)
		} else {
			_ = c.Store.UpdateRevisionStatus(rev.ID, model.RevApplied, "")
		}
	}
}

func shortHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// alertQuota 用量越过告警阈值（默认 80%，可由设置 quota_warn_pct 调整）
// 或配额时触发一次告警回调。用户配额上调、占比回落到阈值以下后级别复位。
func (c *Collector) alertQuota(cl *model.Client, traffic map[int64]*store.TrafficRow) {
	if c.Alert == nil || cl.QuotaGB <= 0 {
		return
	}
	var used int64
	if t, ok := traffic[cl.ID]; ok {
		used = t.UpBytes + t.DownBytes
	}
	quotaBytes := cl.QuotaGB * 1024 * 1024 * 1024
	pct := int(float64(used) / quotaBytes * 100)
	warnPct := quotaWarnPct(c.Store)
	exceeded := pct >= 100
	level := 1
	if exceeded {
		level = 2
	} else if pct < warnPct {
		if c.notified[cl.ID] > 0 {
			delete(c.notified, cl.ID) // 回落到阈值以下：复位，允许下次再告警
		}
		return
	}
	if c.notified[cl.ID] >= level {
		return
	}
	c.notified[cl.ID] = level
	verb := "超过告警阈值，请关注"
	if exceeded {
		verb = "已达配额上限，该用户将被自动停用"
	}
	subject := fmt.Sprintf("【Triune Panel】用户 %s 流量已达 %d%%", cl.Name, pct)
	body := fmt.Sprintf("用户 %s 已用流量 %.2f GB / %.2f GB（%d%%），%s。",
		cl.Name, float64(used)/1073741824, cl.QuotaGB, pct, verb)
	log.Printf("[ops] 用户 %s 流量 %d%%（%s），已发送告警邮件", cl.Name, pct,
		map[bool]string{true: "已达配额", false: "超阈值"}[exceeded])
	c.Alert(subject, body)
}

// quotaWarnPct 读取告警阈值（百分比），默认 80。
func quotaWarnPct(st *store.Store) int {
	v, _ := st.GetSetting("quota_warn_pct")
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 || n > 100 {
		return 80
	}
	return n
}

// buildRenderInput 与 api 层同逻辑（避免循环依赖独立实现）。
func buildRenderInput(st *store.Store, coreName string) ([]core.InboundWithClients, error) {
	ins, err := st.ListInbounds(coreName)
	if err != nil {
		return nil, err
	}
	allClients, err := st.ListClients()
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
		ids, _ := st.InboundClientIDs(in.ID)
		for _, id := range ids {
			if c, ok := byID[id]; ok && c.Enabled {
				iw.Clients = append(iw.Clients, c)
			}
		}
		out = append(out, iw)
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
