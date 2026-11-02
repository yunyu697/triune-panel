package core

import (
	"encoding/json"
	"net"
	"os/exec"
	"strconv"
	"strings"

	"triune/internal/model"
)

// ExecCmdOf 构造命令（供各内核实现 ValidateCmd 使用）。
func ExecCmdOf(bin string, args []string) *exec.Cmd { return exec.Command(bin, args...) }

// TagOf 生成入站 tag：优先用户指定，否则 in-<id>。
func TagOf(in *model.Inbound) string {
	if in.Tag != "" {
		return in.Tag
	}
	return "in-" + itoa(in.ID)
}

// ListenOrAll 空监听地址回退为全接口。
func ListenOrAll(l string) string {
	if l == "" {
		return "::"
	}
	return l
}

// ManagedSettingsKeys 面板表单托管 / 仅客户端使用的 settings 键。
// 这些键由渲染器按结构消费（写入 transport/tls/reality 等正确位置）
// 或直接剔除（如 pbk 仅客户端分享用），绝不原样并入内核配置顶层——
// sing-box 对未知字段是严格校验的，多余顶层键会导致配置校验失败。
var ManagedSettingsKeys = []string{
	"path", "service_name", "server_name", "server_names", "certificate_path", "key_path",
	"certificate_pem", "key_pem",
	"handshake_server", "handshake_port", "private_key", "short_id", "short_ids",
	"spider_x", "max_time_diff", "alpn", "ws_host", "ss_network", "accept_proxy_protocol",
	"method", "flow", "pbk", "fallbacks",
	// socks / http
	"account_auth", "udp",
	// dokodemo-door（端口转发）
	"dd_address", "dd_port", "dd_network", "dd_follow_redirect",
	// 新增传输的附加参数
	"xhttp_mode", "kcp_seed", "kcp_header", "quic_security", "quic_key", "quic_header",
	// WireGuard 的原始 settings（渲染时整体替换 settings 段）
	"xray_raw_settings",
	// sniffing / allocate 刻意**不在此列**：它们是 xray 的入站顶层字段，
	// 由渲染器原样透传；列进来会被 StripManagedKeys 删掉。
}

// StripManagedKeys 从透传合并后的对象中删除面板托管键。
func StripManagedKeys(obj map[string]any) {
	for _, k := range ManagedSettingsKeys {
		delete(obj, k)
	}
}

// Field 从入站 settings JSON 中取字符串字段（含一层嵌套对象平铺查找）。
func Field(settings, key string) string {
	if settings == "" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(settings), &m); err != nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	// 平铺查找一层嵌套，如 tls.server_name
	for _, v := range m {
		if sub, ok := v.(map[string]any); ok {
			if s, ok := sub[key].(string); ok {
				return s
			}
		}
	}
	return ""
}

// ShortIDs 统一读取短 ID 列表，兼容三种写法：
// 数组（导入的配置）、逗号/空格串（表单填写）、老的单个 short_id。
// 空数组时返回 [""]——xray 的 shortIds 至少要有数组，空短 ID 本身合法。
func ShortIDs(settings string) []string {
	switch v := FieldRaw(settings, "short_ids").(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, it := range v {
			out = append(out, strings.TrimSpace(anyToString(it)))
		}
		if len(out) > 0 {
			return out
		}
	case string:
		if s := strings.TrimSpace(v); s != "" {
			parts := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == ';' })
			if len(parts) > 0 {
				return parts
			}
		}
	}
	if sid := Field(settings, "short_id"); sid != "" {
		return []string{sid}
	}
	return []string{""}
}

// RealityDest 返回 Reality 的握手目标 host:port（dest）。
// 表单只填域名时补默认端口，否则 xray 判为非法 target。
func RealityDest(in *model.Inbound) string {
	dest := Field(in.Settings, "handshake_server")
	if dest == "" {
		dest = Field(in.Settings, "server_name")
	}
	if dest == "" {
		return ""
	}
	port := FieldInt(in.Settings, "handshake_port")
	if port <= 0 || port > 65535 {
		port = 443
	}
	if _, _, err := net.SplitHostPort(dest); err == nil {
		return dest
	}
	return net.JoinHostPort(strings.Trim(dest, "[]"), strconv.Itoa(port))
}

// RealitySNI 返回 Reality 入站实际生效的 serverNames 第一个值。
//
// 取值顺序必须与渲染器一致（server_names → server_name → dest 的主机名）。
// 诊断探测用错 SNI 不是「小误差」：Reality 对不在 serverNames 里的 SNI 会按
// 设计拒绝握手（`alert handshake failure`），于是配置完全正常也会被报成
// 「TLS 握手失败」——曾经就是这样，探测只读 server_name，而表单存的是
// 复数 server_names，导致 Reality 入站的握手检查全是误报。
func RealitySNI(in *model.Inbound) string {
	names := FieldList(in.Settings, "server_names")
	if len(names) == 0 {
		names = FieldList(in.Settings, "server_name")
	}
	if len(names) > 0 {
		return names[0]
	}
	if host, _, err := net.SplitHostPort(RealityDest(in)); err == nil {
		return host
	}
	return ""
}

// anyToString 把 JSON 解出来的标量转成字符串。
func anyToString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	}
	return ""
}

// FieldRaw 取原始 JSON 值（数组/对象也能取），给 fallbacks 这类结构化字段用。
func FieldRaw(settings, key string) any {
	if settings == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(settings), &m); err != nil {
		return nil
	}
	return m[key]
}

// FieldInt 取整数字段：settings 里的值可能是数字（导入的配置）或字符串
// （表单填的），两种都要认，否则「导入后端口/时间差丢了」这种问题很难查。
func FieldInt(settings, key string) int {
	if settings == "" {
		return 0
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(settings), &m); err != nil {
		return 0
	}
	return anyToInt(m[key])
}

// FieldList 取多值字段：逗号、空格、分号分隔都认（"a.com,b.com" / "a.com b.com"）。
// 空串与重复值会被去掉，保持顺序。
func FieldList(settings, key string) []string {
	var parts []string
	// 数组形式（导入的配置，如 "serverNames": ["a.com","b.com"]）必须认，
	// 否则整组 SNI 会被丢弃、只剩 dest 兜底的那一个——客户端换用另一个
	// 合法 SNI 就连不上。只认字符串是曾经的漏洞。
	if arr, ok := FieldRaw(settings, key).([]any); ok {
		for _, it := range arr {
			parts = append(parts, anyToString(it))
		}
	} else if s := Field(settings, key); s != "" {
		// 字符串形式：逗号 / 空格 / 分号 / 换行都能分隔（表单里手写的）
		parts = strings.FieldsFunc(s, func(r rune) bool {
			return r == ',' || r == ' ' || r == ';' || r == '\n' || r == '\t'
		})
	}
	if len(parts) == 0 {
		return nil
	}
	out := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" && key != "short_ids" {
			continue // short_id 允许空串（xray 的 "" 是合法短 ID）
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

func anyToInt(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(t))
		return n
	case bool:
		if t {
			return 1
		}
	}
	return 0
}

// SplitUserPass 解析 socks / http 的用户凭据。
// 面板把这对值存成 "user:pass"（导出的 xray 配置就是这种形态）；
// 没有冒号时按「用户名取用户名称、密码取凭据」处理，
// 这样手填的 socks 用户（名称 u1 + 凭据 p1）也能得到 u1/p1。
func SplitUserPass(cred, name string) (user, pass string) {
	if i := strings.Index(cred, ":"); i > 0 {
		return cred[:i], cred[i+1:]
	}
	if name == "" {
		name = "user"
	}
	return name, cred
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
