package api

// 客户端兼容性这一项的测试。
//
// 这一条的判据是一条「版本线」：xray ≥ 26.7.11 的 REALITY 默认只接受
// 26.3.27 以上的客户端，而 mihomo 把客户端版本硬编码成 1.8.2。判错的方向
// 只有两种，代价都不小：
//
//   - 阈值太低 → 明明能用的内核被报成「会拒绝老客户端」，用户白降级一次；
//   - 阈值太高 → 真的连不上时不提示，用户继续在链接参数里找原因。
//
// 所以这里把边界值逐个钉死。

import (
	"testing"

	"triune/internal/model"
)

func TestXrayTooNewForOldRealityClients(t *testing.T) {
	cases := []struct {
		v    string
		want bool
	}{
		// `xray version` 的真实输出形状
		{"Xray 26.10.31 (Xray, Penetrates Everything.) a4a3dff (go1.27.3 darwin/arm64)", true},
		{"Xray 26.7.11 (Xray, Penetrates Everything.) 1a2b3c4 (go1.26.1 linux/amd64)", true},
		{"Xray 26.7.10 (Xray, Penetrates Everything.) 1a2b3c4 (go1.26.1 linux/amd64)", false},
		{"Xray 26.3.27 (Xray, Penetrates Everything.) d2758a0 (go1.26.1 darwin/arm64)", false},
		{"Xray 1.8.24 (Xray, Penetrates Everything.) 6baad79 (go1.23.0 darwin/arm64)", false},
		{"26.8.0", true},
		{"27.0.0", true},
		// 解析不出版本就「不判定」，宁可不说也不要误报
		{"", false},
		{"dev", false},
	}
	for _, c := range cases {
		if got := xrayTooNewForOldRealityClients(c.v); got != c.want {
			t.Errorf("%q: 期望 %v，实际 %v", c.v, c.want, got)
		}
	}
}

// 非 xray + reality 的组合不该出现这一项：sing-box 没有 minClientVer 这套机制。
func TestRealityClientCompatOnlyForXray(t *testing.T) {
	a := newAuthTestServer(t)
	for _, c := range []struct{ core, tls string }{
		{"singbox", "reality"},
		{"xray", "tls"},
		{"xray", "none"},
	} {
		in := &model.Inbound{Core: c.core, TLS: c.tls, Protocol: "vless"}
		if item, ok := a.realityClientCompat(in); ok {
			t.Errorf("%s/%s 不该有客户端兼容性结论: %+v", c.core, c.tls, item)
		}
	}
}

// 没配内核路径时不下结论（宁可不说，也不误报）。
func TestRealityClientCompatSkipsWithoutBinary(t *testing.T) {
	a := newAuthTestServer(t)
	in := &model.Inbound{Core: "xray", TLS: "reality", Protocol: "vless"}
	if item, ok := a.realityClientCompat(in); ok {
		t.Fatalf("没装内核时不该给出结论，实际: %+v", item)
	}
}
