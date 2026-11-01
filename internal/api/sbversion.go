package api

import (
	"fmt"
	"strconv"
	"strings"
)

// ==================== sing-box 版本与配置能力门限 ====================
//
// 同一份 JSON 不能跨版本通用：sing-box 对未知字段是严格校验的，多一个字段
// 就是 FATAL 起不来；少一个字段则行为不对（比如 DNS 不回落到代理侧）。
//
// 实测依据（真实二进制 `sing-box check -c`，2026-11）：
//
//	1.10.7        FATAL route.rules[0].action: unknown field "action"
//	1.11.15       FATAL dns.servers[0].server: unknown field "server"
//	1.12.25       PASS
//	1.13.21       PASS
//	1.14.4        PASS
//	1.15.0-alpha   PASS（tun.stack 已废弃，尚未移除）
//
// 结论：断点只有 1.11 → 1.12 一处（DNS 格式 + default_domain_resolver）。
// 1.12 / 1.13 / 1.14 的配置逐字节等价——不是「每个版本都不一样」。
// 所以这里不按版本号铺 if-else，而是把「这份配置用到哪些能力」抽成门限：
// 将来出现新断点，只在 capsFor 里加一行，生成器不动。

// sbCaps sing-box 配置能力的门限。
type sbCaps struct {
	// NewDNSFormat 1.12+：dns.servers 用 type + server，
	// 取代 1.11 及以前的 address（"https://8.8.8.8/dns-query"）。
	NewDNSFormat bool
	// DomainResolver 1.12+：route.default_domain_resolver。
	// 1.11 不认识这个字段，写了直接 FATAL。
	DomainResolver bool
	// TunStack 1.10–1.14 写 tun.stack；1.15 起废弃、1.17 移除。
	TunStack bool
}

// sbVersion 只比较主次版本：补丁版从不改变配置 schema。
type sbVersion struct {
	Major, Minor, Patch int
}

func (v sbVersion) String() string {
	if v.Patch > 0 {
		return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	}
	return fmt.Sprintf("%d.%d", v.Major, v.Minor)
}

func (v sbVersion) atLeast(major, minor int) bool {
	if v.Major != major {
		return v.Major > major
	}
	return v.Minor >= minor
}

// minSBVersion 低于这个版本不再生成：1.10 及以前没有路由 action，
// 要退回 block / dns 出站加 inet4_address，那是另一套模板，收益不足。
var minSBVersion = sbVersion{Major: 1, Minor: 11}

// defaultSBVersion 用户没指定时的默认值：当前最新稳定版。
var defaultSBVersion = sbVersion{Major: 1, Minor: 14}

// parseSBVersion 解析 "1.12" / "1.12.25" / "v1.12" / "1.15.0-alpha.13"。
// 空字符串回落到默认版本。
func parseSBVersion(s string) (sbVersion, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	if s == "" {
		return defaultSBVersion, nil
	}
	// 先剥掉预发布后缀（1.15.0-alpha.13 / 1.14.0-rc.2 / 1.14.0+build），
	// 否则 "alpha.13" 里的点会把版本段数撑到四段。
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return sbVersion{}, fmt.Errorf("sing-box 版本应形如 1.12 或 1.12.25，收到 %q", s)
	}
	nums := make([]int, 0, 3)
	for _, p := range parts {
		// 预发布后缀（-alpha.13 / -rc.2）只取数字部分
		if i := strings.IndexFunc(p, func(r rune) bool { return r < '0' || r > '9' }); i >= 0 {
			p = p[:i]
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return sbVersion{}, fmt.Errorf("sing-box 版本 %q 无法解析", s)
		}
		nums = append(nums, n)
	}
	v := sbVersion{Major: nums[0], Minor: nums[1]}
	if len(nums) == 3 {
		v.Patch = nums[2]
	}
	return v, nil
}

// capsFor 版本 → 能力门限。整个版本适配只有这一个函数。
func capsFor(v sbVersion) sbCaps {
	return sbCaps{
		NewDNSFormat:   v.atLeast(1, 12),
		DomainResolver: v.atLeast(1, 12),
		TunStack:       !v.atLeast(1, 15),
	}
}

// versionNote 版本状态说明，直接显示给用户。
// 这里刻意说清「哪个版本已停止维护」「哪个还是预览」「哪个尚未发布」，
// 因为选错版本的代价是客户端起不来，而不是样式不好看。
func versionNote(v sbVersion) string {
	switch {
	case !v.atLeast(1, 12):
		return "1.11 已停止维护（1.11.15 是最后一版），配置使用旧 DNS address 格式与无 default_domain_resolver 的路由。建议 1.12+。"
	case v.atLeast(1, 16):
		return "1.16 尚未发布，按 1.15 的规则生成（不写 tun.stack）。正式版发布后需按官方迁移文档复核。"
	case v.atLeast(1, 15):
		return "1.15 仍是预览版（alpha），配置不写 tun.stack（该字段自 1.15 起废弃、1.17 移除）。"
	}
	return ""
}
