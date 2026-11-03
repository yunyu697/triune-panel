package api

import (
	"testing"

	"triune/internal/link"
	"triune/internal/model"
)

// 传输参数在 settings 里是 snake_case，而 link.Node 的 Extra 用 camelCase。
// 转换时漏掉一个键，客户端配置就会少一项，而服务端照常渲染——两端不一致，
// 表现是「节点能加载、一连就不通」，且没有任何日志能指出是哪一项。
//
// gRPC 的服务名踩的就是这个：`inboundToNode` 只读 serviceName，而表单与
// 导入器写的是 service_name，于是导出的 proxy 只有 `network: grpc`、
// 没有 `grpc-opts`，客户端按空服务名握手，服务端按 gsvc 分流。
func TestGrpcServiceNameReachesClientConfig(t *testing.T) {
	a := newAuthTestServer(t)
	in := &model.Inbound{
		Name: "gr-01", Protocol: "vless", Port: 24447, Transport: "grpc", TLS: "none",
		Settings: `{"service_name":"gsvc"}`,
	}
	n := a.inboundToNode(in, &model.Client{Name: "u1", Credential: "uuid-1"}, "1.2.3.4")

	p, err := link.ToClash(n)
	if err != nil {
		t.Fatal(err)
	}
	if p["network"] != "grpc" {
		t.Fatalf("network = %v，期望 grpc", p["network"])
	}
	po, _ := p["grpc-opts"].(map[string]any)
	if po["grpc-service-name"] != "gsvc" {
		t.Fatalf("mihomo 的 grpc-opts.grpc-service-name = %v，期望 gsvc（服务名丢了）", po)
	}

	o, err := link.ToSingboxOutbound(n)
	if err != nil {
		t.Fatal(err)
	}
	tr, _ := o["transport"].(map[string]any)
	if tr["service_name"] != "gsvc" {
		t.Fatalf("sing-box 的 transport.service_name = %v，期望 gsvc", tr)
	}
}

// 驼峰写法（早期手写 settings / 直接调 API 的脚本）也要认。
func TestGrpcServiceNameAcceptsCamelCase(t *testing.T) {
	a := newAuthTestServer(t)
	in := &model.Inbound{
		Name: "gr-02", Protocol: "vless", Port: 24448, Transport: "grpc", TLS: "none",
		Settings: `{"serviceName":"camelsvc"}`,
	}
	n := a.inboundToNode(in, &model.Client{Name: "u1", Credential: "uuid-1"}, "1.2.3.4")
	p, err := link.ToClash(n)
	if err != nil {
		t.Fatal(err)
	}
	po, _ := p["grpc-opts"].(map[string]any)
	if po["grpc-service-name"] != "camelsvc" {
		t.Fatalf("驼峰写法没被识别，grpc-service-name = %v", po)
	}
}
