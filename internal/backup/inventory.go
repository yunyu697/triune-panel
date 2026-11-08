// Package backup 的容量治理部分：把散落在四处的旧版本文件统一盘点、按策略清理。
//
// 为什么单独做一层：此前只有 Rotate 按份数清理数据归档，而面板版本 .bak、
// 内核二进制 .bak、配置修订 .bak 各写各的目录，从不清理；用户看到的症状就是
// 「面板没怎么用，硬盘却一直涨」。更隐蔽的是归档本身打包了 cores/ 下的内核
// 二进制，7 份归档等于把几十 MB 的内核复制了 7 遍。
//
// 删除的两条硬规矩：
//  1. 只认本包/更新器生成的文件名（归档用 isArchiveName，.bak 用带时间戳的
//     正则）。目录里放 unrelated.txt、notes.bak.bak 之类一律不动。
//  2. 调用方传进来的只是 key，绝不拿它拼文件系统路径。Prune 会重新 Scan 一次，
//     只有这次真实存在、且落在允许根目录下的条目才可能被删。
package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Category 是备份条目的分类，直接作为前端分组与 JSON 字段值。
type Category string

const (
	CatData     Category = "data"     // triune-data-*.tar.gz[.age]
	CatPanel    Category = "panel"    // 面板旧版本二进制
	CatCore     Category = "core"     // 内核旧版本二进制
	CatRevision Category = "revision" // 配置修订快照
)

// Entry 是一条备份记录。Path 与 UnixNano 只在本包内部使用，绝不序列化给前端——
// 前端能拿到绝对路径就等于给「删任意文件」提供了线索。
type Entry struct {
	Key      string `json:"key"` // "<category>/<group>/<name>"，重名时追加 #N
	Name     string `json:"name"`
	Category string `json:"category"`
	Group    string `json:"group"`
	Size     int64  `json:"size"`
	ModTime  string `json:"mod_time"` // RFC3339
	AgeDays  int    `json:"age_days"`
	Path     string `json:"-"`
	UnixNano int64  `json:"-"`
}

// Summary 是容量总览。ByCategory 与 DiskFree 让「要不要清」有据可依。
type Summary struct {
	TotalBytes int64            `json:"total_bytes"`
	Count      int              `json:"count"`
	ByCategory map[string]int64 `json:"by_category"`
	DiskFree   int64            `json:"disk_free"`
	DiskTotal  int64            `json:"disk_total"`
	Hint       string           `json:"hint"`
	Roots      []string         `json:"roots"`
}

// Inventory 是一次完整盘点的结果。
type Inventory struct {
	Entries []Entry `json:"entries"`
	Summary Summary `json:"summary"`
}

// ScanOptions 决定扫描哪些根目录。ExtraDataDirs 用来覆盖安装脚本把定时备份
// 落到 <install>/backups（而不是 <data>/backups）的情况——只扫 DataDir 会漏报。
type ScanOptions struct {
	DataDir       string
	ExtraDataDirs []string
	Now           time.Time // 零值用 time.Now()，便于测试注入
}

// Policy 是清理策略，三个条件取「或」；全 0 表示不自动清理。
type Policy struct {
	KeepCount int   `json:"keep_count"` // 每组保留份数，0 = 不限
	MaxBytes  int64 `json:"max_bytes"`  // 总占用上限（字节），0 = 不限
	MaxDays   int   `json:"max_days"`   // 超过天数可清，0 = 不限
}

// PruneResult 区分「计划删」「真删了」「没删成」，让前端能先预览再执行。
type PruneResult struct {
	Planned []Entry  `json:"planned"`
	Removed []Entry  `json:"removed"`
	Freed   int64    `json:"freed"`
	Failed  []string `json:"failed"`
}

// bakNameRe 只认更新器/修订流水线生成的 .bak：名字里必须有 20060102-150405
// 形式的时间戳。光看 ".bak" 后缀太宽，用户手动放的 notes.bak.bak 也会中招。
var bakNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]*[0-9]{8}-[0-9]{6}[A-Za-z0-9._-]*\.bak$`)

func isBakName(n string) bool {
	return n != "" && !strings.HasPrefix(n, ".") && !strings.ContainsAny(n, `/\`) && bakNameRe.MatchString(n)
}

func archiveAllowed(cat Category, name string) bool {
	if cat == CatData {
		return isArchiveName(name)
	}
	return isBakName(name)
}

// scanRoot 描述一个待扫描目录及其归属。
type scanRoot struct {
	dir      string
	category Category
	group    string
}

// Scan 只读盘点：不创建目录、不修改任何文件。目录不存在时静默跳过，
// 因为全新安装上这些目录本来就还没生成。
func Scan(opts ScanOptions) (*Inventory, error) {
	if opts.DataDir == "" {
		return nil, fmt.Errorf("数据目录为空")
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}

	dataBackups := filepath.Join(opts.DataDir, "backups")
	candidate := []scanRoot{{dir: dataBackups, category: CatData, group: "data"}}
	for _, d := range opts.ExtraDataDirs {
		if d != "" {
			candidate = append(candidate, scanRoot{dir: d, category: CatData, group: "data"})
		}
	}
	candidate = append(candidate,
		scanRoot{dir: filepath.Join(dataBackups, "panel"), category: CatPanel, group: "panel"},
	)

	// cores/<组件> 与 revisions/<内核> 按子目录分组，每组各自保留最新一份，
	// 否则「xray 有 20 份、sing-box 只有 1 份」会被一并按总数误删。
	children := []struct {
		parent string
		cat    Category
	}{{filepath.Join(dataBackups, "cores"), CatCore}, {filepath.Join(opts.DataDir, "revisions"), CatRevision}}
	for _, c := range children {
		subs, err := os.ReadDir(c.parent)
		if err != nil {
			continue
		}
		for _, sub := range subs {
			if !sub.IsDir() || (sub.Type()&os.ModeSymlink) != 0 {
				continue
			}
			candidate = append(candidate, scanRoot{
				dir:      filepath.Join(c.parent, sub.Name()),
				category: c.cat,
				group:    sub.Name(),
			})
		}
	}

	inv := &Inventory{Summary: Summary{ByCategory: map[string]int64{}}}
	seenRoot := map[string]bool{}
	seenKey := map[string]bool{}
	for _, root := range candidate {
		abs, err := filepath.Abs(root.dir)
		if err != nil || seenRoot[abs] {
			continue
		}
		seenRoot[abs] = true
		entries, err := os.ReadDir(abs)
		if err != nil {
			continue // 目录不存在 / 无权限，跳过而不是让整个盘点失败
		}
		inv.Summary.Roots = append(inv.Summary.Roots, abs)
		for _, de := range entries {
			if de.IsDir() || (de.Type()&os.ModeSymlink) != 0 {
				continue // 不递归、不跟随软链
			}
			name := de.Name()
			if !archiveAllowed(root.category, name) {
				continue
			}
			fi, err := de.Info()
			if err != nil {
				continue
			}
			key := string(root.category) + "/" + root.group + "/" + name
			// 两个归档根目录可能有同时间戳文件；key 必须唯一，否则删除会歧义。
			if seenKey[key] {
				for i := 2; ; i++ {
					alt := fmt.Sprintf("%s#%d", key, i)
					if !seenKey[alt] {
						key = alt
						break
					}
				}
			}
			seenKey[key] = true
			age := int(now.Sub(fi.ModTime()) / (24 * time.Hour))
			if age < 0 {
				age = 0
			}
			e := Entry{
				Key:      key,
				Name:     name,
				Category: string(root.category),
				Group:    root.group,
				Size:     fi.Size(),
				ModTime:  fi.ModTime().UTC().Format(time.RFC3339),
				AgeDays:  age,
				Path:     filepath.Join(abs, name),
				UnixNano: fi.ModTime().UnixNano(),
			}
			inv.Entries = append(inv.Entries, e)
			inv.Summary.TotalBytes += e.Size
			inv.Summary.ByCategory[e.Category] += e.Size
		}
	}

	inv.Summary.Count = len(inv.Entries)
	inv.Summary.DiskFree, inv.Summary.DiskTotal = diskUsage(opts.DataDir, inv.Summary.Roots)
	inv.Summary.Hint = hintFor(inv)
	return inv, nil
}

func hintFor(inv *Inventory) string {
	var dataBytes int64
	var dataCount int
	for _, e := range inv.Entries {
		if e.Category == string(CatData) {
			dataBytes += e.Size
			dataCount++
		}
	}
	if inv.Summary.Count == 0 {
		return "暂未发现面板生成的备份文件。手动备份、升级与「应用配置」都会在这里留下旧版本。"
	}
	base := fmt.Sprintf("共 %d 份、占用 %s；数据归档 %d 份占 %s。归档会把 cores/ 下的内核二进制一起打包，份数越多越占空间。",
		inv.Summary.Count, humanBytes(inv.Summary.TotalBytes), dataCount, humanBytes(dataBytes))
	if inv.Summary.DiskFree > 0 && inv.Summary.TotalBytes > inv.Summary.DiskFree/10 {
		base += " 当前占用已超过磁盘可用空间的十分之一，建议设一个总容量上限。"
	}
	base += " 每个分类至少保留最新一份，清理不会把回滚能力清空。"
	return base
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	v := float64(n)
	for _, u := range []string{"KB", "MB", "GB", "TB"} {
		v /= unit
		if v < unit {
			return fmt.Sprintf("%.1f %s", v, u)
		}
	}
	return fmt.Sprintf("%.1f PB", v/unit)
}

// byOldest 让删除顺序稳定：先旧后新；时间相同按 key 排序，避免同一秒生成的
// 多份文件每次跑出不同结果。
func byOldest(a, b Entry) bool {
	if a.UnixNano != b.UnixNano {
		return a.UnixNano < b.UnixNano
	}
	return a.Key < b.Key
}

func groupOf(e Entry) string { return e.Category + "/" + e.Group }

// Plan 只计算、不碰磁盘。每种条件都是「或」，任一命中就进待删清单；
// 但每组（Category+Group）最新一份是硬底线，任何策略都不能突破——
// 否则把 MaxBytes 设小一点就可能一次删光所有可回滚版本。
func Plan(inv *Inventory, p Policy) []Entry {
	if inv == nil || len(inv.Entries) == 0 {
		return nil
	}
	groups := map[string][]Entry{}
	for _, e := range inv.Entries {
		g := groupOf(e)
		groups[g] = append(groups[g], e)
	}
	newest := map[string]string{}
	for g, list := range groups {
		sort.Slice(list, func(i, j int) bool { return !byOldest(list[i], list[j]) }) // 新→旧
		newest[g] = list[0].Key
	}

	marked := map[string]bool{}
	for _, list := range groups {
		if p.KeepCount > 0 {
			for i, e := range list {
				if i >= p.KeepCount {
					marked[e.Key] = true
				}
			}
		}
		if p.MaxDays > 0 {
			for _, e := range list {
				if e.AgeDays > p.MaxDays {
					marked[e.Key] = true
				}
			}
		}
	}

	// 总容量：先看已标记删除后还剩多少，不够再从最旧开始补删。
	if p.MaxBytes > 0 {
		remaining := inv.Summary.TotalBytes
		for _, e := range inv.Entries {
			if marked[e.Key] {
				remaining -= e.Size
			}
		}
		if remaining > p.MaxBytes {
			rest := make([]Entry, 0, len(inv.Entries))
			for _, e := range inv.Entries {
				if !marked[e.Key] && e.Key != newest[groupOf(e)] {
					rest = append(rest, e)
				}
			}
			sort.Slice(rest, func(i, j int) bool { return byOldest(rest[i], rest[j]) })
			for _, e := range rest {
				if remaining <= p.MaxBytes {
					break
				}
				// 删掉一份后，它所在组的最新一份仍然受保护
				marked[e.Key] = true
				remaining -= e.Size
			}
		}
	}

	out := make([]Entry, 0, len(marked))
	for _, e := range inv.Entries {
		if marked[e.Key] && e.Key != newest[groupOf(e)] {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return byOldest(out[i], out[j]) })
	return out
}

// Preview 与 Prune 共用同一套筛选，只是不执行 os.Remove。前端「先看看会删哪些」
// 必须和真正点下去看到的是同一份清单，否则预览就成了摆设。
func Preview(opts ScanOptions, keys []string, p Policy) (*PruneResult, error) {
	inv, err := Scan(opts)
	if err != nil {
		return nil, err
	}
	res := &PruneResult{}
	res.Planned, res.Failed = selectTargets(inv, keys, p)
	return res, nil
}

// selectTargets 把「用户给的 key」翻译成「真实存在且可删的条目」。
// key 只用于查表，绝不参与路径拼接：即便传 ../../etc/passwd 也只会进 Failed。
func selectTargets(inv *Inventory, keys []string, p Policy) ([]Entry, []string) {
	if inv == nil {
		return nil, nil
	}
	byKey := map[string]Entry{}
	for _, e := range inv.Entries {
		byKey[e.Key] = e
	}
	var targets []Entry
	var failed []string

	if len(keys) == 0 {
		targets = Plan(inv, p)
	} else {
		// 手动删除同样守住「每组至少一份」：用户可能一次全选，把回滚能力清空。
		newest := map[string]string{}
		for _, e := range inv.Entries {
			g := groupOf(e)
			if cur, ok := newest[g]; !ok || e.UnixNano > byKey[cur].UnixNano {
				newest[g] = e.Key
			}
		}
		for _, k := range keys {
			if strings.Contains(k, "..") || strings.HasPrefix(k, "/") || strings.ContainsAny(k, `\`) {
				failed = append(failed, k+"：非法 key")
				continue
			}
			e, ok := byKey[k]
			if !ok {
				failed = append(failed, k+"：不在可清理清单里")
				continue
			}
			if e.Key == newest[groupOf(e)] {
				failed = append(failed, k+"：每组至少保留最新一份")
				continue
			}
			targets = append(targets, e)
		}
	}

	// 第二道防线也放进筛选（而不是等到删除那一步），让预览与执行口径一致。
	roots := append([]string(nil), inv.Summary.Roots...)
	var ok []Entry
	for _, e := range targets {
		if !pathWithinRoots(e.Path, roots) || !archiveAllowed(Category(e.Category), e.Name) {
			failed = append(failed, e.Key+"：路径或文件名不在白名单内")
			continue
		}
		ok = append(ok, e)
	}
	return ok, failed
}

// Prune 删除指定 key；keys 为空时按 Policy 重新 Plan 全部。
func Prune(opts ScanOptions, keys []string, p Policy) (*PruneResult, error) {
	inv, err := Scan(opts)
	if err != nil {
		return nil, err
	}
	res := &PruneResult{}
	res.Planned, res.Failed = selectTargets(inv, keys, p)
	for _, e := range res.Planned {
		if err := os.Remove(e.Path); err != nil {
			res.Failed = append(res.Failed, e.Key+"："+err.Error())
			continue
		}
		res.Removed = append(res.Removed, e)
		res.Freed += e.Size
	}
	return res, nil
}

// pathWithinRoots 是第二道防线：即便将来有人手滑把用户输入的路径塞进 Entry，
// 也删不到允许根目录之外。用 EvalSymlinks 解析软链后再比，防止备份目录本身
// 是链接到别处的符号链接。
func pathWithinRoots(p string, roots []string) bool {
	target, err := filepath.EvalSymlinks(p)
	if err != nil {
		return false
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return false
	}
	for _, r := range roots {
		root, err := filepath.EvalSymlinks(r)
		if err != nil {
			continue
		}
		root, err = filepath.Abs(root)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(root, target)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			continue
		}
		// 只允许顶层文件，不许落到子目录里
		if strings.ContainsAny(rel, `/\`) {
			continue
		}
		return true
	}
	return false
}
