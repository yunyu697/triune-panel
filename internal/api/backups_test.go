package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"triune/internal/store"
)

// 测试布局照安装脚本：data 在 <base>/data，定时备份在 <base>/backups。
// 只造 data/backups 的话，backupScanOptions 的额外根目录就永远测不到。
func newBackupTestServer(t *testing.T) (*Server, string, string) {
	t.Helper()
	base := t.TempDir()
	dir := filepath.Join(base, "data")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return New(st, nil, nil, dir), dir, base
}

func writeBackup(t *testing.T, path string, size int, ageDays int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", size)), 0o644); err != nil {
		t.Fatal(err)
	}
	mtime := time.Now().Add(-time.Duration(ageDays) * 24 * time.Hour)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func authedReq(t *testing.T, a *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+loginForToken(t, a))
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	return w
}

func decodeMap(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("响应不是 JSON: %s", w.Body.String())
	}
	return m
}

func TestBackupInventoryCoversBothArchiveDirs(t *testing.T) {
	a, dir, base := newBackupTestServer(t)
	writeBackup(t, filepath.Join(dir, "backups", "triune-data-20261101-030000.tar.gz"), 100, 7)
	writeBackup(t, filepath.Join(base, "backups", "triune-data-20261102-030000.tar.gz"), 100, 6)
	writeBackup(t, filepath.Join(dir, "backups", "panel", "v1.3.1-20261101-030000.bak"), 50, 7)
	writeBackup(t, filepath.Join(dir, "backups", "unrelated.txt"), 30, 7)

	w := authedReq(t, a, "GET", "/api/backups/inventory", nil)
	if w.Code != 200 {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	m := decodeMap(t, w)
	entries, ok := m["entries"].([]any)
	if !ok || len(entries) != 3 {
		t.Fatalf("应盘到 3 条（两处归档 + 面板 .bak），实际 %v", entries)
	}
	summary := m["summary"].(map[string]any)
	if summary["total_bytes"].(float64) != 250 {
		t.Errorf("总占用应为 250，实际 %v", summary["total_bytes"])
	}
	if !strings.Contains(summary["hint"].(string), "内核二进制") {
		t.Errorf("提示应说明归档含内核二进制：%v", summary["hint"])
	}
	policy := m["policy"].(map[string]any)
	if policy["auto_prune"].(bool) || policy["keep_count"].(float64) != 0 {
		t.Errorf("默认策略应全 0 且自动清理关闭，实际 %v", policy)
	}
	if planned := m["planned"].([]any); len(planned) != 0 {
		t.Errorf("没设策略时不该预演删除，实际 %v", planned)
	}
	// 绝对路径只放在 summary.roots 里，单条记录绝不能泄漏 Path
	for _, raw := range entries {
		if e, ok := raw.(map[string]any); ok {
			if _, leaked := e["Path"]; leaked {
				t.Error("条目里泄漏了内部路径字段")
			}
			if _, leaked := e["path"]; leaked {
				t.Error("条目里泄漏了内部路径字段")
			}
		}
	}
}

func TestBackupPruneNeedsConfirmAndKeepsNewest(t *testing.T) {
	a, dir, _ := newBackupTestServer(t)
	for i, age := range []int{10, 8, 6, 4, 0} {
		writeBackup(t, filepath.Join(dir, "backups",
			"triune-data-2026110"+string(rune('1'+i))+"-030000.tar.gz"), 100, age)
	}
	// 先设策略：超过 3 天的可清
	w := authedReq(t, a, "PUT", "/api/settings", map[string]string{"backup_max_days": "3"})
	if w.Code != 200 {
		t.Fatalf("保存策略失败: %d %s", w.Code, w.Body.String())
	}

	// dry_run 与缺 confirm 都只能预览
	for _, body := range []map[string]any{
		{"dry_run": true}, {"confirm": false},
	} {
		p := authedReq(t, a, "POST", "/api/backups/prune", body)
		if p.Code != 200 {
			t.Fatalf("预览失败: %d %s", p.Code, p.Body.String())
		}
		pm := decodeMap(t, p)
		if pm["dry_run"].(bool) != true || len(pm["planned"].([]any)) != 4 {
			t.Errorf("应预演 4 条，实际 %v", pm)
		}
		entries, _ := os.ReadDir(filepath.Join(dir, "backups"))
		if len(entries) != 5 {
			t.Fatalf("预览删了文件：剩 %d 份", len(entries))
		}
	}

	done := authedReq(t, a, "POST", "/api/backups/prune", map[string]any{"confirm": true})
	if done.Code != 200 {
		t.Fatalf("清理失败: %d %s", done.Code, done.Body.String())
	}
	dm := decodeMap(t, done)
	if len(dm["removed"].([]any)) != 4 {
		t.Errorf("应删 4 份，实际 %v", dm["removed"])
	}
	if dm["freed"].(float64) != 400 {
		t.Errorf("应释放 400 字节，实际 %v", dm["freed"])
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "backups"))
	if len(entries) != 1 {
		t.Errorf("最新一份应保留，实际剩 %d 份", len(entries))
	}
	list, err := a.Store.ListAudit(store.AuditFilter{Prefix: AuditBackupPrune})
	if err != nil || len(list) != 1 {
		t.Errorf("清理应留一条审计，err=%v len=%d", err, len(list))
	}
}

func TestBackupPruneRejectsForeignKeys(t *testing.T) {
	a, dir, base := newBackupTestServer(t)
	writeBackup(t, filepath.Join(dir, "backups", "triune-data-20261101-030000.tar.gz"), 100, 7)
	writeBackup(t, filepath.Join(dir, "backups", "triune-data-20261108-030000.tar.gz"), 100, 0)
	outside := filepath.Join(base, "outside", "20261101-030000.bak")
	writeBackup(t, outside, 50, 7)
	foreign := filepath.Join(dir, "backups", "unrelated.txt")
	writeBackup(t, foreign, 30, 7)

	w := authedReq(t, a, "POST", "/api/backups/prune", map[string]any{
		"confirm": true,
		"keys":    []string{"../../etc/passwd", "/etc/passwd", "data/data/unrelated.txt", "panel/panel/../../outside/20261101-030000.bak"},
	})
	if w.Code != 200 {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	m := decodeMap(t, w)
	if len(m["removed"].([]any)) != 0 {
		t.Errorf("非法 key 不该删任何文件，实际 %v", m["removed"])
	}
	if len(m["failed"].([]any)) != 4 {
		t.Errorf("4 个非法 key 都应被拒，实际 %v", m["failed"])
	}
	for _, p := range []string{outside, foreign} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("对照文件被动过：%s", p)
		}
	}
}

func TestBackupSettingsRoundtripAndValidation(t *testing.T) {
	a, _, _ := newBackupTestServer(t)
	good := map[string]string{
		SettingBackupKeepCount: "7", SettingBackupMaxMB: "512",
		SettingBackupMaxDays: "30", SettingBackupAutoPrune: "1",
	}
	if w := authedReq(t, a, "PUT", "/api/settings", good); w.Code != 200 {
		t.Fatalf("保存策略失败: %d %s", w.Code, w.Body.String())
	}
	got := decodeMap(t, authedReq(t, a, "GET", "/api/settings", nil))
	for k, v := range good {
		if got[k] != v {
			t.Errorf("GET %s = %v，期望 %q", k, got[k], v)
		}
	}
	p := a.backupPolicy()
	if p.KeepCount != 7 || p.MaxDays != 30 || p.MaxBytes != 512*1024*1024 {
		t.Errorf("策略解析不对：%+v", p)
	}
	for _, bad := range []map[string]string{
		{SettingBackupMaxMB: "-1"}, {SettingBackupKeepCount: "abc"},
		{SettingBackupMaxDays: "99999999"}, {SettingBackupAutoPrune: "2"},
	} {
		if w := authedReq(t, a, "PUT", "/api/settings", bad); w.Code != 400 {
			t.Errorf("非法策略 %v 应 400，实际 %d: %s", bad, w.Code, w.Body.String())
		}
	}
}

func TestBackupPolicyClampsTamperedValues(t *testing.T) {
	a, _, _ := newBackupTestServer(t)
	_ = a.Store.SetSetting(SettingBackupKeepCount, "-5")
	_ = a.Store.SetSetting(SettingBackupMaxMB, "999999999999")
	_ = a.Store.SetSetting(SettingBackupMaxDays, "not-a-number")
	p := a.backupPolicy()
	if p.KeepCount != 0 || p.MaxDays != 0 {
		t.Errorf("非法值应收敛为 0：%+v", p)
	}
	if p.MaxBytes != int64(1024*1024)*1024*1024 {
		t.Errorf("超大值应被夹到上限，实际 %d", p.MaxBytes)
	}
}

func TestBackupJanitorOnlyWhenExplicitlyEnabled(t *testing.T) {
	a, dir, _ := newBackupTestServer(t)
	writeBackup(t, filepath.Join(dir, "backups", "triune-data-20261101-030000.tar.gz"), 100, 10)
	writeBackup(t, filepath.Join(dir, "backups", "triune-data-20261108-030000.tar.gz"), 100, 0)
	if w := authedReq(t, a, "PUT", "/api/settings", map[string]string{SettingBackupMaxDays: "3"}); w.Code != 200 {
		t.Fatalf("保存策略失败: %s", w.Body.String())
	}

	a.runBackupJanitor() // auto_prune 默认关
	entries, _ := os.ReadDir(filepath.Join(dir, "backups"))
	if len(entries) != 2 {
		t.Fatal("没开自动清理却删了文件")
	}

	if w := authedReq(t, a, "PUT", "/api/settings", map[string]string{SettingBackupAutoPrune: "1"}); w.Code != 200 {
		t.Fatalf("开启自动清理失败: %s", w.Body.String())
	}
	a.runBackupJanitor()
	entries, _ = os.ReadDir(filepath.Join(dir, "backups"))
	if len(entries) != 1 {
		t.Errorf("开启后应清掉旧份、保留最新一份，实际剩 %d 份", len(entries))
	}
	list, err := a.Store.ListAudit(store.AuditFilter{Prefix: AuditBackupPrune})
	if err != nil || len(list) != 1 || list[0].Actor != "system" {
		t.Errorf("自动清理应留一条 system 审计，err=%v list=%+v", err, list)
	}
}

func TestBackupPruneRejectsOversizedBatch(t *testing.T) {
	a, _, _ := newBackupTestServer(t)
	keys := make([]string, 501)
	w := authedReq(t, a, "POST", "/api/backups/prune", map[string]any{"confirm": true, "keys": keys})
	if w.Code != http.StatusBadRequest {
		t.Errorf("超限批次应 400，实际 %d", w.Code)
	}
}
