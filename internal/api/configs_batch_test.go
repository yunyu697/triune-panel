package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// ==================== 配置批量操作 ====================

func cfgID(c map[string]any) int64 { return int64(c["id"].(float64)) }

func TestConfigBatchDelete(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")
	keep := saveOne(t, a, owner, "留着")
	c1 := saveOne(t, a, owner, "删一")
	c2 := saveOne(t, a, owner, "删二")

	// 重复 id 不该把同一份删两遍（第二遍会算成失败），
	// 不存在的 id 要如实回给前端，而不是让整批 500。
	w := do(a, owner, "POST", "/api/configs/batch-delete", map[string]any{
		"ids": []int64{cfgID(c1), cfgID(c2), 9999, cfgID(c1)},
	})
	if w.Code != 200 {
		t.Fatalf("批量删除应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var res struct {
		Deleted int     `json:"deleted"`
		Failed  []int64 `json:"failed"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Deleted != 2 {
		t.Errorf("deleted 应为 2（重复 id 只算一次），实际 %d", res.Deleted)
	}
	if len(res.Failed) != 1 || res.Failed[0] != 9999 {
		t.Errorf("failed 应只含 9999，实际 %v", res.Failed)
	}

	w = do(a, owner, "GET", "/api/configs", nil)
	var list []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || cfgID(list[0]) != cfgID(keep) {
		t.Fatalf("应只剩选中保留的那份，实际 %d 份", len(list))
	}
}

func TestConfigBatchDeleteRejectsBadInput(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")

	for _, c := range []struct {
		name string
		ids  []int64
		want int
	}{
		{"空 ids", []int64{}, http.StatusBadRequest},
		{"全是非法值", []int64{0, -1}, http.StatusBadRequest},
		{"超过上限", make([]int64, configBatchMax+1), http.StatusBadRequest},
	} {
		// 上限用例要填满正数才有意义，否则会被「全是非法值」先行拦掉
		ids := c.ids
		if len(ids) == configBatchMax+1 {
			for i := range ids {
				ids[i] = int64(i + 1)
			}
		}
		if w := do(a, owner, "POST", "/api/configs/batch-delete", map[string]any{"ids": ids}); w.Code != c.want {
			t.Errorf("%s 应 %d，实际 %d", c.name, c.want, w.Code)
		}
	}

	// 请求体根本不是 JSON
	w := do(a, owner, "POST", "/api/configs/batch-delete", "not-json")
	if w.Code != http.StatusBadRequest {
		t.Errorf("非法请求体应 400，实际 %d", w.Code)
	}
}

func TestConfigBatchDownloadZip(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")
	// 同名的存两版是正常用法，zip 里必须能区分开
	x1 := saveOne(t, a, owner, "同款")
	x2 := saveOne(t, a, owner, "同款")
	y := saveOne(t, a, owner, "另一份")

	w := do(a, owner, "POST", "/api/configs/batch-download", map[string]any{
		"ids": []int64{cfgID(x1), cfgID(x2), cfgID(y)},
	})
	if w.Code != 200 {
		t.Fatalf("打包下载应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/zip" {
		t.Errorf("Content-Type 应为 application/zip，实际 %q", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, ".zip") {
		t.Errorf("Content-Disposition 应带 .zip，实际 %q", cd)
	}

	body := w.Body.Bytes()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("返回的不是合法 zip: %v", err)
	}
	if len(zr.File) != 3 {
		t.Fatalf("应有 3 个条目，实际 %d", len(zr.File))
	}
	got := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		got[f.Name] = string(b)
	}
	if _, ok := got["同款.json"]; !ok {
		t.Errorf("缺第一条「同款.json」，实际条目 %v", keysOf(got))
	}
	if _, ok := got["同款 (2).json"]; !ok {
		t.Errorf("同名第二份应改名为「同款 (2).json」，实际条目 %v", keysOf(got))
	}
	if _, ok := got["另一份.json"]; !ok {
		t.Errorf("缺「另一份.json」，实际条目 %v", keysOf(got))
	}
	for name, content := range got {
		if content != "{\n  \"log\": {}\n}\n" {
			t.Errorf("%s 内容不对: %q", name, content)
		}
	}
}

// 配置名是用户自己起的，可能带斜杠或 ..；zip 条目名必须清洗，
// 否则解压时会造出子目录、甚至写到目标目录外面。
func TestConfigBatchDownloadSanitizesName(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")
	c := saveOne(t, a, owner, "../../etc/passwd")

	w := do(a, owner, "POST", "/api/configs/batch-download", map[string]any{
		"ids": []int64{cfgID(c)},
	})
	if w.Code != 200 {
		t.Fatalf("应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.Bytes()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if strings.ContainsAny(f.Name, `/\`) || strings.HasPrefix(f.Name, ".") {
			t.Errorf("zip 条目名未清洗: %q", f.Name)
		}
	}
}

func TestConfigBatchDownloadSkipsMissing(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")
	c := saveOne(t, a, owner, "在的")

	w := do(a, owner, "POST", "/api/configs/batch-download", map[string]any{
		"ids": []int64{9999, cfgID(c)},
	})
	if w.Code != 200 {
		t.Fatalf("部分缺失不该让整包失败，实际 %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.Bytes()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != 1 {
		t.Errorf("应只打包存在的那一份，实际 %d 条", len(zr.File))
	}

	// 一份都取不到时才报错
	if w := do(a, owner, "POST", "/api/configs/batch-download", map[string]any{
		"ids": []int64{8888},
	}); w.Code != http.StatusNotFound {
		t.Errorf("全部缺失应 404，实际 %d", w.Code)
	}
}

func TestConfigBatchViewerDenied(t *testing.T) {
	a := newRoleTestServer(t)
	owner := loginAs(t, a, "admin", "admin")
	guest := loginAs(t, a, "guest", "guest-password-1")
	c := saveOne(t, a, owner, "secret")

	for _, path := range []string{"/api/configs/batch-delete", "/api/configs/batch-download"} {
		w := do(a, guest, "POST", path, map[string]any{"ids": []int64{cfgID(c)}})
		if w.Code != http.StatusForbidden {
			t.Errorf("只读账号 POST %s 应 403，实际 %d", path, w.Code)
		}
	}
	// 确认没被顺手删掉
	if w := do(a, owner, "GET", "/api/configs", nil); w.Code != 200 ||
		!strings.Contains(w.Body.String(), "secret") {
		t.Errorf("只读账号的请求不该产生副作用")
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, fmt.Sprintf("%q", k))
	}
	return out
}
