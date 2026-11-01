package importer

import (
	"strings"
	"testing"
)

// 3x-ui 2.x 的入站导出：settings / streamSettings 是**字符串**形式的 JSON。
const xuiExport = `[
  {
    "id": 1, "remark": "vless-reality", "enable": true,
    "listen": "", "port": 443, "protocol": "vless",
    "settings": "{\"clients\":[{\"id\":\"11111111-2222-3333-4444-555555555555\",\"flow\":\"xtls-rprx-vision\",\"email\":\"alice\",\"totalGB\":107374182400,\"expiryTime\":1798761600000,\"enable\":true},{\"id\":\"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee\",\"email\":\"bob\",\"enable\":false}],\"decryption\":\"none\",\"fallbacks\":[]}",
    "streamSettings": "{\"network\":\"tcp\",\"security\":\"reality\",\"realitySettings\":{\"show\":false,\"xver\":0,\"dest\":\"www.apple.com:8443\",\"serverNames\":[\"www.apple.com\",\"apple.com\"],\"privateKey\":\"PRIVKEY\",\"shortIds\":[\"ab12\",\"\"],\"spiderX\":\"/x\",\"maxTimeDiff\":60000,\"settings\":{\"publicKey\":\"PUBKEY\",\"fingerprint\":\"chrome\"}}}",
    "tag": "inbound-443",
    "sniffing": "{\"enabled\":true,\"destOverride\":[\"http\",\"tls\"]}",
    "allocate": "{\"strategy\":\"always\",\"refresh\":5,\"concurrency\":3}"
  },
  {
    "id": 2, "remark": "vmess-ws", "port": 8080, "protocol": "vmess",
    "settings": "{\"clients\":[{\"id\":\"99999999-8888-7777-6666-555555555555\",\"email\":\"carol\",\"enable\":true}]}",
    "streamSettings": "{\"network\":\"ws\",\"security\":\"tls\",\"wsSettings\":{\"path\":\"/ray\",\"host\":\"cdn.example.com\"},\"tlsSettings\":{\"serverName\":\"cdn.example.com\",\"alpn\":[\"h2\",\"http/1.1\"],\"certificates\":[{\"certificateFile\":\"/etc/certs/full.pem\",\"keyFile\":\"/etc/certs/key.pem\"}]}}",
    "tag": "inbound-8080"
  },
  {
    "id": 3, "remark": "ss-2022", "port": 8388, "protocol": "shadowsocks",
    "settings": "{\"method\":\"2022-blake3-aes-256-gcm\",\"password\":\"SERVERPASS\",\"network\":\"tcp,udp\",\"clients\":[{\"password\":\"KEY1\",\"email\":\"dave\",\"enable\":true}]}",
    "streamSettings": "{\"network\":\"tcp\",\"security\":\"none\"}"
  },
  {
    "id": 4, "remark": "dokodemo", "port": 9999, "protocol": "dokodemo-door",
    "settings": "{\"address\":\"1.1.1.1\",\"port\":80,\"network\":\"tcp\",\"followRedirect\":true}", "streamSettings": "{}"
  }
]`

func TestParse3xuiExport(t *testing.T) {
	ibs, err := Parse(xuiExport)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(ibs) != 4 {
		t.Fatalf("应解析出 4 条，实际 %d", len(ibs))
	}

	// 1) vless + reality：dest 拆分、多 serverNames、多 shortIds、私钥、pbk
	r := ibs[0]
	if r.Name != "vless-reality" || r.Protocol != "vless" || r.Port != 443 {
		t.Errorf("基础字段错：%+v", r)
	}
	if r.TLS != "reality" || r.Transport != "tcp" {
		t.Errorf("TLS/传输错：%s/%s", r.TLS, r.Transport)
	}
	want := map[string]string{
		"handshake_server": "www.apple.com",
		"server_names":     "www.apple.com,apple.com",
		"private_key":      "PRIVKEY",
		"short_id":         "ab12",
		"spider_x":         "/x",
		"pbk":              "PUBKEY",
		"flow":             "xtls-rprx-vision",
	}
	for k, v := range want {
		got, _ := r.Settings[k].(string)
		if got != v {
			t.Errorf("settings[%s] = %q，期望 %q", k, got, v)
		}
	}
	// shortIds 里的空串是有意义的值（xray 允许空短 ID），必须原样保留
	sids, ok := r.Settings["short_ids"].([]string)
	if !ok || len(sids) != 2 || sids[0] != "ab12" || sids[1] != "" {
		t.Errorf("short_ids = %#v，期望 [ab12 \"\"]", r.Settings["short_ids"])
	}
	if port, _ := r.Settings["handshake_port"].(int); port != 8443 {
		t.Errorf("handshake_port = %v，期望 8443", r.Settings["handshake_port"])
	}
	if mtd, _ := r.Settings["max_time_diff"].(int); mtd != 60000 {
		t.Errorf("max_time_diff = %v，期望 60000", r.Settings["max_time_diff"])
	}
	if len(r.Clients) != 2 {
		t.Fatalf("应有 2 个客户端，实际 %d", len(r.Clients))
	}
	if r.Clients[0].Name != "alice" || r.Clients[0].Credential != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("客户端映射错：%+v", r.Clients[0])
	}
	if r.Clients[0].QuotaGB != 100 { // 107374182400 字节 = 100 GB
		t.Errorf("配额 = %v GB，期望 100", r.Clients[0].QuotaGB)
	}
	if r.Clients[0].ExpiresAt == nil {
		t.Error("到期时间丢了")
	}
	if r.Clients[1].Enabled {
		t.Error("enable=false 的客户端应导入为停用")
	}
	// sniffing/allocate 只透传给 xray 目标内核
	if _, ok := r.PassThrough["sniffing"]; !ok {
		t.Error("sniffing 应保留在 PassThrough 里")
	}

	// 2) vmess + ws + tls：path/host/alpn/证书路径
	w := ibs[1]
	if w.Transport != "ws" || w.TLS != "tls" {
		t.Errorf("ws/tls 映射错：%s/%s", w.Transport, w.TLS)
	}
	for k, v := range map[string]string{
		"path": "/ray", "ws_host": "cdn.example.com", "server_name": "cdn.example.com",
		"alpn": "h2,http/1.1", "certificate_path": "/etc/certs/full.pem", "key_path": "/etc/certs/key.pem",
	} {
		got, _ := w.Settings[k].(string)
		if got != v {
			t.Errorf("ws settings[%s] = %q，期望 %q", k, got, v)
		}
	}

	// 3) shadowsocks 2022：method + 多用户密码
	s := ibs[2]
	if s.Settings["method"] != "2022-blake3-aes-256-gcm" {
		t.Errorf("ss method 错：%v", s.Settings["method"])
	}
	if len(s.Clients) != 1 || s.Clients[0].Credential != "KEY1" {
		t.Errorf("ss 客户端错：%+v", s.Clients)
	}

	// 4) dokodemo-door（端口转发）：不再跳过，目标地址/端口要映射出来
	if ibs[3].Skip {
		t.Errorf("dokodemo-door 不该再被跳过：%+v", ibs[3])
	}
	if got := ibs[3].Settings["dd_address"]; got != "1.1.1.1" {
		t.Errorf("dokodemo-door dd_address = %v，期望 1.1.1.1", got)
	}
	if got := ibs[3].Settings["dd_port"]; got != 80 {
		t.Errorf("dokodemo-door dd_port = %v，期望 80", got)
	}
}

// 原生 xray 配置：inbounds 段，字段本身就是 JSON 对象。
const xrayConfig = `{
  "log": {"loglevel": "warning"},
  "inbounds": [
    {
      "tag": "vless-tcp-tls",
      "listen": "0.0.0.0",
      "port": 8443,
      "protocol": "vless",
      "settings": {"clients": [{"id": "12345678-1234-1234-1234-123456789012", "email": "u1"}], "decryption": "none", "fallbacks": [{"dest": 80, "alpn": ""}]},
      "streamSettings": {
        "network": "tcp",
        "security": "tls",
        "tlsSettings": {"serverName": "example.com", "certificates": [{"certificateFile": "/c.pem", "keyFile": "/k.pem"}]}
      },
      "sniffing": {"enabled": true, "destOverride": ["http", "tls"]}
    }
  ],
  "outbounds": [{"protocol": "freedom"}]
}`

func TestParseXrayConfig(t *testing.T) {
	ibs, err := Parse(xrayConfig)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(ibs) != 1 {
		t.Fatalf("应解析出 1 条，实际 %d", len(ibs))
	}
	ib := ibs[0]
	if ib.Name != "vless-tcp-tls" || ib.Port != 8443 || ib.Listen != "0.0.0.0" {
		t.Errorf("基础字段错：%+v", ib)
	}
	if ib.Settings["server_name"] != "example.com" {
		t.Errorf("server_name 错：%v", ib.Settings["server_name"])
	}
	if ib.Settings["certificate_path"] != "/c.pem" || ib.Settings["key_path"] != "/k.pem" {
		t.Errorf("证书路径错：%v / %v", ib.Settings["certificate_path"], ib.Settings["key_path"])
	}
	if _, ok := ib.Settings["fallbacks"].([]any); !ok {
		t.Errorf("fallbacks 应保留：%T %v", ib.Settings["fallbacks"], ib.Settings["fallbacks"])
	}
	if len(ib.Clients) != 1 || ib.Clients[0].Credential != "12345678-1234-1234-1234-123456789012" {
		t.Errorf("客户端错：%+v", ib.Clients)
	}
}

func TestParseSingleObject(t *testing.T) {
	ibs, err := Parse(`{"remark":"单个","port":2096,"protocol":"trojan","settings":"{\"clients\":[{\"password\":\"pw1\"}]}","streamSettings":"{\"network\":\"ws\",\"security\":\"tls\",\"wsSettings\":{\"path\":\"/t\"},\"tlsSettings\":{\"serverName\":\"t.example.com\"}}"}`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(ibs) != 1 || ibs[0].Protocol != "trojan" || ibs[0].Transport != "ws" {
		t.Fatalf("单个对象解析错：%+v", ibs[0])
	}
	if ibs[0].Clients[0].Credential != "pw1" {
		t.Errorf("trojan 密码错：%+v", ibs[0].Clients)
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if _, err := Parse("SQLite format 3\x00rest"); err == nil ||
		!strings.Contains(err.Error(), "SQLite") {
		t.Errorf("SQLite 备份应给出可读提示，实际 %v", err)
	}
	if _, err := Parse("not json at all"); err == nil {
		t.Error("非 JSON 应报错")
	}
	if _, err := Parse(`{"foo": 1}`); err == nil {
		t.Error("结构性 JSON 但无 inbounds 应报错")
	}
}

// Reality 缺私钥时必须明确提示，而不是导入一个渲染就崩的入站。
func TestParseRealityWithoutKeyWarns(t *testing.T) {
	ibs, err := Parse(`{"remark":"r","port":443,"protocol":"vless","settings":"{}","streamSettings":"{\"network\":\"tcp\",\"security\":\"reality\",\"realitySettings\":{\"dest\":\"www.apple.com:443\",\"serverNames\":[\"www.apple.com\"]}}"}`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var found bool
	for _, w := range ibs[0].Warnings {
		if strings.Contains(w, "私钥") {
			found = true
		}
	}
	if !found {
		t.Errorf("缺私钥应给出警告，实际 %v", ibs[0].Warnings)
	}
}

// 新一轮补齐的协议与传输：socks/http 账号、dokodemo 端口转发、wireguard 透传、
// h2/httpupgrade/xhttp/kcp/quic 传输、内联 PEM 证书、DNS 与路由全局段。
const xuiExtras = `[
  {"remark":"socks5","port":1080,"protocol":"socks",
   "settings":"{\"auth\":\"password\",\"udp\":true,\"accounts\":[{\"user\":\"u1\",\"pass\":\"p1\"},{\"user\":\"u2\",\"pass\":\"p2\"}]}",
   "streamSettings":"{}"},
  {"remark":"http-proxy","port":8080,"protocol":"http",
   "settings":"{\"accounts\":[{\"user\":\"h1\",\"pass\":\"hp1\"}]}","streamSettings":"{}"},
  {"remark":"wg","port":51820,"protocol":"wireguard",
   "settings":"{\"secretKey\":\"SECRET\",\"mtu\":1420,\"peers\":[{\"publicKey\":\"PUB\",\"allowedIPs\":[\"10.0.0.2/32\"]}]}",
   "streamSettings":"{}"},
  {"remark":"h2-node","port":4443,"protocol":"vless",
   "settings":"{\"clients\":[{\"id\":\"u-uuid\",\"email\":\"a\"}]}",
   "streamSettings":"{\"network\":\"h2\",\"security\":\"tls\",\"httpSettings\":{\"path\":\"/h2\",\"host\":[\"h2.example.com\"]},\"tlsSettings\":{\"certificates\":[{\"certificate\":[\"-----BEGIN CERTIFICATE-----\",\"MIIB\",\"-----END CERTIFICATE-----\"],\"key\":[\"-----BEGIN PRIVATE KEY-----\",\"MIIE\",\"-----END PRIVATE KEY-----\"]}]}}"},
  {"remark":"hu-node","port":4444,"protocol":"vless",
   "settings":"{\"clients\":[{\"id\":\"u2-uuid\"}]}",
   "streamSettings":"{\"network\":\"httpupgrade\",\"httpupgradeSettings\":{\"path\":\"/hu\",\"host\":\"hu.example.com\"}}"},
  {"remark":"xhttp-node","port":4445,"protocol":"vless",
   "settings":"{\"clients\":[{\"id\":\"u3-uuid\"}]}",
   "streamSettings":"{\"network\":\"xhttp\",\"xhttpSettings\":{\"path\":\"/xh\",\"host\":\"x.example.com\",\"mode\":\"packet-up\"}}"},
  {"remark":"kcp-node","port":4446,"protocol":"vmess",
   "settings":"{\"clients\":[{\"id\":\"k-uuid\"}]}",
   "streamSettings":"{\"network\":\"kcp\",\"kcpSettings\":{\"seed\":\"s3\",\"header\":{\"type\":\"srtp\"}}}"},
  {"remark":"quic-node","port":4447,"protocol":"vmess",
   "settings":"{\"clients\":[{\"id\":\"q-uuid\"}]}",
   "streamSettings":"{\"network\":\"quic\",\"quicSettings\":{\"security\":\"aes-128-gcm\",\"key\":\"qk\",\"header\":{\"type\":\"dtls\"}}}"}
]`

func TestParseNewProtocolsAndTransports(t *testing.T) {
	ibs, err := Parse(xuiExtras)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	byName := map[string]*Inbound{}
	for _, ib := range ibs {
		byName[ib.Name] = ib
		if ib.Skip {
			t.Errorf("%s 不应被跳过：%v", ib.Name, ib.Warnings)
		}
	}

	// socks：账号 → user:pass 凭据，auth/udp 落 settings
	s := byName["socks5"]
	if s.Settings["account_auth"] != "password" || s.Settings["udp"] != true {
		t.Errorf("socks auth/udp 映射错：%+v", s.Settings)
	}
	if len(s.Clients) != 2 || s.Clients[0].Credential != "u1:p1" || s.Clients[0].Name != "u1" {
		t.Errorf("socks accounts 映射错：%+v", s.Clients)
	}

	// http：同样按 accounts 取凭据
	h := byName["http-proxy"]
	if len(h.Clients) != 1 || h.Clients[0].Credential != "h1:hp1" {
		t.Errorf("http accounts 映射错：%+v", h.Clients)
	}

	// wireguard：整段原样透传，没有用户
	w := byName["wg"]
	if _, ok := w.Settings["xray_raw_settings"]; !ok {
		t.Errorf("wireguard 应保留原始 settings：%+v", w.Settings)
	}
	if len(w.Clients) != 0 {
		t.Errorf("wireguard 不该产生用户：%+v", w.Clients)
	}

	// 传输映射
	// 传输名保持中立（h2 / quic 保留原名），由 API 层按目标内核决定落地方式
	for name, want := range map[string]string{
		"h2-node": "h2", "hu-node": "httpupgrade", "xhttp-node": "xhttp",
		"kcp-node": "kcp", "quic-node": "quic",
	} {
		if got := byName[name].Transport; got != want {
			t.Errorf("%s 传输 = %q，期望 %q", name, got, want)
		}
	}
	if v := byName["h2-node"].Settings["path"]; v != "/h2" {
		t.Errorf("h2 path = %v", v)
	}
	if v := byName["h2-node"].Settings["ws_host"]; v != "h2.example.com" {
		t.Errorf("h2 host = %v", v)
	}
	if v := byName["xhttp-node"].Settings["xhttp_mode"]; v != "packet-up" {
		t.Errorf("xhttp mode = %v", v)
	}
	if v := byName["kcp-node"].Settings["kcp_seed"]; v != "s3" {
		t.Errorf("kcp seed = %v", v)
	}
	if v := byName["kcp-node"].Settings["kcp_header"]; v != "srtp" {
		t.Errorf("kcp header = %v", v)
	}
	if v := byName["quic-node"].Settings["quic_security"]; v != "aes-128-gcm" {
		t.Errorf("quic security = %v", v)
	}

	// 内联 PEM：不再丢成一句「不支持」，而是保留 PEM 供写库时落盘
	pem, _ := byName["h2-node"].Settings["certificate_pem"].(string)
	if !strings.Contains(pem, "BEGIN CERTIFICATE") || !strings.Contains(pem, "MIIB") {
		t.Errorf("内联证书 PEM 应保留：%q", pem)
	}
	if kp, _ := byName["h2-node"].Settings["key_pem"].(string); !strings.Contains(kp, "BEGIN PRIVATE KEY") {
		t.Errorf("内联私钥 PEM 应保留：%q", kp)
	}
}

// 整份 xray 配置：inbounds 之外的 dns / routing / outbounds 也要能用。
func TestParseGlobalDNSAndRouting(t *testing.T) {
	cfg := `{
	  "dns": {"servers": ["1.1.1.1", {"address": "8.8.8.8", "domains": ["geosite:cn"]}], "queryStrategy": "UseIP"},
	  "routing": {"domainStrategy": "IPIfNonMatch", "rules": [
	    {"type": "field", "domain": ["geosite:cn"], "outboundTag": "direct"},
	    {"type": "field", "ip": ["geoip:private"], "outboundTag": "block"},
	    {"type": "field", "domain": ["geosite:google"], "outboundTag": "proxy"}
	  ]},
	  "inbounds": [{"tag":"in1","port":443,"protocol":"vless","settings":{"clients":[{"id":"x"}]},"streamSettings":{"network":"tcp"}}],
	  "outbounds": [{"protocol":"freedom"},{"protocol":"vmess","tag":"proxy"}]
	}`
	r, err := ParseFull(cfg)
	if err != nil {
		t.Fatalf("ParseFull: %v", err)
	}
	if r.Global == nil {
		t.Fatal("应解析出全局段")
	}
	if servers, ok := r.Global.DNS["servers"].([]any); !ok || len(servers) != 2 {
		t.Errorf("DNS servers 应保留：%#v", r.Global.DNS["servers"])
	}
	rules, _ := r.Global.Routing["rules"].([]any)
	if len(rules) != 2 {
		t.Fatalf("应只保留 direct/block 两条规则，实际 %d：%#v", len(rules), rules)
	}
	if rules[0].(map[string]any)["outboundTag"] != nil {
		t.Errorf("direct 规则的 outboundTag 应去掉（默认即 direct）：%#v", rules[0])
	}
	if rules[1].(map[string]any)["outboundTag"] != "block" {
		t.Errorf("block 规则应统一成 block：%#v", rules[1])
	}
	joined := strings.Join(r.Global.Warnings, " | ")
	if !strings.Contains(joined, "代理出站") || !strings.Contains(joined, "路由规则") {
		t.Errorf("丢弃的代理规则/出站应有提示：%v", r.Global.Warnings)
	}
}
