// Package api 的备份治理端点。
//
// 设计取舍：清理动作只允许删「本面板生成的、且这次重新扫描确实还在」的文件，
// 而且预览与执行走 backup.Preview / backup.Prune 的同一套筛选。前端传的只是
// key 字符串，后端从不拿它拼路径；权限上盘点只读角色可看，删除要管理员且
// 必须带 confirm=true，避免误触把可回滚版本清空。
package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"triune/internal/backup"
	"triune/internal/store"
)

// 备份保留策略存在 store.settings 里，与其余设置共用 PUT /api/settings。
// 默认全 0 = 不自动清理；自动清理开关单独一个键，默认关。
const (
	SettingBackupKeepCount = "backup_keep_count"
	SettingBackupMaxMB     = "backup_max_mb"
	SettingBackupMaxDays   = "backup_max_days"
	SettingBackupAutoPrune = "backup_auto_prune"
)

// backupScanOptions 覆盖两处归档目录：手动备份落 <data>/backups，
// 安装脚本的 systemd timer 用 --out 落 <install>/backups（见 install.sh）。
// 只扫前者会漏报定时备份，用户看到的总占用永远偏小。
func (a *Server) backupScanOptions() backup.ScanOptions {
	o := backup.ScanOptions{DataDir: a.DataDir}
	if a.DataDir != "" {
		installBackups := filepath.Join(filepath.Dir(filepath.Clean(a.DataDir)), "backups")
		if installBackups != filepath.Join(a.DataDir, "backups") {
			o.ExtraDataDirs = append(o.ExtraDataDirs, installBackups)
		}
	}
	return o
}

// clampSetting 把库里的字符串收敛到合法区间。设置是用户可写的纯文本，
// 万一被填成负数或天文数字，清理策略绝不能因此变成「全删」。
func clampSetting(s string, lo, hi int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

func (a *Server) backupPolicy() backup.Policy {
	var p backup.Policy
	if a.Store == nil {
		return p
	}
	get := func(k string) string { v, _ := a.Store.GetSetting(k); return v }
	p.KeepCount = clampSetting(get(SettingBackupKeepCount), 0, 1000)
	p.MaxDays = clampSetting(get(SettingBackupMaxDays), 0, 3650)
	p.MaxBytes = int64(clampSetting(get(SettingBackupMaxMB), 0, 1024*1024)) * 1024 * 1024
	return p
}

// validateBackupSettings 在 putSettings 落库前拦掉非法策略值，避免库里躺着
// 一条「看起来设置了上限、实际被 clamp 成 0」的静默失效配置。
func validateBackupSettings(m map[string]string) error {
	limits := map[string]int{
		SettingBackupKeepCount: 1000,
		SettingBackupMaxMB:     1024 * 1024,
		SettingBackupMaxDays:   3650,
	}
	for k, hi := range limits {
		v, ok := m[k]
		if !ok || v == "" {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < 0 || n > hi {
			return fmt.Errorf("%s 只能是 0～%d 之间的整数", k, hi)
		}
	}
	if v, ok := m[SettingBackupAutoPrune]; ok && v != "" && v != "0" && v != "1" {
		return fmt.Errorf("%s 只能是 1 或 0", SettingBackupAutoPrune)
	}
	return nil
}

// nil slice 会被 encoding/json 编成 null，前端拿到后 `.length` 直接炸。
// 盘点/预览在全新安装上经常是空的，统一换成 [] 再出网。
func nonNilEntries(v []backup.Entry) []backup.Entry {
	if v == nil {
		return []backup.Entry{}
	}
	return v
}

func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func (a *Server) policyJSON(p backup.Policy) map[string]any {
	auto := ""
	if a.Store != nil {
		auto, _ = a.Store.GetSetting(SettingBackupAutoPrune)
	}
	return map[string]any{
		"keep_count": p.KeepCount,
		"max_mb":     p.MaxBytes / 1024 / 1024,
		"max_days":   p.MaxDays,
		"auto_prune": auto == "1",
	}
}

// GET /api/backups/inventory —— 只读盘点 + 按当前策略预演清理清单。
func (a *Server) handleBackupInventory(w http.ResponseWriter, r *http.Request) {
	inv, err := backup.Scan(a.backupScanOptions())
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	p := a.backupPolicy()
	writeJSON(w, 200, map[string]any{
		"entries": nonNilEntries(inv.Entries),
		"summary": inv.Summary,
		"policy":  a.policyJSON(p),
		"planned": nonNilEntries(backup.Plan(inv, p)),
	})
}

type backupPruneReq struct {
	DryRun  bool     `json:"dry_run"`
	Confirm bool     `json:"confirm"`
	Keys    []string `json:"keys"`
}

// POST /api/backups/prune —— dry_run 或没带 confirm 时只预览，不删任何文件。
func (a *Server) handleBackupPrune(w http.ResponseWriter, r *http.Request) {
	var req backupPruneReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, "请求体格式有误："+err.Error())
		return
	}
	if len(req.Keys) > 500 {
		fail(w, 400, "一次最多清理 500 份")
		return
	}
	opts := a.backupScanOptions()
	p := a.backupPolicy()

	if req.DryRun || !req.Confirm {
		preview, err := backup.Preview(opts, req.Keys, p)
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{
			"dry_run": true,
			"planned": nonNilEntries(preview.Planned),
			"failed":  nonNilStrings(preview.Failed),
			"policy":  a.policyJSON(p),
		})
		return
	}

	res, err := backup.Prune(opts, req.Keys, p)
	if err != nil {
		a.auditFail(r, AuditBackupPrune, "清理备份", err.Error())
		fail(w, 500, err.Error())
		return
	}
	detail := fmt.Sprintf("删除 %d 份、释放 %d 字节；跳过/失败 %d 条",
		len(res.Removed), res.Freed, len(res.Failed))
	a.auditOK(r, AuditBackupPrune, "清理备份", detail)

	inv, scanErr := backup.Scan(opts)
	var summary any
	var planned any = []backup.Entry{}
	if scanErr == nil {
		summary = inv.Summary
		planned = nonNilEntries(backup.Plan(inv, p))
	}
	writeJSON(w, 200, map[string]any{
		"dry_run": false,
		"removed": nonNilEntries(res.Removed),
		"failed":  nonNilStrings(res.Failed),
		"freed":   res.Freed,
		"summary": summary,
		"planned": planned,
		"policy":  a.policyJSON(p),
	})
}

// StartBackupJanitor 每 6 小时按策略清理一次，只有显式打开 auto_prune 才生效。
// 启动后先等 10 分钟：刚升级完立刻删旧版本，万一要回滚就没得回了。
func (a *Server) StartBackupJanitor() {
	go func() {
		time.Sleep(10 * time.Minute)
		tick := time.NewTicker(6 * time.Hour)
		defer tick.Stop()
		for {
			a.runBackupJanitor()
			<-tick.C
		}
	}()
}

func (a *Server) runBackupJanitor() {
	if a.Store == nil {
		return
	}
	v, _ := a.Store.GetSetting(SettingBackupAutoPrune)
	if v != "1" {
		return
	}
	p := a.backupPolicy()
	if p.KeepCount == 0 && p.MaxBytes == 0 && p.MaxDays == 0 {
		return // 没设任何上限，自动清理不该凭空删文件
	}
	res, err := backup.Prune(a.backupScanOptions(), nil, p)
	if err != nil {
		log.Printf("[backup] 自动清理失败: %v", err)
		return
	}
	if len(res.Removed) == 0 {
		return
	}
	log.Printf("[backup] 自动清理 %d 份旧备份，释放 %d 字节，跳过/失败 %d 条",
		len(res.Removed), res.Freed, len(res.Failed))
	// r 传 nil 是安全的：auditAs 只在 r != nil 时取客户端 IP。
	a.auditAs("system", nil, AuditBackupPrune, "自动清理",
		store.AuditOK, fmt.Sprintf("删除 %d 份、释放 %d 字节", len(res.Removed), res.Freed))
}
