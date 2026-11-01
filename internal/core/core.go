// Package core 定义内核抽象与应用流水线。
//
// 核心思想：面板 DB 是唯一真相源（用户意图），
// 运行时配置文件只是“渲染产物”。产物必须先通过内核自带的
// 校验命令，再以原子替换方式落盘 —— 校验不过，运行中的配置
// 一个字节都不会被改动。
package core

import (
	"fmt"
	"os/exec"

	"triune/internal/model"
)

// InboundWithClients 渲染输入：一个入站及其关联用户。
type InboundWithClients struct {
	Inbound *model.Inbound
	Clients []*model.Client
}

// RenderOpts 全局渲染选项（由流水线从设置中读出注入）。
type RenderOpts struct {
	StatsEnabled bool   // 是否注入流量统计配置
	StatsSecret  string // 统计 API 鉴权密钥
	// DNSJSON / RouteJSON 是「随入站一起导入的全局段」，内核原生 JSON。
	// 空字符串表示没有导入过，渲染时保持面板默认（不写 dns / route）。
	DNSJSON   string
	RouteJSON string
}

// Core 内核适配器接口。
type Core interface {
	// Name 内核标识（singbox/xray/mihomo）。
	Name() string
	// Ext 配置文件扩展名（决定语法：json/yaml）。
	Ext() string
	// FileName 运行时配置文件名。
	FileName() string
	// Render 把入站意图渲染为完整配置文件内容。
	Render(ins []InboundWithClients, opts RenderOpts) ([]byte, error)
	// ValidateCmd 构造内核自带的配置校验命令。
	ValidateCmd(binPath, cfgPath string) *exec.Cmd
}

var registry = map[string]Core{}

func Register(c Core) { registry[c.Name()] = c }

func Get(name string) (Core, error) {
	c, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown core %q (supported: singbox, xray, mihomo)", name)
	}
	return c, nil
}

// All 返回已注册的内核，顺序固定为 sing-box → xray → mihomo。
// 未注册（未 import 对应实现包）的内核会被跳过而不是返回 nil 元素——
// 否则调用方 range 后直接 c.Name() 会空指针 panic（单测里尤其常见）。
func All() []Core {
	names := []string{model.CoreSingBox, model.CoreXray, model.CoreMihomo}
	out := make([]Core, 0, len(names))
	for _, n := range names {
		if c, ok := registry[n]; ok && c != nil {
			out = append(out, c)
		}
	}
	return out
}
