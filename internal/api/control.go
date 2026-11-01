package api

// 控制套接字：让 `tp` 的写操作把意图交给 daemon 执行。
//
// 为什么写操作必须走 daemon，而不是 CLI 直接改库：
// 内核进程状态只存在 proc.Manager 的内存里，没有 pid 文件。CLI 自己拉起
// 内核会和 daemon 抢端口，并且让 daemon 的内存状态和磁盘对不上——表现为
// 「面板里显示运行中，实际早死了」。所以写操作只能有一个执行者。
//
// 认证用 SO_PEERCRED（Linux）/ Getpeereid（BSD、macOS）：内核在 connect
// 的那一刻就把对端 uid 记在 socket 上，不需要对端配合，也没法伪造。
// 允许 uid 0 或 daemon 自己的 uid——「能 sudo 的人本来就能改库」，多卡一层
// 只是把同样的权限换个更绕的路走。
//
// 传输用 HTTP over unix socket 而不是自定义协议：业务逻辑全在现有 handler
// 里（apply 的预检与回滚、防火墙对账、审计、变更通知），复用它们比照着
// 重写一遍安全得多。顺带一个好处：`curl --unix-socket` 就能手工调试。

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"triune/internal/authn"
	"triune/internal/store"
)

const (
	controlSockName = "control.sock"
	controlRunDir   = "/run/triune-panel"
)

// ControlSocketCandidates 按优先级列出控制套接字的可能位置。
//
// daemon 与 CLI 用的是同一份顺序：daemon 挑第一个建得起来的，CLI 挑第一个
// 存在的。两边各写一份路径规则迟早会走岔，所以只留这一处。
func ControlSocketCandidates(dataDir string) []string {
	if p := strings.TrimSpace(os.Getenv("TRIUNE_CONTROL_SOCK")); p != "" {
		return []string{p}
	}
	out := []string{filepath.Join(controlRunDir, controlSockName)}
	if dataDir != "" {
		out = append(out, filepath.Join(dataDir, controlSockName))
	}
	return out
}

// OpenControlSocket 建好目录并监听控制套接字。
//
// 目录优先用 /run：它是 tmpfs，重启即清空，不会留下上次运行崩掉时的陈尸
// 套接字；而且 /run 只有 root 能写，文件系统权限本身就是第一道门。
// 非 root 环境（本地开发、用户目录安装）退到数据目录。
func OpenControlSocket(dataDir string) (net.Listener, string, error) {
	var lastErr error
	for _, path := range ControlSocketCandidates(dataDir) {
		l, err := listenUnix(path)
		if err == nil {
			return l, path, nil
		}
		lastErr = err
	}
	return nil, "", lastErr
}

func listenUnix(path string) (net.Listener, error) {
	dir := filepath.Dir(path)
	// 只在目录不存在时创建：已存在就按现有权限用，不去 chmod 别人的数据目录。
	if _, err := os.Stat(dir); err != nil {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	// 上次进程被 SIGKILL 时会留下套接字文件，Listen 会报 address already in use。
	// 只清理「确实是套接字」的文件，别把同名普通文件删了。
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSocket != 0 {
		_ = os.Remove(path)
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	// 0600：能连上就等于「以 root 身份改面板」，这个文件不该有第二个读者。
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

// ServeControl 在 l 上服务控制套接字，直到 l 被关闭。
//
// 调用方应当 `go` 出去：控制套接字起不来不该拦住面板启动（CLI 的写操作
// 会报「套接字不存在」，而面板本身照常工作）。
func (a *Server) ServeControl(l net.Listener) error {
	srv := &http.Server{
		Handler:           a.controlHandler(),
		ReadHeaderTimeout: 10 * time.Second,
		// 把连接塞进请求 context，鉴权中间件要靠它拿对端 uid。
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			return context.WithValue(ctx, ctxConnKey, c)
		},
	}
	err := srv.Serve(l)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

type controlCtxKey int

const ctxConnKey controlCtxKey = iota

// controlHandler 白名单 + 身份校验。
//
// **不直接把 a.Handler() 挂上来**：那等于把整套 API（账号管理、面板自更新、
// 整面板回滚、设置读写）搬到本地套接字上。一旦 peer 校验哪天出了岔子，
// 后果从「改错一个入站」变成「整个面板被接管」。这里只放 CLI 真正需要的那
// 几个写接口，清单短到可以一眼看完。
func (a *Server) controlHandler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("PUT /api/inbounds/{id}", a.updateInbound)    // tp in on/off
	mux.HandleFunc("DELETE /api/inbounds/{id}", a.deleteInbound) // tp in rm

	mux.HandleFunc("POST /api/clients", a.createClient)                   // tp user add
	mux.HandleFunc("PUT /api/clients/{id}", a.updateClient)               // tp user set
	mux.HandleFunc("DELETE /api/clients/{id}", a.deleteClient)            // tp user rm
	mux.HandleFunc("PUT /api/clients/{id}/inbounds", a.setClientInbounds) // tp user set --inbounds
	mux.HandleFunc("POST /api/clients/{id}/reset-token", a.resetSubToken) // tp user token

	mux.HandleFunc("POST /api/apply", a.handleApply)       // tp apply
	mux.HandleFunc("POST /api/rollback", a.handleRollback) // tp rollback

	return a.controlAuth(mux)
}

// controlAuth 校验对端 uid，然后把一个合成身份塞进 context。
//
// 这里不走 a.auth()：那条路认的是会话 token，而本地命令行没有 token——
// 它有的是「进程 uid」这个更强的凭据。
func (a *Server) controlAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := r.Context().Value(ctxConnKey).(*net.UnixConn)
		if c == nil {
			fail(w, http.StatusForbidden, "控制套接字只接受 unix 连接")
			return
		}
		uid, err := peerUID(c)
		if err != nil {
			fail(w, http.StatusForbidden, "无法确认对端身份，拒绝访问（"+err.Error()+"）")
			return
		}
		if !controlUIDAllowed(uid) {
			fail(w, http.StatusForbidden,
				fmt.Sprintf("控制套接字只接受 root 或面板运行账号（对端 uid=%d）", uid))
			return
		}

		// 套接字没有 IP，审计里记回环地址；操作人字段已经写明是 cli，
		// 两者合起来足够定位。转发头在这里没有任何意义，而且本机任何进程
		// 都能伪造，直接丢掉。
		r.RemoteAddr = "127.0.0.1:0"
		r.Header.Del("X-Forwarded-For")
		r.Header.Del("X-Real-IP")

		next.ServeHTTP(w, r.WithContext(
			context.WithValue(r.Context(), ctxUserKey, controlUser(uid))))
	})
}

// controlUIDAllowed 谁能用控制套接字：root，或者面板自己的运行账号。
//
// 后者是为了非 root 安装（TRIUNE_INSTALL_DIR=~/triune-panel）：那种情况下
// 面板和用户是同一个人，要求 sudo 只会逼人多绕一步。
func controlUIDAllowed(uid int) bool {
	return uid == 0 || uid == os.Geteuid()
}

// controlUser 合成一个「本机命令行」身份。
//
// 不借用真实的 owner 账号：审计里需要能分清「谁在面板上点的」和「谁在服务器
// 上敲的命令」。冒充 owner 会让这两种来源在审计里长得一模一样，出事时查不清。
// 名字里带 uid，多用户机器上也能看出是哪个账号。
func controlUser(uid int) *store.AdminUser {
	name := "cli@root"
	if uid != 0 {
		name = fmt.Sprintf("cli:uid%d", uid)
	}
	return &store.AdminUser{
		Username: name,
		Role:     authn.RoleOwner,
		Enabled:  true,
	}
}
