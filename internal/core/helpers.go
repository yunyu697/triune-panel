package core

import (
	"encoding/json"
	"os/exec"

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
	"path", "service_name", "server_name", "certificate_path", "key_path",
	"handshake_server", "server_names", "private_key", "short_id",
	"method", "flow", "pbk",
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
