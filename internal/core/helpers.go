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
	// xray 原生写法 / 结构残留：用户在 settings 里直接贴 xray 片段时会出现。
	// 它们由渲染器结构化消费（dest → realitySettings.dest），不能原样留在
	// 入站顶层——xray 会宽容忽略，sing-box 则严格拒收（unknown field），
	// 表现为「xray 好好的，一切到 sing-box 就校验失败」。
	// streamSettings 本身是合法的入站顶层字段（渲染器会自己写），不能列进来；
	// 它的内层结构（tlsSettings 等）不是，透传上去只会污染。
	"dest", "serverNames", "realitySettings", "tlsSettings", "wsSettings",
	// sniffing / allocate 刻意**不在此列**：它们是 xray 的入站顶层字段，
	// 由渲染器原样透传；列进来会被 StripManagedKeys 删掉。
}

// StripManagedKeys 从透传合并后的对象中删除面板托管键。
func StripManagedKeys(obj map[string]any) {
	for _, k := range ManagedSettingsKeys {
		delete(obj, k)
	}
}

// xrayOnlyKeys 是「只属于 xray 入站顶层、别的内核对它是未知字段」的键。
//
// 它们**不能**进 ManagedSettingsKeys：xray 渲染器要把 sniffing / allocate
// 原样透传下去，列进托管键就会被删掉，导入的 3x-ui 入站会丢掉协议嗅探。
// 但反过来，同一条入站切到 sing-box / mihomo 时，这两个键就成了未知字段：
// sing-box 严格校验，直接报
//
//	inbounds[0].sniffing: json: unknown field "sniffing"
//
// 于是「从 3x-ui 导入 → 想切到 sing-box 内核」这条最常见的路径必然失败，
// 而报错只说"校验失败"，看不出是哪个字段、更看不出它是 xray 专属的。
var xrayOnlyKeys = []string{"sniffing", "allocate"}

// StripXrayOnlyKeys 删掉 xray 专属的入站顶层字段（sing-box / mihomo 渲染用）。
func StripXrayOnlyKeys(obj map[string]any) {
	for _, k := range xrayOnlyKeys {
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
//
// 取值顺序：handshake_server → dest → server_name → server_names 第一个。
// 中间的 `dest` 是 xray 原生字段名，导入的片段 / 手工贴进来的 settings 里常见。
// 不认它会渲染出「没有 dest 的 realitySettings」，xray 26 对此的报错是
// `non-empty "serverNames", please use "serverName" instead`——看起来在说 SNI
// 字段名写错了，其实原因是缺 dest，会把排查方向带偏。
//
// 末尾用 server_names 兜底是为了跟渲染器反向的兜底对称：xray 渲染时
// serverNames 缺失会用 dest 的主机名，那么 dest 缺失时就该用 serverNames 的
// 第一个。否则「只填了 SNI 的 xray 入站」切到 sing-box 会得到空 handshake
// （sing-box check 不拦空串，面板显示已启用，客户端永远握不上手）。
func RealityDest(in *model.Inbound) string {
	dest := Field(in.Settings, "handshake_server")
	if dest == "" {
		dest = Field(in.Settings, "dest")
	}
	if dest == "" {
		dest = Field(in.Settings, "server_name")
	}
	if dest == "" {
		if names := FieldList(in.Settings, "server_names"); len(names) > 0 {
			dest = names[0]
		}
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

// RealityDestParts 把 RealityDest 拆成主机与端口。
// 两个渲染器都需要拆分后的值（xray 要 host:port，sing-box 要 host + server_port），
// 各自手写一次就是两处漂移的起点。
func RealityDestParts(in *model.Inbound) (host string, port int) {
	dest := RealityDest(in)
	if dest == "" {
		return "", 0
	}
	h, p, err := net.SplitHostPort(dest)
	if err != nil {
		return strings.Trim(dest, "[]"), 443
	}
	n, err := strconv.Atoi(p)
	if err != nil || n <= 0 || n > 65535 {
		return h, 443
	}
	return h, n
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
