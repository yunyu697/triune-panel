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
    "settings": "{}", "streamSettings": "{}"
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

	// 4) 不支持的协议：跳过但保留条目与原因
	if !ibs[3].Skip || len(ibs[3].Warnings) == 0 {
		t.Errorf("dokodemo-door 应标记 Skip 并给原因：%+v", ibs[3])
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
