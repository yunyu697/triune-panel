package ops

import (
	"testing"
	"time"

	"triune/internal/model"
	"triune/internal/store"
)

func TestClashUserPriority(t *testing.T) {
	tagMap := map[string]string{
		"in-3": "alice",
		"vip":  "bob",
	}
	cases := []struct {
		name    string
		user    string
		inbound string
		typ     string
		want    string
	}{
		{"mihomo inboundUser 优先", "ignored", "carol", "socks/in-3", "carol"},
		{"mihomo user 次之", "dave", "", "socks/in-3", "dave"},
		{"sing-box type 末段 tag 反查", "", "", "socks/in-3", "alice"},
		{"sing-box 自定义 tag", "", "", "vless/vip", "bob"},
		{"type 无斜杠直接当 tag", "", "", "in-3", "alice"},
		{"未知 tag 不臆造归属", "", "", "socks/in-999", ""},
		{"全空无归属", "", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := clashUser(tc.user, tc.inbound, tc.typ, tagMap)
			if got != tc.want {
				t.Fatalf("clashUser = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTagToUserSingleVsMultiUser(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// 单用户入站 → 应映射；多用户入站 → 不映射；禁用入站 → 不映射
	in1 := &model.Inbound{Name: "s1", Core: model.CoreSingBox, Protocol: "socks", Port: 1001, Enabled: true}
	in2 := &model.Inbound{Name: "s2", Core: model.CoreSingBox, Protocol: "socks", Port: 1002, Enabled: true, Tag: "custom"}
	in3 := &model.Inbound{Name: "s3", Core: model.CoreSingBox, Protocol: "socks", Port: 1003, Enabled: false}
	for _, in := range []*model.Inbound{in1, in2, in3} {
		if err := st.CreateInbound(in); err != nil {
			t.Fatal(err)
		}
	}
	alice := &model.Client{Name: "alice", SubToken: "ta", Enabled: true}
	bob := &model.Client{Name: "bob", SubToken: "tb", Enabled: true}
	carol := &model.Client{Name: "carol", SubToken: "tc", Enabled: true}
	for _, cl := range []*model.Client{alice, bob, carol} {
		if err := st.CreateClient(cl); err != nil {
			t.Fatal(err)
		}
	}
	// in1 单用户 alice；in2 两用户 bob+carol；in3 单用户但入站禁用
	_ = st.SetClientInbounds(alice.ID, []int64{in1.ID})
	_ = st.SetClientInbounds(bob.ID, []int64{in2.ID})
	_ = st.SetClientInbounds(carol.ID, []int64{in2.ID, in3.ID})

	c := NewCollector(st, nil)
	got := c.tagToUser()

	if got["in-"+itoa64(in1.ID)] != "alice" {
		t.Fatalf("单用户入站应映射 alice, got map=%v", got)
	}
	if _, ok := got["custom"]; ok {
		t.Fatalf("多用户共享入站不应臆造归属, got map=%v", got)
	}
	tag3 := "in-" + itoa64(in3.ID)
	if _, ok := got[tag3]; ok {
		t.Fatalf("禁用入站不应映射, got map=%v", got)
	}
}

func TestSampleIntervalIsShort(t *testing.T) {
	// 短连接丢失与采样间隔成正比；钉住 5s 防止回退到 30s
	if sampleInterval > 5*time.Second {
		t.Fatalf("sampleInterval = %v, 应 ≤ 5s 以减少短连接丢失", sampleInterval)
	}
	c := NewCollector(nil, nil)
	if c.Interval != sampleInterval {
		t.Fatalf("NewCollector.Interval = %v, want %v", c.Interval, sampleInterval)
	}
}

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
