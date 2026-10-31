package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"triune/internal/model"
)

// mkInbound 建一个入站并返回它。
func mkInbound(t *testing.T, h http.Handler, tok, body string) model.Inbound {
	t.Helper()
	w := apiReq(t, h, tok, "POST", "/api/inbounds", body)
	if w.Code != http.StatusOK {
		t.Fatalf("建入站应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var in model.Inbound
	if err := json.Unmarshal(w.Body.Bytes(), &in); err != nil {
		t.Fatal(err)
	}
	return in
}

// TestUpdateInboundPartialKeepsFields 部分更新不能动没传的字段。
//
// 旧实现把请求体解成完整 model.Inbound 再整行 UPDATE，`PUT {"name":"改名"}`
// 会把 core/protocol/port/tag/settings 全清成零值（settings 变 "{}"、port 变 0），
// 还返回 200——一个入站就这么变成空壳。
func TestUpdateInboundPartialKeepsFields(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	in := mkInbound(t, h, tok, `{"name":"hk-01","core":"singbox","protocol":"vless",
		"port":20001,"tag":"hk01","transport":"ws","tls":"tls",
		"settings":"{\"path\":\"/ray\",\"server_name\":\"a.example.com\"}","enabled":false}`)
	path := "/api/inbounds/" + strconv.FormatInt(in.ID, 10)

	if w := apiReq(t, h, tok, "PUT", path, `{"name":"hk-01-renamed"}`); w.Code != http.StatusOK {
		t.Fatalf("部分更新应 200，实际 %d: %s", w.Code, w.Body.String())
	}

	got, err := a.Store.GetInbound(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "hk-01-renamed" {
		t.Errorf("name 未更新: %q", got.Name)
	}
	// 其余字段一个都不能变
	if got.Core != "singbox" || got.Protocol != "vless" || got.Port != 20001 {
		t.Errorf("核心字段被清空: core=%q protocol=%q port=%d", got.Core, got.Protocol, got.Port)
	}
	if got.Tag != "hk01" || got.Transport != "ws" || got.TLS != "tls" {
		t.Errorf("连接参数被清空: tag=%q transport=%q tls=%q", got.Tag, got.Transport, got.TLS)
	}
	if got.Settings != in.Settings {
		t.Errorf("settings 被改动: %q → %q", in.Settings, got.Settings)
	}
}

// TestUpdateInboundEnabledRenameAllowed 启用中的入站只改名字必须放行。
//
// 旧实现里内核切换守卫拿零值 core 去比，于是任何「只改名」的请求都会收到
// 「请使用 /api/switch-core」——调用方根本没想换内核，这条错误只会误导人。
func TestUpdateInboundEnabledRenameAllowed(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	in := mkInbound(t, h, tok, `{"name":"jp-02","core":"singbox","protocol":"vless","port":20002,"enabled":true}`)
	path := "/api/inbounds/" + strconv.FormatInt(in.ID, 10)

	w := apiReq(t, h, tok, "PUT", path, `{"name":"jp-02-renamed"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("启用中的入站只改名应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	got, err := a.Store.GetInbound(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "jp-02-renamed" || got.Core != "singbox" || !got.Enabled {
		t.Fatalf("字段异常: %+v", got)
	}
}

// TestUpdateInboundRejectsCoreSwitch 真的想换内核时必须拦下来，
// 指向 /api/switch-core（那条路径有预检与失败回滚）。
func TestUpdateInboundRejectsCoreSwitch(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	in := mkInbound(t, h, tok, `{"name":"sg-03","core":"singbox","protocol":"vless","port":20003,"enabled":true}`)
	path := "/api/inbounds/" + strconv.FormatInt(in.ID, 10)

	w := apiReq(t, h, tok, "PUT", path, `{"core":"xray"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("运行中换内核应 400，实际 %d: %s", w.Code, w.Body.String())
	}
	got, _ := a.Store.GetInbound(in.ID)
	if got.Core != "singbox" {
		t.Fatalf("被拒绝的请求改动了 core: %q", got.Core)
	}
}

// TestUpdateInboundDraftPromotion 草稿转正：只传 draft=false，其余字段保留。
func TestUpdateInboundDraftPromotion(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	in := mkInbound(t, h, tok, `{"name":"draft-04","core":"singbox","protocol":"vless",
		"port":20004,"settings":"{\"path\":\"/d\"}","draft":true,"enabled":true}`)
	if !in.Draft {
		t.Fatal("应建成草稿")
	}
	path := "/api/inbounds/" + strconv.FormatInt(in.ID, 10)

	if w := apiReq(t, h, tok, "PUT", path, `{"draft":false}`); w.Code != http.StatusOK {
		t.Fatalf("草稿转正应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	got, err := a.Store.GetInbound(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Draft {
		t.Error("draft 仍为 true")
	}
	if got.Port != 20004 || got.Protocol != "vless" || got.Settings != in.Settings {
		t.Fatalf("转正过程丢了字段: %+v", got)
	}
}

// TestUpdateInboundPortChange 改端口只动 port，其他保留。
func TestUpdateInboundPortChange(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	in := mkInbound(t, h, tok, `{"name":"p-05","core":"xray","protocol":"trojan","port":20005,"enabled":false}`)
	path := "/api/inbounds/" + strconv.FormatInt(in.ID, 10)

	if w := apiReq(t, h, tok, "PUT", path, `{"port":20105}`); w.Code != http.StatusOK {
		t.Fatalf("改端口应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	got, _ := a.Store.GetInbound(in.ID)
	if got.Port != 20105 || got.Protocol != "trojan" || got.Core != "xray" || got.Name != "p-05" {
		t.Fatalf("字段异常: %+v", got)
	}
}

// TestUpdateInboundNotFound 改不存在的入站要 404，不能假装成功。
func TestUpdateInboundNotFound(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	if w := apiReq(t, h, tok, "PUT", "/api/inbounds/999", `{"name":"ghost"}`); w.Code != http.StatusNotFound {
		t.Fatalf("应 404，实际 %d: %s", w.Code, w.Body.String())
	}
}
