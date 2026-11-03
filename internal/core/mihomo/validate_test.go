package mihomo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 校验命令必须带 -d：不带的话 mihomo 会去初始化 $HOME/.config/mihomo，
// rootfs 只读时直接 fatal，配置再正确也会被判成「内核拒绝这份配置」。
// 真实报错（v1.1.3 现场）：
//
//	Initial configuration directory error: can't create config directory
//	/root/.config/mihomo: mkdir /root/.config: read-only file system
func TestValidateCmdHasHomeDir(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("mode: rule\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := (&Mihomo{}).ValidateCmd("/usr/bin/mihomo", cfg)
	args := cmd.Args
	joined := strings.Join(args, " ")

	i := -1
	for k, a := range args {
		if a == "-d" {
			i = k
			break
		}
	}
	if i < 0 {
		t.Fatalf("-d 缺失，内核会在 $HOME 下建目录：%s", joined)
	}
	if i+1 >= len(args) {
		t.Fatalf("-d 后面没有值：%s", joined)
	}
	// home 必须是配置文件所在目录：mihomo 缺 GeoSite.dat / MMDB 时
	// 会联网下载并缓存在这里，复用运行目录才不会每次校验都重下一遍。
	if got := args[i+1]; got != dir {
		t.Errorf("home = %q, want %q（与配置同目录，才能复用 geo 缓存）", got, dir)
	}
	if !strings.Contains(joined, "-t") || !strings.Contains(joined, "-f "+cfg) {
		t.Errorf("校验参数不完整：%s", joined)
	}
}

// home 目录不存在时必须建出来，否则 -d 指过去仍然会失败。
func TestValidateCmdCreatesHome(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cores", "mihomo")
	cfg := filepath.Join(dir, "config.yaml")

	cmd := (&Mihomo{}).ValidateCmd("/usr/bin/mihomo", cfg)
	_ = cmd

	st, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("home 目录未创建: %v", err)
	}
	if !st.IsDir() {
		t.Fatalf("%s 不是目录", dir)
	}
}

func TestVerifyHomeFallback(t *testing.T) {
	// 相对文件名（无目录）时退回临时目录，不能返回 "."（可能只读）。
	if got := verifyHome("config.yaml"); got != os.TempDir() {
		t.Errorf("verifyHome(\"config.yaml\") = %q, want %q", got, os.TempDir())
	}
}
