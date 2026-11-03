package api

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"triune/internal/link"
)

// exportableProtocols 是能导出为**客户端节点**的协议白名单，仅用于报错文案。
//
// 真正的判定仍然交给 ToClash / ToSingboxOutbound——这里列出来只是为了让
// 用户看到「应该改选什么」，不要拿它做逻辑分支。
var exportableProtocols = []string{"vless", "vmess", "trojan", "shadowsocks", "hysteria2", "tuic"}

// splitExport 按目标内核试跑一遍节点导出，返回「导不出去的说明」与「能导出的节点数」。
//
// 为什么需要它：ToClash / ToSingboxOutbound 只认上面那 6 种协议，且各有传输限制。
// mixed、socks、http、dokodemo-door 这些是**本地代理协议**——它们监听在本机，
// 客户端配置里没有对应的 proxy / outbound 类型，没法拿来连远程服务器。此前两个
// 生成器都是 `if p, err := ToClash(n); err == nil { append }` 这样静默跳过，于是：
//
//   - 面板生成配置 → `proxies: []` + 空 proxy-group → 内核直接拒绝，
//     用户只看到「内核拒绝这份配置」，完全不知道是协议本身导不出去；
//   - 订阅端点 → 返回一份 proxies 为空、但 proxy-group 里列着名字的 YAML，
//     mihomo 报 `proxy 'xxx' not found`。
//
// 传输也会导不出去（mihomo 没有 httpupgrade/xhttp），那是另一类原因：
// 换传输就行，不用换协议。两种都塞进同一个「协议不支持」的话里会把用户引错方向，
// 所以这里把传输原因单独标出来。
//
// 所以调用方必须拿这个结果做两件事：一个都导不出时**别生成**，直接给出可读
// 原因；部分导不出时把跳过的项放进 warnings，别让节点无声消失。
func splitExport(nodes []*link.Node, core string) (skipped []string, ok int) {
	bad := map[string]string{} // 标签 → "proxy"（协议导不出）/ "transport"（传输导不出）
	for _, n := range nodes {
		var err error
		if core == "mihomo" {
			_, err = link.ToClash(n)
		} else {
			_, err = link.ToSingboxOutbound(n)
		}
		if err == nil {
			ok++
			continue
		}
		if errors.Is(err, link.ErrTransport) {
			bad[n.Protocol+"（"+n.Network+" 传输）"] = "transport"
		} else {
			bad[n.Protocol] = "proxy"
		}
	}
	skipped = make([]string, 0, len(bad))
	for p := range bad {
		skipped = append(skipped, p)
	}
	sort.Strings(skipped)
	return skipped, ok
}

// hasTransportSkip 判断跳过原因里有没有「传输不支持」。noExportMsg 靠它选文案：
// 说成协议不支持，用户会去换协议，其实只要换传输。
func hasTransportSkip(skipped []string) bool {
	for _, s := range skipped {
		if strings.Contains(s, " 传输）") {
			return true
		}
	}
	return false
}

// coreLabel 内核的显示名。空值或未知值都按 sing-box 处理——生成器那边也是这么兜底的。
func coreLabel(core string) string {
	if core == "mihomo" {
		return "mihomo"
	}
	return "sing-box"
}

// noExportMsg 一个节点都导不出时的说明文案。genconfig 与订阅端点共用，
// 免得同一件事在两处写出两种说法。
func noExportMsg(skipped []string, core string) string {
	if hasTransportSkip(skipped) {
		return "所选入站（" + strings.Join(skipped, "、") + "）无法导出为 " +
			coreLabel(core) + " 客户端节点：这个传输在 " + coreLabel(core) +
			" 的客户端里没有对应类型（服务端支持不等于客户端支持）。" +
			"请把入站的传输改成 tcp / ws / grpc 再导出；h2 只有 mihomo 与 sing-box 认，" +
			"httpupgrade 只有 sing-box 认。"
	}
	return "所选入站的协议（" + strings.Join(skipped, "、") + "）无法导出为 " +
		coreLabel(core) + " 客户端节点：这类是本地代理协议，客户端配置里没有对应的出站类型，" +
		"不能拿来连远程服务器。请改选 " + strings.Join(exportableProtocols, " / ") + " 的入站。"
}

// dedupeNodeNames 保证节点名唯一。
//
// 在客户端配置里，节点名就是 tag（sing-box）或 proxy name（mihomo），重名会被
// 内核直接判死：
//
//	sing-box  FATAL: duplicate outbound/endpoint tag: hk-01-bob
//	mihomo    level=error msg="proxy hk-01-bob is the duplicate name"
//
// 而入站名与用户名都**没有**唯一约束（入站只校验 tag 唯一，用户只校验 ID）：
// 建两个都叫「香港-01」的入站、或者两个同名用户，`入站名-用户名` 就会撞在一起。
// 这同样表现为「内核拒绝这份配置」，同样让人无从下手。既然重名本身是允许的，
// 就由生成侧补序号，而不是反过来要求用户改名。
func dedupeNodeNames(nodes []*link.Node) {
	used := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		base := n.Name
		if base == "" {
			base = fmt.Sprintf("%s:%d", n.Host, n.Port)
		}
		name := base
		for i := 2; used[name]; i++ {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		used[name] = true
		n.Name = name
	}
}
