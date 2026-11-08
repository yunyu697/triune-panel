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
	"time"

	"triune/internal/api"
	"triune/internal/authn"
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

// 构建时注入（主包符号名固定为 main，故用 main.Version）：
//
//	go build -ldflags "-X main.Version=v0.8.7"
var Version = "dev"

// main 是命令分发入口。
//
// 用法约定：
//   - 无子命令、或以 - 开头（`-addr :10010`、`-version`）→ 跑服务，
//     完全兼容旧用法（systemd unit 与 install.sh 的版本校验都依赖它）；
//   - 有子命令 → 交给 tp 命令表分发（见 tp.go）。旧命令名全部保留，
//     `serve` 除外——它是唯一的「前台跑起来」入口，不走命令表。
func main() {
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		if os.Args[1] == "serve" {
			serve(os.Args[2:])
			return
		}
		tpMain(os.Args[1:])
		return
	}
	// 以 tp 这个名字调用（安装脚本软链到 /usr/local/bin/tp）时，
	// 不带参数是「看状态」；以 triune-panel 调用时仍然是启动服务。
	if invokedAsTP() {
		tpMain(os.Args[1:])
		return
	}
	serve(os.Args[1:])
}

func printVersion() {
	fmt.Printf("triune-panel %s (%s/%s)\n", Version, runtime.GOOS, runtime.GOARCH)
}

func usage() {
	fmt.Print(`triune-panel（tp）— 三内核（mihomo / sing-box / xray）统一管理面板

用法（两个名字是同一个二进制；下面写 triune-panel 的地方都可以换成 tp）:
  triune-panel                       启动服务（等价 serve）
  triune-panel serve [-addr :10010] [-data ./data]
  triune-panel reset-password [--user <名>] [--password-stdin] [--keep-2fa]
                                     重置管理密码（忘记密码时用；默认生成随机密码）
  triune-panel status [--json]       只读汇总：版本/入站/用户/证书/上次应用
  triune-panel doctor                环境自检（数据目录、密码状态、内核、防火墙、端口）
  triune-panel backup [--out DIR] [--keep N] [--encrypt | --gen-passphrase]
                     [--upload REMOTE] [--upload-keep N]
                                     打包数据目录到 backups/（默认保留 7 份）
  triune-panel restore <归档.tar.gz[.age]> [--yes]
                                     用备份覆盖数据目录（恢复前自动备份当前数据）
  triune-panel version               打印版本
  triune-panel help                  本帮助

通用参数:
  --data DIR    数据目录；默认自动探测 ./data → /opt/triune-panel/data
                （也可用环境变量 TRIUNE_DATA 指定）

备份加密（age 口令模式，可选）:
  backup --encrypt            交互输入口令（两次确认，无回显）
  backup --gen-passphrase     随机生成口令，只打印一次
  backup/restore --passphrase-file FILE    从文件读口令（systemd timer 用）
  backup/restore --passphrase-stdin        从标准输入读口令
  TRIUNE_BACKUP_PASSPHRASE    环境变量，优先级低于上面两个参数
  口令至少 12 位；丢失口令 = 归档永久打不开（无后门、无法找回）。
  归档是标准 age 格式，可用 "age -d 归档.tar.gz.age > 还原.tar.gz" 独立解密。

异地备份（可选，需要 rclone）:
  backup --upload r2:triune-backups            上传到远端，传完回读校验字节数
  backup --upload r2:triune-backups --upload-keep 7   同时清理远端旧归档（只删本包命名的）
  backup --rclone /usr/local/bin/rclone        指定 rclone 路径（默认走 PATH）
  远端写法即 rclone 的 "remote:path"，支持 S3 / R2 / B2 / WebDAV / SFTP / 网盘等。
  上传失败时退出码 1，但本地归档仍然可用——不要把"没传上去"当成"已备份"。

退出码: 0 成功 · 1 执行失败 · 2 用法错误
`)
}

func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", ":10010", "listen address")
	dataDir := fs.String("data", "./data", "data directory")
	showVer := fs.Bool("version", false, "print version and exit")
	_ = fs.Parse(args)

	if *showVer {
		printVersion()
		return
	}

	// 统一转绝对路径，下游一律拿绝对值（见 absDataDir）。
	*dataDir = absDataDir(*dataDir)

	st, err := store.Open(*dataDir)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	applyBootstrapCreds(*dataDir, st)

	pm := proc.NewManager()
	// Version 会写进渲染戳（data/cores/<内核>/.rendered.json），
	// 启动时靠它判断磁盘上那份配置是不是上一版渲染的。
	pipe := &core.PipelineManager{DataDir: *dataDir, ProcMgr: pm, Settings: st, Version: Version}

	srv := api.New(st, pipe, pm, *dataDir)
	api.Version = Version

	// 上一次自更新若「换了二进制却没重启成功」，会留下一条说明。
	// 现在能走到这里就说明进程是新的——那条记录已经过期，留着只会误导。
	_ = st.SetSetting(api.SettingRestartFailed, "")

	// 守望者：磁盘上的二进制被换成别的版本就自己 exec 过去。
	//
	// 自更新自己会 exec，但「文件换了、进程没换」不止那一条路：一键脚本 cp、
	// 手工 mv、容器里 docker cp、systemd 只换了文件没重启……用户要的是
	// 「升级完成后立刻切到最新版」，不是「面板告诉你该重启了」。
	srv.StartBinaryWatch()

	// 备份容量治理：默认只统计不删除，用户在设置里显式打开 auto_prune 并设了
	// 上限才会在后台清理旧版本。启动后先等 10 分钟，留足回滚窗口。
	srv.StartBackupJanitor()

	// ACME 证书管理器：API 查询与后台签发/续期循环必须使用同一实例，
	// 否则 API 侧永远看不到后台签发的证书状态。
	certMgr := certmgr.New(*dataDir, st)
	srv.CertMgr = certMgr

	// 内置 Web UI（若外置 web 目录不存在则使用嵌入版）
	if err := webui.EmbedIfNeeded(*dataDir); err != nil {
		log.Printf("embed web ui: %v", err)
	}

	// 审计日志保留期清理：启动跑一次，之后每 12 小时一次。
	// 不清理的话长期运行会把库撑大（每条登录失败都记一条）。
	go func() {
		for {
			if n, err := st.PruneAudit(store.AuditKeepDays); err == nil && n > 0 {
				log.Printf("[audit] 清理超过 %d 天的审计记录 %d 条", store.AuditKeepDays, n)
			}
			time.Sleep(12 * time.Hour)
		}
	}()

	// 后台运维循环：流量采样 + 到期/配额自动禁用（同时供实时速率查询）
	coll := ops.NewCollector(st, pipe)
	srv.Coll = coll
	// 流量告警：越过阈值/配额时经 SMTP 发邮件（ops 不能反向依赖 api，注入回调）
	coll.Alert = func(subject, body string) { api.SendPanelAlert(st, subject, body) }
	go coll.Run()
	// ACME 证书自动签发/续期
	go certMgr.Run()

	// 控制套接字：给 tp 的写操作提供执行通道（对端 uid 校验见 api/control.go）。
	// 起不来不算致命——CLI 的写操作会报「套接字不存在」，面板本身照常工作。
	if l, path, err := api.OpenControlSocket(*dataDir); err != nil {
		log.Printf("[control] 控制套接字不可用，tp 的写操作将无法使用: %v", err)
	} else {
		log.Printf("[control] 控制套接字 %s", path)
		go func() {
			if err := srv.ServeControl(l); err != nil {
				log.Printf("[control] 服务退出: %v", err)
			}
		}()
	}

	// 启动对账：先按现有「已启用且非草稿」入站在防火墙补放行端口，
	// 再把该跑的内核拉起来。
	//
	// 两件事都放在同一个 goroutine 里按顺序做：先开端口再起进程，
	// 起来之后立刻就能对外服务，不用等下一次重试。
	// 内核恢复是必须的——systemctl restart 会连内核一起杀掉（systemd 默认
	// KillMode=control-group），而内核状态只存在内存里，不主动拉起的话
	// 节点会一直断到有人手动「应用配置」。
	go func() {
		srv.SyncFirewallPorts()
		srv.RestoreCores()
	}()

	log.Printf("triune-panel %s listening on %s, data dir %s", Version, *addr, *dataDir)
	if authn.IsFactoryDefault(st) {
		log.Printf("管理密码仍为出厂默认，首次登录将要求修改；忘记密码可执行 `triune-panel reset-password`")
	}

	handler := srv.Handler()
	if v, _ := st.GetSetting(api.SettingPanelHTTPS); v == "1" {
		cert, key, err := api.EnsurePanelCert(*dataDir)
		if err != nil {
			// 开了 HTTPS 就绝不静默退回明文：用户以为在加密、实际在明文传密码，
			// 比「起不来」危险得多。这里的报错必须自带走出来的路。
			log.Fatalf("已开启面板 HTTPS 但证书不可用，拒绝以明文启动: %v\n"+
				"  恢复：删除 %s/tls/ 下的 panel.crt / panel.key 后重启（会自动重建自签证书），\n"+
				"        或把设置 panel_https 置 0 关闭 HTTPS。", err, *dataDir)
			return
		}
		srv.TLSActive = true
		log.Printf("[tls] HTTPS 已启用，证书 %s", cert)
		log.Printf("[tls] 自签证书浏览器会警告「不安全」——这是预期的：自签只保证加密，不保证身份。")
		log.Printf("[tls] 首次在设备上信任前，请对照设置 → 安全 → 面板 HTTPS 里显示的 SHA-256 指纹。")
		s := &http.Server{
			Addr:              *addr,
			Handler:           handler,
			ReadHeaderTimeout: 15 * time.Second,
		}
		if err := s.ListenAndServeTLS(cert, key); err != nil && err != http.ErrServerClosed {
			// 证书/私钥不配对也会走到这里（LoadX509KeyPair 在监听前就失败）。
			// 同样不能退回明文，把走出来的路一并说清。
			log.Fatalf("HTTPS 监听失败: %v\n"+
				"  恢复：检查 %s 与 %s 是否配对；或删掉这两个文件让面板重建自签证书；\n"+
				"        也可以把设置 panel_https 置 0 关闭 HTTPS。", err, cert, key)
		}
		return
	}
	if err := http.ListenAndServe(*addr, handler); err != nil {
		log.Fatal(err)
	}
}

// absDataDir 把 -data 统一转成绝对路径。
//
// 为什么必须转：dataDir 会被拼进 certificate_path / key_path 存进 DB，而内核
// 进程的 cmd.Dir 会被切到 cores/<内核>——相对路径在那个 cwd 下指向不存在的地方。
// `serve -data ./data` 起面板时，导入证书的入站会报
// `open <别的目录>/certs/imported/…/fullchain.pem: no such file`，
// 看起来像路径拼接写错，实际是相对路径被换了参照系。
//
// install.sh 用的本来就是绝对路径，所以只有手工用相对路径起才会踩到；
// 转不了（极端情况）就原样返回，不因为这件事让面板起不来。
func absDataDir(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return dir
}

// applyBootstrapCreds 消费安装脚本落地的初始凭据文件 data/bootstrap.creds
// （key=value 逐行，如 admin_user=... / admin_pass=...，权限 0600）。
//
// 安全规则：只在管理密码仍是出厂默认时应用——用户一旦改过密码，文件里的
// 旧初始值一律忽略，直接删除，绝不覆盖真实密码。
// 密码在这里直接写成 bcrypt 哈希，明文只存在于 bootstrap.creds 本身（0600，
// 消费后立即删除），不落库。
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
	if !authn.IsFactoryDefault(st) {
		// 管理密码已被修改过（或此前已消费过初始凭据）：不覆盖
		log.Printf("bootstrap.creds 已忽略（管理密码已自定义）")
		return
	}
	if user == "" {
		user = "admin"
	}
	// 初始凭据写进超级管理员账号（多管理员之后不再是 settings 里的单账号槽）
	owner, err := st.OwnerAdmin()
	if err != nil {
		log.Printf("bootstrap: 找不到超级管理员账号: %v", err)
		return
	}
	if user != owner.Username {
		if err := authn.ValidateUsername(user); err != nil {
			log.Printf("bootstrap admin_user: %v", err)
			return
		}
		owner.Username = user
		if err := st.UpdateAdmin(owner); err != nil {
			log.Printf("bootstrap admin_user: %v", err)
			return
		}
	}
	if err := authn.SetPassword(st, pass); err != nil {
		log.Printf("bootstrap admin_pass: %v", err)
		return
	}
	log.Printf("已应用安装时生成的初始管理凭据（用户 %s），bootstrap.creds 已删除", user)
}
