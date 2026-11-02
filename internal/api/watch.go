package api

import (
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"time"

	"triune/internal/store"
	"triune/internal/update"
)

// 面板「升级完成后立刻切换成最新版本」。
//
// 自更新内部会 syscall.Exec 换掉自己，那是最快的一条路。但「磁盘上的二进制
// 变了、进程还是旧的」不止自更新一种来源：一键脚本 cp 过去、手工 mv、
// 容器里 docker cp / 重挂镜像、systemd 没配对导致只换了文件没重启、
// 甚至 exec 被 seccomp 挡掉——这些情况下进程都还跑着旧映像。
//
// 以前面板只会把这个事实「显示」出来（设置页那条黄框），让用户自己去重启。
// 但用户要的是**立刻切过去**，不是被通知该怎么做。所以这里加一个守望者：
// 盯着磁盘上的二进制，一旦它稳定地变成另一个版本，就自己 exec 过去。
//
// 几个必须守住的边界：
//   - 不能每轮都 fork 一个进程去读版本（长驻进程，3 秒一次会很难看）：
//     只有文件的 size/mtime 变了才去探一次。
//   - 不能对「正在写入的文件」动手：探测成功且下一轮签名没变才切。
//   - 不能无限重启：同一目标版本最多切 2 次，之后只留提示（见 claimSwitchTries）。
//   - 拿不到「稳定的同一版本」就不动：探不到版本（文件正在写、架构不符、
//     不可执行）一律跳过，等下一轮。

// AutoSwitchSetting 是否允许「检测到新二进制就自动切过去」，默认开。
//
// 关掉它的场景：面板跑在某种外部监管下（容器编排、supervisor），
// 用户希望重启时机由外部决定，而不是面板自己动。
const AutoSwitchSetting = "auto_switch_panel"

// autoSwitchTriesSetting 自动切换的次数记录，形如 "v1.1.0|2"。
// 落库而不是放内存：否则「切过去 → 新进程仍对不上 → 再切」会变成无限重启。
const autoSwitchTriesSetting = "auto_switch_tries"

// binaryWatchInterval 守望间隔。3 秒是「足够快」与「别太吵」的折中：
// 文件没变时这一轮只是一次 stat，没有 fork。
var binaryWatchInterval = 3 * time.Second

// maxAutoSwitchTries 同一目标版本最多自动切几次。
const maxAutoSwitchTries = 2

// verToken 从「二进制自报的那一行」里抠出版本号。
// 形如 `triune-panel v1.1.0 (darwin/arm64)` → `v1.1.0`。
var verToken = regexp.MustCompile(`v?\d+\.\d+\.\d+[-+.\w]*`)

// normVerString 归一化版本串，用于比较「运行中」与「磁盘上」。
// 二进制自报的是整行（`triune-panel v1.1.1 (linux/amd64)`），构建注入的是
// 短版本号（`v1.1.1`），两者必须判成同一个版本——否则每次启动都会白切一次。
//
// 认不出标准版本号时（`dev` 构建）退化为「去掉程序名与平台后缀的最后一词」，
// 至少不会因为多了一段程序名就误判成不同版本。
func normVerString(s string) string {
	if m := verToken.FindString(s); m != "" {
		return strings.ToLower(strings.TrimPrefix(m, "v"))
	}
	s = strings.TrimSpace(s)
	if i := strings.Index(s, " ("); i > 0 {
		s = s[:i]
	}
	if f := strings.Fields(s); len(f) > 1 {
		s = f[len(f)-1]
	}
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "v"))
}

// probeVer 让某个二进制自报版本。抽成变量只为了能在测试里替换：
// 真去 fork 一个进程的话，用例就变成「跑一个真二进制」的集成测试了。
var probeVer = func(path string) (string, error) {
	return update.BinaryVersion(path, []string{"-version"})
}

func (a *Server) autoSwitchOn() bool {
	v, _ := a.Store.GetSetting(AutoSwitchSetting)
	// 未设置视为开启：这是默认行为，用户要的也是「升级完就切过去」
	return v != "0"
}

// claimSwitchTries 申请一次自动切换。同一目标版本超过上限就拒绝，
// 把「切了还是不对」变成一条留在界面上的提示，而不是无限重启。
func (a *Server) claimSwitchTries(ver string) bool {
	cur, _ := a.Store.GetSetting(autoSwitchTriesSetting)
	n := 0
	if i := strings.LastIndex(cur, "|"); i >= 0 {
		if normVerString(cur[:i]) == normVerString(ver) {
			fmt.Sscanf(cur[i+1:], "%d", &n)
		}
	}
	if n >= maxAutoSwitchTries {
		return false
	}
	_ = a.Store.SetSetting(autoSwitchTriesSetting, fmt.Sprintf("%s|%d", ver, n+1))
	return true
}

func (a *Server) clearSwitchTries() {
	if cur, _ := a.Store.GetSetting(autoSwitchTriesSetting); cur != "" {
		_ = a.Store.SetSetting(autoSwitchTriesSetting, "")
	}
}

// binWatch 是守望者跨轮次要记住的东西。
type binWatch struct {
	lastSig string // 上一轮看到的 size:mtime
	pendSig string // 已探测到「版本不同」的签名，等下一轮确认稳定
	pendVer string // 那个不同的版本
}

// watchTick 走一轮守望。返回非空表示「磁盘上的二进制已经稳定地变成了这个版本，
// 该切过去了」；空串表示这轮什么都不用做。
//
// 抽成独立方法是为了能测：真循环里 sleep + fork + exec 混在一起，
// 最容易出错的那部分（什么时候该切、什么时候该等）反而测不到。
func (a *Server) watchTick(w *binWatch, exe string) string {
	st, err := os.Stat(exe)
	if err != nil {
		// 替换过程中会有一瞬间文件不存在（先 rename 走、再 rename 回来），跳过
		return ""
	}
	sig := fmt.Sprintf("%d:%d", st.Size(), st.ModTime().UnixNano())

	if sig == w.lastSig {
		// 文件这一轮没变。只有「上一轮已经探到新版本」时才动手——
		// 多等一轮是为了避开「正在被 copy 的文件」：探版本要真的执行它，
		// 拷贝到一半的二进制通常跑不起来，但万一跑起来了就是个坏版本。
		if w.pendSig == sig && w.pendVer != "" {
			return w.pendVer
		}
		return ""
	}

	// 文件变了：探一次版本（只有这里才 fork）
	w.lastSig = sig
	v, err := probeVer(exe)
	if err != nil {
		// 可能正写到一半、或临时不可执行；下一轮文件再变时会重新探
		return ""
	}
	if normVerString(v) == normVerString(Version) {
		// 磁盘上就是要跑的那个版本：清掉待办与计数
		w.pendSig, w.pendVer = "", ""
		a.clearSwitchTries()
		return ""
	}
	w.pendSig, w.pendVer = sig, v
	return ""
}

// watchOnce 走一轮完整守望：探 → 判 → 必要时切。返回是否真的尝试过切换。
//
// 拆出来而不是全塞进 for 里，是为了让「切还是不切」这条判断能被测试驱动：
// 循环里混着 sleep 与 exec，测试只能跑个真二进制才知道对不对。
func (a *Server) watchOnce(w *binWatch, exe string) bool {
	target := a.watchTick(w, exe)
	if target == "" {
		return false
	}
	if !a.claimSwitchTries(target) {
		// 到上限了：同版本反复切还是对不上，说明换进程这条路走不通
		//（seccomp、noexec…），别再折腾，留给界面上的说明。
		w.pendSig, w.pendVer = "", ""
		log.Printf("[update] 已自动切换 %d 次仍不是 %s，停止重试；请看设置页的说明",
			maxAutoSwitchTries, target)
		return false
	}
	log.Printf("[update] 磁盘上的二进制已是 %s（运行中 %s），自动切换", target, Version)
	a.auditAs("system", nil, AuditUpdate, "panel", store.AuditOK, "自动切换到 "+target)
	// exec 成功就不会返回——进程映像已经是新的了，这个 goroutine 随之消失。
	// 返回即失败：restartSelf 已写日志与失败说明；保留 pendSig 让下一轮再试
	//（exec 有 ETXTBSY 这类瞬时失败，重试一次是对的，上限见 claimSwitchTries）。
	a.restartSelf(exe, "切换到 "+target)
	return true
}

// watchBinary 盯着磁盘上的二进制，发现它被换成别的版本就自己切过去。
// 由 cmd/server 在启动时拉起，随进程结束而结束。
func (a *Server) watchBinary() {
	exe, err := selfExe()
	if err != nil {
		log.Printf("[update] 拿不到自身路径，自动切换不可用: %v", err)
		return
	}
	var w binWatch
	for {
		time.Sleep(binaryWatchInterval)
		if !a.autoSwitchOn() {
			continue
		}
		a.watchOnce(&w, exe)
	}
}

// StartBinaryWatch 在后台拉起守望者，由 cmd/server 启动时调用一次。
func (a *Server) StartBinaryWatch() {
	go a.watchBinary()
}
