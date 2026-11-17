package api

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"triune/internal/store"
)

// ==================== 保存的配置 + 规则标签 ====================
//
// 这两组接口都属于「用户自己的资产」：配置含凭据（UUID / 密码 / 私钥），
// 标签是共享的运维经验，所以整组按 write 档——只读账号看不到，也就不会
// 出现「能下载到别人凭据」的口子。
//
// 审计照记（config.save / config.delete / tag.save / tag.delete），
// 但不进变更通知：存一份配置不是基础设施变更，推出去只会稀释真正重要的告警。

const (
	configMaxContent = 4 << 20 // 单份配置上限 4MB（几百节点的订阅也就几十 KB）
	configMaxCount   = 200     // 总份数上限，防止把库撑爆
	configMaxParams  = 256 << 10
)

func contentHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// configExt 按内核决定扩展名。
func configExt(core string) string {
	if core == "mihomo" {
		return "yaml"
	}
	return "json"
}

// safeFilename 生成 Content-Disposition 用的文件名。
//
// 中文名不能直接塞进 filename=：HTTP 头是 latin-1，非 ASCII 会被某些客户端
// 解成乱码。所以 ASCII 回退 + RFC 5987 的 filename* 双写，现代客户端取后者。
func safeFilename(name, ext string) string {
	ascii := make([]rune, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			ascii = append(ascii, r)
		case r == ' ':
			ascii = append(ascii, '_')
		}
	}
	base := string(ascii)
	if base == "" {
		base = "config"
	}
	full := name + "." + ext
	return fmt.Sprintf(`attachment; filename="%s.%s"; filename*=UTF-8''%s`,
		base, ext, url.PathEscape(full))
}

// ==================== 配置 ====================

func (a *Server) handleConfigsList(w http.ResponseWriter, r *http.Request) {
	cs, err := a.Store.ListSavedConfigs()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if cs == nil {
		cs = []*store.SavedConfig{}
	}
	writeJSON(w, 200, cs)
}

type saveConfigReq struct {
	Name        string `json:"name"`
	Core        string `json:"core"`
	CoreVersion string `json:"core_version"`
	Target      string `json:"target"`
	Scenario    string `json:"scenario"`
	Note        string `json:"note"`
	Params      string `json:"params"`
	Content     string `json:"content"`
}

func (a *Server) handleConfigSave(w http.ResponseWriter, r *http.Request) {
	var q saveConfigReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, configMaxContent)).Decode(&q); err != nil {
		fail(w, 400, "请求体过大或格式错误")
		return
	}
	q.Name = strings.TrimSpace(q.Name)
	if q.Name == "" {
		fail(w, 400, "配置名称不能为空")
		return
	}
	if len([]rune(q.Name)) > 64 {
		fail(w, 400, "配置名称最多 64 个字符")
		return
	}
	if q.Core != "mihomo" && q.Core != "singbox" {
		fail(w, 400, "core 必须是 mihomo 或 singbox")
		return
	}
	if strings.TrimSpace(q.Content) == "" {
		fail(w, 400, "配置内容为空，先生成再保存")
		return
	}
	if len(q.Content) > configMaxContent {
		fail(w, 400, fmt.Sprintf("配置超过 %d MB 上限", configMaxContent>>20))
		return
	}
	if len(q.Params) > configMaxParams {
		fail(w, 400, "生成参数过大")
		return
	}
	if n, err := a.Store.CountSavedConfigs(); err == nil && n >= configMaxCount {
		fail(w, 400, fmt.Sprintf("最多保存 %d 份配置，请先删掉不再需要的", configMaxCount))
		return
	}

	c := &store.SavedConfig{
		Name: q.Name, Core: q.Core, CoreVersion: q.CoreVersion,
		Target: q.Target, Scenario: q.Scenario, Note: strings.TrimSpace(q.Note),
		Params: q.Params, Content: q.Content, Hash: contentHash(q.Content),
		CreatedBy: a.actorName(r),
	}
	if err := a.Store.CreateSavedConfig(c); err != nil {
		fail(w, 500, err.Error())
		return
	}
	a.auditOK(r, AuditConfig, c.Name, fmt.Sprintf("保存配置 #%d（%s / %s，%d 字节）",
		c.ID, c.Core, c.Target, c.Size))
	c.Content = ""
	writeJSON(w, 200, c)
}

func (a *Server) handleConfigGet(w http.ResponseWriter, r *http.Request) {
	c, err := a.Store.GetSavedConfig(pathID(r))
	if err != nil {
		if store.ErrNotFound(err) {
			fail(w, 404, "配置不存在")
			return
		}
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, c)
}

// handleConfigDownload 直接以附件形式返回，省掉「先拿 JSON 再自己存文件」。
func (a *Server) handleConfigDownload(w http.ResponseWriter, r *http.Request) {
	c, err := a.Store.GetSavedConfig(pathID(r))
	if err != nil {
		if store.ErrNotFound(err) {
			fail(w, 404, "配置不存在")
			return
		}
		fail(w, 500, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", safeFilename(c.Name, configExt(c.Core)))
	w.Header().Set("Content-Length", strconv.Itoa(len(c.Content)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(c.Content))
	a.auditOK(r, AuditConfig, c.Name, "下载配置")
}

type updateConfigReq struct {
	Name string `json:"name"`
	// Note 用指针区分「没传」和「传了空串」：
	// 前者是只想覆盖内容（不该动备注），后者是真的想清空备注。
	// 用 string 的话这两种情况长得一样，覆盖内容时会把备注顺手抹掉。
	Note        *string `json:"note"`
	Content     string  `json:"content"`
	Params      string  `json:"params"`
	CoreVersion string  `json:"core_version"`
}

func (a *Server) handleConfigUpdate(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	old, err := a.Store.GetSavedConfig(id)
	if err != nil {
		if store.ErrNotFound(err) {
			fail(w, 404, "配置不存在")
			return
		}
		fail(w, 500, err.Error())
		return
	}
	var q updateConfigReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, configMaxContent)).Decode(&q); err != nil {
		fail(w, 400, "请求体过大或格式错误")
		return
	}
	q.Name = strings.TrimSpace(q.Name)
	if q.Name != "" && len([]rune(q.Name)) > 64 {
		fail(w, 400, "配置名称最多 64 个字符")
		return
	}
	if len(q.Content) > configMaxContent {
		fail(w, 400, fmt.Sprintf("配置超过 %d MB 上限", configMaxContent>>20))
		return
	}
	// 没传 note 就沿用旧值；传了空串才是「清空备注」
	note := old.Note
	if q.Note != nil {
		note = strings.TrimSpace(*q.Note)
	}

	c := &store.SavedConfig{
		ID: id, Name: q.Name, Note: note,
		Content: q.Content, Params: q.Params, CoreVersion: q.CoreVersion,
	}
	if c.Content != "" {
		c.Hash = contentHash(c.Content)
	}
	if err := a.Store.UpdateSavedConfig(c); err != nil {
		fail(w, 500, err.Error())
		return
	}
	detail := "修改配置元信息"
	if c.Content != "" {
		detail = fmt.Sprintf("覆盖配置内容（%d → %d 字节）", len(old.Content), len(c.Content))
	}
	a.auditOK(r, AuditConfig, old.Name, detail)

	out, err := a.Store.GetSavedConfig(id)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, out)
}

func (a *Server) handleConfigDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	c, err := a.Store.GetSavedConfig(id)
	if err != nil {
		if store.ErrNotFound(err) {
			fail(w, 404, "配置不存在")
			return
		}
		fail(w, 500, err.Error())
		return
	}
	if err := a.Store.DeleteSavedConfig(id); err != nil {
		fail(w, 500, err.Error())
		return
	}
	a.auditOK(r, AuditConfig, c.Name, fmt.Sprintf("删除配置 #%d", id))
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

// ==================== 批量操作 ====================
//
// 「配置管理」攒到几十份之后，逐个点删除纯粹是体力活。下面两个接口只把
// 「选中一批」这个动作搬过来，语义与单条接口保持一致：仍然逐条校验存在性，
// 审计仍记一条汇总（不按条刷屏），删不掉的 id 原样回给前端去提示。

const configBatchMax = configMaxCount // 与总份数上限一致：一次最多清空全部

type batchIDsReq struct {
	IDs []int64 `json:"ids"`
}

// cleanIDs 去重、丢掉非正数，保持用户点击的顺序（审计里读起来更自然）。
func cleanIDs(in []int64) []int64 {
	seen := make(map[int64]bool, len(in))
	out := make([]int64, 0, len(in))
	for _, id := range in {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// summarizeNames 取前三个名字，其余折成「等 N 份」——审计详情要一行装得下。
func summarizeNames(names []string) string {
	const head = 3
	if len(names) <= head {
		return strings.Join(names, "、")
	}
	return strings.Join(names[:head], "、") + fmt.Sprintf(" 等 %d 份", len(names))
}

// decodeBatchIDs 解析并校验 ids。校验不过时已经写过响应，返回 ok=false。
func decodeBatchIDs(w http.ResponseWriter, r *http.Request) ([]int64, bool) {
	var q batchIDsReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&q); err != nil {
		fail(w, 400, "请求体格式错误")
		return nil, false
	}
	ids := cleanIDs(q.IDs)
	if len(ids) == 0 {
		fail(w, 400, "没有选中任何配置")
		return nil, false
	}
	if len(ids) > configBatchMax {
		fail(w, 400, fmt.Sprintf("一次最多操作 %d 份", configBatchMax))
		return nil, false
	}
	return ids, true
}

func (a *Server) handleConfigBatchDelete(w http.ResponseWriter, r *http.Request) {
	ids, ok := decodeBatchIDs(w, r)
	if !ok {
		return
	}
	// 逐条删而不是一条 IN 语句：删不掉的那几份要能点名回给前端，
	// 而且清单最多 200 份，逐条的开销远小于排查「为什么少删了一份」。
	var names []string
	failed := make([]int64, 0, 2)
	for _, id := range ids {
		c, err := a.Store.GetSavedConfig(id)
		if err != nil {
			failed = append(failed, id)
			continue
		}
		if err := a.Store.DeleteSavedConfig(id); err != nil {
			failed = append(failed, id)
			continue
		}
		names = append(names, c.Name)
	}
	if len(names) > 0 {
		a.auditOK(r, AuditConfig, summarizeNames(names),
			fmt.Sprintf("批量删除 %d 份配置", len(names)))
	}
	writeJSON(w, 200, map[string]any{
		"deleted": len(names),
		"failed":  failed,
	})
}

// safeZipName 换掉文件名里的路径分隔符与控制字符。
//
// zip 条目名允许出现斜杠，解压时会造出子目录；配合 .. 甚至能写到目标目录外面。
// 配置名是用户自己起的，不能假设它规整。
func safeZipName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f:
			b.WriteRune('_')
		case strings.ContainsRune(`/\:*?"<>|`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimLeft(strings.TrimSpace(b.String()), ".")
	if out == "" {
		out = "config"
	}
	return out
}

func (a *Server) handleConfigBatchDownload(w http.ResponseWriter, r *http.Request) {
	ids, ok := decodeBatchIDs(w, r)
	if !ok {
		return
	}
	type entry struct{ name, ext, content string }
	entries := make([]entry, 0, len(ids))
	for _, id := range ids {
		c, err := a.Store.GetSavedConfig(id)
		if err != nil || c.Content == "" {
			continue // 取不到的跳过，不让一份坏数据废掉整包
		}
		entries = append(entries, entry{c.Name, configExt(c.Core), c.Content})
	}
	if len(entries) == 0 {
		fail(w, 404, "选中的配置都取不到内容")
		return
	}

	h := w.Header()
	// 包名固定用 ASCII：HTTP 头是 latin-1，中文会变乱码，而这个名字是服务端
	// 生成的、不需要对应某一份配置（单条下载才用 safeFilename 的双写方案）。
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition",
		`attachment; filename="triune-configs-`+time.Now().Format("20060102-150405")+`.zip"`)
	h.Set("X-Content-Type-Options", "nosniff")

	zw := zip.NewWriter(w)
	used := make(map[string]int, len(entries))
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		base := e.name
		key := e.name + "." + e.ext
		// 同一份配置存好几版是正常用法。重名不补序号的话，zip 里会出现两个
		// 同名条目，解压时后一个直接覆盖前一个——用户以为都下来了。
		if n := used[key]; n > 0 {
			base = fmt.Sprintf("%s (%d)", e.name, n+1)
		}
		used[key]++
		fh := &zip.FileHeader{Name: safeZipName(base) + "." + e.ext, Method: zip.Deflate}
		fh.SetModTime(time.Now())
		fw, err := zw.CreateHeader(fh)
		if err != nil {
			break // 响应头已经发出去了，只能中断，不能再 fail()
		}
		if _, err := io.WriteString(fw, e.content); err != nil {
			break
		}
		names = append(names, e.name)
	}
	_ = zw.Close()
	if len(names) > 0 {
		a.auditOK(r, AuditConfig, summarizeNames(names),
			fmt.Sprintf("批量下载 %d 份配置", len(names)))
	}
}

// ==================== 规则标签 ====================

func (a *Server) handleTagsList(w http.ResponseWriter, r *http.Request) {
	ts, err := a.Store.ListRuleTags()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if ts == nil {
		ts = []*store.RuleTag{}
	}
	writeJSON(w, 200, ts)
}

type tagReq struct {
	Name  string `json:"name"`
	Rules string `json:"rules"`
}

// cleanRuleLines 去空行、去首尾空白、去重（保留首次出现的顺序）。
//
// 去重放在服务端也做一遍：前端会在写入 textarea 时去重，但接口是公开的，
// 不能让「同一行出现三次」变成规则里出现三次——内核不会报错，只是白跑。
func cleanRuleLines(rules string) string {
	seen := make(map[string]bool, 16)
	out := make([]string, 0, 16)
	for _, line := range strings.Split(rules, "\n") {
		s := strings.TrimSpace(line)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return strings.Join(out, "\n")
}

func (a *Server) handleTagCreate(w http.ResponseWriter, r *http.Request) {
	var q tagReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&q); err != nil {
		fail(w, 400, "请求体格式错误")
		return
	}
	q.Name = strings.TrimSpace(q.Name)
	if q.Name == "" {
		fail(w, 400, "标签名称不能为空")
		return
	}
	if len([]rune(q.Name)) > 32 {
		fail(w, 400, "标签名称最多 32 个字符")
		return
	}
	rules := cleanRuleLines(q.Rules)
	if rules == "" {
		fail(w, 400, "标签内容为空")
		return
	}
	t := &store.RuleTag{Name: q.Name, Rules: rules, CreatedBy: a.actorName(r)}
	if err := a.Store.CreateRuleTag(t); err != nil {
		if err == store.ErrTagNameTaken {
			fail(w, 400, "同名标签已存在")
			return
		}
		fail(w, 500, err.Error())
		return
	}
	a.auditOK(r, AuditTag, t.Name, fmt.Sprintf("新建规则标签（%d 条）", t.Count))
	writeJSON(w, 200, t)
}

func (a *Server) handleTagUpdate(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	old, err := a.Store.GetRuleTag(id)
	if err != nil {
		if store.ErrNotFound(err) {
			fail(w, 404, "标签不存在")
			return
		}
		fail(w, 500, err.Error())
		return
	}
	var q tagReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&q); err != nil {
		fail(w, 400, "请求体格式错误")
		return
	}
	q.Name = strings.TrimSpace(q.Name)
	if q.Name == "" {
		q.Name = old.Name
	}
	rules := cleanRuleLines(q.Rules)
	if rules == "" {
		fail(w, 400, "标签内容为空")
		return
	}
	t := &store.RuleTag{ID: id, Name: q.Name, Rules: rules}
	if err := a.Store.UpdateRuleTag(t); err != nil {
		if err == store.ErrTagNameTaken {
			fail(w, 400, "同名标签已存在")
			return
		}
		fail(w, 500, err.Error())
		return
	}
	a.auditOK(r, AuditTag, t.Name, fmt.Sprintf("更新规则标签（%d 条）", t.Count))
	writeJSON(w, 200, t)
}

func (a *Server) handleTagDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	t, err := a.Store.GetRuleTag(id)
	if err != nil {
		if store.ErrNotFound(err) {
			fail(w, 404, "标签不存在")
			return
		}
		fail(w, 500, err.Error())
		return
	}
	if err := a.Store.DeleteRuleTag(id); err != nil {
		fail(w, 500, err.Error())
		return
	}
	a.auditOK(r, AuditTag, t.Name, "删除规则标签")
	writeJSON(w, 200, map[string]string{"ok": "1"})
}
