package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"triune/internal/model"
	"triune/internal/update"
)

func execCmd(bin string, args ...string) *exec.Cmd { return exec.Command(bin, args...) }

// ---- traffic ----

// GET /api/traffic：每用户累计流量（含配额判断所需字段）。
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
	}
	out := []row{}
	for _, c := range cs {
		var up, down int64
		if t, ok := traffic[c.ID]; ok {
			up, down = t.UpBytes, t.DownBytes
		}
		total := up + down
		out = append(out, row{
			ID: c.ID, Name: c.Name, Up: up, Down: down, Total: total,
			QuotaGB: c.QuotaGB, Enabled: c.Enabled,
			Exceeded: c.QuotaGB > 0 && float64(total) >= c.QuotaGB*1024*1024*1024,
		})
	}
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

// POST /api/tools/reality-keypair {"core":"singbox"|"xray"}
func (a *Server) handleRealityKeypair(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Core string `json:"core"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	bin, _ := a.Store.GetSetting("bin_" + req.Core)
	if bin == "" {
		fail(w, 400, "未配置 "+req.Core+" 二进制路径")
		return
	}
	var cmd *exec.Cmd
	if req.Core == model.CoreXray {
		cmd = execCmd(bin, "x25519")
	} else {
		cmd = execCmd(bin, "generate", "reality-keypair")
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		fail(w, 500, fmt.Sprintf("%v: %s", err, string(out)))
		return
	}
	priv, pub := parseKeypair(string(out))
	if priv == "" || pub == "" {
		fail(w, 500, "无法解析输出:\n"+string(out))
		return
	}
	writeJSON(w, 200, map[string]string{"private_key": priv, "public_key": pub})
}

func parseKeypair(out string) (priv, pub string) {
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(ln)
		lower := strings.ToLower(ln)
		switch {
		case strings.HasPrefix(lower, "private") || strings.HasPrefix(lower, "privatekey"):
			priv = afterColon(ln)
		case strings.HasPrefix(lower, "public") || strings.HasPrefix(lower, "password"):
			pub = afterColon(ln)
		}
	}
	return
}

func afterColon(s string) string {
	if i := strings.Index(s, ":"); i >= 0 {
		return strings.TrimSpace(s[i+1:])
	}
	return s
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
func (a *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]string{"version": Version})
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
		URL   string `json:"url"`
		Sha256 string `json:"sha256"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.URL == "" {
		fail(w, 400, "需要 url 字段")
		return
	}
	exe, err := os.Executable()
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
	bak := exe + ".bak"
	os.Remove(bak)
	if err := os.Rename(exe, bak); err != nil {
		os.Remove(newBin)
		fail(w, 500, "备份旧二进制失败: "+err.Error())
		return
	}
	if err := os.Rename(newBin, exe); err != nil {
		_ = os.Rename(bak, exe) // 还原
		fail(w, 500, "替换失败: "+err.Error())
		return
	}
	os.Chmod(exe, 0o755)

	// 4. 重启（exec 替换进程映像，监听与数据无缝衔接）
	writeJSON(w, 200, map[string]string{"ok": "1", "sha256": got, "new_version": newVer, "msg": "更新完成，正在重启..."})
	go func() {
		time.Sleep(500 * time.Millisecond)
		syscall.Exec(exe, os.Args, os.Environ())
	}()
}
