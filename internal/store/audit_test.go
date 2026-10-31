package store

import (
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestAuditAddList(t *testing.T) {
	st := newTestStore(t)

	entries := []AuditEntry{
		{Actor: "admin", IP: "1.1.1.1", Action: "login", Target: "admin", Result: AuditOK, Detail: "登录成功"},
		{Actor: "admin", IP: "1.1.1.2", Action: "login", Target: "admin", Result: AuditFail, Detail: "密码错误"},
		{Actor: "admin", IP: "1.1.1.1", Action: "inbound.create", Target: "香港-01", Result: AuditOK},
	}
	for _, e := range entries {
		if err := st.AddAudit(e); err != nil {
			t.Fatalf("AddAudit: %v", err)
		}
	}

	all, err := st.ListAudit(AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("want 3 entries, got %d", len(all))
	}
	// 倒序：最新在前
	if all[0].Action != "inbound.create" {
		t.Fatalf("want newest first, got %q", all[0].Action)
	}
	if all[0].ID <= all[2].ID {
		t.Fatalf("id 应自增倒序: %d vs %d", all[0].ID, all[2].ID)
	}
	if all[0].Time == "" {
		t.Fatal("Time 应自动填充")
	}

	// 前缀筛选
	logins, err := st.ListAudit(AuditFilter{Prefix: "login"})
	if err != nil {
		t.Fatal(err)
	}
	if len(logins) != 2 {
		t.Fatalf("prefix=login: want 2, got %d", len(logins))
	}

	// 结果筛选
	fails, err := st.ListAudit(AuditFilter{Result: AuditFail})
	if err != nil {
		t.Fatal(err)
	}
	if len(fails) != 1 || fails[0].IP != "1.1.1.2" {
		t.Fatalf("result=fail 不对: %+v", fails)
	}

	// 分页
	page, err := st.ListAudit(AuditFilter{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].Action != "login" {
		t.Fatalf("offset=1 limit=1 不对: %+v", page)
	}

	// limit 上限收敛，不因为传 99999 就全表拉出来
	big, err := st.ListAudit(AuditFilter{Limit: 99999})
	if err != nil {
		t.Fatal(err)
	}
	if len(big) != 3 {
		t.Fatalf("want 3, got %d", len(big))
	}
}

// TestAuditPrefixEscaping 前缀里的 % 与 _ 必须当普通字符，不能被当通配符。
func TestAuditPrefixEscaping(t *testing.T) {
	st := newTestStore(t)
	_ = st.AddAudit(AuditEntry{Action: "a%b", Result: AuditOK})
	_ = st.AddAudit(AuditEntry{Action: "axb", Result: AuditOK})

	got, err := st.ListAudit(AuditFilter{Prefix: "a%"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Action != "a%b" {
		t.Fatalf("通配符未转义: %+v", got)
	}
}

func TestAuditPruneAndClear(t *testing.T) {
	st := newTestStore(t)

	old := time.Now().AddDate(0, 0, -100).Format("2006-01-02 15:04:05")
	fresh := time.Now().Format("2006-01-02 15:04:05")
	if err := st.AddAudit(AuditEntry{Time: old, Action: "old", Result: AuditOK}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddAudit(AuditEntry{Time: fresh, Action: "fresh", Result: AuditOK}); err != nil {
		t.Fatal(err)
	}

	n, err := st.PruneAudit(90)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 pruned, got %d", n)
	}
	left, _ := st.ListAudit(AuditFilter{})
	if len(left) != 1 || left[0].Action != "fresh" {
		t.Fatalf("剩余记录不对: %+v", left)
	}

	if err := st.ClearAudit(); err != nil {
		t.Fatal(err)
	}
	left, _ = st.ListAudit(AuditFilter{})
	if len(left) != 0 {
		t.Fatalf("清空后应为空, got %d", len(left))
	}
}

// TestAuditDetailTruncated 超长详情应被截断，避免整段错误输出把库撑大。
func TestAuditDetailTruncated(t *testing.T) {
	st := newTestStore(t)
	long := make([]rune, 2000)
	for i := range long {
		long[i] = 'x'
	}
	if err := st.AddAudit(AuditEntry{Action: "x", Detail: string(long)}); err != nil {
		t.Fatal(err)
	}
	got, _ := st.ListAudit(AuditFilter{})
	if len(got) != 1 {
		t.Fatalf("want 1, got %d", len(got))
	}
	if n := len([]rune(got[0].Detail)); n > 810 {
		t.Fatalf("详情未截断，长度 %d", n)
	}
}
