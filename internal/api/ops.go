package api

import (
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"triune/internal/update"
)

func execCmd(bin string, args ...string) *exec.Cmd { return exec.Command(bin, args...) }

// ---- traffic ----

// GET /api/traffic：每用户累计流量（按用量降序排行，含配额百分比与告警标记）。
func (a *Server) handleTraffic(w http.ResponseWriter, r *http.Request) {
	cs, err := a.Store.ListClients()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	traffic, err := a.Store.GetTrafficAll()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	type row struct {
		ID       int64   `json:"id"`
		Name     string  `json:"name"`
		Up       int64   `json:"up"`
		Down     int64   `json:"down"`
		Total    int64   `json:"total"`
		QuotaGB  float64 `json:"quota_gb"`
		Enabled  bool    `json:"enabled"`
		Exceeded bool    `json:"exceeded"`
		Warn     bool    `json:"warn"`     // 越过告警阈值（未达配额）
		Pct      float64 `json:"pct"`      // 配额使用百分比（无限额为 0）
		Expiring bool    `json:"expiring"` // 3 天内到期
		Expired  bool    `json:"expired"`
	}
	warnPctD := 80
	if v, err := a.Store.GetSetting("quota_warn_pct"); err == nil {
		if n := atoiDefault(strings.TrimSpace(v), 0); n > 0 && n <= 100 {
			warnPctD = n
		}
	}
	out := []row{}
	for _, c := range cs {
		var up, down int64
		if t, ok := traffic[c.ID]; ok {
			up, down = t.UpBytes, t.DownBytes
		}
		total := up + down
		pct := 0.0
		exceeded := false
		if c.QuotaGB > 0 {
			pct = float64(total) / (c.QuotaGB * 1024 * 1024 * 1024) * 100
			exceeded = pct >= 100
		}
		var expiring, expired bool
		if c.ExpiresAt != nil {
			expired = c.ExpiresAt.Before(timeNow())
			expiring = !expired && c.ExpiresAt.Before(timeNow().Add(72*time.Hour))
		}
		out = append(out, row{
			ID: c.ID, Name: c.Name, Up: up, Down: down, Total: total,
			QuotaGB: c.QuotaGB, Enabled: c.Enabled, Exceeded: exceeded,
			Warn: !exceeded && c.QuotaGB > 0 && pct >= float64(warnPctD),
			Pct: pct, Expiring: expiring, Expired: expired,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Total > out[j].Total })
	writeJSON(w, 200, out)
}

// GET /api/traffic/daily?days=30&client_id=0：近 N 天按日流量（供图表）。
func (a *Server) handleTrafficDaily(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	days := 30
	if n, err := strconv.Atoi(q.Get("days")); err == nil && n > 0 {
		days = n
	}
	var cid int64
	if n, err := strconv.ParseInt(q.Get("client_id"), 10, 64); err == nil {
		cid = n
	}
	points, err := a.Store.GetTrafficDaily(cid, days)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, points)
}

// ---- tools ----

// POST /api/tools/reality-keypair
//
//	{"core":"singbox"|"xray"}            生成一对新密钥
//	{"private_key":"..."}                由已有私钥推导公钥（导入配置后核对用）
//
// 密钥用 Go 的 crypto/ecdh 算，不依赖内核二进制：X25519 是标准算法，
// 私钥 32 字节、编码为 base64url（43 字符），与 xray/sing-box 完全一致。
// 这样即使面板还没装内核，也能编辑 Reality 入站——以前的实现会在这里
// 报「未配置二进制路径」，把一个纯数学问题变成运维问题。
func (a *Server) handleRealityKeypair(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Core       string `json:"core"`
		PrivateKey string `json:"private_key"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	if pk := strings.TrimSpace(req.PrivateKey); pk != "" {
		pub, err := realityPubFromPriv(pk)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"private_key": pk, "public_key": pub})
		return
	}

	priv, pub, err := newRealityKeypair()
	if err != nil {
		fail(w, 500, "生成密钥对失败: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"private_key": priv, "public_key": pub})
}

// newRealityKeypair 生成 X25519 密钥对（base64url，无填充）。
func newRealityKeypair() (priv, pub string, err error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString(k.Bytes()), enc.EncodeToString(k.PublicKey().Bytes()), nil
}

// realityPubFromPriv 由私钥推导公钥。私钥格式兼容 base64url（xray/sing-box）
// 与标准 base64（部分面板导出的可能带 +/=）。
func realityPubFromPriv(s string) (string, error) {
	raw, err := decodeRealityKey(s)
	if err != nil {
		return "", err
	}
	k, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", fmt.Errorf("私钥不合法（%d 字节，应为 32 字节的 base64）：%v", len(raw), err)
	}
	return base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes()), nil
}

func decodeRealityKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "private_key=")
	for _, enc := range []*base64.Encoding{
		base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil && len(b) == 32 {
			return b, nil
		}
	}
	return nil, fmt.Errorf("私钥应是 32 字节的 base64（base64url 43 字符），收到 %q", truncate(s, 20))
}

// GET /api/tools/uuid：生成一个 UUID。
func (a *Server) handleUUID(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]string{"uuid": uuid4()})
}

// ---- version & self-update ----

// Version 由构建时 ldflags 注入：go build -ldflags "-X triune/internal/api.Version=v1.0.0"
var Version = "dev"

// GET /api/certs：ACME 证书状态。
func (a *Server) handleCerts(w http.ResponseWriter, r *http.Request) {
	if a.CertMgr == nil {
		writeJSON(w, 200, []map[string]any{})
		return
	}
	writeJSON(w, 200, a.CertMgr.Status())
}

// POST /api/certs/issue：手动触发一次 ACME 签发/续期检查（异步执行）。
// HTTP-01 验证需域名解析到本机且面板可绑定 :80；完成后 GET /api/certs 可见结果。
func (a *Server) handleCertsIssue(w http.ResponseWriter, r *http.Request) {
	if a.CertMgr == nil {
		fail(w, 400, "证书管理器未初始化")
		return
	}
	go a.CertMgr.CheckNow()
	a.auditOK(r, AuditCert, "", "手动触发证书签发 / 续期检查")
	writeJSON(w, 200, map[string]string{
		"ok":  "1",
		"msg": "已触发签发检查；HTTP-01 验证通常几秒到一分钟完成，稍后刷新可查看状态",
	})
}

// GET /api/traffic/live：实时上下行速率（由最近一个采样周期的差值计算，字节/秒）。
func (a *Server) handleTrafficLive(w http.ResponseWriter, r *http.Request) {
	if a.Coll == nil {
		writeJSON(w, 200, map[string]any{"up_bps": 0, "down_bps": 0, "interval": 30})
		return
	}
	writeJSON(w, 200, a.Coll.Live())
}

// GET /api/version
//
// 除运行中版本外，还回三样用于排障的东西：
//   - binary_version：磁盘上那个二进制自报的版本
//   - restart_failed：上一次自更新「换了文件却没重启成功」的说明
//   - auto_switch：磁盘上的二进制变了会不会自动切过去（见 watch.go）
//
// 为什么需要它们：面板显示的版本号来自**进程**，所以「二进制换了、进程没换」
// 时界面看起来一切正常、只有版本号不动。以前这种情况只能靠用户自己去
// `readlink /proc/<pid>/exe` 看有没有 " (deleted)"。两个值一对比就能一眼看出。
func (a *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	restartFailed, _ := a.Store.GetSetting(SettingRestartFailed)
	autoSwitch := "0"
	if a.autoSwitchOn() {
		autoSwitch = "1"
	}
	writeJSON(w, 200, map[string]string{
		"version":        Version,
		"binary_version": a.diskBinaryVersion(),
		"restart_failed": restartFailed,
		// 界面据此决定「磁盘版本 ≠ 运行版本」时该说什么：
		// 开着就说「几秒内自动切换」，关着才让用户自己重启。
		"auto_switch": autoSwitch,
	})
}

// diskBinaryVersion 读磁盘上那个二进制自报的版本。
//
// 要 fork 一个进程去跑 `-version`，所以缓存 10 秒：页面每次加载都会问一次，
// 而磁盘上的文件不会一秒一变。读不到就返回空串——这只是个诊断字段，
// 不能因为它让整个版本接口失败。
func (a *Server) diskBinaryVersion() string {
	a.diskVerMu.Lock()
	defer a.diskVerMu.Unlock()
	if time.Since(a.diskVerAt) < 10*time.Second {
		return a.diskVer
	}
	a.diskVerAt = time.Now()
	a.diskVer = ""
	exe, err := selfExe()
	if err != nil {
		return ""
	}
	if v, err := probeVer(exe); err == nil {
		a.diskVer = v
	}
	return a.diskVer
}

// GET /api/public/info：免认证，仅暴露面板版本号（登录页展示用）。
// 不返回任何用户数据、路径或配置信息。
func (a *Server) handlePublicInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]string{"version": Version})
}

// POST /api/self-update {"url":"https://.../triune-panel","sha256":"可选"}
//
// 流程：下载 → 校验 → 备份旧二进制 → 原子替换 → exec 重启。
// 数据目录（含 SQLite WAL）不受影响 —— schema 迁移向前兼容，
// 这正是"升级不失效"设计在面板自身上的应用。
func (a *Server) handleSelfUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL    string `json:"url"`
		Sha256 string `json:"sha256"`
		// Mirror 本次下载用的镜像前缀（指针：区分「没传」与「传空串=强制官方」）
		Mirror *string `json:"mirror"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.URL == "" {
		fail(w, 400, "需要 url 字段")
		return
	}
	mirror, merr := a.resolveMirror(req.Mirror)
	if merr != nil {
		fail(w, 400, merr.Error())
		return
	}
	req.URL = update.ApplyMirror(req.URL, mirror)
	exe, err := selfExe()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}

	// 1. 下载
	resp, err := http.Get(req.URL)
	if err != nil {
		fail(w, 500, "下载失败: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		fail(w, 500, fmt.Sprintf("下载失败: HTTP %d", resp.StatusCode))
		return
	}
	newBin := filepath.Join(a.DataDir, ".update.new")
	f, err := os.OpenFile(newBin, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	sum := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, sum), io.LimitReader(resp.Body, 1<<30)); err != nil {
		f.Close()
		os.Remove(newBin)
		fail(w, 500, "写入失败: "+err.Error())
		return
	}
	f.Close()

	// 2. sha256 校验（可选）+ 可执行验证：跑不通 -version 的二进制一律拒收
	got := hex.EncodeToString(sum.Sum(nil))
	if req.Sha256 != "" && !strings.EqualFold(got, req.Sha256) {
		os.Remove(newBin)
		fail(w, 400, "sha256 不匹配: got "+got)
		return
	}
	_ = os.Chmod(newBin, 0o755)
	newVer, verr := update.BinaryVersion(newBin, []string{"-version"})
	if verr != nil {
		os.Remove(newBin)
		fail(w, 422, "新二进制不可执行（已拒收，现有环境零改动）: "+verr.Error())
		return
	}

	// 3. 备份：exe.bak（快速回滚）+ data/backups/panel/<旧版本>-<时间>.bak（任意版本回滚）
	// 备份内容是"旧版本"二进制，因此用当前运行版本号命名，回滚时一目了然
	versionedDir := filepath.Join(a.DataDir, "backups", "panel")
	_ = os.MkdirAll(versionedDir, 0o755)
	versioned := filepath.Join(versionedDir, fmt.Sprintf("%s-%s.bak", Version, time.Now().Format("20060102-150405")))
	if data, err := os.ReadFile(exe); err == nil {
		_ = os.WriteFile(versioned, data, 0o755)
	}

	// 4. 原子替换。新二进制必须先**落到安装目录**再 rename，见 stageReplace。
	if err := stageReplace(newBin, exe); err != nil {
		os.Remove(newBin)
		fail(w, 500, "替换失败: "+err.Error())
		return
	}
	os.Remove(newBin) // 跨设备时源文件还在，清掉

	// 5. 重启（exec 替换进程映像，监听与数据无缝衔接）
	a.auditOK(r, AuditUpdate, "panel", "面板自更新到 "+newVer)
	writeJSON(w, 200, map[string]string{"ok": "1", "sha256": got, "new_version": newVer, "msg": "更新完成，正在重启..."})
	go a.restartSelf(exe, "更新到 "+newVer)
}

// execSelf / execSelfWait 抽成变量只为了能在测试里替换：
// 「exec 失败」这条路平时跑不到，但它恰恰是最难查的一类故障。
var (
	execSelf     = syscall.Exec
	execSelfWait = 500 * time.Millisecond
)

// stageReplace 用「同目录暂存 + rename」把 exe 换成 newBin。
//
// 关键在暂存文件的位置：它必须和 exe 在**同一个文件系统**上，rename 才是原子的、
// 才不会 EXDEV。以前是直接把数据目录里下载好的文件 rename 到安装目录——
// 单分区机器上一直是对的，但数据目录经常被指到别处（容器挂载卷、独立的
// /var 分区、tmpfs），那时 rename 返回 "cross-device link"，整个升级原地失败：
// 二进制没换、进程没换，用户只看到一句英文错误，版本号当然还是旧的。
func stageReplace(newBin, exe string) error {
	staged := exe + ".update-new"
	if err := copyFileTo(newBin, staged); err != nil {
		return fmt.Errorf("写入安装目录失败（该目录可写吗？）: %w", err)
	}
	if err := os.Chmod(staged, 0o755); err != nil {
		os.Remove(staged)
		return err
	}
	bak := exe + ".bak"
	os.Remove(bak)
	if err := os.Rename(exe, bak); err != nil {
		os.Remove(staged)
		return fmt.Errorf("备份旧二进制失败（安装目录可写吗？）: %w", err)
	}
	if err := os.Rename(staged, exe); err != nil {
		_ = os.Rename(bak, exe) // 还原
		os.Remove(staged)
		return err
	}
	return nil
}

// selfExe 返回当前进程可执行文件的真实路径。
//
// 不能直接用 os.Executable()：Linux 上它读 /proc/self/exe，而**文件一旦被替换或
// 删除，内核会在路径后面补上 " (deleted)"**。自更新恰恰就是「先换文件、再重启」，
// 所以第一次 exec 失败后进程还活着时，第二次更新拿到的就是
// `/usr/local/bin/triune-panel (deleted)` —— 拿它去 rename、去 exec 全都失败，
// 而且错得莫名其妙（目录里还会多出一个带 " (deleted)" 的垃圾文件）。
// 这里统一剥掉后缀，并确认文件真的还在。
func selfExe() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	return selfExeAt(p)
}

// selfExeAt 是 selfExe 去掉平台调用之后的部分，单独拆出来才能测。
func selfExeAt(p string) (string, error) {
	if i := strings.Index(p, " (deleted)"); i > 0 {
		p = p[:i]
	}
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("%s 不存在（二进制已被替换？）: %w", p, err)
	}
	return p, nil
}

// restartSelf 用 exec 把当前进程映像换成刚替换好的二进制。what 是给人看的动作描述
//（"更新到 v1.0.9" / "回滚到 v1.0.8"），只用于失败时的那句话。
//
// exec 成功时**不会返回**——进程映像已经被换掉了。走到 return 就说明它失败了，
// 而失败后进程会继续以**旧映像**跑下去（新二进制只在磁盘上）。这时面板对外
// 一切正常，只有版本号不变，用户看到的就是「更新完了还是旧版本」。
// 所以这里必须把失败记下来：写日志 + 落一个标记，让 /api/version 和界面能说清楚。
//
// 失败原因现实中主要有两种：安装目录所在分区 noexec、以及容器 / systemd 的
// 安全策略禁掉了 execve（seccomp、SystemCallFilter）。这两种环境下**任何**
// 自重启手段都不可用，只能明确告诉用户去 `systemctl restart`。
func (a *Server) restartSelf(exe, what string) {
	time.Sleep(execSelfWait)
	err := execSelf(exe, os.Args, os.Environ())
	if err == nil {
		return
	}
	msg := fmt.Sprintf("二进制已%s，但重启失败（%v）；进程仍在运行 %s", what, err, Version)
	log.Printf("[update] %s", msg)
	_ = a.Store.SetSetting(SettingRestartFailed, msg)
}
