package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// 面板自更新「更新完了还是旧版本」这条路上的两个静默坑。
//
// 症状：点了更新，提示「更新完成，面板重启中」，刷新后版本号没变。
// 面板显示的版本号来自**正在运行的进程**，所以这句话的真实含义是
// 「新二进制已经落盘，但跑着的还是老进程」——而它有两种成因：
//
//  1. 替换那一步就失败了：新二进制被下载到**数据目录**，再 rename 到安装目录。
//     两者不在同一个文件系统时（容器挂载卷、独立 /var 分区、tmpfs）rename
//     返回 EXDEV，整个升级原地不动。data 目录默认在 <安装目录>/data，所以
//     标准安装碰不到；Docker 与自定义数据目录一碰一个准。
//  2. 替换成功了、重启那一步失败：`syscall.Exec` 的返回值以前被直接丢掉。
//     exec 失败时进程继续跑旧映像，面板一切正常，只有版本号不动。
//
// 这里钉住三件事：替换必须跨设备可用、exec 失败必须留痕、接口必须能看出
// 「磁盘版本 ≠ 运行版本」。

// TestStageReplaceCrossDevice 新二进制在另一个文件系统上时也要能替换成功。
//
// 正是 EXDEV 的现场：老实现用 os.Rename 把数据目录里的新二进制直接搬到安装目录，
// 两者不同分区时必然失败。默认拿 /dev/shm（Linux 上是 tmpfs，与工作区不同分区）；
// macOS 没有 /dev/shm，可用 TRIUNE_XDEV_DIR 指一个别的卷（如挂载的 dmg）来跑。
func TestStageReplaceCrossDevice(t *testing.T) {
	shm := os.Getenv("TRIUNE_XDEV_DIR")
	if shm == "" {
		shm = "/dev/shm"
	}
	if st, err := os.Stat(shm); err != nil || !st.IsDir() {
		if runtime.GOOS == "linux" {
			// Linux 一定有 /dev/shm。真找不到就说明这条用例没跑成，
			// 而它是 EXDEV 唯一的回归保护——静默跳过等于没有。
			t.Fatalf("Linux 上找不到 %s，跨设备用例没有真正执行", shm)
		}
		t.Skipf("没有可用的第二文件系统（%s），跳过跨设备用例；Linux CI 会跑", shm)
	}
	src := filepath.Join(shm, fmt.Sprintf("tp-stage-%d", time.Now().UnixNano()))
	if err := os.WriteFile(src, []byte("new-binary"), 0o755); err != nil {
		t.Skipf("%s 不可写: %v", shm, err)
	}
	defer os.Remove(src)

	dir := t.TempDir()
	exe := filepath.Join(dir, "triune-panel")
	if err := os.WriteFile(exe, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := stageReplace(src, exe); err != nil {
		t.Fatalf("跨文件系统替换失败（就是 EXDEV 那个坑）: %v", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "new-binary" {
		t.Errorf("目标文件没被换成新内容，实际 %q", got)
	}
	// 快速回滚用的 .bak 必须留着旧内容，否则回滚无从下手
	if got, _ := os.ReadFile(exe + ".bak"); string(got) != "old-binary" {
		t.Errorf(".bak 应是旧二进制，实际 %q", got)
	}
	// 暂存文件不能留在安装目录里
	if _, err := os.Stat(exe + ".update-new"); !os.IsNotExist(err) {
		t.Error("暂存文件 .update-new 应被清掉")
	}
}

// TestRestartSelfRecordsExecFailure exec 失败必须留下可查的痕迹。
//
// 以前这个错误被 goroutine 直接丢掉：进程继续以旧映像跑，面板对外毫无异常，
// 用户只能自己去 readlink /proc/<pid>/exe 看有没有 " (deleted)"。
func TestRestartSelfRecordsExecFailure(t *testing.T) {
	a := newAuthTestServer(t)
	oldExec, oldWait := execSelf, execSelfWait
	defer func() { execSelf, execSelfWait = oldExec, oldWait }()
	execSelfWait = time.Millisecond

	execSelf = func(string, []string, []string) error {
		return errors.New("operation not permitted")
	}
	a.restartSelf("/nonexistent/triune-panel", "更新到 v1.0.9")

	got, _ := a.Store.GetSetting(SettingRestartFailed)
	if !strings.Contains(got, "v1.0.9") || !strings.Contains(got, "operation not permitted") {
		t.Fatalf("失败说明应包含目标版本与原因，实际 %q", got)
	}
	if !strings.Contains(got, Version) {
		t.Errorf("失败说明应指出当前仍在运行的版本 %q，实际 %q", Version, got)
	}

	// exec 成功（真实环境里进程映像已被替换，不会走到这里）不该写标记
	_ = a.Store.SetSetting(SettingRestartFailed, "")
	execSelf = func(string, []string, []string) error { return nil }
	a.restartSelf("/nonexistent/triune-panel", "更新到 v1.0.9")
	if got, _ := a.Store.GetSetting(SettingRestartFailed); got != "" {
		t.Errorf("exec 成功时不应留标记，实际 %q", got)
	}
}

// TestVersionExposesDiskAndRestartState /api/version 要能看出
// 「磁盘上的二进制」和「正在运行的进程」是不是同一份。
//
// 界面上的版本号只反映进程。没有这两个字段时，用户面对「更新完了还是旧版本」
// 只能靠猜；有了它们，页面能直接把「磁盘 v1.0.9 / 进程 v1.0.8」摆出来。
func TestVersionExposesDiskAndRestartState(t *testing.T) {
	a := newAuthTestServer(t)
	tok := loginForToken(t, a)

	w := apiReq(t, a.Handler(), tok, "GET", "/api/version", "")
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["binary_version"]; !ok {
		t.Error("缺少 binary_version：界面无法判断「二进制换了但进程没换」")
	}
	if got["version"] != Version {
		t.Errorf("version 应为运行中版本 %q，实际 %q", Version, got["version"])
	}

	// 重启失败说明要原样透出
	_ = a.Store.SetSetting(SettingRestartFailed, "二进制已更新到 v1.0.9，但重启失败")
	w = apiReq(t, a.Handler(), tok, "GET", "/api/version", "")
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got["restart_failed"], "重启失败") {
		t.Errorf("restart_failed 应透出失败说明，实际 %q", got["restart_failed"])
	}
}
