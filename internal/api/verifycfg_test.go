package api

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyCmdMihomoHome(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	cmd := verifyCmd("mihomo", "/usr/bin/mihomo", cfg, dir)
	if cmd == nil {
		t.Fatal("mihomo 校验命令为空")
	}
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "-t") || !strings.Contains(joined, "-d "+dir) {
		t.Fatalf("mihomo 校验命令缺少 -t/-d：%s", joined)
	}
}

// 三家内核都要在可写 HOME 下跑：真实环境里 $HOME 可能是只读的 /root，
// 内核哪怕只是「可能写」也不该由它决定配置能不能用。
func TestVerifyCmdEnvHasWritableHome(t *testing.T) {
	dir := t.TempDir()
	for _, core := range []string{"singbox", "mihomo", "xray"} {
		cmd := verifyCmd(core, "/usr/bin/"+core, filepath.Join(dir, "c"), dir)
		if cmd == nil {
			t.Fatalf("%s 校验命令为空", core)
		}
		var hasHome, hasCache bool
		for _, kv := range cmd.Env {
			switch {
			case kv == "HOME="+dir:
				hasHome = true
			case strings.HasPrefix(kv, "XDG_CACHE_HOME="+dir):
				hasCache = true
			case strings.HasPrefix(kv, "HOME="):
				t.Errorf("%s 仍带着旧 HOME=%s", core, strings.TrimPrefix(kv, "HOME="))
			}
		}
		if !hasHome || !hasCache {
			t.Errorf("%s 环境未指向可写目录（HOME=%v cache=%v）", core, hasHome, hasCache)
		}
	}
}

func TestEnvHint(t *testing.T) {
	cases := []struct {
		out  string
		want bool
	}{
		{"Initial configuration directory error: can't create config directory /root/.config/mihomo: mkdir /root/.config: read-only file system", true},
		{"open /opt/triune-panel/data/cores/mihomo/cache.db: permission denied", true},
		{"write cache.db: no space left on device", true},
		{"parse config: yaml: line 3: did not find expected key", false},
	}
	for _, c := range cases {
		got := envHint(c.out)
		if c.want && got == "" {
			t.Errorf("应识别环境问题却返回空：%s", c.out)
		}
		if !c.want && got != "" {
			t.Errorf("把配置错误误判成环境问题：%s → %s", c.out, got)
		}
	}
}
