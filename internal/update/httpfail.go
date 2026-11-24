package update

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RateLimitError 上游把请求挡在配额上。
//
// 这不是「面板坏了」：GitHub 对匿名请求限 60 次/小时，而且按**出口 IP** 算——
// 一个 NAT 后面的所有人、以及同一台机器上的其它工具共用这一份。原来的实现
// 只把 `HTTP 403` 原样甩出来，用户看到的是「获取版本列表失败: HTTP 403」，
// 既不知道是限流、也不知道等多久、更不知道能做什么，只能反复点重试。
type RateLimitError struct {
	Limit     int       // 配额上限（匿名 60，带 Token 5000；可能为 0 表示未知）
	Remaining int       // 剩余次数
	Reset     time.Time // 配额恢复时刻
	Resource  string    // core / search / graphql
	Status    int       // 原始状态码（403 或 429）
}

func (e *RateLimitError) Error() string {
	var b strings.Builder
	b.WriteString("GitHub 接口配额已用完")
	if e.Limit > 0 {
		fmt.Fprintf(&b, "（%d 次/小时，匿名请求按出口 IP 计）", e.Limit)
	}
	if !e.Reset.IsZero() {
		if wait := time.Until(e.Reset); wait > 0 {
			// 向上取整到分钟：说「约 5 分钟」实际还要等 5 分 40 秒，比不说更糟。
			mins := int((wait + time.Minute - 1) / time.Minute)
			fmt.Fprintf(&b, "，%s 恢复（约 %d 分钟）", e.Reset.Local().Format("15:04"), mins)
		}
	}
	b.WriteString("。可以：①在「更新源」填一个 GitHub Token（上限提到 5000 次/小时）；")
	b.WriteString("②把面板改走版本索引清单；③等配额恢复后再试。")
	return b.String()
}

// IsRateLimited 判断错误是不是配额耗尽。
//
// 调用方据此决定要不要换一条**不计配额**的通道（releases 订阅），
// 而不是把错误原样交给用户。按类型判断而不是匹配错误字符串：
// 后者在文案改动时会静默失效。
func IsRateLimited(err error) bool {
	var rl *RateLimitError
	return errors.As(err, &rl)
}

// httpFail 把上游的非 200 响应翻译成能照做的错误。
//
// 配额耗尽单独成型（见 RateLimitError），其余状态码只补一句最可能的成因：
// 版本列表拿不到时，404 十有八九是仓库名写错或镜像不支持这个路径。
func httpFail(what string, resp *http.Response) error {
	if rl := rateLimitFrom(resp); rl != nil {
		return rl
	}
	switch resp.StatusCode {
	case http.StatusNotFound:
		return fmt.Errorf("%s: HTTP 404（仓库或路径不存在；若填了 gh_api_base 镜像，"+
			"确认它支持 /repos/<owner>/<repo>/releases）", what)
	case http.StatusUnauthorized:
		return fmt.Errorf("%s: HTTP 401（GitHub Token 无效或已过期，"+
			"清空「更新源」里的 Token 可退回匿名访问）", what)
	}
	return fmt.Errorf("%s: HTTP %d", what, resp.StatusCode)
}

// rateLimitFrom 从响应头判断这是不是配额耗尽。
//
// 判据是 GitHub 自己给的信号，不是状态码：403 也可能是 Token 权限不足或被
// 代理拦，那些情况等一小时也不会好，不能当成限流去「重试」。
func rateLimitFrom(resp *http.Response) *RateLimitError {
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
		return nil
	}
	rem := strings.TrimSpace(resp.Header.Get("X-RateLimit-Remaining"))
	retry := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if rem != "0" && retry == "" {
		return nil
	}
	rl := &RateLimitError{
		Status:   resp.StatusCode,
		Resource: strings.TrimSpace(resp.Header.Get("X-RateLimit-Resource")),
	}
	rl.Limit, _ = strconv.Atoi(strings.TrimSpace(resp.Header.Get("X-RateLimit-Limit")))
	rl.Remaining, _ = strconv.Atoi(rem)
	if v, err := strconv.ParseInt(strings.TrimSpace(resp.Header.Get("X-RateLimit-Reset")), 10, 64); err == nil && v > 0 {
		rl.Reset = time.Unix(v, 0)
	}
	// Retry-After 优先：它是「次级限流」（并发过高）时唯一的恢复时刻。
	if retry != "" {
		if sec, err := strconv.Atoi(retry); err == nil && sec > 0 {
			rl.Reset = time.Now().Add(time.Duration(sec) * time.Second)
		}
	}
	return rl
}
