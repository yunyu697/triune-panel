package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"triune/internal/model"
	"triune/internal/store"
)

// TestPortInUseByOthers 验证「删除/改端口时是否还有别的入站在占用该端口」的判定：
// 停用、草稿、不同协议的入站都不算占用，避免误关仍在服务的端口。
func TestPortInUseByOthers(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := New(st, nil, nil, dir)

	mk := func(in *model.Inbound) *model.Inbound {
		if err := st.CreateInbound(in); err != nil {
			t.Fatalf("create inbound %s: %v", in.Name, err)
		}
		return in
	}

	// 自身：启用中的 vless/tcp 8443
	self := mk(&model.Inbound{Name: "self", Core: "singbox", Protocol: "vless",
		Port: 8443, Transport: "tcp", Enabled: true})
	// 另一个启用中的同端口 tcp 入站 → 占用
	other := mk(&model.Inbound{Name: "other", Core: "singbox", Protocol: "trojan",
		Port: 8443, Transport: "ws", Enabled: true})
	// 停用的同端口入站 → 不算占用
	mk(&model.Inbound{Name: "off", Core: "singbox", Protocol: "vmess",
		Port: 8443, Transport: "tcp", Enabled: false})
	// 草稿同端口 → 不算占用
	mk(&model.Inbound{Name: "draft", Core: "singbox", Protocol: "vmess",
		Port: 8443, Transport: "tcp", Enabled: true, Draft: true})
	// 同端口但走 UDP（hysteria2）→ 与 tcp 互不影响
	mk(&model.Inbound{Name: "hy2", Core: "singbox", Protocol: "hysteria2",
		Port: 8443, Transport: "udp", Enabled: true})

	if !a.portInUseByOthers(self.ID, 8443, "tcp") {
		t.Error("8443/tcp 仍被 other 占用，应返回 true")
	}
	if !a.portInUseByOthers(self.ID, 8443, "udp") {
		t.Error("8443/udp 被 hy2 占用，应返回 true")
	}

	// 删掉 other 后 tcp 侧只剩自身、停用、草稿 → 不再占用
	if err := st.DeleteInbound(other.ID); err != nil {
		t.Fatal(err)
	}
	if a.portInUseByOthers(self.ID, 8443, "tcp") {
		t.Error("other 已删除，8443/tcp 不应再被视为占用（停用/草稿不计）")
	}

	// 从未使用过的端口
	if a.portInUseByOthers(self.ID, 12345, "tcp") {
		t.Error("12345/tcp 无任何入站，应返回 false")
	}
}

// TestFwAutoDefault 验证 fw_auto 默认开启、写入 "0" 后关闭（设置缺失按开启处理）。
func TestFwAutoDefault(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := New(st, nil, nil, dir)

	if !fwAuto(a) {
		t.Error("fw_auto 未设置时应默认开启")
	}
	if err := st.SetSetting("fw_auto", "0"); err != nil {
		t.Fatal(err)
	}
	if fwAuto(a) {
		t.Error(`fw_auto="0" 时应关闭`)
	}
	if err := st.SetSetting("fw_auto", "1"); err != nil {
		t.Fatal(err)
	}
	if !fwAuto(a) {
		t.Error(`fw_auto="1" 时应开启`)
	}
}

// TestFwAutoSettingsRoundtrip 守住「PUT 能存但 GET 不返回」这类白名单不一致的坑：
// 前端开关依赖 GET 的 fw_auto 值回显，缺了就会永远显示为开启。
func TestFwAutoSettingsRoundtrip(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := New(st, nil, nil, dir)

	body, _ := json.Marshal(map[string]string{"fw_auto": "0"})
	req := httptest.NewRequest("PUT", "/api/settings", bytes.NewReader(body))
	w := httptest.NewRecorder()
	a.putSettings(w, req)
	if w.Code != 200 {
		t.Fatalf("putSettings code = %d, body %s", w.Code, w.Body.String())
	}

	w2 := httptest.NewRecorder()
	a.getSettings(w2, httptest.NewRequest("GET", "/api/settings", nil))
	var got map[string]string
	if err := json.Unmarshal(w2.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if v, ok := got["fw_auto"]; !ok || v != "0" {
		t.Errorf("GET settings fw_auto = %q (present=%v), want \"0\"", v, ok)
	}
}
