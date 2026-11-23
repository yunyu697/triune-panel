package api

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// mihomo 的产物以前只跑过 `-t`（配置校验），而 -t 不 bind 端口、不初始化
// DNS 与规则提供者——端口被占、DNS 段写坏、启动即退出它都看不见。
// v2.1.0 的 sing-box detour 事故就是这个盲区的另一面，所以 mihomo 也补一条
// 真正启动的用例。设 TRINE_MIHOMO_BIN 才跑。
func TestMihomoClientConfigStarts(t *testing.T) {
	bin := os.Getenv("TRINE_MIHOMO_BIN")
	if bin == "" {
		t.Skip("未设置 TRINE_MIHOMO_BIN，跳过真机启动校验")
	}
	warns := []string{}
	g := genMihomo("client", testNodes(), nil, nil, &warns, ruleMirrorSel{}, bypassSpec{})

	// 端口全部换掉：本机 7890 / 9090 很可能是用户自己在跑的实例。
	g.doc["mixed-port"] = freeTCPPort(t)
	g.doc["external-controller"] = "127.0.0.1:" + strconv.Itoa(freeTCPPort(t))

	raw, err := yaml.Marshal(g.doc)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(file, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	cmd := exec.Command(bin, "-d", dir, "-f", file)
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动 mihomo 失败: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		t.Fatalf("mihomo 启动即退出（%v）：\n%s", err, buf.String())
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
	if strings.Contains(buf.String(), "level=fatal") {
		t.Fatalf("启动日志里有 fatal：\n%s", buf.String())
	}
}

// router 模式（旁路由）的配置在 client 用例里完全覆盖不到：TUN、sniffer、
// DNS listen 都只在这一支出现。真启动 TUN 要 root，这里用 `-t` 只验结构：
// 它能抓「字段类型不对、嵌套层级错」，抓不到「键名拼错」——mihomo 对未知键
// 是静默忽略的。键名本身的正确性只能靠生成器断言
// （genconfig_hardening_test.go 的 TestMihomoSnifferCoversQUIC）。
func TestMihomoRouterConfigPassesCheck(t *testing.T) {
	bin := os.Getenv("TRINE_MIHOMO_BIN")
	if bin == "" {
		t.Skip("未设置 TRINE_MIHOMO_BIN，跳过真机校验")
	}
	warns := []string{}
	g := genMihomo("main-router", testNodes(), nil, nil, &warns, ruleMirrorSel{}, bypassSpec{})
	// 端口换掉：本机 7890 / 9090 很可能被用户自己的实例占着。
	g.doc["mixed-port"] = freeTCPPort(t)
	g.doc["external-controller"] = "127.0.0.1:" + strconv.Itoa(freeTCPPort(t))

	raw, err := yaml.Marshal(g.doc)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(file, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command(bin, "-t", "-d", dir, "-f", file).CombinedOutput()
	if err != nil {
		t.Fatalf("mihomo -t 拒绝 router 配置（%v）：\n%s", err, out)
	}
	if strings.Contains(string(out), "level=fatal") {
		t.Fatalf("校验日志里有 fatal：\n%s", out)
	}
}

// 豁免规则用的是 DOMAIN-KEYWORD / DOMAIN-SUFFIX。关键字规则类型写错时
// mihomo 未必报错——宽容忽略之后表现为「规则在、永不命中」，正是这次红果
// 事故的形态。所以除了产物断言，还要让真内核亲口认一次。
//
// 这里走自定义规则通道传同一份清单，不带远程规则集：要证的是「类型合法」，
// 不是「镜像拉得下来」（后者归 livecheck 与门禁管，别让本用例依赖网络）。
func TestMihomoRouterAcceptsBlockExemptRules(t *testing.T) {
	bin := os.Getenv("TRINE_MIHOMO_BIN")
	if bin == "" {
		t.Skip("未设置 TRINE_MIHOMO_BIN，跳过真机校验")
	}
	warns := []string{}
	g := genMihomo("main-router", testNodes(), nil, blockExemptRules, &warns, ruleMirrorSel{}, bypassSpec{})
	g.doc["mixed-port"] = freeTCPPort(t)
	g.doc["external-controller"] = "127.0.0.1:" + strconv.Itoa(freeTCPPort(t))

	raw, err := yaml.Marshal(g.doc)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(file, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bin, "-t", "-d", dir, "-f", file).CombinedOutput()
	if err != nil {
		t.Fatalf("mihomo -t 拒绝豁免规则（%v）：\n%s", err, out)
	}
	if strings.Contains(string(out), "level=fatal") {
		t.Fatalf("校验日志里有 fatal：\n%s", out)
	}
}
