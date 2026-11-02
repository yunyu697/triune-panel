package core

// Reality 的 SNI / dest 推导测试。
//
// 这两个函数被渲染器和连通性诊断共用，那正是它们的意义：曾经诊断自己
// 读 server_name，而表单存的是 server_names，于是 Reality 入站的握手检查
// 对每一台正常运行的服务器都报「失败」。所以这里断言的是「取值顺序」，
// 不是「能取到值」——顺序错了就是误报。

import (
	"testing"

	"triune/internal/model"
)

func mkIn(settings string, tls string) *model.Inbound {
	return &model.Inbound{Settings: settings, TLS: tls, Protocol: "vless"}
}

func TestRealitySNIPriority(t *testing.T) {
	cases := []struct {
		name     string
		settings string
		want     string
	}{
		{
			// 表单的标准写法：逗号分隔的多个 SNI，取第一个
			name:     "server_names 多个取第一个",
			settings: `{"server_names":"www.apple.com,apple.com","handshake_server":"www.apple.com"}`,
			want:     "www.apple.com",
		},
		{
			name:     "server_names 是数组（导入的配置）",
			settings: `{"server_names":["a.example.com","b.example.com"]}`,
			want:     "a.example.com",
		},
		{
			name:     "只有单数 server_name",
			settings: `{"server_name":"single.example.com"}`,
			want:     "single.example.com",
		},
		{
			// 只填了握手目标：xray 渲染时用 dest 主机名兜底，探测必须一致
			name:     "都没有时回退到 dest 主机名",
			settings: `{"handshake_server":"www.microsoft.com","handshake_port":443}`,
			want:     "www.microsoft.com",
		},
		{
			name:     "dest 带端口也能取出主机名",
			settings: `{"handshake_server":"www.bing.com:8443"}`,
			want:     "www.bing.com",
		},
		{
			name:     "什么都没配",
			settings: `{}`,
			want:     "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := RealitySNI(mkIn(c.settings, "reality")); got != c.want {
				t.Errorf("RealitySNI = %q，期望 %q", got, c.want)
			}
		})
	}
}

func TestRealityDestPortFill(t *testing.T) {
	cases := []struct {
		name     string
		settings string
		want     string
	}{
		{
			// 面板表单的默认值就是裸域名，必须补 443，否则 xray 判非法 target
			name:     "裸域名补默认端口",
			settings: `{"handshake_server":"www.apple.com"}`,
			want:     "www.apple.com:443",
		},
		{
			name:     "自定义端口",
			settings: `{"handshake_server":"www.apple.com","handshake_port":8443}`,
			want:     "www.apple.com:8443",
		},
		{
			name:     "已带端口则原样保留",
			settings: `{"handshake_server":"www.apple.com:2053","handshake_port":443}`,
			want:     "www.apple.com:2053",
		},
		{
			name:     "只配了 server_name 时也当作 dest",
			settings: `{"server_name":"www.apple.com"}`,
			want:     "www.apple.com:443",
		},
		{
			name:     "非法端口回退 443",
			settings: `{"handshake_server":"www.apple.com","handshake_port":99999}`,
			want:     "www.apple.com:443",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := RealityDest(mkIn(c.settings, "reality")); got != c.want {
				t.Errorf("RealityDest = %q，期望 %q", got, c.want)
			}
		})
	}
}
