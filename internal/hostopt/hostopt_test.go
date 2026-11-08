package hostopt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture 用 t.TempDir() 造一整套假的 /proc 与 /etc，所有可替换变量在
// t.Cleanup 里还原。刻意用真实临时文件（而不是内存 map）来跑 Apply/Revert，
// 这样 rename/remove/权限/幂等/回滚的语义都是真的。
//
// 注意：包级变量是共享状态，所有用例都不用 t.Parallel()。
type fixture struct {
	dir string

	goosVal          string
	root             bool
	cgroup           string
	cc               string
	avail            string
	qdisc            string
	version          string
	route            string
	moduleDir        bool
	tcFound          bool
	sysctlFound      bool
	tcOutput         string
	kubeEnv          bool
	dockerFile       bool
	containerEnvFile bool
	vzDir            bool

	// sysctl -p 的桩：cmdHook 模拟「值已被写进内核」，cmdErr 模拟生效失败。
	cmdHook func()
	cmdErr  error

	// 记录任何写动作，用于断言「confirm 错 / 不支持时绝不落盘」与「Detect 只读」。
	mutations int
}

func defaultFixture() *fixture {
	return &fixture{
		goosVal:     "linux",
		root:        true,
		cgroup:      "0::/\n",
		cc:          "bbr\n",
		avail:       "reno cubic bbr\n",
		qdisc:       "fq\n",
		version:     "Linux version 5.15.0-91-generic (buildd@lcy02) #101-Ubuntu SMP\n",
		route:       "Iface\tDestination\tGateway\tFlags\neth0\t00000000\t0100000A\t0003\n",
		moduleDir:   true,
		tcFound:     true,
		sysctlFound: true,
		tcOutput:    "qdisc fq 8001: dev eth0 root refcnt 2 limit 10000p buckets 1024\n",
	}
}

func (f *fixture) install(t *testing.T) {
	t.Helper()
	f.dir = t.TempDir()

	// 先保存全部包级变量，用例结束统一还原。
	oRead, oWrite, oStat, oMkdir, oRemove, oRename := readFile, writeFile, stat, mkdirAll, remove, rename
	oGeteuid, oLookPath, oLookupEnv, oRunCmd := geteuid, lookPath, lookupEnv, runCmd
	oGoos := goos
	oCfg, oSysctlDir, oProcSys := cfgPath, sysctlDir, procSys
	oVer, oRoute, oCgroup := procVersion, procRoute, procCgroup
	oModule, oVZ, oDocker, oContainerEnv := bbrModuleDir, procVZDir, dockerEnv, containerEnv
	t.Cleanup(func() {
		readFile, writeFile, stat, mkdirAll, remove, rename = oRead, oWrite, oStat, oMkdir, oRemove, oRename
		geteuid, lookPath, lookupEnv, runCmd = oGeteuid, oLookPath, oLookupEnv, oRunCmd
		goos = oGoos
		cfgPath, sysctlDir, procSys = oCfg, oSysctlDir, oProcSys
		procVersion, procRoute, procCgroup = oVer, oRoute, oCgroup
		bbrModuleDir, procVZDir, dockerEnv, containerEnv = oModule, oVZ, oDocker, oContainerEnv
	})

	// 所有路径指向临时目录，绝不碰真实 /proc / /etc。
	goos = f.goosVal
	procSys = filepath.Join(f.dir, "proc", "sys")
	sysctlDir = filepath.Join(f.dir, "etc", "sysctl.d")
	cfgPath = filepath.Join(sysctlDir, "99-triune-bbr.conf")
	procVersion = filepath.Join(f.dir, "proc", "version")
	procRoute = filepath.Join(f.dir, "proc", "net", "route")
	procCgroup = filepath.Join(f.dir, "proc", "1", "cgroup")
	bbrModuleDir = filepath.Join(f.dir, "sys", "module", "tcp_bbr")
	procVZDir = filepath.Join(f.dir, "proc", "vz")
	dockerEnv = filepath.Join(f.dir, ".dockerenv")
	containerEnv = filepath.Join(f.dir, ".containerenv")

	mustMkdir(t, filepath.Join(procSys, "net", "ipv4"))
	mustMkdir(t, filepath.Join(procSys, "net", "core"))
	mustMkdir(t, sysctlDir)
	mustMkdir(t, filepath.Dir(procVersion))
	mustMkdir(t, filepath.Dir(procRoute))
	mustMkdir(t, filepath.Dir(procCgroup))
	mustWrite(t, ccPath(), f.cc)
	mustWrite(t, availPath(), f.avail)
	mustWrite(t, qdiscPath(), f.qdisc)
	mustWrite(t, procVersion, f.version)
	mustWrite(t, procRoute, f.route)
	mustWrite(t, procCgroup, f.cgroup)
	if f.moduleDir {
		mustMkdir(t, bbrModuleDir)
	}
	if f.vzDir {
		mustMkdir(t, procVZDir)
	}
	if f.dockerFile {
		mustWrite(t, dockerEnv, "")
	}
	if f.containerEnvFile {
		mustWrite(t, containerEnv, "")
	}

	// 只读依赖保持真实实现；写依赖加计数，便于断言「零写入」。
	readFile, stat = os.ReadFile, os.Stat
	realWrite, realMkdir, realRemove, realRename := os.WriteFile, os.MkdirAll, os.Remove, os.Rename
	writeFile = func(p string, b []byte, m os.FileMode) error {
		f.mutations++
		return realWrite(p, b, m)
	}
	mkdirAll = func(p string, m os.FileMode) error {
		f.mutations++
		return realMkdir(p, m)
	}
	remove = func(p string) error {
		f.mutations++
		return realRemove(p)
	}
	rename = func(a, b string) error {
		f.mutations++
		return realRename(a, b)
	}

	geteuid = func() int {
		if f.root {
			return 0
		}
		return 1000
	}
	lookupEnv = func(k string) (string, bool) {
		if k == "KUBERNETES_SERVICE_HOST" && f.kubeEnv {
			return "10.96.0.1", true
		}
		return "", false
	}
	lookPath = func(name string) (string, error) {
		switch name {
		case "tc":
			if f.tcFound {
				return filepath.Join(f.dir, "bin", "tc"), nil
			}
		case "sysctl":
			if f.sysctlFound {
				return filepath.Join(f.dir, "bin", "sysctl"), nil
			}
		}
		return "", fmt.Errorf("%s: not found", name)
	}
	runCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "qdisc" {
			return []byte(f.tcOutput), nil
		}
		// sysctl -p：先模拟「配置已生效」，再按用例决定成功/失败。
		if f.cmdHook != nil {
			f.cmdHook()
		}
		return nil, f.cmdErr
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatalf("MkdirAll(%s): %v", p, err)
	}
}

func mustWrite(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", p, err)
	}
}

func readTrim(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", p, err)
	}
	return strings.TrimSpace(string(b))
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func checkByName(r Report, name string) (Check, bool) {
	for _, c := range r.Checks {
		if c.Name == name {
			return c, true
		}
	}
	return Check{}, false
}

func TestDetectLinuxNormal(t *testing.T) {
	f := defaultFixture()
	f.install(t)

	r := Detect(f.dir)
	if r.Platform != "linux" {
		t.Errorf("Platform=%q", r.Platform)
	}
	if r.Kernel != "5.15.0-91-generic" {
		t.Errorf("Kernel=%q", r.Kernel)
	}
	if r.CurrentCC != "bbr" || !containsFold(r.AvailableCC, "bbr") || !r.BBRModule {
		t.Errorf("BBR 相关字段异常: cc=%q avail=%v module=%v", r.CurrentCC, r.AvailableCC, r.BBRModule)
	}
	if r.DefaultQdisc != "fq" || !strings.Contains(r.QdiscDetail, "fq") {
		t.Errorf("qdisc 相关字段异常: default=%q detail=%q", r.DefaultQdisc, r.QdiscDetail)
	}
	if r.InContainer || !r.IsRoot {
		t.Errorf("环境判定异常: container=%v root=%v", r.InContainer, r.IsRoot)
	}
	if !r.ApplyEnabled || r.Reason != "" {
		t.Errorf("应放行应用: enabled=%v reason=%q", r.ApplyEnabled, r.Reason)
	}
	if len(r.Suggested) == 0 {
		t.Error("Suggested 应始终给出手动命令")
	}
	if r.Applied {
		t.Error("尚未应用时 Applied 应为 false")
	}
	if r.ConfigPath != cfgPath {
		t.Errorf("ConfigPath=%q", r.ConfigPath)
	}
	for _, name := range []string{
		"内核版本 ≥ 4.9", "BBR 可用", "当前拥塞控制已是 bbr",
		"默认 qdisc 为 fq/fq_codel", "tc 实测 qdisc",
		"当前不在容器里", "面板具备 root 权限",
	} {
		c, ok := checkByName(r, name)
		if !ok {
			t.Fatalf("缺少检查项 %q", name)
		}
		if !c.OK {
			t.Errorf("检查项 %q 应为 OK，detail=%s", name, c.Detail)
		}
	}
	// Detect 的硬约束：纯只读，连一个目录/文件都不许创建。
	if f.mutations != 0 {
		t.Fatalf("Detect 发生了 %d 次写动作", f.mutations)
	}
}

func TestDetectContainerDisablesApply(t *testing.T) {
	for _, tc := range []struct {
		name string
		tune func(*fixture)
	}{
		{"cgroup docker", func(f *fixture) { f.cgroup = "12:pids:/docker/9f1a2b3c\n" }},
		{"cgroup kubepods", func(f *fixture) { f.cgroup = "11:memory:/kubepods/burstable/pod123/abc\n" }},
		{"cgroup lxc", func(f *fixture) { f.cgroup = "5:cpuset:/lxc/guest\n" }},
		{"cgroup containerd", func(f *fixture) { f.cgroup = "0::/system.slice/containerd.service\n" }},
		{"dockerenv", func(f *fixture) { f.dockerFile = true }},
		{"containerenv", func(f *fixture) { f.containerEnvFile = true }},
		{"openvz", func(f *fixture) { f.vzDir = true }},
		{"kubernetes env", func(f *fixture) { f.kubeEnv = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := defaultFixture()
			tc.tune(f)
			f.install(t)
			r := Detect(f.dir)
			if !r.InContainer {
				t.Fatal("应识别为容器")
			}
			if r.ApplyEnabled {
				t.Fatal("容器内必须禁用应用按钮")
			}
			if !strings.Contains(r.Reason, "容器") {
				t.Errorf("Reason=%q 应含「容器」", r.Reason)
			}
			if c, ok := checkByName(r, "当前不在容器里"); !ok || c.OK {
				t.Errorf("容器检查项应存在且非 OK: %+v", c)
			}
		})
	}
}

func TestDetectWarnAndReasonCases(t *testing.T) {
	t.Run("非 root", func(t *testing.T) {
		f := defaultFixture()
		f.root = false
		f.install(t)
		r := Detect(f.dir)
		if r.IsRoot || r.ApplyEnabled {
			t.Fatalf("root=%v enabled=%v", r.IsRoot, r.ApplyEnabled)
		}
		if !strings.Contains(r.Reason, "root") {
			t.Errorf("Reason=%q 应含 root", r.Reason)
		}
		if c, ok := checkByName(r, "面板具备 root 权限"); !ok || c.OK {
			t.Errorf("root 检查项应为非 OK: %+v", c)
		}
	})

	t.Run("BBR 模块缺失", func(t *testing.T) {
		f := defaultFixture()
		f.avail = "reno cubic\n"
		f.moduleDir = false
		f.install(t)
		r := Detect(f.dir)
		if r.BBRModule || containsFold(r.AvailableCC, "bbr") {
			t.Fatalf("BBR 应判定为不可用: module=%v avail=%v", r.BBRModule, r.AvailableCC)
		}
		if r.ApplyEnabled {
			t.Fatal("BBR 不可用时应禁用应用")
		}
		if !strings.Contains(strings.ToLower(r.Reason), "bbr") {
			t.Errorf("Reason=%q 应含 bbr", r.Reason)
		}
		if c, ok := checkByName(r, "BBR 可用"); !ok || c.OK {
			t.Errorf("BBR 检查项应为非 OK: %+v", c)
		}
	})

	t.Run("tc 不存在", func(t *testing.T) {
		f := defaultFixture()
		f.tcFound = false
		f.install(t)
		r := Detect(f.dir)
		if !strings.Contains(r.QdiscDetail, "未安装 tc") {
			t.Fatalf("QdiscDetail=%q 应写明「未安装 tc」", r.QdiscDetail)
		}
		c, ok := checkByName(r, "tc 实测 qdisc")
		if !ok {
			t.Fatal("缺少 tc 检查项")
		}
		if !c.Warn || c.OK {
			t.Errorf("tc 缺失应 Warn=true / OK=false: %+v", c)
		}
		// tc 只是「无法确认」，不该连带禁用应用。
		if !r.ApplyEnabled {
			t.Error("tc 缺失不应禁用应用")
		}
	})

	t.Run("内核低于 4.9", func(t *testing.T) {
		f := defaultFixture()
		f.version = "Linux version 4.4.0-210-generic (gcc) #1 SMP\n"
		f.install(t)
		r := Detect(f.dir)
		c, ok := checkByName(r, "内核版本 ≥ 4.9")
		if !ok || c.OK || c.Warn {
			t.Errorf("低版本内核应 OK=false 且已知（非 Warn）: %+v", c)
		}
	})
}

func TestApplyRejectsWrongConfirmWithoutTouchingFiles(t *testing.T) {
	f := defaultFixture()
	f.install(t)

	for _, bad := range []string{"", "apply bbr", ApplyConfirm + " ", RevertConfirm} {
		if err := Apply(f.dir, bad); err != ErrConfirm {
			t.Fatalf("Apply(confirm=%q) = %v, 想要 ErrConfirm", bad, err)
		}
	}
	if err := Revert(f.dir, "REVERT"); err != ErrConfirm {
		t.Fatalf("Revert 错误确认 = %v, 想要 ErrConfirm", err)
	}
	if f.mutations != 0 {
		t.Fatalf("confirm 错误却发生了 %d 次写动作", f.mutations)
	}
	if exists(cfgPath) || exists(origPath(f.dir)) || exists(appliedPath(f.dir)) {
		t.Fatal("confirm 错误不得创建任何文件")
	}
}

func TestApplyUnsupportedWithoutTouchingFiles(t *testing.T) {
	for _, tc := range []struct {
		name string
		tune func(*fixture)
	}{
		{"容器", func(f *fixture) { f.cgroup = "0::/docker/abc\n" }},
		{"非 root", func(f *fixture) { f.root = false }},
		{"BBR 缺失", func(f *fixture) { f.avail = "reno cubic\n"; f.moduleDir = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := defaultFixture()
			tc.tune(f)
			f.install(t)
			err := Apply(f.dir, ApplyConfirm)
			if !errors.Is(err, ErrUnsupported) {
				t.Fatalf("err = %v, 想要 ErrUnsupported", err)
			}
			if err.Error() == ErrUnsupported.Error() {
				t.Error("错误应附带中文 Reason 信息")
			}
			if f.mutations != 0 {
				t.Fatalf("不支持时却发生了 %d 次写动作", f.mutations)
			}
			if exists(cfgPath) || exists(origPath(f.dir)) || exists(appliedPath(f.dir)) {
				t.Fatal("不支持时不得创建任何文件")
			}
		})
	}
}

func TestApplySuccess(t *testing.T) {
	f := defaultFixture()
	f.install(t)

	if err := Apply(f.dir, ApplyConfirm); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// 配置内容恰好两行，且权限 0644。
	content, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != bbrConfig {
		t.Fatalf("配置内容不符: %q", content)
	}
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) != 2 || lines[0] != "net.core.default_qdisc=fq" || lines[1] != "net.ipv4.tcp_congestion_control=bbr" {
		t.Fatalf("配置应恰好两行: %q", lines)
	}
	if fi, err := os.Stat(cfgPath); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("配置文件权限应为 0644: %v %v", fi, err)
	}

	// 备份 JSON 含原值，权限 0600。
	blob, err := os.ReadFile(origPath(f.dir))
	if err != nil {
		t.Fatal(err)
	}
	var o bbrOrig
	if err := json.Unmarshal(blob, &o); err != nil {
		t.Fatal(err)
	}
	if o.CurrentCC != "bbr" || o.DefaultQdisc != "fq" || o.ConfigExisted {
		t.Errorf("备份原值不符: %+v", o)
	}
	if fi, err := os.Stat(origPath(f.dir)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("备份文件权限应为 0600: %v %v", fi, err)
	}

	// applied 标记存在且 0600。
	if !exists(appliedPath(f.dir)) {
		t.Fatal("缺少 applied 标记")
	}
	if fi, err := os.Stat(appliedPath(f.dir)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("标记文件权限应为 0600: %v %v", fi, err)
	}
	if !Detect(f.dir).Applied {
		t.Error("应用后 Detect().Applied 应为 true")
	}

	// 幂等：再次 Apply 返回 nil，且不覆盖首次留下的备份。
	if err := Apply(f.dir, ApplyConfirm); err != nil {
		t.Fatalf("重复 Apply: %v", err)
	}
	blob2, err := os.ReadFile(origPath(f.dir))
	if err != nil {
		t.Fatal(err)
	}
	if string(blob2) != string(blob) {
		t.Fatal("重复应用覆盖了备份文件")
	}
}

func TestApplyActivateFailureRollsBack(t *testing.T) {
	f := defaultFixture()
	// 原值设成非 bbr，回滚是否真的写回原值才可被观测。
	f.cc = "cubic\n"
	f.qdisc = "pfifo_fast\n"
	f.cmdHook = func() {
		// 模拟 sysctl -p 已把新值写进内核、随后才报错的「半生效」场景。
		_ = os.WriteFile(ccPath(), []byte("bbr"), 0o644)
		_ = os.WriteFile(qdiscPath(), []byte("fq"), 0o644)
	}
	f.cmdErr = errors.New("sysctl: permission denied")
	f.install(t)

	err := Apply(f.dir, ApplyConfirm)
	if err == nil {
		t.Fatal("生效失败应返回 error")
	}
	if !strings.Contains(err.Error(), "回滚") {
		t.Errorf("错误应说明已回滚: %v", err)
	}
	if exists(cfgPath) {
		t.Error("失败后配置文件应被清除")
	}
	if exists(appliedPath(f.dir)) {
		t.Error("失败后 applied 标记应被清除")
	}
	if Detect(f.dir).Applied {
		t.Error("失败后 Applied 应为 false")
	}
	if got := readTrim(t, ccPath()); got != "cubic" {
		t.Errorf("tcp_congestion_control 未恢复原值: %q", got)
	}
	if got := readTrim(t, qdiscPath()); got != "pfifo_fast" {
		t.Errorf("default_qdisc 未恢复原值: %q", got)
	}
	if !exists(origPath(f.dir)) {
		t.Error("orig 备份应保留供审计")
	}
}

// 回滚自己也可能失败（/proc/sys 变只读、配置文件被占用）。此时若还对外说
// 「已整体回滚」，用户就不会再去核对系统状态——必须明确报「回滚不完整」。
func TestApplyReportsIncompleteRollback(t *testing.T) {
	f := defaultFixture()
	f.cc = "cubic\n"
	f.qdisc = "pfifo_fast\n"
	f.cmdErr = errors.New("sysctl: permission denied")
	f.install(t)

	origRemove := remove
	remove = func(p string) error {
		if p == cfgPath {
			return errors.New("read-only file system")
		}
		return origRemove(p)
	}

	err := Apply(f.dir, ApplyConfirm)
	if err == nil {
		t.Fatal("生效失败应返回 error")
	}
	if !strings.Contains(err.Error(), "回滚不完整") {
		t.Errorf("回滚失败时必须明说，不能宣称已整体回滚: %v", err)
	}
	if !strings.Contains(err.Error(), "删除配置") {
		t.Errorf("应指出是哪一步没回滚成功: %v", err)
	}
}

func TestRevertRestoresAndKeepsAudit(t *testing.T) {
	f := defaultFixture()
	f.cc = "cubic\n"
	f.qdisc = "pfifo_fast\n"
	f.cmdHook = func() {
		_ = os.WriteFile(ccPath(), []byte("bbr"), 0o644)
		_ = os.WriteFile(qdiscPath(), []byte("fq"), 0o644)
	}
	f.install(t)

	if err := Apply(f.dir, ApplyConfirm); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !exists(cfgPath) || !exists(appliedPath(f.dir)) {
		t.Fatal("前置条件：应已写入配置与标记")
	}

	if err := Revert(f.dir, RevertConfirm); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if exists(cfgPath) {
		t.Error("Revert 后配置文件应删除")
	}
	if exists(appliedPath(f.dir)) {
		t.Error("Revert 后标记应删除")
	}
	if !exists(origPath(f.dir)) {
		t.Error("orig 备份应保留供审计")
	}
	if got := readTrim(t, ccPath()); got != "cubic" {
		t.Errorf("tcp_congestion_control 未恢复: %q", got)
	}
	if got := readTrim(t, qdiscPath()); got != "pfifo_fast" {
		t.Errorf("default_qdisc 未恢复: %q", got)
	}
	if Detect(f.dir).Applied {
		t.Error("Revert 后 Applied 应为 false")
	}

	// Revert 的确认串同样必须精确，且错误时不得有任何动作。
	before := f.mutations
	if err := Revert(f.dir, "REVERT"); err != ErrConfirm {
		t.Fatalf("Revert 错误确认 = %v", err)
	}
	if f.mutations != before {
		t.Error("confirm 错误不得触发任何写动作")
	}
}

func TestRevertWithoutBackupRefusesToDeleteConfig(t *testing.T) {
	f := defaultFixture()
	f.install(t)
	// 造出「看起来已应用」的状态，但故意不写 orig 备份。
	mustWrite(t, cfgPath, "net.ipv4.tcp_congestion_control=bbr\n")
	mustWrite(t, appliedPath(f.dir), "applied\n")

	err := Revert(f.dir, RevertConfirm)
	if err == nil {
		t.Fatal("无备份时应报明确错误")
	}
	if !strings.Contains(err.Error(), "缺少备份") {
		t.Errorf("错误信息应点明缺少备份: %v", err)
	}
	if !exists(cfgPath) {
		t.Fatal("无备份时绝不能删除配置文件")
	}
	if !exists(appliedPath(f.dir)) {
		t.Fatal("无备份时绝不能删除标记")
	}
}

func TestNonLinuxFallback(t *testing.T) {
	// build tag 是编译期决定的，改 goos 并不能切换编译进来的文件；
	// 因此这里直接验证兜底函数 platformUnsupported() 与 !linux 的兜底报告。
	for _, fake := range []string{"plan9", "darwin"} {
		t.Run(fake, func(t *testing.T) {
			f := defaultFixture()
			f.goosVal = fake
			f.install(t)

			if !platformUnsupported() {
				t.Fatal("platformUnsupported() 应为 true")
			}
			r := Detect(f.dir)
			if r.Platform != fake {
				t.Errorf("Platform=%q, 想要 %q", r.Platform, fake)
			}
			if r.ApplyEnabled {
				t.Error("非 Linux 不得放行应用")
			}
			if !strings.Contains(r.Reason, "Linux") {
				t.Errorf("Reason=%q", r.Reason)
			}
			if len(r.Checks) == 0 || !r.Checks[0].Warn {
				t.Errorf("兜底报告应含一条 Warn 说明: %+v", r.Checks)
			}
			if r.CurrentCC != "" || r.Kernel != "" {
				t.Error("非 Linux 分支不应读取 /proc")
			}
			if err := Apply(f.dir, ApplyConfirm); err != ErrUnsupported {
				t.Fatalf("Apply = %v, 想要 ErrUnsupported", err)
			}
			if err := Revert(f.dir, RevertConfirm); err != ErrUnsupported {
				t.Fatalf("Revert = %v, 想要 ErrUnsupported", err)
			}
			if f.mutations != 0 {
				t.Fatalf("非 Linux 分支发生了 %d 次写动作", f.mutations)
			}
		})
	}
}

// 变异自检（写在代码里方便对照最终报告）：
//   - 若把 Apply 开头的「confirm != ApplyConfirm」校验删掉，
//     TestApplyRejectsWrongConfirmWithoutTouchingFiles 会红：错误确认串也会一路
//     走到写备份/写配置，mutations 不再为 0，且文件被创建。
//   - 若把 activate 失败后的 rollback(...) 调用删掉，
//     TestApplyActivateFailureRollsBack 会红：cmdHook 已把 /proc/sys 改成 bbr/fq，
//     配置文件与 applied 标记也会残留，Applied 仍为 true。
