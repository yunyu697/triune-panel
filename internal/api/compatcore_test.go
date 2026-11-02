package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"triune/internal/model"
)

// mihomo 从「三等分内核」降级为「兼容内核」后的行为约束。
//
// 背景：mihomo 支持的协议是 sing-box 的**真子集**，而且缺 vless / reality。
// 面板是服务端，mihomo 的强项（分流、proxy-groups、订阅聚合）一个都用不上，
// 它只被当作一堆 listener。所以「新建时选 mihomo」不是另一种选择，而是纯降级：
// 选完才发现没有 vless，或者建完发现客户端连不上。
//
// 但它不能删：已经有 mihomo 入站的用户升级后配置必须照常跑，而且机器上原本
// 就在跑 mihomo 的人得能原地纳管。所以默认从新建入口摘掉、代码保留。

// TestCompatCoreHiddenByDefault 默认不允许新建 mihomo 入站。
func TestCompatCoreHiddenByDefault(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	w := apiReq(t, h, tok, "POST", "/api/inbounds",
		`{"name":"m1","core":"mihomo","protocol":"socks","port":24001,"tag":"m1"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("默认应拒绝新建 mihomo 入站，实际 %d: %s", w.Code, w.Body.String())
	}
	// 文案要说明「为什么」和「怎么开」，否则用户只会觉得面板坏了
	if !strings.Contains(w.Body.String(), "兼容内核") ||
		!strings.Contains(w.Body.String(), "设置") {
		t.Errorf("拒绝文案应说明原因与开关位置，实际: %s", w.Body.String())
	}

	// sing-box / xray 不受影响
	for i, core := range []string{"singbox", "xray"} {
		body := fmt.Sprintf(`{"name":"ok-%s","core":"%s","protocol":"socks","port":%d,"tag":"ok-%s"}`,
			core, core, 24010+i, core)
		if w := apiReq(t, h, tok, "POST", "/api/inbounds", body); w.Code != http.StatusOK {
			t.Errorf("%s 应允许新建，实际 %d: %s", core, w.Code, w.Body.String())
		}
	}
}

// TestCompatCoreSwitchOn 打开开关后可以新建，关掉后又拦下。
func TestCompatCoreSwitchOn(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	if w := apiReq(t, h, tok, "PUT", "/api/settings", `{"show_compat_core":"1"}`); w.Code != http.StatusOK {
		t.Fatalf("打开开关应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	in := mkInbound(t, h, tok,
		`{"name":"m1","core":"mihomo","protocol":"socks","port":24011,"tag":"m1"}`)
	if in.Core != model.CoreMihomo {
		t.Fatalf("内核应为 mihomo，实际 %q", in.Core)
	}

	// 关闭后不能再新建（但上面那条已经存在，不受影响）
	if w := apiReq(t, h, tok, "PUT", "/api/settings", `{"show_compat_core":"0"}`); w.Code != http.StatusOK {
		t.Fatalf("关闭开关应 200，实际 %d", w.Code)
	}
	if w := apiReq(t, h, tok, "POST", "/api/inbounds",
		`{"name":"m2","core":"mihomo","protocol":"socks","port":24012,"tag":"m2"}`); w.Code != http.StatusBadRequest {
		t.Errorf("关闭开关后新建应被拒，实际 %d", w.Code)
	}
}

// TestCompatCoreExistingInboundStillEditable 存量 mihomo 入站必须还能改。
//
// 这是最容易写错的一处：如果只在「新建」放行、把更新路径也一起拦了，
// 老用户升级后会连改个备注都改不了——而且报错说「不能新建」，莫名其妙。
func TestCompatCoreExistingInboundStillEditable(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	// 直接写库模拟「升级前就存在的 mihomo 入站」
	in := &model.Inbound{Name: "legacy", Core: model.CoreMihomo, Protocol: "socks",
		Port: 24021, Transport: "tcp", TLS: "none", Tag: "legacy", Enabled: true,
		Settings: `{}`}
	if err := a.Store.CreateInbound(in); err != nil {
		t.Fatal(err)
	}

	path := "/api/inbounds/" + strconv.FormatInt(in.ID, 10)
	if w := apiReq(t, h, tok, "PUT", path, `{"name":"legacy-renamed"}`); w.Code != http.StatusOK {
		t.Fatalf("存量的 mihomo 入站应能改名，实际 %d: %s", w.Code, w.Body.String())
	}

	// 但从 singbox 改成 mihomo 等于新建一个降级入站，要拦
	sb := mkInbound(t, h, tok,
		`{"name":"sb","core":"singbox","protocol":"socks","port":24022,"tag":"sb"}`)
	sbPath := "/api/inbounds/" + strconv.FormatInt(sb.ID, 10)
	if w := apiReq(t, h, tok, "PUT", sbPath, `{"core":"mihomo"}`); w.Code != http.StatusBadRequest {
		t.Errorf("把 singbox 入站改成 mihomo 应被拒，实际 %d: %s", w.Code, w.Body.String())
	}
}

// TestCompatCoreRejectedAsSwitchTarget 不能把入站切到 mihomo。
//
// 反方向（mihomo → sing-box / xray）必须放行，那才是这次降级的出口。但那条
// 路径要过 DryRun 与进程管理，测试服务器没有构造 Pipe（`New(st, nil, nil, dir)`），
// 会直接 nil panic —— 所以放行路径由 deploy/verify-inbounds.py 在真机内核上验证，
// 这里只钉住「不许切进去」和 compatCoreAllowed 本身的真假值。
func TestCompatCoreRejectedAsSwitchTarget(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	in := &model.Inbound{Name: "sb", Core: model.CoreSingBox, Protocol: "socks",
		Port: 24032, Transport: "tcp", TLS: "none", Tag: "sb", Enabled: true,
		Settings: `{}`}
	if err := a.Store.CreateInbound(in); err != nil {
		t.Fatal(err)
	}

	w := apiReq(t, h, tok, "POST", "/api/switch-core",
		`{"to":"mihomo","inbound_ids":[`+strconv.FormatInt(in.ID, 10)+`]}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("切到 mihomo 应被拒，实际 %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "不能作为切换目标") {
		t.Errorf("拒绝文案应说明不能作为切换目标，实际: %s", w.Body.String())
	}
}

// TestCompatCoreAllowed 开关只影响 mihomo，其它内核恒放行。
//
// 这条独立测是因为它决定了「存量 mihomo 用户能不能迁出去」：如果写反成
// `core == mihomo` 返回 false 之类，用户就永远被困在一个降级内核上。
func TestCompatCoreAllowed(t *testing.T) {
	a := newAuthTestServer(t)

	for _, core := range []string{model.CoreSingBox, model.CoreXray, "", "unknown"} {
		if !a.compatCoreAllowed(core) {
			t.Errorf("%q 不应受兼容开关影响，应恒为 true", core)
		}
	}
	if a.compatCoreAllowed(model.CoreMihomo) {
		t.Error("默认（开关关闭）应为 false")
	}
	if err := a.Store.SetSetting(ShowCompatCoreSetting, "1"); err != nil {
		t.Fatal(err)
	}
	if !a.compatCoreAllowed(model.CoreMihomo) {
		t.Error("开关打开后应为 true")
	}
	if err := a.Store.SetSetting(ShowCompatCoreSetting, "0"); err != nil {
		t.Fatal(err)
	}
	if a.compatCoreAllowed(model.CoreMihomo) {
		t.Error("开关关回去后应为 false")
	}
}
