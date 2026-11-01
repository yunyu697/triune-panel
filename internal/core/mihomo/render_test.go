package mihomo

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"triune/internal/core"
	"triune/internal/model"
)

// mihomo 的混合 listener 用户必须是对象数组（'users[0]' expected a map），
// 不能是 "user:pass" 字符串。
func TestRenderMixedUsersAreObjects(t *testing.T) {
	m := &Mihomo{}
	in := &model.Inbound{Name: "mx", Core: "mihomo", Protocol: "mixed", Port: 7891, Transport: "tcp"}
	l, err := m.renderListener(core.InboundWithClients{Inbound: in,
		Clients: []*model.Client{{Name: "u1", Credential: "p1", Enabled: true}}})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	users, ok := l["users"].([]any)
	if !ok || len(users) != 1 {
		t.Fatalf("users 应为对象数组: %#v", l["users"])
	}
	u := users[0].(map[string]any)
	if u["username"] != "u1" || u["password"] != "p1" {
		t.Errorf("users[0] = %v", u)
	}
}

// tuic/hysteria2 的 users 是 map（用户名 → 密码），且必须带证书。
// 证书以 PEM 内容内联（mihomo 对证书路径有安全白名单，写路径会静默失败）。
func TestRenderQUICUsersAreMap(t *testing.T) {
	m := &Mihomo{}
	dir := t.TempDir()
	certPEM := "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----"
	keyPEM := "-----BEGIN PRIVATE KEY-----\nMIIB\n-----END PRIVATE KEY-----"
	cp, kp := dir+"/c.pem", dir+"/k.pem"
	if err := os.WriteFile(cp, []byte(certPEM), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kp, []byte(keyPEM), 0o600); err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf(`{"certificate_path":%q,"key_path":%q,"server_name":"a.com"}`, cp, kp)
	for _, proto := range []string{"tuic", "hysteria2"} {
		in := &model.Inbound{Name: proto, Core: "mihomo", Protocol: proto,
			Port: 443, Transport: "udp", TLS: "tls", Settings: base}
		l, err := m.renderListener(core.InboundWithClients{Inbound: in,
			Clients: []*model.Client{{Name: "u1", Credential: "p1", Enabled: true}}})
		if err != nil {
			t.Fatalf("%s render: %v", proto, err)
		}
		users, ok := l["users"].(map[string]string)
		if !ok {
			t.Fatalf("%s users 应为 map: %#v", proto, l["users"])
		}
		if users["u1"] != "p1" {
			t.Errorf("%s users = %v", proto, users)
		}
		if l["certificate"] != certPEM || l["private-key"] != keyPEM {
			t.Errorf("%s 证书应内联为 PEM 内容: %v", proto, l["certificate"])
		}
		if _, ok := l["tls"]; ok {
			t.Errorf("%s 不应写 tls 布尔字段（mihomo listener 无此字段）", proto)
		}
	}
}

// 证书路径不存在时必须在渲染期就报错（而不是让 listener 静默不监听）。
func TestRenderCertMissing(t *testing.T) {
	m := &Mihomo{}
	in := &model.Inbound{Name: "t", Core: "mihomo", Protocol: "trojan", Port: 443,
		Transport: "tcp", TLS: "tls",
		Settings: `{"certificate_path":"/nonexistent/c.pem","key_path":"/nonexistent/k.pem"}`}
	if _, err := m.renderListener(core.InboundWithClients{Inbound: in,
		Clients: []*model.Client{{Name: "u", Credential: "p", Enabled: true}}}); err == nil {
		t.Fatal("证书不可读时应报错")
	}
}

// tuic/hysteria2 缺证书时应在渲染期给出可读错误。
func TestRenderQUICRequiresCert(t *testing.T) {
	m := &Mihomo{}
	in := &model.Inbound{Name: "t", Core: "mihomo", Protocol: "tuic", Port: 443, Transport: "udp", TLS: "tls"}
	if _, err := m.renderListener(core.InboundWithClients{Inbound: in}); err == nil {
		t.Fatal("缺证书时应报错")
	}
}

// 服务端不需要 mixed-port：多余的 7890 监听会与宿主机服务抢端口，
// 抢不到时内核启动即退出。
func TestRenderNoDefaultMixedPort(t *testing.T) {
	m := &Mihomo{}
	out, err := m.Render(nil, core.RenderOpts{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(string(out), "mixed-port") {
		t.Errorf("不应再写 mixed-port:\n%s", out)
	}
}
