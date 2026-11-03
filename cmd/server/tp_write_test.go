package main

import (
	"testing"
	"time"

	"triune/internal/model"
	"triune/internal/store"
)

// tp 的写路径直连 SQLite（或经控制套接字），错一次就是「配置改了但内核没变」
// 这类看不见的故障。这里锁住其中三个纯决策点——它们决定「改完之后要重启哪些内核」，
// 算错了不会报错，只会静默不生效。

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func addInbound(t *testing.T, st *store.Store, name, core string, port int, enabled, draft bool) int64 {
	t.Helper()
	in := &model.Inbound{
		Name: name, Core: core, Protocol: "vless", Listen: "127.0.0.1",
		Port: port, Tag: name, Transport: "tcp", TLS: "none", Settings: "{}",
		Enabled: enabled, Draft: draft,
	}
	if err := st.CreateInbound(in); err != nil {
		t.Fatal(err)
	}
	return in.ID
}

// TestCoresForInboundIDs 用户名下有几个入站、分布在哪些内核 → 决定删用户后要
// 重启哪几个内核。漏一个内核，那个内核里还留着已删用户（订阅失效但节点仍能连）。
func TestCoresForInboundIDs(t *testing.T) {
	st := openTestStore(t)
	a := addInbound(t, st, "a", "xray", 21001, true, false)
	b := addInbound(t, st, "b", "xray", 21002, true, false)
	c := addInbound(t, st, "c", "singbox", 21003, true, false)

	cases := []struct {
		name string
		ids  []int64
		want []string
	}{
		{"单个内核去重", []int64{a, b}, []string{"xray"}},
		{"跨内核按固定顺序", []int64{c, a}, []string{"singbox", "xray"}},
		{"空列表", nil, nil},
		{"不存在的 id 跳过", []int64{a, 999999}, []string{"xray"}},
	}
	for _, tc := range cases {
		got := coresForInboundIDs(st, tc.ids)
		if len(got) != len(tc.want) {
			t.Fatalf("%s: want %v, got %v", tc.name, tc.want, got)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("%s: want %v, got %v", tc.name, tc.want, got)
			}
		}
	}
}

// TestCoresWithEnabledInbounds 停用的、草稿的入站不算「这个内核在跑」——
// 否则 tp apply 会对一个没有可用节点的内核做无意义的渲染与重启。
func TestCoresWithEnabledInbounds(t *testing.T) {
	st := openTestStore(t)
	addInbound(t, st, "live", "xray", 21011, true, false)
	addInbound(t, st, "off", "singbox", 21012, false, false)
	addInbound(t, st, "draft", "mihomo", 21013, true, true)

	got := coresWithEnabledInbounds(st)
	if len(got) != 1 || got[0] != "xray" {
		t.Fatalf("只应返回 xray（singbox 停用、mihomo 是草稿），实际 %v", got)
	}
}

// TestResolveInboundRefs 同时认 id 与名称（脚本用 id、手敲用名字），
// 顺序与用户写的顺序一致，空项跳过、重复保留（由调用方决定去重）。
func TestResolveInboundRefs(t *testing.T) {
	st := openTestStore(t)
	a := addInbound(t, st, "hk-01", "xray", 21021, true, false)
	b := addInbound(t, st, "jp-01", "singbox", 21022, true, false)

	cases := []struct {
		spec string
		want []int64
	}{
		{"", nil},
		{"   ", nil},
		{"hk-01", []int64{a}},
		{"jp-01,hk-01", []int64{b, a}},
		{" , hk-01 ,, ", []int64{a}},
	}
	for _, tc := range cases {
		got := resolveInboundRefs(st, tc.spec)
		if len(got) != len(tc.want) {
			t.Fatalf("spec %q: want %v, got %v", tc.spec, tc.want, got)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("spec %q: want %v, got %v", tc.spec, tc.want, got)
			}
		}
	}

	// 按 id 引用
	if got := resolveInboundRefs(st, itoa(a)); len(got) != 1 || got[0] != a {
		t.Fatalf("按 id 引用失败: %v", got)
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// TestParseExpiry 到期日落在当天 23:59:59，而不是零点——
// 差这一天，用户会在「还有一天」的时候突然连不上。
func TestParseExpiry(t *testing.T) {
	got := parseExpiry(" 2026-12-31 ")
	if got == nil {
		t.Fatal("应解析出时间")
	}
	want := time.Date(2026, 12, 31, 23, 59, 59, 0, time.Local)
	if !got.Equal(want) {
		t.Fatalf("want %v, got %v", want, *got)
	}
}

// TestParseIDOrZero 既用于「入站引用」，也用于「id 参数」，认错会把名称当 id。
func TestParseIDOrZero(t *testing.T) {
	cases := map[string]int64{
		"12": 12, " 7 ": 7, "0": 0, "-3": 0, "hk-01": 0, "": 0,
	}
	for in, want := range cases {
		if got := parseIDOrZero(in); got != want {
			t.Fatalf("%q: want %d, got %d", in, want, got)
		}
	}
}

// TestLookupUserNameBeforeID 名称优先于 id。
//
// 用户名叫「2」、同时另有一个 id 恰好也是 2 的用户时，`tp user rm 2` 必须删
// 名字叫 2 的那个。顺序反了不会报错，只会删错人——订阅链接、配额、关联入站
// 全都跟着错。
func TestLookupUserNameBeforeID(t *testing.T) {
	st := openTestStore(t)
	named := &model.Client{Name: "2", Credential: "cred-a", SubToken: "tok-a", Enabled: true}
	if err := st.CreateClient(named); err != nil {
		t.Fatal(err)
	}
	other := &model.Client{Name: "bob", Credential: "cred-b", SubToken: "tok-b", Enabled: true}
	if err := st.CreateClient(other); err != nil {
		t.Fatal(err)
	}
	if named.ID == other.ID {
		t.Fatalf("测试前提不成立：两个用户拿到同一个 id %d", named.ID)
	}

	got := lookupUser(st, "2")
	if got == nil {
		t.Fatal("应找到名为 2 的用户")
	}
	if got.ID != named.ID {
		t.Fatalf("名称应优先于 id：want id=%d, got id=%d（name=%s）", named.ID, got.ID, got.Name)
	}
	if lookupUser(st, "nobody") != nil {
		t.Fatal("不存在的名字应返回 nil")
	}

	// 没有同名用户时，数字参数退化成按 id 找（省掉「先 ls 拿 id」那一步）
	st2 := openTestStore(t)
	bob := &model.Client{Name: "bob", Credential: "cred-c", SubToken: "tok-c", Enabled: true}
	if err := st2.CreateClient(bob); err != nil {
		t.Fatal(err)
	}
	if c := lookupUser(st2, itoa(bob.ID)); c == nil || c.ID != bob.ID {
		t.Fatalf("退化成按 id 查找失败: %+v", c)
	}
}
