package hostopt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---- 时钟同步 ----

func TestClockCheck(t *testing.T) {
	t.Run("已同步", func(t *testing.T) {
		f := defaultFixture()
		f.timedatectlFound = true
		f.clockOut = "yes\nAsia/Shanghai\n"
		f.install(t)
		c, ok := checkByName(Detect(f.dir), "时钟同步（NTP）")
		if !ok {
			t.Fatal("缺少时钟检查项")
		}
		if !c.OK || c.Warn {
			t.Errorf("已同步应 OK: %+v", c)
		}
		if !strings.Contains(c.Detail, "Asia/Shanghai") {
			t.Errorf("detail 应带时区: %q", c.Detail)
		}
	})

	t.Run("未同步", func(t *testing.T) {
		f := defaultFixture()
		f.timedatectlFound = true
		f.clockOut = "no\nUTC\n"
		f.install(t)
		c, _ := checkByName(Detect(f.dir), "时钟同步（NTP）")
		if c.OK || c.Warn {
			t.Errorf("未同步应 OK=false 且不是 Warn: %+v", c)
		}
		if !strings.Contains(c.Detail, "REALITY") {
			t.Errorf("detail 应说明影响: %q", c.Detail)
		}
	})

	t.Run("没有 timedatectl", func(t *testing.T) {
		f := defaultFixture()
		f.install(t)
		c, _ := checkByName(Detect(f.dir), "时钟同步（NTP）")
		if !c.Warn || c.OK {
			t.Errorf("无法确认时应 Warn: %+v", c)
		}
	})
}

// ---- 文件描述符 ----

func TestFDLimitCheck(t *testing.T) {
	t.Run("充足", func(t *testing.T) {
		f := defaultFixture()
		f.install(t)
		c, ok := checkByName(Detect(f.dir), "文件描述符上限")
		if !ok || !c.OK {
			t.Fatalf("上限充足应 OK: %+v", c)
		}
	})

	t.Run("soft 过低", func(t *testing.T) {
		f := defaultFixture()
		f.limits = "Limit                     Soft Limit           Hard Limit           Units\n" +
			"Max open files            1024                 1048576              files\n"
		f.install(t)
		c, _ := checkByName(Detect(f.dir), "文件描述符上限")
		if c.OK {
			t.Errorf("soft=1024 应判不达标: %+v", c)
		}
		if !strings.Contains(c.Detail, "LimitNOFILE") {
			t.Errorf("detail 应给出解决方向: %q", c.Detail)
		}
	})

	t.Run("系统已用超过八成", func(t *testing.T) {
		f := defaultFixture()
		f.fileMax = "1000\n"
		f.fileNr = "900\t0\t1000\n"
		f.install(t)
		c, _ := checkByName(Detect(f.dir), "文件描述符上限")
		if c.OK {
			t.Errorf("已用 90%% 应判不达标: %+v", c)
		}
	})
}

// ---- conntrack ----

func TestConntrackCheck(t *testing.T) {
	t.Run("正常水位", func(t *testing.T) {
		f := defaultFixture()
		f.install(t)
		c, ok := checkByName(Detect(f.dir), "conntrack 连接跟踪水位")
		if !ok || !c.OK {
			t.Fatalf("低水位应 OK: %+v", c)
		}
	})

	t.Run("接近满表", func(t *testing.T) {
		f := defaultFixture()
		f.ctMax = "1000\n"
		f.ctCount = "900\n"
		f.install(t)
		c, _ := checkByName(Detect(f.dir), "conntrack 连接跟踪水位")
		if c.OK {
			t.Errorf("90%% 应判不达标: %+v", c)
		}
	})

	t.Run("未加载模块", func(t *testing.T) {
		f := defaultFixture()
		f.ctMax = ""
		f.install(t)
		c, _ := checkByName(Detect(f.dir), "conntrack 连接跟踪水位")
		if !c.Warn || c.OK {
			t.Errorf("模块未加载应 Warn: %+v", c)
		}
	})
}

// ---- sysctl 覆盖 ----

func TestSysctlConflictCheck(t *testing.T) {
	t.Run("更靠后的文件会覆盖", func(t *testing.T) {
		f := defaultFixture()
		f.install(t)
		mustWrite(t, filepath.Join(sysctlDir, "99-zz-cloud.conf"), "net.core.somaxconn = 16384\n")
		c, ok := checkByName(Detect(f.dir), "sysctl 配置覆盖检查")
		if !ok {
			t.Fatal("缺少覆盖检查项")
		}
		if c.OK {
			t.Errorf("99-zz 在面板之后加载，应判为覆盖: %+v", c)
		}
		if !strings.Contains(c.Detail, "99-zz-cloud.conf") {
			t.Errorf("detail 应点名文件: %q", c.Detail)
		}
	})

	t.Run("更靠前的文件不算覆盖", func(t *testing.T) {
		f := defaultFixture()
		f.install(t)
		mustWrite(t, filepath.Join(sysctlDir, "50-default.conf"), "net.core.somaxconn = 16384\n")
		c, _ := checkByName(Detect(f.dir), "sysctl 配置覆盖检查")
		if !c.OK {
			t.Errorf("50- 先于面板加载，不该判为覆盖: %+v", c)
		}
	})

	t.Run("/etc/sysctl.conf 最后生效", func(t *testing.T) {
		f := defaultFixture()
		f.install(t)
		mustWrite(t, etcSysctlConf, "# 云厂商默认\nnet.ipv4.tcp_congestion_control = cubic\n")
		c, _ := checkByName(Detect(f.dir), "sysctl 配置覆盖检查")
		if c.OK {
			t.Errorf("/etc/sysctl.conf 会盖掉面板配置，应报: %+v", c)
		}
	})
}

// ---- 参数校验与差异预览 ----

func TestValidateTune(t *testing.T) {
	avail := []string{"cubic", "bbr"}
	good := DefaultTune()
	if err := validateTune(good, avail); err != nil {
		t.Fatalf("默认方案应通过: %v", err)
	}

	bad := []struct {
		name string
		tune func(Tune) Tune
	}{
		{"内核没有该算法", func(x Tune) Tune { x.CC = "illinois"; return x }},
		{"不支持的 qdisc", func(x Tune) Tune { x.Qdisc = "pfifo_fast"; return x }},
		{"缓冲过小", func(x Tune) Tune { x.RmemMax = 1024; return x }},
		{"缓冲过大", func(x Tune) Tune { x.WmemMax = 1 << 30; return x }},
		{"backlog 越界", func(x Tune) Tune { x.Somaxconn = 10; return x }},
		{"端口区间倒置", func(x Tune) Tune { x.PortStart, x.PortEnd = 60000, 50000; return x }},
		{"端口越界", func(x Tune) Tune { x.PortEnd = 70000; return x }},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateTune(tc.tune(good), avail); err == nil {
				t.Fatal("应拒绝非法参数")
			}
		})
	}
}

// 非法参数必须在写盘前就被拦住：一次写动作都不许发生。
func TestApplyRejectsInvalidTuneBeforeWriting(t *testing.T) {
	f := defaultFixture()
	f.install(t)

	bad := DefaultTune()
	bad.Somaxconn = 1
	if err := Apply(f.dir, ApplyConfirm, bad); err == nil {
		t.Fatal("应拒绝非法参数")
	}
	if f.mutations != 0 {
		t.Errorf("参数校验失败时不得有任何写动作，实际 %d 次", f.mutations)
	}
}

func TestPlanTuneMarksChanges(t *testing.T) {
	cur := Tune{CC: "bbr", Qdisc: "fq", RmemMax: 212992, WmemMax: 212992, Somaxconn: 4096, PortStart: 32768, PortEnd: 60999}
	plan := PlanTune(cur, DefaultTune())
	changed := map[string]bool{}
	for _, p := range plan {
		changed[p.Key] = p.Changed
	}
	for _, k := range []string{keyRmem, keyWmem, keySomaxconn, keyPortRange} {
		if !changed[k] {
			t.Errorf("%s 与默认值不同，应标记为变更", k)
		}
	}
	for _, k := range []string{keyCC, keyQdisc} {
		if changed[k] {
			t.Errorf("%s 相同，不该标记变更", k)
		}
	}
}

// ---- 复检闭环 ----

// sysctl 报告成功、但值其实没写进内核时，必须复检发现并整体回滚。
func TestApplyVerifyFailureRollsBack(t *testing.T) {
	f := defaultFixture()
	f.cc = "cubic\n"
	f.qdisc = "pfifo_fast\n"
	f.cmdHook = func() {} // 什么都不做：模拟「sysctl 说成功、实际没生效」
	f.install(t)

	err := Apply(f.dir, ApplyConfirm)
	if err == nil {
		t.Fatal("复检不符时应返回 error")
	}
	if !strings.Contains(err.Error(), "复检") || !strings.Contains(err.Error(), "回滚") {
		t.Errorf("错误应说明复检失败并已回滚: %v", err)
	}
	if exists(cfgPath) {
		t.Error("失败后配置文件应被清除")
	}
	if exists(appliedPath(f.dir)) || exists(expectedPath(f.dir)) {
		t.Error("失败后标记与期望值都应清除")
	}
	if got := readTrim(t, ccPath()); got != "cubic" {
		t.Errorf("拥塞控制应回滚到原值: %q", got)
	}
}

// 应用成功后，Detect 应给出「已生效」的复检项；外部改回去后该项转红。
func TestDetectReportsExpectedValues(t *testing.T) {
	f := defaultFixture()
	f.install(t)

	if err := Apply(f.dir, ApplyConfirm); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	r := Detect(f.dir)
	if r.Expected == nil {
		t.Fatal("应用后应记录期望值")
	}
	c, ok := checkByName(r, "面板写入的参数已生效")
	if !ok || !c.OK {
		t.Fatalf("刚应用完复检应通过: %+v", c)
	}

	// 模拟被别处改回去。
	if err := os.WriteFile(keyPath(keySomaxconn), []byte("4096\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, _ = checkByName(Detect(f.dir), "面板写入的参数已生效")
	if c.OK {
		t.Errorf("值被改回后复检应转红: %+v", c)
	}
	if !strings.Contains(c.Detail, "somaxconn") {
		t.Errorf("detail 应点名出问题的键: %q", c.Detail)
	}
}

// 系统概览是只读探测，字段应真的被填上。
func TestSystemOverview(t *testing.T) {
	f := defaultFixture()
	f.install(t)
	s := Detect(f.dir).System
	if len(s.Load) != 3 || s.CPUCount != 2 {
		t.Errorf("负载/CPU 解析异常: %+v", s)
	}
	if s.MemTotal == 0 || s.MemAvail == 0 || s.DiskTotal == 0 || s.DiskFree == 0 {
		t.Errorf("内存/磁盘应读到值: %+v", s)
	}
	if !s.IPv6 || s.MTU != 1500 || s.MTUIf != "eth0" {
		t.Errorf("IPv6/MTU 解析异常: %+v", s)
	}
	if f.mutations != 0 {
		t.Errorf("系统概览不得写盘，实际 %d 次", f.mutations)
	}
}
