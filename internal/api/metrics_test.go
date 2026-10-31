package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"triune/internal/model"
)

// metricsReq 发一个 /metrics 请求（走完整 mux，含安全头中间件）。
func metricsReq(h http.Handler, header, query string) *httptest.ResponseRecorder {
	url := "/metrics"
	if query != "" {
		url += "?token=" + query
	}
	req := httptest.NewRequest("GET", url, nil)
	if header != "" {
		req.Header.Set("Authorization", "Bearer "+header)
	}
	req.RemoteAddr = "203.0.113.40:1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// TestMetricsDisabledByDefault 没配令牌时 /metrics 必须关闭。
//
// 升级不该凭空多出一个「知道地址就能看用户列表和流量」的接口。
func TestMetricsDisabledByDefault(t *testing.T) {
	a := newAuthTestServer(t)
	w := metricsReq(a.Handler(), "", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("未配置令牌应 403，实际 %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "未启用") {
		t.Fatalf("应给出启用提示，实际: %s", w.Body.String())
	}
}

// TestMetricsRequiresToken 配了令牌后，错误/缺失令牌一律 401。
func TestMetricsRequiresToken(t *testing.T) {
	a := newAuthTestServer(t)
	if err := a.Store.SetSetting(MetricsTokenSetting, "s3cret-metrics-token"); err != nil {
		t.Fatal(err)
	}
	h := a.Handler()

	if w := metricsReq(h, "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("缺令牌应 401，实际 %d", w.Code)
	}
	if w := metricsReq(h, "wrong-token", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("错令牌应 401，实际 %d", w.Code)
	}
	if w := metricsReq(h, "s3cret-metrics-toke", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("令牌前缀应 401，实际 %d", w.Code)
	}
	// Bearer 与 ?token= 两种给法都要能用
	if w := metricsReq(h, "s3cret-metrics-token", ""); w.Code != http.StatusOK {
		t.Fatalf("正确令牌（Bearer）应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	if w := metricsReq(h, "", "s3cret-metrics-token"); w.Code != http.StatusOK {
		t.Fatalf("正确令牌（query）应 200，实际 %d", w.Code)
	}
}

// TestMetricsFormat 校验 Prometheus 文本格式的基本要素。
func TestMetricsFormat(t *testing.T) {
	a := newAuthTestServer(t)
	if err := a.Store.SetSetting(MetricsTokenSetting, "tk"); err != nil {
		t.Fatal(err)
	}
	w := metricsReq(a.Handler(), "tk", "")
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Fatalf("Content-Type 不对: %q", ct)
	}
	body := w.Body.String()
	for _, want := range []string{
		"# HELP triune_up ",
		"# TYPE triune_up gauge",
		"triune_up 1\n",
		"triune_build_info{version=",
		"triune_db_up 1\n",
		"triune_uptime_seconds ",
		"triune_go_goroutines ",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("输出缺少 %q\n---\n%s", want, body)
		}
	}
	// 每条 # TYPE 只能出现一次
	if n := strings.Count(body, "# TYPE triune_up "); n != 1 {
		t.Errorf("# TYPE triune_up 应出现 1 次，实际 %d", n)
	}
}

// TestMetricsClientSeries 用户与流量指标要反映真实数据。
func TestMetricsClientSeries(t *testing.T) {
	a := newAuthTestServer(t)
	if err := a.Store.SetSetting(MetricsTokenSetting, "tk"); err != nil {
		t.Fatal(err)
	}
	c := &model.Client{Name: "alice", Credential: "u1", SubToken: "t1", QuotaGB: 10, Enabled: true}
	if err := a.Store.CreateClient(c); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.AddTraffic(c.ID, 1000, 2000); err != nil {
		t.Fatal(err)
	}

	body := metricsReq(a.Handler(), "tk", "").Body.String()
	for _, want := range []string{
		`triune_client_enabled{client="alice"} 1`,
		`triune_client_quota_bytes{client="alice"} 10737418240`,
		`triune_client_traffic_bytes{client="alice",direction="uplink"} 1000`,
		`triune_client_traffic_bytes{client="alice",direction="downlink"} 2000`,
		`triune_clients_total{enabled="true"} 1`,
		`triune_clients_total{enabled="false"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("输出缺少 %q\n---\n%s", want, body)
		}
	}
}

// TestMetricsLabelEscaping 用户可控的名字必须转义。
//
// 不转义的话，一个叫 `x"} 1\ntriune_fake 999` 的用户就能往指标里塞任意行，
// 伪造出根本不存在的序列——监控数据被污染比没有监控更危险。
func TestMetricsLabelEscaping(t *testing.T) {
	a := newAuthTestServer(t)
	if err := a.Store.SetSetting(MetricsTokenSetting, "tk"); err != nil {
		t.Fatal(err)
	}
	evil := "evil\"} 1\ntriune_fake 999"
	backslash := `back\slash`
	for i, name := range []string{evil, backslash} {
		c := &model.Client{Name: name, Credential: "u", SubToken: "tok-" + string(rune('a'+i)), Enabled: true}
		if err := a.Store.CreateClient(c); err != nil {
			t.Fatalf("第 %d 个: %v", i, err)
		}
	}

	body := metricsReq(a.Handler(), "tk", "").Body.String()

	// 注入的 triune_fake 不能成为独立一行
	for _, ln := range strings.Split(body, "\n") {
		if strings.HasPrefix(ln, "triune_fake") {
			t.Fatalf("标签值注入成功，出现了伪造指标行: %q", ln)
		}
	}
	// 转义后的形式应出现（引号 \"、换行 \n、反斜杠 \\）
	if !strings.Contains(body, `client="evil\"} 1\ntriune_fake 999"`) {
		t.Fatalf("注入用的名字未按预期转义\n---\n%s", body)
	}
	if !strings.Contains(body, `client="back\\slash"`) {
		t.Fatalf("反斜杠未转义\n---\n%s", body)
	}
	// 每个用户只应产生一行 triune_client_enabled
	n := 0
	for _, ln := range strings.Split(body, "\n") {
		if strings.HasPrefix(ln, "triune_client_enabled{") {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("应有 2 条 triune_client_enabled，实际 %d\n---\n%s", n, body)
	}
}

// TestMetricsExcludesSecrets 指标里不能出现凭据、订阅令牌或设置里的密钥。
func TestMetricsExcludesSecrets(t *testing.T) {
	a := newAuthTestServer(t)
	if err := a.Store.SetSetting(MetricsTokenSetting, "tk"); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.SetSetting("stats_secret", "STATS-SECRET-VALUE"); err != nil {
		t.Fatal(err)
	}
	c := &model.Client{Name: "bob", Credential: "CRED-VALUE-123", SubToken: "SUB-TOKEN-456", Enabled: true}
	if err := a.Store.CreateClient(c); err != nil {
		t.Fatal(err)
	}

	body := metricsReq(a.Handler(), "tk", "").Body.String()
	for _, secret := range []string{"CRED-VALUE-123", "SUB-TOKEN-456", "STATS-SECRET-VALUE", "tk"} {
		// 令牌 "tk" 太短会误判（出现在别的词里），只查长串
		if len(secret) < 6 {
			continue
		}
		if strings.Contains(body, secret) {
			t.Fatalf("指标里泄漏了敏感值 %q\n---\n%s", secret, body)
		}
	}
}

// TestMetricsGrouping 同一家族的 HELP/TYPE 与样本必须连成一组。
//
// 这条守的是格式合规性：如果先把所有表头写完再写样本，样本就会跟自己的表头
// 被别的家族隔开。宽松的解析器也许能忍，严格的会直接判整份输出格式错误。
func TestMetricsGrouping(t *testing.T) {
	a := newAuthTestServer(t)
	if err := a.Store.SetSetting(MetricsTokenSetting, "tk"); err != nil {
		t.Fatal(err)
	}
	// 两个用户 + 一个入站，确保多个家族交织输出
	for i, n := range []string{"a1", "a2"} {
		c := &model.Client{Name: n, Credential: "c", SubToken: "s" + strconv.Itoa(i), Enabled: true}
		if err := a.Store.CreateClient(c); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Store.CreateInbound(&model.Inbound{Name: "hk", Core: "singbox", Protocol: "vless", Port: 20001, Enabled: true}); err != nil {
		t.Fatal(err)
	}

	body := metricsReq(a.Handler(), "tk", "").Body.String()

	cur := ""
	for i, ln := range strings.Split(body, "\n") {
		if ln == "" {
			continue
		}
		switch {
		case strings.HasPrefix(ln, "# HELP "):
			cur = strings.Fields(ln)[2]
		case strings.HasPrefix(ln, "# TYPE "):
			name := strings.Fields(ln)[2]
			if name != cur {
				t.Fatalf("第 %d 行：TYPE %q 与前面的 HELP %q 不匹配", i+1, name, cur)
			}
		default:
			name := ln
			if j := strings.IndexAny(ln, "{ "); j > 0 {
				name = ln[:j]
			}
			if name != cur {
				t.Fatalf("第 %d 行：样本 %q 落在 %q 的组里（家族被拆散）\n---\n%s", i+1, name, cur, body)
			}
		}
	}
}
