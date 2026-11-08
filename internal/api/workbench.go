package api

import (
	"encoding/json"
	"fmt"
)

// SettingWorkbenchLayout 保存命令工作台的卡片顺序 / 折叠 / 置顶状态。
//
// 它是**界面偏好**而不是服务配置，但存服务端才能跨设备一致（手机和电脑看到
// 同一套工作台）；前端同时留一份 localStorage 作为只读账号 / 接口失败时的
// 本机降级。值是一个小 JSON，不是可执行内容，落库前仍按白名单校验。
const SettingWorkbenchLayout = "wb_layout"

// 卡片 key 与 index.html 的 data-wb 一一对应。后端只认这几个：不校验就等
// 于把 store.settings 变成一个任意字符串仓库，前端拿到脏值还可能拼进 DOM。
var wbCardKeys = map[string]bool{
	"todo": true, "kpis": true, "trend": true, "cores": true,
	"users": true, "rank": true, "quick": true,
}

type workbenchLayout struct {
	Order     []string `json:"order"`
	Collapsed []string `json:"collapsed"`
	Pinned    []string `json:"pinned"`
}

func validateWorkbenchLayout(s string) error {
	if s == "" {
		return nil // 空串 = 恢复默认布局
	}
	if len(s) > 4096 {
		return fmt.Errorf("工作台布局数据过长（上限 4096 字节）")
	}
	var v workbenchLayout
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return fmt.Errorf("工作台布局格式有误：%w", err)
	}
	seen := map[string]bool{}
	for _, k := range v.Order {
		if !wbCardKeys[k] {
			return fmt.Errorf("工作台布局含未知卡片 %q", k)
		}
		if seen[k] {
			return fmt.Errorf("工作台布局里 %q 重复", k)
		}
		seen[k] = true
	}
	for _, group := range [][]string{v.Collapsed, v.Pinned} {
		for _, k := range group {
			if !wbCardKeys[k] {
				return fmt.Errorf("工作台布局含未知卡片 %q", k)
			}
		}
	}
	return nil
}
