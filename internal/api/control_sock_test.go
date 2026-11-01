// 控制套接字的端到端测试：真起一个 unix 监听，真连上去，真走一遍鉴权与白名单。
//
// 放在 api_test 外部包：需要 import 三个内核实现包完成注册，否则 core.Get
// 报 unknown core，apply 那条路径根本走不到。
package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"triune/internal/api"
	"triune/internal/core"
	"triune/internal/model"
	"triune/internal/proc"
	"triune/internal/store"

	_ "triune/internal/core/mihomo"
	_ "triune/internal/core/singbox"
	_ "triune/internal/core/xray"
)

// shortSockPath 给测试造一个够短的套接字路径。
//
// unix 套接字路径有长度上限（Linux 108、macOS 104），而 t.TempDir() 会把
// 测试名拼进目录，长测试名直接 bind: invalid argument。生产路径
// /run/triune-panel/control.sock 只有 30 来个字符，不会碰到这个限制。
func shortSockPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ctl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "c.sock")
}

// startControl 起一个带控制套接字的面板实例，返回 (server, 套接字路径, 客户端)。
func startControl(t *testing.T) (*api.Server, string, *http.Client) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	pm := proc.NewManager()
	pipe := &core.PipelineManager{DataDir: dir, ProcMgr: pm, Settings: st}
	a := api.New(st, pipe, pm, dir)

	// 固定套接字路径：否则 root 环境会去 /run 建目录，测试之间互相干扰。
	sock := shortSockPath(t)
	t.Setenv("TRIUNE_CONTROL_SOCK", sock)

	l, path, err := api.OpenControlSocket(dir)
	if err != nil {
		t.Fatalf("open control socket: %v", err)
	}
	if path != sock {
		t.Fatalf("套接字路径应为 %s，得到 %s", sock, path)
	}
	t.Cleanup(func() { l.Close() })
	go func() { _ = a.ServeControl(l) }()

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			},
		},
	}
	return a, sock, client
}

// ctl 发一个控制套接字请求。
func ctl(t *testing.T, c *http.Client, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "http://unix"+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	data, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(data, &out)
	return resp.StatusCode, out
}

// TestControlSocketFileMode 套接字必须是 0600。
// 能连上就等于「以面板身份改配置」，这个文件不该有第二个读者。
func TestControlSocketFileMode(t *testing.T) {
	_, sock, _ := startControl(t)
	fi, err := os.Stat(sock)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("%s 不是套接字", sock)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("套接字权限应为 0600，得到 %o", perm)
	}
}

// TestControlWhitelist 白名单外的接口必须 404。
//
// 这条是安全边界：控制套接字只暴露 CLI 需要的那几个写接口。整套 API
// （账号管理、设置读写、面板自更新）绝不能从这里进得来——一旦 peer 校验
// 出岔子，后果从「改错一个入站」变成「整个面板被接管」。
func TestControlWhitelist(t *testing.T) {
	_, _, c := startControl(t)
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/settings"},
		{"PUT", "/api/settings"},
		{"GET", "/api/admins"},
		{"POST", "/api/admins"},
		{"GET", "/api/inbounds"}, // 读接口也不开放：CLI 直连 SQLite 就够了
		{"POST", "/api/self-update"},
		{"POST", "/api/panel/rollback"},
		{"GET", "/healthz"},
	} {
		code, _ := ctl(t, c, tc.method, tc.path, nil)
		if code != http.StatusNotFound {
			t.Errorf("%s %s 应当 404（不在白名单），得到 %d", tc.method, tc.path, code)
		}
	}
}

// TestControlWhitelistedRouteReachesHandler 白名单内的路由必须真的进到 handler。
//
// 用「不存在的入站 id」来证明：404 但带 error 字段，说明请求到了
// updateInbound，而不是被 mux 挡掉。
func TestControlWhitelistedRouteReachesHandler(t *testing.T) {
	_, _, c := startControl(t)
	code, out := ctl(t, c, "PUT", "/api/inbounds/999", map[string]any{"enabled": false})
	if code != http.StatusNotFound {
		t.Fatalf("期望 404，得到 %d", code)
	}
	if msg, _ := out["error"].(string); msg == "" {
		t.Fatalf("应当带 error 字段（说明进到了 handler），得到 %v", out)
	}
}

// TestControlApplyWorks apply 是唯一会重启内核的接口，必须真的能跑通。
func TestControlApplyWorks(t *testing.T) {
	a, _, c := startControl(t)
	if err := a.Store.CreateInbound(&model.Inbound{
		Name: "n1", Core: "singbox", Protocol: "vless", Port: 24443,
		Transport: "tcp", Enabled: true, Settings: "{}",
	}); err != nil {
		t.Fatal(err)
	}
	code, out := ctl(t, c, "POST", "/api/apply", map[string]any{"core": "singbox", "note": "test"})
	if code != http.StatusOK {
		t.Fatalf("apply 失败: %d %v", code, out)
	}
	if rev, _ := out["revision_id"].(float64); rev <= 0 {
		t.Fatalf("应当返回修订号，得到 %v", out["revision_id"])
	}
	// 配置文件必须真的落到磁盘上
	if _, err := os.Stat(filepath.Join(a.DataDir, "cores", "singbox", "config.json")); err != nil {
		t.Fatalf("配置没落盘: %v", err)
	}
}

// TestControlAuditUsesCLIActor 审计里必须能分清「命令行改的」和「面板上点的」。
func TestControlAuditUsesCLIActor(t *testing.T) {
	a, _, c := startControl(t)
	if err := a.Store.CreateInbound(&model.Inbound{
		Name: "n1", Core: "singbox", Protocol: "vless", Port: 24443,
		Transport: "tcp", Enabled: true, Settings: "{}",
	}); err != nil {
		t.Fatal(err)
	}
	if code, out := ctl(t, c, "PUT", "/api/inbounds/1", map[string]any{"enabled": false}); code != http.StatusOK {
		t.Fatalf("更新入站失败: %d %v", code, out)
	}

	entries, err := a.Store.ListAudit(store.AuditFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var actor string
	for _, e := range entries {
		if e.Action == api.AuditInboundUpdate {
			actor = e.Actor
			break
		}
	}
	if actor == "" {
		t.Fatal("没有找到 inbound.update 审计记录")
	}
	if !strings.HasPrefix(actor, "cli") {
		t.Fatalf("操作人应当是 cli 开头的合成身份，得到 %q", actor)
	}
	if e := entries[0]; e.IP != "127.0.0.1" {
		t.Errorf("套接字没有 IP，审计里应记回环，得到 %q", e.IP)
	}
}

// TestControlUserCrud 用户增删改经套接字走一遍。
func TestControlUserCrud(t *testing.T) {
	a, _, c := startControl(t)

	code, out := ctl(t, c, "POST", "/api/clients", map[string]any{
		"name": "u1", "quota_gb": 10, "inbound_ids": []int64{},
	})
	if code != http.StatusOK {
		t.Fatalf("建用户失败: %d %v", code, out)
	}
	id := int64(out["id"].(float64))
	if out["credential"] == "" || out["sub_token"] == "" {
		t.Fatalf("应当自动生成凭据与订阅令牌: %v", out)
	}

	if code, out := ctl(t, c, "PUT", fmt.Sprintf("/api/clients/%d", id),
		map[string]any{"quota_gb": 20}); code != http.StatusOK {
		t.Fatalf("改用户失败: %d %v", code, out)
	}
	got, err := a.Store.GetClient(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.QuotaGB != 20 {
		t.Errorf("配额应为 20，得到 %v", got.QuotaGB)
	}

	code, out = ctl(t, c, "POST", fmt.Sprintf("/api/clients/%d/reset-token", id), nil)
	if code != http.StatusOK {
		t.Fatalf("重置令牌失败: %d %v", code, out)
	}
	if out["sub_token"] == got.SubToken {
		t.Error("令牌应当变了")
	}

	if code, out := ctl(t, c, "DELETE", fmt.Sprintf("/api/clients/%d", id), nil); code != http.StatusOK {
		t.Fatalf("删用户失败: %d %v", code, out)
	}
	if _, err := a.Store.GetClient(id); err == nil {
		t.Error("用户应当已被删除")
	}
}
