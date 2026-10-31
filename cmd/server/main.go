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
	"runtime"

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
	go coll.Run()
	// ACME 证书自动签发/续期
	go certMgr.Run()

	log.Printf("triune-panel %s listening on %s, data dir %s", version, *addr, *dataDir)
	log.Printf("默认账号 admin/admin，登录后请立即修改")
	if err := http.ListenAndServe(*addr, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}
