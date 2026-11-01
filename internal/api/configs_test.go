package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"triune/internal/store"
)

// ==================== 保存的配置 ====================

func saveOne(t *testing.T, a *Server, tok, name string) map[string]any {
	t.Helper()
	w := do(a, tok, "POST", "/api/configs", map[string]any{
		"name": name, "core": "singbox", "core_version": "1.14",
		"target": "main-router", "scenario": "balanced",
		"note":    "改端口前",
		"params":  `{"core":"singbox","target":"main-router","inbound_ids":[1,2]}`,
		"content": "{\n  \"log\": {}\n}\n",
	})
	if w.Code != 200 {
		t.Fatalf("保存配置失败 %d: %s", w.Code, w.Body.String())
	}
	var c map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestConfigSaveListGetDelete(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")

	c := saveOne(t, a, owner, "家宽-主路由")
	if c["id"] == nil || c["hash"] == "" {
		t.Fatalf("保存应返回 id 与 hash，实际 %v", c)
	}
	if c["size"].(float64) != float64(len("{\n  \"log\": {}\n}\n")) {
		t.Errorf("size 应为内容字节数，实际 %v", c["size"])
	}
	if c["created_by"] != "admin" {
		t.Errorf("created_by 应为 admin，实际 %v", c["created_by"])
	}
	if c["created_at"] == "" {
		t.Error("created_at 不应为空")
	}

	// 列表不含 content（避免一次拉几 MB）
	w := do(a, owner, "GET", "/api/configs", nil)
	if w.Code != 200 {
		t.Fatalf("列表应 200，实际 %d", w.Code)
	}
	var list []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("应有 1 份配置，实际 %d", len(list))
	}
	if v, ok := list[0]["content"]; ok && v != "" {
		t.Errorf("列表不应返回 content，实际 %q", v)
	}
	if list[0]["params"] == "" {
		t.Error("列表应返回 params（重新生成要用）")
	}

	// 详情含 content
	id := int64(c["id"].(float64))
	w = do(a, owner, "GET", "/api/configs/"+fmt.Sprint(id), nil)
	if w.Code != 200 {
		t.Fatalf("详情应 200，实际 %d", w.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if !strings.Contains(got["content"].(string), `"log"`) {
		t.Errorf("详情应含完整 content，实际 %v", got["content"])
	}

	// 下载：Content-Disposition 要能正确表达中文名
	w = do(a, owner, "GET", "/api/configs/"+fmt.Sprint(id)+"/download", nil)
	if w.Code != 200 {
		t.Fatalf("下载应 200，实际 %d", w.Code)
	}
	cd := w.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") {
		t.Errorf("下载应带 attachment，实际 %q", cd)
	}
	if !strings.Contains(cd, "filename*=UTF-8''") {
		t.Errorf("中文名应带 RFC 5987 编码，实际 %q", cd)
	}
	if !strings.Contains(cd, url.PathEscape("家宽-主路由.json")) {
		t.Errorf("文件名编码不对，实际 %q", cd)
	}
	if w.Body.String() != "{\n  \"log\": {}\n}\n" {
		t.Errorf("下载内容应与保存一致，实际 %q", w.Body.String())
	}

	// 删除
	w = do(a, owner, "DELETE", "/api/configs/"+fmt.Sprint(id), nil)
	if w.Code != 200 {
		t.Fatalf("删除应 200，实际 %d", w.Code)
	}
	w = do(a, owner, "GET", "/api/configs/"+fmt.Sprint(id), nil)
	if w.Code != 404 {
		t.Errorf("删除后应 404，实际 %d", w.Code)
	}
}

func TestConfigSaveValidation(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")

	cases := []struct {
		name string
		body map[string]any
	}{
		{"空名称", map[string]any{"name": "  ", "core": "singbox", "content": "x"}},
		{"空内容", map[string]any{"name": "a", "core": "singbox", "content": "  "}},
		{"非法内核", map[string]any{"name": "a", "core": "xray", "content": "x"}},
		{"名称过长", map[string]any{"name": strings.Repeat("长", 65), "core": "singbox", "content": "x"}},
	}
	for _, c := range cases {
		w := do(a, owner, "POST", "/api/configs", c.body)
		if w.Code != 400 {
			t.Errorf("%s 应 400，实际 %d: %s", c.name, w.Code, w.Body.String())
		}
	}

	// 校验失败不应留下半条记录
	w := do(a, owner, "GET", "/api/configs", nil)
	var list []any
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) != 0 {
		t.Errorf("校验失败不应写入记录，实际 %d 条", len(list))
	}
}

// TestConfigUpdateOverwritesContent 「重新生成后覆盖」的路径。
func TestConfigUpdateOverwritesContent(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")
	c := saveOne(t, a, owner, "v1")
	id := fmt.Sprint(int64(c["id"].(float64)))
	oldHash := c["hash"]

	// 只改备注，内容不动
	w := do(a, owner, "PUT", "/api/configs/"+id, map[string]any{"note": "新备注"})
	if w.Code != 200 {
		t.Fatalf("改备注应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["note"] != "新备注" {
		t.Errorf("备注未更新，实际 %v", got["note"])
	}
	if got["hash"] != oldHash {
		t.Errorf("只改备注不该动 hash，实际 %v → %v", oldHash, got["hash"])
	}
	if !strings.Contains(got["content"].(string), `"log"`) {
		t.Error("只改备注不该清空 content")
	}

	// 覆盖内容
	newContent := "{\n  \"log\": {},\n  \"inbounds\": []\n}\n"
	w = do(a, owner, "PUT", "/api/configs/"+id, map[string]any{"content": newContent})
	if w.Code != 200 {
		t.Fatalf("覆盖内容应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["content"] != newContent {
		t.Errorf("content 未覆盖，实际 %q", got["content"])
	}
	if got["hash"] == oldHash {
		t.Error("覆盖内容后 hash 应该变")
	}
	if got["note"] != "新备注" {
		t.Errorf("覆盖内容不该清空备注，实际 %v", got["note"])
	}
}

func TestConfigNotFound(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/configs/999"},
		{"PUT", "/api/configs/999"},
		{"DELETE", "/api/configs/999"},
		{"GET", "/api/configs/999/download"},
	} {
		if w := do(a, owner, c.method, c.path, map[string]any{"content": "x"}); w.Code != 404 {
			t.Errorf("%s %s 应 404，实际 %d", c.method, c.path, w.Code)
		}
	}
}

// TestConfigViewerDenied 配置含节点凭据，只读账号整组不可见。
func TestConfigViewerDenied(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")
	guest := loginAs(t, a, "guest", "guest-password-1")
	c := saveOne(t, a, owner, "secret")
	id := fmt.Sprint(int64(c["id"].(float64)))

	for _, path := range []string{"/api/configs", "/api/configs/" + id, "/api/configs/" + id + "/download", "/api/rule-tags"} {
		if w := do(a, guest, "GET", path, nil); w.Code != http.StatusForbidden {
			t.Errorf("viewer GET %s 应 403，实际 %d", path, w.Code)
		}
	}
	if w := do(a, guest, "POST", "/api/configs", map[string]any{
		"name": "x", "core": "singbox", "content": "y"}); w.Code != http.StatusForbidden {
		t.Errorf("viewer 保存应 403，实际 %d", w.Code)
	}
}

// ==================== 规则标签 ====================

func TestRuleTagCRUD(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")

	w := do(a, owner, "POST", "/api/rule-tags", map[string]any{
		"name": "公司内网",
		"rules": "DOMAIN-SUFFIX,corp.example.com,DIRECT\n\n  IP-CIDR,10.0.0.0/8,DIRECT  \n" +
			"DOMAIN-SUFFIX,corp.example.com,DIRECT\nIP-CIDR,10.0.0.0/8,DIRECT",
	})
	if w.Code != 200 {
		t.Fatalf("新建标签应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var tag map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &tag)
	// 两行重复 + 空行 + 首尾空白，去重后应剩 2 条
	if tag["count"].(float64) != 2 {
		t.Errorf("去重后应为 2 条，实际 %v（rules=%q）", tag["count"], tag["rules"])
	}
	rules := tag["rules"].(string)
	if strings.Count(rules, "corp.example.com") != 1 {
		t.Errorf("重复行应被去掉，实际 %q", rules)
	}
	if strings.Contains(rules, "  ") || strings.Contains(rules, "\n\n") {
		t.Errorf("不应保留空行或多余空白，实际 %q", rules)
	}
	id := fmt.Sprint(int64(tag["id"].(float64)))

	// 重名
	if w := do(a, owner, "POST", "/api/rule-tags", map[string]any{
		"name": "公司内网", "rules": "MATCH,DIRECT"}); w.Code != 400 {
		t.Errorf("重名应 400，实际 %d", w.Code)
	}

	// 列表按名字排序
	do(a, owner, "POST", "/api/rule-tags", map[string]any{"name": "广告", "rules": "DOMAIN-KEYWORD,ads,REJECT"})
	w = do(a, owner, "GET", "/api/rule-tags", nil)
	var list []map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) != 2 {
		t.Fatalf("应有 2 个标签，实际 %d", len(list))
	}
	if list[0]["name"] != "公司内网" {
		t.Errorf("应按名字排序（公司内网 < 广告），实际首个是 %v", list[0]["name"])
	}

	// 更新
	w = do(a, owner, "PUT", "/api/rule-tags/"+id, map[string]any{
		"name": "公司内网v2", "rules": "DOMAIN-SUFFIX,corp.example.com,DIRECT"})
	if w.Code != 200 {
		t.Fatalf("更新应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var upd map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &upd)
	if upd["name"] != "公司内网v2" || upd["count"].(float64) != 1 {
		t.Errorf("更新结果不对：%v", upd)
	}

	// 空内容
	if w := do(a, owner, "PUT", "/api/rule-tags/"+id, map[string]any{"rules": "  \n \n"}); w.Code != 400 {
		t.Errorf("空内容应 400，实际 %d", w.Code)
	}

	// 删除
	if w := do(a, owner, "DELETE", "/api/rule-tags/"+id, nil); w.Code != 200 {
		t.Fatalf("删除应 200，实际 %d", w.Code)
	}
	w = do(a, owner, "GET", "/api/rule-tags", nil)
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) != 1 {
		t.Errorf("删除后应剩 1 个，实际 %d", len(list))
	}
	if w := do(a, owner, "DELETE", "/api/rule-tags/"+id, nil); w.Code != 404 {
		t.Errorf("重复删除应 404，实际 %d", w.Code)
	}
}

func TestCleanRuleLines(t *testing.T) {
	in := "  A  \n\nA\nB\n  \nB\nC"
	if got := cleanRuleLines(in); got != "A\nB\nC" {
		t.Errorf("cleanRuleLines(%q) = %q，期望 A\\nB\\nC", in, got)
	}
	if got := cleanRuleLines("   \n\n "); got != "" {
		t.Errorf("全空白应得空串，实际 %q", got)
	}
}

// TestConfigAudited 保存与删除要留痕，但不该进变更通知。
func TestConfigAudited(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")
	c := saveOne(t, a, owner, "留痕测试")
	id := fmt.Sprint(int64(c["id"].(float64)))
	do(a, owner, "DELETE", "/api/configs/"+id, nil)

	entries, err := a.Store.ListAudit(store.AuditFilter{Prefix: AuditConfig})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 2 {
		t.Fatalf("应有保存与删除两条审计，实际 %d", len(entries))
	}
	if entries[0].Action != AuditConfig || entries[0].Actor != "admin" {
		t.Errorf("审计内容不对：%+v", entries[0])
	}
}
