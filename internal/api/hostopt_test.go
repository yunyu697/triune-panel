package api

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

// 体检接口是纯只读的：连数据目录都不该多出一个文件。写 /etc 的路径更不能
// 由 GET 触发——这条用例同时守住「Detect 不落盘」和「没有隐式副作用」。
func TestHostOptReportIsReadOnlyAndStructured(t *testing.T) {
	a, dir, _ := newBackupTestServer(t)
	before := dirNames(t, dir)

	w := authedReq(t, a, "GET", "/api/host-opt", nil)
	if w.Code != 200 {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	m := decodeMap(t, w)
	for _, k := range []string{"platform", "checks", "suggested", "config_path", "apply_enabled", "applied", "reason"} {
		if _, ok := m[k]; !ok {
			t.Errorf("报告缺字段 %s", k)
		}
	}
	checks, ok := m["checks"].([]any)
	if !ok || len(checks) == 0 {
		t.Errorf("至少要有体检结论，实际 %v", m["checks"])
	}
	// 非 Linux 只有一条「平台不支持」的 Warn；完整 7 项体检只在 Linux 上有意义。
	if m["platform"] == "linux" && len(checks) < 4 {
		t.Errorf("Linux 上体检项应至少 4 条，实际 %v", m["checks"])
	}
	if sug, ok := m["suggested"].([]any); !ok || len(sug) == 0 {
		t.Errorf("无论能否应用，都要给手动执行命令，实际 %v", m["suggested"])
	}
	if m["config_path"].(string) == "" {
		t.Error("配置路径不该为空")
	}
	if _, ok := m["apply_enabled"].(bool); !ok {
		t.Error("apply_enabled 应为布尔")
	}
	if m["applied"].(bool) {
		t.Error("全新数据目录不该显示「已应用」")
	}
	if after := dirNames(t, dir); !reflect.DeepEqual(before, after) {
		t.Errorf("只读体检改变了数据目录：%v → %v", before, after)
	}
}

// 两个写端点都必须先收到精确确认串才能做任何事。测试里绝不能传正确的确认串：
// Linux + root 的 CI runner 上那会真的去写 /etc/sysctl.d。
func TestHostOptWriteRequiresExplicitConfirm(t *testing.T) {
	a, dir, _ := newBackupTestServer(t)
	before := dirNames(t, dir)

	for _, path := range []string{"/api/host-opt/apply", "/api/host-opt/revert"} {
		for _, body := range []map[string]string{{}, {"confirm": ""}, {"confirm": "yes"}, {"confirm": "APPLY BBR "}} {
			w := authedReq(t, a, "POST", path, body)
			if w.Code != 400 {
				t.Errorf("%s 传 %v 应 400，实际 %d: %s", path, body, w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "确认") {
				t.Errorf("%s 的错误应提示需要显式确认，实际 %s", path, w.Body.String())
			}
		}
	}
	if after := dirNames(t, dir); !reflect.DeepEqual(before, after) {
		t.Errorf("确认失败时不应写任何文件：%v → %v", before, after)
	}
}

// 静态守卫：写系统配置的路由必须是 ownerOnly。仅靠功能测试看不出「有人
// 顺手把权限降到 a.write」，但那等于把改 /etc/sysctl.d 的能力给了普通管理员。
func TestHostOptWriteRoutesAreOwnerOnly(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("api.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		`mux.HandleFunc("POST /api/host-opt/apply", a.ownerOnly(a.handleHostOptApply))`,
		`mux.HandleFunc("POST /api/host-opt/revert", a.ownerOnly(a.handleHostOptRevert))`,
	} {
		if !strings.Contains(string(src), line) {
			t.Errorf("路由权限被改弱或丢失：%s", line)
		}
	}
}
