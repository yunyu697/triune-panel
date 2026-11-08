package api

import (
	"strings"
	"testing"
)

func TestWorkbenchLayoutSettingRoundTrip(t *testing.T) {
	a, _, _ := newBackupTestServer(t)
	layout := `{"order":["todo","kpis","trend","cores","users","rank","quick"],"collapsed":["rank"],"pinned":["todo"]}`
	w := authedReq(t, a, "PUT", "/api/settings", map[string]string{SettingWorkbenchLayout: layout})
	if w.Code != 200 {
		t.Fatalf("合法布局应能保存，got %d: %s", w.Code, w.Body.String())
	}
	got := decodeMap(t, authedReq(t, a, "GET", "/api/settings", nil))
	if got[SettingWorkbenchLayout] != layout {
		t.Errorf("布局没有原样回显：%v", got[SettingWorkbenchLayout])
	}
	// 空串 = 恢复默认布局，也要能写进去。
	w = authedReq(t, a, "PUT", "/api/settings", map[string]string{SettingWorkbenchLayout: ""})
	if w.Code != 200 {
		t.Fatalf("清空布局应能保存，got %d: %s", w.Code, w.Body.String())
	}
}

func TestValidateWorkbenchLayoutRejectsBadValues(t *testing.T) {
	cases := []struct {
		name, val string
	}{
		{"非 JSON", "not-json"},
		{"未知卡片", `{"order":["../../etc/passwd"]}`},
		{"顺序重复", `{"order":["todo","todo"]}`},
		{"折叠项未知", `{"collapsed":["evil"]}`},
		{"类型不对", `{"order":"todo"}`},
		{"超长", `{"order":["todo"],"junk":"` + strings.Repeat("x", 4200) + `"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := validateWorkbenchLayout(c.val); err == nil {
				t.Error("非法布局不应通过校验")
			}
		})
	}
	for _, ok := range []string{"", `{"order":["todo","kpis"],"collapsed":[],"pinned":[]}`} {
		if err := validateWorkbenchLayout(ok); err != nil {
			t.Errorf("合法布局 %q 被误拒：%v", ok, err)
		}
	}
}

func TestWorkbenchLayoutBadValueNotPersisted(t *testing.T) {
	a, _, _ := newBackupTestServer(t)
	bad := `{"order":["unknown-card"]}`
	w := authedReq(t, a, "PUT", "/api/settings", map[string]string{SettingWorkbenchLayout: bad})
	if w.Code != 400 {
		t.Fatalf("非法布局应被拒，got %d: %s", w.Code, w.Body.String())
	}
	got := decodeMap(t, authedReq(t, a, "GET", "/api/settings", nil))
	if got[SettingWorkbenchLayout] == bad {
		t.Error("被拒绝的布局仍然落库了")
	}
}
