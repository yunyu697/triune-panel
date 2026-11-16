package webui

import (
	"strings"
	"testing"
)

// TestTrafficSamplingCopyAccurate 钉住流量页文案与实际采样行为一致。
//
// 背景：流量采样间隔曾从 30s 改短，但页面仍写「每 30 秒采样」——用户按页面
// 说明等 30s 看不到数据，误判统计坏了。文案必须与后端 ops.sampleInterval
// （当前 5s，见 internal/ops/collector.go）同步；这里的 "5" 是刻意硬编码的
// 期望值，改采样间隔的人若忘了改页面，这个测试会转红。
func TestTrafficSamplingCopyAccurate(t *testing.T) {
	html := string(indexHTML)

	if !strings.Contains(html, "每 5 秒采样") {
		t.Error("流量页采样周期文案应为「每 5 秒采样」（与 ops.sampleInterval=5s 一致）")
	}
	if strings.Contains(html, "每 30 秒采样") {
		t.Error("流量页仍留有旧的「每 30 秒采样」文案，与实际采样周期不符")
	}

	// sing-box 官方内核 clash API 不输出 user 字段，必须如实告知归属口径与
	// 短连接限制，否则用户会把「结构性漏计」当成 bug 反复报。
	for _, s := range []string{
		"仅当入站只关联一个用户时准确",
		"存活不足一个采样周期的极短连接",
		"xray 内核使用累计计数，无此问题",
	} {
		if !strings.Contains(html, s) {
			t.Errorf("流量页缺少 sing-box 统计限制说明：%q", s)
		}
	}
}
