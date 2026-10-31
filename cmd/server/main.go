// triune-panel：三内核（mihomo/sing-box/xray）统一管理面板。
//
// 配置安全保证：
//   - DB 只存用户意图，运行配置是渲染产物；
//   - 应用前必须通过内核自带校验，失败则运行配置零改动；
//   - 每次替换前自动备份，支持一键回滚；
//   - 面板升级 schema 只增不删，未知字段 JSON 原样保留。
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"triune/internal/api"
	"triune/internal/certmgr"
	"triune/internal/core"
	_ "triune/internal/core/mihomo"
	_ "triune/internal/core/singbox"
	_ "triune/internal/core/xray"
	"triune/internal/ops"
	"triune/internal/proc"
	"triune/internal/store"
	"triune/internal/webui"
)

// 构建时注入：go build -ldflags "-X main.version=v1.0.0"
var version = "dev"

func main() {
	addr := flag.String("addr", ":10010", "listen address")
	dataDir := flag.String("data", "./data", "data directory")
	showVer := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVer {
		fmt.Printf("triune-panel %s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
		return
	}

	st, err := store.Open(*dataDir)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	applyBootstrapCreds(*dataDir, st)

	pm := proc.NewManager()
	pipe := &core.PipelineManager{DataDir: *dataDir, ProcMgr: pm, Settings: st}

	srv := api.New(st, pipe, pm, *dataDir)
	api.Version = version

	// ACME 证书管理器：API 查询与后台签发/续期循环必须使用同一实例，
	// 否则 API 侧永远看不到后台签发的证书状态。
	certMgr := certmgr.New(*dataDir, st)
	srv.CertMgr = certMgr

	// 内置 Web UI（若外置 web 目录不存在则使用嵌入版）
	if err := webui.EmbedIfNeeded(*dataDir); err != nil {
		log.Printf("embed web ui: %v", err)
	}

	// 后台运维循环：流量采样 + 到期/配额自动禁用（同时供实时速率查询）
	coll := ops.NewCollector(st, pipe)
	srv.Coll = coll
	// 流量告警：越过阈值/配额时经 SMTP 发邮件（ops 不能反向依赖 api，注入回调）
	coll.Alert = func(subject, body string) { api.SendPanelAlert(st, subject, body) }
	go coll.Run()
	// ACME 证书自动签发/续期
	go certMgr.Run()

	log.Printf("triune-panel %s listening on %s, data dir %s", version, *addr, *dataDir)
	if p, _ := st.GetSetting("admin_pass"); p == "" {
		log.Printf("管理密码仍为出厂默认，首次登录将要求修改；新装可用 data/bootstrap.creds 设置初始凭据")
	}
	if err := http.ListenAndServe(*addr, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}

// applyBootstrapCreds 消费安装脚本落地的初始凭据文件 data/bootstrap.creds
// （key=value 逐行，如 admin_user=... / admin_pass=...，权限 0600）。
//
// 安全规则：只在管理密码仍是出厂默认（admin_pass 设置为空）时应用——
// 用户一旦改过密码，文件里的旧初始值一律忽略，直接删除，绝不覆盖真实密码。
func applyBootstrapCreds(dataDir string, st *store.Store) {
	path := filepath.Join(dataDir, "bootstrap.creds")
	raw, err := os.ReadFile(path)
	if err != nil {
		return // 没有该文件是常态（升级/手动安装）
	}
	defer func() {
		if rmErr := os.Remove(path); rmErr != nil {
			log.Printf("remove bootstrap.creds: %v", rmErr)
		}
	}()

	creds := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			creds[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	user, pass := creds["admin_user"], creds["admin_pass"]
	if pass == "" {
		log.Printf("bootstrap.creds 缺少 admin_pass，忽略")
		return
	}
	cur, _ := st.GetSetting("admin_pass")
	if cur != "" {
		// 管理密码已被修改过（或此前已消费过初始凭据）：不覆盖
		log.Printf("bootstrap.creds 已忽略（管理密码已自定义）")
		return
	}
	if user == "" {
		user = "admin"
	}
	if err := st.SetSetting("admin_user", user); err != nil {
		log.Printf("bootstrap admin_user: %v", err)
		return
	}
	if err := st.SetSetting("admin_pass", pass); err != nil {
		log.Printf("bootstrap admin_pass: %v", err)
		return
	}
	log.Printf("已应用安装时生成的初始管理凭据（用户 %s），bootstrap.creds 已删除", user)
}
