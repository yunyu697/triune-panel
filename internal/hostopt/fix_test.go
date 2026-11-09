package hostopt

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// 体检项的一键修复必须同时满足四件事，任何一条缺失都会变成「点了不知道发生了
// 什么」的工具：
//  1. 只对确实失败、且面板真能安全代劳的项挂按钮（挂上去就得管用）；
//  2. 写之前先把原值记下来，回退按记录走；
//  3. 写完回读复检，不符就恢复原状；
//  4. 记录写不进去时宁可撤销这次修复——「改过但没法还原」比不改更危险。

func fixAction(t *testing.T, r Report, key string) FixAction {
	t.Helper()
	for _, a := range r.Fixes {
		if a.Key == key {
			return a
		}
	}
	t.Fatalf("报告里没有修复项 %q，实际 %+v", key, r.Fixes)
	return FixAction{}
}

// 体检列表本身也要能安全渲染：三项动作齐全、都带文案，且 Detect 依然一个字节都不写。
func TestFixActionsAreListedAndReadOnly(t *testing.T) {
	f := defaultFixture()
	f.install(t)

	r := Detect(f.dir)
	if len(r.Fixes) != 3 {
		t.Fatalf("应有 3 项修复动作，实际 %d：%+v", len(r.Fixes), r.Fixes)
	}
	for _, a := range r.Fixes {
		if a.Key == "" || a.Check == "" || a.Label == "" || a.Revert == "" || a.Note == "" {
			t.Errorf("修复项字段不全：%+v", a)
		}
		// 健康主机上三项都「无事可做」：要么可执行，要么给出不执行的原因，
		// 但不许两者皆空（那会让前端渲染一个点不动的按钮）。
		if !a.Ready && a.Blocked == "" {
			t.Errorf("%s 既不可执行又没有原因", a.Key)
		}
		if a.Applied {
			t.Errorf("未修复过时 %s 不该显示 applied", a.Key)
		}
	}
	if f.mutations != 0 {
		t.Fatalf("Detect 发生了 %d 次写动作", f.mutations)
	}
	// 健康的 fd 不该挂修复按钮：不失败就没有可修的东西。
	if c, ok := checkByName(r, "文件描述符上限"); !ok || c.Fix != "" {
		t.Errorf("健康的文件描述符项不应带 fix，实际 %+v", c)
	}
}

// fd：低 soft 上限 → 修复（进程内提上限）→ 体检转 OK → 回退到原值。
func TestFixFileDescriptorAppliesAndReverts(t *testing.T) {
	f := defaultFixture()
	f.limits = "Limit                     Soft Limit           Hard Limit           Units\n" +
		"Max open files            1024                 1048576              files\n"
	f.nofileCur, f.nofileMax = 1024, 1048576
	f.install(t)

	r := Detect(f.dir)
	c, ok := checkByName(r, "文件描述符上限")
	if !ok || c.OK || c.Fix != fixKeyFD {
		t.Fatalf("低 soft 上限应失败并挂修复：%+v", c)
	}
	if a := fixAction(t, r, fixKeyFD); !a.Ready {
		t.Fatalf("应可修复，却 blocked=%q", a.Blocked)
	}

	if err := ApplyFix(f.dir, FixConfirm, fixKeyFD); err != nil {
		t.Fatalf("ApplyFix: %v", err)
	}
	if f.nofileCur != fdWarnThreshold {
		t.Errorf("soft 应提到 %d，实际 %d", fdWarnThreshold, f.nofileCur)
	}
	r = Detect(f.dir)
	if c, _ := checkByName(r, "文件描述符上限"); !c.OK {
		t.Errorf("修复后体检项应转 OK，detail=%s", c.Detail)
	}
	a := fixAction(t, r, fixKeyFD)
	if !a.Applied || a.At == "" || !strings.Contains(a.Changes, "1024") {
		t.Errorf("修复记录应可读：%+v", a)
	}
	if !exists(fixRecordPath(f.dir)) {
		t.Error("修复记录文件没落盘，回退将无据可依")
	}

	if err := RevertFix(f.dir, FixRevertConfirm, fixKeyFD); err != nil {
		t.Fatalf("RevertFix: %v", err)
	}
	if f.nofileCur != 1024 {
		t.Errorf("回退后 soft 应回到 1024，实际 %d", f.nofileCur)
	}
	if exists(fixRecordPath(f.dir)) {
		t.Error("回退后记录应删除（没有可回退的东西了）")
	}
	if a := fixAction(t, Detect(f.dir), fixKeyFD); a.Applied {
		t.Error("回退后不该再显示已修复")
	}
}

// hard 上限低于建议值时进程内提不上去：必须明确说「改 systemd 单元」，
// 而且一次写动作都不能发生。
func TestFixFileDescriptorBlockedByHardLimit(t *testing.T) {
	f := defaultFixture()
	f.limits = "Limit                     Soft Limit           Hard Limit           Units\n" +
		"Max open files            1024                 1024                 files\n"
	f.nofileCur, f.nofileMax = 1024, 1024
	f.install(t)

	a := fixAction(t, Detect(f.dir), fixKeyFD)
	if a.Ready {
		t.Fatal("hard 上限不足时不该显示可修复")
	}
	if !strings.Contains(a.Blocked, "LimitNOFILE") {
		t.Errorf("不可用原因应指出改 systemd 单元，实际 %q", a.Blocked)
	}
	before := f.mutations
	if err := ApplyFix(f.dir, FixConfirm, fixKeyFD); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("应返回 ErrUnsupported，实际 %v", err)
	}
	if f.mutations != before {
		t.Errorf("不可用时发生了 %d 次写动作", f.mutations-before)
	}
}

// 时钟：开关被关掉 → 打开 → 再关回原样。
func TestFixClockAppliesAndReverts(t *testing.T) {
	f := defaultFixture()
	f.timedatectlFound = true
	f.ntpState = "no"
	f.clockOut = "no\nAsia/Shanghai\n"
	f.install(t)

	r := Detect(f.dir)
	c, _ := checkByName(r, "时钟同步（NTP）")
	if c.OK || c.Fix != fixKeyClock {
		t.Fatalf("未同步应失败并挂修复：%+v", c)
	}
	if a := fixAction(t, r, fixKeyClock); !a.Ready {
		t.Fatalf("应可修复，却 blocked=%q", a.Blocked)
	}

	if err := ApplyFix(f.dir, FixConfirm, fixKeyClock); err != nil {
		t.Fatalf("ApplyFix: %v", err)
	}
	if f.ntpState != "true" {
		t.Errorf("应执行 timedatectl set-ntp true，实际 ntpState=%q", f.ntpState)
	}
	if a := fixAction(t, Detect(f.dir), fixKeyClock); !a.Applied || !strings.Contains(a.Changes, "no → yes") {
		t.Errorf("修复记录应写明开关变化：%+v", a)
	}

	if err := RevertFix(f.dir, FixRevertConfirm, fixKeyClock); err != nil {
		t.Fatalf("RevertFix: %v", err)
	}
	if f.ntpState != "false" {
		t.Errorf("回退应把开关拨回 false，实际 %q", f.ntpState)
	}
}

// 复检不通过必须把开关拨回去，而不是留一个「点了没生效」的状态。
func TestFixClockRollsBackWhenToggleDoesNotStick(t *testing.T) {
	f := defaultFixture()
	f.timedatectlFound = true
	f.ntpState = "no"
	f.clockOut = "no\nAsia/Shanghai\n"
	f.install(t)

	// 模拟「set-ntp 报成功但开关没变」——systemd 在某些容器里就是这样。
	realRun := runCmd
	runCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		f.cmds = append(f.cmds, strings.Join(args, " "))
		if len(args) > 0 && args[0] == "show" {
			for _, a := range args {
				if a == "NTP" {
					return []byte("no\n"), nil // 永远是 no
				}
			}
			return []byte(f.clockOut), nil
		}
		return nil, nil
	}
	t.Cleanup(func() { runCmd = realRun })

	err := ApplyFix(f.dir, FixConfirm, fixKeyClock)
	if err == nil || !strings.Contains(err.Error(), "复检未通过") {
		t.Fatalf("应因复检失败而报错，实际 %v", err)
	}
	joined := strings.Join(f.cmds, " | ")
	if !strings.Contains(joined, "set-ntp true") || !strings.Contains(joined, "set-ntp false") {
		t.Errorf("应先尝试打开再拨回 false，实际命令序列 %s", joined)
	}
	if exists(fixRecordPath(f.dir)) {
		t.Error("失败时不该留下修复记录")
	}
}

// conntrack：满表 → 翻倍写入配置文件并生效 → 回退恢复原值并删除文件。
func TestFixConntrackAppliesAndReverts(t *testing.T) {
	f := defaultFixture()
	f.ctMax = "65536\n"
	f.ctCount = "60000\n" // 91%，超过 80% 告警线
	f.install(t)

	r := Detect(f.dir)
	c, _ := checkByName(r, "conntrack 连接跟踪水位")
	if c.OK || c.Fix != fixKeyConntrack {
		t.Fatalf("满表应失败并挂修复：%+v", c)
	}

	if err := ApplyFix(f.dir, FixConfirm, fixKeyConntrack); err != nil {
		t.Fatalf("ApplyFix: %v", err)
	}
	if got := readTrim(t, procCtMax); got != "131072" {
		t.Errorf("nf_conntrack_max 应为 131072，实际 %s", got)
	}
	if !exists(conntrackConfPath) {
		t.Error("应写出 conntrack 配置文件（重启后仍生效）")
	}
	a := fixAction(t, Detect(f.dir), fixKeyConntrack)
	if !a.Applied || !strings.Contains(a.Changes, "65536 → 131072") {
		t.Errorf("修复记录应写明上限变化：%+v", a)
	}

	if err := RevertFix(f.dir, FixRevertConfirm, fixKeyConntrack); err != nil {
		t.Fatalf("RevertFix: %v", err)
	}
	if got := readTrim(t, procCtMax); got != "65536" {
		t.Errorf("回退后应恢复 65536，实际 %s", got)
	}
	if exists(conntrackConfPath) {
		t.Error("回退后应删除面板写入的配置文件")
	}
}

// 生效失败要「失败零改动」：配置文件删掉、运行值不动、不留记录。
func TestFixConntrackRestoresOnActivateFailure(t *testing.T) {
	f := defaultFixture()
	f.ctMax = "65536\n"
	f.ctCount = "60000\n"
	f.install(t)
	// 让桩「执行失败且不生效」：真实 sysctl 失败时不会把新值写进内核。
	f.cmdHook = func() {}
	f.cmdErr = errors.New("sysctl: permission denied")

	err := ApplyFix(f.dir, FixConfirm, fixKeyConntrack)
	if err == nil || !strings.Contains(err.Error(), "已删除写入的配置文件") {
		t.Fatalf("应报错并说明已清理配置，实际 %v", err)
	}
	if exists(conntrackConfPath) {
		t.Error("生效失败后配置文件必须删除")
	}
	if got := readTrim(t, procCtMax); got != "65536" {
		t.Errorf("运行值不该被改动，实际 %s", got)
	}
	if exists(fixRecordPath(f.dir)) {
		t.Error("失败时不该留下修复记录")
	}
}

// 已有同名配置文件时要原样还原，不能粗暴删除用户自己的配置。
func TestFixConntrackRestoresExistingConfig(t *testing.T) {
	f := defaultFixture()
	f.ctMax = "65536\n"
	f.ctCount = "60000\n"
	f.install(t)
	orig := "# 用户自己的配置\nnet.netfilter.nf_conntrack_max=65536\n"
	mustWrite(t, conntrackConfPath, orig)

	if err := ApplyFix(f.dir, FixConfirm, fixKeyConntrack); err != nil {
		t.Fatalf("ApplyFix: %v", err)
	}
	if err := RevertFix(f.dir, FixRevertConfirm, fixKeyConntrack); err != nil {
		t.Fatalf("RevertFix: %v", err)
	}
	if got := readTrim(t, conntrackConfPath); got != strings.TrimSpace(orig) {
		t.Errorf("原配置文件应逐字还原，实际 %q", got)
	}
}

// 确认串、未知键、无记录回退：三条都不许碰系统。
func TestFixGuardsRequireConfirmAndRecord(t *testing.T) {
	f := defaultFixture()
	f.ctMax = "65536\n"
	f.ctCount = "60000\n"
	f.install(t)
	before := f.mutations

	// 两个动作的确认串互不通用：拿 APPLY FIX 去回退、拿 REVERT FIX 去修复都必须被拒。
	for _, c := range []string{"", "APPLY NET", "apply fix", "REVERT FIX", "APPLY BBR"} {
		if err := ApplyFix(f.dir, c, fixKeyConntrack); !errors.Is(err, ErrConfirm) {
			t.Errorf("ApplyFix(%q) 应拒绝，实际 %v", c, err)
		}
	}
	for _, c := range []string{"", "REVERT NET", "revert fix", "APPLY FIX", "REVERT BBR"} {
		if err := RevertFix(f.dir, c, fixKeyConntrack); !errors.Is(err, ErrConfirm) {
			t.Errorf("RevertFix(%q) 应拒绝，实际 %v", c, err)
		}
	}
	if f.mutations != before {
		t.Errorf("确认串不对时发生了 %d 次写动作", f.mutations-before)
	}

	if err := ApplyFix(f.dir, FixConfirm, "no-such-fix"); err == nil {
		t.Error("未知修复项应报错")
	}
	err := RevertFix(f.dir, FixRevertConfirm, fixKeyConntrack)
	if err == nil || !strings.Contains(err.Error(), "拒绝回退") {
		t.Errorf("没有记录时必须拒绝回退，实际 %v", err)
	}
	if f.mutations != before {
		t.Errorf("拒绝路径发生了 %d 次写动作", f.mutations-before)
	}
}

// 记录写不进去 → 立刻撤销这次修复，别留下「改了但说不清改了什么」的状态。
func TestFixRollsBackWhenRecordCannotBeSaved(t *testing.T) {
	f := defaultFixture()
	f.limits = "Limit                     Soft Limit           Hard Limit           Units\n" +
		"Max open files            1024                 1048576              files\n"
	f.nofileCur, f.nofileMax = 1024, 1048576
	f.install(t)

	realWrite := writeFile
	writeFile = func(p string, b []byte, m os.FileMode) error {
		if strings.Contains(p, fixFileName) {
			return errors.New("disk full")
		}
		f.mutations++
		return os.WriteFile(p, b, m)
	}
	t.Cleanup(func() { writeFile = realWrite })

	err := ApplyFix(f.dir, FixConfirm, fixKeyFD)
	if err == nil || !strings.Contains(err.Error(), "已撤销本次改动") {
		t.Fatalf("应报错并说明已撤销，实际 %v", err)
	}
	if f.nofileCur != 1024 {
		t.Errorf("记录失败后 rlimit 应恢复 1024，实际 %d", f.nofileCur)
	}
}

// 体检项上的 fix 标识必须都能在 fixes 列表里找到，否则前端会渲染一个点不动的按钮。
func TestCheckFixKeysResolveToActions(t *testing.T) {
	f := defaultFixture()
	f.timedatectlFound = true
	f.ntpState = "no"
	f.clockOut = "no\nAsia/Shanghai\n"
	f.limits = "Limit                     Soft Limit           Hard Limit           Units\n" +
		"Max open files            1024                 1048576              files\n"
	f.nofileCur, f.nofileMax = 1024, 1048576
	f.ctMax = "65536\n"
	f.ctCount = "60000\n"
	f.install(t)

	r := Detect(f.dir)
	known := map[string]bool{}
	for _, a := range r.Fixes {
		known[a.Key] = true
	}
	var withFix int
	for _, c := range r.Checks {
		if c.Fix == "" {
			continue
		}
		withFix++
		if !known[c.Fix] {
			t.Errorf("体检项 %q 指向不存在的修复项 %q", c.Name, c.Fix)
		}
		if c.OK {
			t.Errorf("体检项 %q 已 OK 却仍挂着修复按钮", c.Name)
		}
	}
	if withFix != 3 {
		t.Errorf("三个病态项都应挂修复，实际 %d 项", withFix)
	}
}
