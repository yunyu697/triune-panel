package api

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"triune/internal/store"

	"gopkg.in/yaml.v3"
)

// ---- 解析与校验 ----

// 规范化不是洁癖：库里的值是回显给用户、又原样写进三种产物的，同一个地址
// 存成三种写法（`10.0.0.1` / `10.0.0.1:53` / ` 10.0.0.1 `）会让「我填了为什么
// 没生效」变成一道猜谜。
func TestParseLANResolverNormalizes(t *testing.T) {
	cases := map[string]string{
		"":               "",
		"   ":            "",
		"system":         "system",
		" SYSTEM ":       "system",
		"10.0.0.1":       "10.0.0.1",
		" 10.0.0.1 ":     "10.0.0.1",
		"10.0.0.1:53":    "10.0.0.1",
		"10.0.0.1:5353":  "10.0.0.1:5353",
		"fd00::1":        "[fd00::1]:53",
		"[fd00::1]":      "[fd00::1]:53",
		"[fd00::1]:5353": "[fd00::1]:5353",
	}
	for in, want := range cases {
		got, msg := parseLANResolver(in)
		if msg != "" {
			t.Errorf("%q 被拒了：%s", in, msg)
			continue
		}
		if got.raw != want {
			t.Errorf("%q 规范化成 %q，期望 %q", in, got.raw, want)
		}
	}
}

// 每一条拒收理由都对应一种「填了等于没填」或「填了更糟」。
//
// 尤其是回环 :53：router 目标下这份配置自己就 listen 0.0.0.0:53 并 hijack
// any:53，填 127.0.0.1 等于让内核问自己，全屋 DNS 立刻死掉——而症状（什么都
// 解析不了）与「没配好」完全一样，事后极难归因。
func TestParseLANResolverRejects(t *testing.T) {
	cases := []struct{ in, wantSub string }{
		{"dns.example.com", "必须填 IP"},
		{"router.lan", "必须填 IP"},
		{"0.0.0.0", "通配"},
		{"::", "通配"},
		{"[::]:53", "通配"},
		{"127.0.0.1", "DNS 环"},
		{"127.0.0.1:53", "DNS 环"},
		{"[::1]:53", "DNS 环"},
		{"10.0.0.1:0", "请填 IP"},
		{"10.0.0.1:70000", "请填 IP"},
		{"10.0.0.1:abc", "请填 IP"},
		{"10.0.0.1:", "请填 IP"},
	}
	for _, c := range cases {
		got, msg := parseLANResolver(c.in)
		if msg == "" {
			t.Errorf("%q 应该被拒，实际通过并规范化成 %q", c.in, got.raw)
			continue
		}
		if !strings.Contains(msg, c.wantSub) {
			t.Errorf("%q 的拒收理由没提到 %q：%s", c.in, c.wantSub, msg)
		}
	}
}

// 回环但换了端口就允许：本机另起一个 DNS（dnsmasq 在 5353、unbound 在 5335）
// 是完全正常的部署，真正会成环的只有「这份配置自己占着的那个 53」。
func TestParseLANResolverAllowsLoopbackOtherPort(t *testing.T) {
	got, msg := parseLANResolver("127.0.0.1:5353")
	if msg != "" {
		t.Fatalf("回环 + 非 53 端口不该被拒：%s", msg)
	}
	if got.raw != "127.0.0.1:5353" {
		t.Errorf("规范化成 %q", got.raw)
	}
}

// 设置项要走完「PUT 能存 / GET 能读」这一圈：漏了 GET 白名单，前端永远显示
// 为空，用户会以为设置没保存、反复重填。
func TestLANResolverSettingsRoundtrip(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := New(st, nil, nil, dir)

	body, _ := json.Marshal(map[string]string{SettingLANDNS: " 10.0.0.1:53 "})
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
	if got[SettingLANDNS] != "10.0.0.1" {
		t.Errorf("GET 回来的是 %q，期望规范化后的 10.0.0.1", got[SettingLANDNS])
	}
}

// 脏值必须在保存期拒掉，而不是写进库、等生成时才表现为「某个名字解析不了」。
func TestLANResolverSettingsRejectsBadValue(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := New(st, nil, nil, dir)

	for _, bad := range []string{"dns.example.com", "127.0.0.1:53", "0.0.0.0"} {
		body, _ := json.Marshal(map[string]string{SettingLANDNS: bad})
		w := httptest.NewRecorder()
		a.putSettings(w, httptest.NewRequest("PUT", "/api/settings", bytes.NewReader(body)))
		if w.Code != 400 {
			t.Errorf("%q 应该 400，实际 %d", bad, w.Code)
			continue
		}
		if v, _ := st.GetSetting(SettingLANDNS); v != "" {
			t.Errorf("%q 被拒之后库里却留下了 %q", bad, v)
		}
	}
}

// ---- 两套产物的写法 ----

// dnsServersOf 取 sing-box 产物里的 dns.servers。
func dnsServersOf(t *testing.T, doc map[string]any) []map[string]any {
	t.Helper()
	dns, ok := doc["dns"].(map[string]any)
	if !ok {
		t.Fatal("没有 dns 段")
	}
	raw, _ := dns["servers"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, s := range raw {
		if m, ok := s.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// dnsRuleServerFor 返回第一条覆盖该域名的 sing-box DNS 规则的 server 名。
func dnsRuleServerFor(t *testing.T, doc map[string]any, domain string) string {
	t.Helper()
	dns := doc["dns"].(map[string]any)
	rules, _ := dns["rules"].([]any)
	for _, r := range rules {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		for _, key := range []string{"domain", "domain_suffix"} {
			arr, _ := m[key].([]any)
			for _, v := range arr {
				if s, _ := v.(string); s == domain {
					srv, _ := m["server"].(string)
					return srv
				}
			}
		}
	}
	return ""
}

// 同一份 localOnlySuffixes，两套产物都得指向用户填的那台机器。
//
// 这是「跨内核同源」那条不变量的又一次落地：同一需求两种机制（mihomo 是
// nameserver-policy，sing-box 是 dns.rules → 一个 server），各写一遍必然漏
// 一边——v2.1.7 之前 sing-box 侧整条规则都不存在，就是因为它只写在 mihomo 那支。
func TestLANResolverBothCores(t *testing.T) {
	lan := lanResolver{raw: "10.0.0.1"}
	warns := []string{}

	mg := genMihomo("main-router", testNodes(), nil, nil, &warns, genOpts{LAN: lan})
	dns := mg.doc["dns"].(map[string]any)
	policy, ok := dns["nameserver-policy"].(map[string]any)
	if !ok {
		t.Fatal("mihomo 产物里没有 nameserver-policy")
	}
	if len(policy) != len(localOnlySuffixes) {
		t.Errorf("policy 有 %d 条，localOnlySuffixes 有 %d 条——两边不再是同一份清单",
			len(policy), len(localOnlySuffixes))
	}
	for _, s := range localOnlySuffixes {
		if got := policy["+."+s]; got != "10.0.0.1" {
			t.Errorf("mihomo 的 +.%s 指向 %v，期望 10.0.0.1", s, got)
		}
	}

	v, _ := parseSBVersion("1.14")
	sg := genSingbox("main-router", v, testNodes(), nil, nil, &warns, genOpts{LAN: lan})
	var lanServer map[string]any
	for _, s := range dnsServersOf(t, sg.doc) {
		if s["tag"] == "lan" {
			lanServer = s
		}
	}
	if lanServer == nil {
		t.Fatal("sing-box 产物里没有 tag=lan 的 DNS 服务器")
	}
	if lanServer["server"] != "10.0.0.1" {
		t.Errorf("sing-box 的 lan 服务器指向 %v", lanServer["server"])
	}
	for _, s := range localOnlySuffixes {
		if got := dnsRuleServerFor(t, sg.doc, s); got != "lan" {
			t.Errorf("sing-box 的 .%s 由 %q 解析，期望 lan", s, got)
		}
	}
}

// 空值是初始状态，不是问题：不写策略、也不产生警告。
//
// 断言「没有警告」是刻意的。警告在这套界面里会留在页面上、还会弹 toast，并顶掉
// 「生成完成」——把一条几乎人人可忽略的提示塞进去，结果是所有警告一起被无视。
// 「留空意味着什么」写在输入框的说明里，不占警告通道。
func TestLANResolverEmptyWritesNothing(t *testing.T) {
	warns := []string{}
	mg := genMihomo("main-router", testNodes(), nil, nil, &warns, genOpts{})
	dns := mg.doc["dns"].(map[string]any)
	if _, ok := dns["nameserver-policy"]; ok {
		t.Error("没填局域网 DNS 却写了 nameserver-policy")
	}
	if len(warns) != 0 {
		t.Errorf("空值不该产生警告：%v", warns)
	}
	// fake-ip-filter 不能因为这件事被顺手删掉：它管的是「给不给假 IP」，
	// 与「问谁解析」是两件事，两个都要在。
	if f, _ := dns["fake-ip-filter"].([]any); len(f) == 0 {
		t.Error("fake-ip-filter 空了：nas.lan 会拿到 198.18.x.x")
	}

	v, _ := parseSBVersion("1.14")
	sg := genSingbox("main-router", v, testNodes(), nil, nil, &warns, genOpts{})
	for _, s := range dnsServersOf(t, sg.doc) {
		if s["tag"] == "lan" {
			t.Error("没填局域网 DNS 却多了 tag=lan 的服务器")
		}
	}
	if got := dnsRuleServerFor(t, sg.doc, "lan"); got != "local" {
		t.Errorf("sing-box 的 .lan 由 %q 解析，期望回落到 local", got)
	}
}

// 「系统解析器 + router」是唯一真会成环的组合，必须给出警告；其它组合不该吵。
func TestLANResolverSystemWarnsOnlyForRouter(t *testing.T) {
	lan := lanResolver{raw: "system"}

	warns := []string{}
	genMihomo("main-router", testNodes(), nil, nil, &warns, genOpts{LAN: lan})
	if len(warns) == 0 || !strings.Contains(strings.Join(warns, " "), "DNS 环") {
		t.Errorf("router 目标 + 系统解析器必须提醒成环风险：%v", warns)
	}

	warns2 := []string{}
	genMihomo("client", testNodes(), nil, nil, &warns2, genOpts{LAN: lan})
	if len(warns2) != 0 {
		t.Errorf("client 目标不接管 :53，不该提醒环：%v", warns2)
	}

	// 填了具体地址就更不该吵
	warns3 := []string{}
	genMihomo("main-router", testNodes(), nil, nil, &warns3, genOpts{LAN: lanResolver{raw: "10.0.0.1"}})
	if len(warns3) != 0 {
		t.Errorf("填了具体地址不该有警告：%v", warns3)
	}
}

// 1.11 与 1.12+ 是两套 DNS 结构，写错一边就是启动 FATAL。
//
// 旧格式尤其容易漏 detour: direct——不写时 DNS 查询走 route.final（本模板是
// PROXY），「查局域网 DNS」的包被送进隧道，那台服务器在隧道另一头根本不可达，
// 失败信息只有超时。
func TestLANResolverVersionShape(t *testing.T) {
	cases := []struct{ ver, walWant string }{
		{"1.11", `address`},
		{"1.12", `type`},
	}
	for _, c := range cases {
		v, err := parseSBVersion(c.ver)
		if err != nil {
			t.Fatal(err)
		}
		warns := []string{}
		sg := genSingbox("main-router", v, testNodes(), nil, nil, &warns,
			genOpts{LAN: lanResolver{raw: "10.0.0.1:5353"}})
		var lan map[string]any
		for _, s := range dnsServersOf(t, sg.doc) {
			if s["tag"] == "lan" {
				lan = s
			}
		}
		if lan == nil {
			t.Fatalf("sing-box %s 没有 lan 服务器", c.ver)
		}
		if _, ok := lan["address"]; ok != (c.walWant == "address") {
			t.Errorf("%s 的字段形态不对：%v", c.ver, lan)
		}
		if c.ver == "1.11" {
			if lan["address"] != "10.0.0.1:5353" {
				t.Errorf("1.11 的 address 应是 host:port，实际 %v", lan["address"])
			}
			if lan["detour"] != "direct" {
				t.Errorf("1.11 必须写 detour: direct，否则查询会被送进代理：%v", lan)
			}
		} else {
			if lan["server"] != "10.0.0.1" || lan["server_port"] != 5353 {
				t.Errorf("1.12+ 应拆成 server + server_port：%v", lan)
			}
			if _, ok := lan["detour"]; ok {
				t.Errorf("1.12+ 写 detour 会被启动校验拒绝：%v", lan)
			}
		}
	}
}

// 1.12+ 的 `type: local` 是「系统解析器」的唯一写法，别拿 223.5.5.5 顶替——
// 那个 tag 叫 local 的公网服务器正是这次要绕开的东西。
func TestLANResolverSystemShape(t *testing.T) {
	v, _ := parseSBVersion("1.14")
	warns := []string{}
	sg := genSingbox("main-router", v, testNodes(), nil, nil, &warns,
		genOpts{LAN: lanResolver{raw: "system"}})
	for _, s := range dnsServersOf(t, sg.doc) {
		if s["tag"] == "lan" {
			if s["type"] != "local" {
				t.Errorf("system 应写成 type: local，实际 %v", s)
			}
			return
		}
	}
	t.Fatal("没有 lan 服务器")
}

// ---- 真内核：策略是不是真被用上了 ----

// 起一个只会回 10.9.9.9 的 UDP DNS，让 mihomo 拿它当「局域网 DNS」，
// 再问内核要 nas.lan 的地址。
//
// 断言的是「查询真的走到了我们指定的那台服务器」，不是「产物里有这一行字」。
// 只看文件的话，nameserver-policy 拼错一个键名也照样是绿的——mihomo 对未知键
// 静默忽略，产物每一行都合法。
func TestMihomoUsesLANResolverPolicy(t *testing.T) {
	bin := os.Getenv("TRINE_MIHOMO_BIN")
	if bin == "" {
		t.Skip("未设置 TRINE_MIHOMO_BIN，跳过真机 DNS 校验")
	}
	dnsAddr, stopDNS := startTestDNS(t, "10.9.9.9")
	defer stopDNS()

	warns := []string{}
	g := genMihomo("client", testNodes(), nil, nil, &warns,
		genOpts{LAN: lanResolver{raw: dnsAddr}})
	ctrl := "127.0.0.1:" + strconv.Itoa(freeTCPPort(t))
	g.doc["mixed-port"] = freeTCPPort(t)
	g.doc["external-controller"] = ctrl

	raw, err := yaml.Marshal(g.doc)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(file, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	cmd := exec.Command(bin, "-d", dir, "-f", file)
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动 mihomo 失败: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() {
		_ = cmd.Process.Kill()
		<-done
	}()

	// 等控制面起来（DNS 查询走它的 HTTP API）。
	deadline := time.Now().Add(8 * time.Second)
	var up bool
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("mihomo 启动即退出（%v）：\n%s", err, buf.String())
		default:
		}
		resp, err := http.Get("http://" + ctrl + "/version")
		if err == nil {
			resp.Body.Close()
			up = true
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if !up {
		t.Fatalf("mihomo 控制面 8 秒内没起来：\n%s", buf.String())
	}

	resp, err := http.Get("http://" + ctrl + "/dns/query?name=nas.lan&type=A")
	if err != nil {
		t.Fatalf("查询失败: %v\n%s", err, buf.String())
	}
	defer resp.Body.Close()
	var q struct {
		Answer []struct {
			Data string `json:"data"`
		} `json:"Answer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&q); err != nil {
		t.Fatalf("解析 DNS 应答失败: %v", err)
	}
	for _, a := range q.Answer {
		if a.Data == "10.9.9.9" {
			return
		}
	}
	t.Fatalf("nas.lan 没有解析到 10.9.9.9（拿到 %+v）——nameserver-policy 没生效：\n%s", q.Answer, buf.String())
}

// startTestDNS 起一个只会把任何 A 查询答成 answer 的 UDP DNS。
func startTestDNS(t *testing.T, answer string) (string, func()) {
	t.Helper()
	ip := net.ParseIP(answer).To4()
	if ip == nil {
		t.Fatalf("answer 必须是 IPv4：%s", answer)
	}
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听测试 DNS 失败: %v", err)
	}
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if resp := dnsAAnswer(buf[:n], ip); resp != nil {
				_, _ = pc.WriteTo(resp, from)
			}
		}
	}()
	return pc.LocalAddr().String(), func() { _ = pc.Close() }
}

// dnsAAnswer 把一个 A 查询拼成「一条 A 记录」的应答。
//
// 只做测试用得到的那一点点：抄回 question，追加 name pointer + A + 地址。
func dnsAAnswer(q []byte, ip net.IP) []byte {
	if len(q) < 12 || q[2]&0x80 != 0 {
		return nil // 不是查询
	}
	// 跳过 question 的名字，找到它的结尾
	i := 12
	for i < len(q) {
		if q[i] == 0 {
			i++
			break
		}
		if q[i]&0xc0 == 0xc0 { // 压缩指针，不该出现在 question 里
			i += 2
			break
		}
		i += int(q[i]) + 1
	}
	if i+4 > len(q) {
		return nil
	}
	questionEnd := i + 4 // type + class
	qtype := binary.BigEndian.Uint16(q[i : i+2])
	if qtype != 1 { // 只答 A
		return nil
	}

	resp := make([]byte, 0, questionEnd+16)
	resp = append(resp, q[:12]...)
	resp[2] = 0x81                            // QR=1, RD=1
	resp[3] = 0x80                            // RA=1
	binary.BigEndian.PutUint16(resp[6:8], 1)  // ANCOUNT
	binary.BigEndian.PutUint16(resp[8:10], 0) // NSCOUNT
	binary.BigEndian.PutUint16(resp[10:12], 0)
	resp = append(resp, q[12:questionEnd]...)
	resp = append(resp, 0xc0, 0x0c)             // 指回 question 的名字
	resp = append(resp, 0x00, 0x01, 0x00, 0x01) // A / IN
	resp = append(resp, 0x00, 0x00, 0x00, 0x3c) // TTL 60
	resp = append(resp, 0x00, 0x04)             // RDLENGTH
	resp = append(resp, ip...)
	return resp
}

// 真启动：1.12+ 的 type/server 与 1.11 的 address 两代格式都要能起来。
//
// 单测只能证明字写成了期望的形状，「内核认不认」是另一回事——v2.1.0 那次
// detour 事故就是 check 通过、start service 才报 FATAL。用 TRINE_SINGBOX_VER
// 指定生成版本，就能拿同一套用例验旧版。
func TestSingboxClientConfigStartsWithLANResolver(t *testing.T) {
	bin := os.Getenv("TRINE_SINGBOX_BIN")
	if bin == "" {
		t.Skip("未设置 TRINE_SINGBOX_BIN，跳过真机启动校验")
	}
	v, err := parseSBVersion(os.Getenv("TRINE_SINGBOX_VER"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, raw string }{
		{"ip", "10.0.0.1:5353"},
		{"system", "system"},
	} {
		t.Run(c.name, func(t *testing.T) {
			warns := []string{}
			g := genSingbox("client", v, testNodes(), nil, nil, &warns,
				genOpts{LAN: lanResolver{raw: c.raw}})
			port := freeTCPPort(t)
			if inbounds, ok := g.doc["inbounds"].([]any); ok {
				for _, it := range inbounds {
					if m, ok := it.(map[string]any); ok {
						m["listen_port"] = port
					}
				}
			}
			raw, err := json.MarshalIndent(g.doc, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(t.TempDir(), "client.json")
			if err := os.WriteFile(file, raw, 0o644); err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			cmd := exec.Command(bin, "run", "-c", file)
			cmd.Stdout, cmd.Stderr = &buf, &buf
			if err := cmd.Start(); err != nil {
				t.Fatalf("启动 sing-box 失败: %v", err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				t.Fatalf("sing-box 启动即退出（%v）：\n%s", err, buf.String())
			case <-time.After(2 * time.Second):
				_ = cmd.Process.Kill()
				<-done
			}
		})
	}
}

// 老配置（保存时还没有 lan_dns 字段）点「重新生成」时，按当前设置走。
//
// 用指针区分「没带这个字段」与「明确清空」就是为了这一步：
//   - 不带：v2.1.8 之前保存的 params、脚本直调。此时若读不到设置，用户填过的
//     地址会在每次重新生成时**悄悄消失**，而他看到的是「我明明填了」；
//   - 空串：用户在输入框里清空了，就是不要这条策略，不能又被设置塞回来。
func TestLANResolverFallsBackToSettingWhenFieldOmitted(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)
	exportFixture(t, a, tok,
		`{"name":"hk-01","core":"xray","protocol":"vless","port":24443,"tag":"hk",`+
			`"transport":"tcp","tls":"none","enabled":true,"settings":"{}"}`)

	if err := a.Store.SetSetting(SettingLANDNS, "10.0.0.1"); err != nil {
		t.Fatal(err)
	}

	export := func(body string) string {
		w := apiReq(t, h, tok, "POST", "/api/export/config", body)
		if w.Code != http.StatusOK {
			t.Fatalf("导出应 200，实际 %d: %s", w.Code, w.Body.String())
		}
		var resp struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp.Content
	}

	out := export(`{"core":"mihomo","target":"main-router","inbound_ids":[1]}`)
	if !strings.Contains(out, "nameserver-policy") || !strings.Contains(out, "10.0.0.1") {
		t.Errorf("没带 lan_dns 时应回落到设置里的 10.0.0.1\n%s", firstLines(out, 3))
	}

	out2 := export(`{"core":"mihomo","target":"main-router","inbound_ids":[1],"lan_dns":""}`)
	if strings.Contains(out2, "nameserver-policy") {
		t.Error("明确传空串时不该写 nameserver-policy，否则这个设置清不掉")
	}
}
