package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 「detour 指向一个空 direct 出站」是 sing-box 1.12 起会直接拒绝启动的配置，
// 而 `sing-box check` 查不出来（校验发生在 start service 阶段）：
//
//	FATAL start service: start dns/udp[local]:
//	detour to an empty direct outbound makes no sense
//
// 上游说明（SagerNet/sing-box#2803、#2778）：新格式 DNS server 本身就是一个
// dialer，"equivalent to using an empty direct outbound by default"——不写
// detour 就是直连，写 detour: "direct" 是重复声明同一个东西，所以被拒。
//
// 这两条用例守住的是机制：以后谁再把 detour: "direct" 加回 DNS 段，或者
// 给 direct 出站加字段让它「非空」以为能绕过，都会立刻红。

// plainDirectTags 返回「除了 type 和 tag 没有任何选项」的 direct 出站 tag。
// 判定与 sing-box 一致：整个 options blob 为空才算 plain。
func plainDirectTags(doc map[string]any) map[string]bool {
	plain := map[string]bool{}
	obs, _ := doc["outbounds"].([]any)
	for _, it := range obs {
		o, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := o["type"].(string); t != "direct" {
			continue
		}
		empty := true
		for k := range o {
			if k != "type" && k != "tag" {
				empty = false
				break
			}
		}
		if !empty {
			continue
		}
		if tag, _ := o["tag"].(string); tag != "" {
			plain[tag] = true
		}
	}
	return plain
}

// detoursToPlainDirect 列出所有指向空 direct 出站的 detour，形如 dns/udp[local]。
func detoursToPlainDirect(doc map[string]any) []string {
	plain := plainDirectTags(doc)
	if len(plain) == 0 {
		return nil
	}
	var bad []string
	dns, _ := doc["dns"].(map[string]any)
	servers, _ := dns["servers"].([]any)
	for _, it := range servers {
		s, ok := it.(map[string]any)
		if !ok {
			continue
		}
		d, _ := s["detour"].(string)
		if d == "" || !plain[d] {
			continue
		}
		bad = append(bad, fmt.Sprintf("dns/%v[%v] -> %s", s["type"], s["tag"], d))
	}
	return bad
}

// 1.12+ 的新格式绝不能出现这种 detour；1.11 的旧格式则必须显式写 direct
// （旧格式不写 detour 会走 route.final，本地 DNS 就绕进代理了）。
func TestNoDetourToPlainDirect(t *testing.T) {
	versions := []string{"1.11", "1.12", "1.13", "1.14", "1.15"}
	check := func(name string, doc map[string]any, newFormat bool) {
		t.Helper()
		bad := detoursToPlainDirect(doc)
		if newFormat {
			if len(bad) > 0 {
				t.Errorf("%s：新格式 DNS server 不能 detour 到空 direct 出站（会 FATAL 起不来）：%s\n"+
					"1.12 起不写 detour 就等价于直连，删掉这个字段即可", name, strings.Join(bad, "、"))
			}
			return
		}
		// 旧格式：local 必须显式直连，否则走 route.final
		found := false
		dns, _ := doc["dns"].(map[string]any)
		servers, _ := dns["servers"].([]any)
		for _, it := range servers {
			s, _ := it.(map[string]any)
			if s["tag"] == "local" && s["detour"] == "direct" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s：1.11 旧格式的 local DNS 必须显式 detour: direct（不写会走 route.final 绕进代理）", name)
		}
	}

	for _, ver := range versions {
		v, err := parseSBVersion(ver)
		if err != nil {
			t.Fatalf("%s: %v", ver, err)
		}
		newFormat := capsFor(v).NewDNSFormat
		for _, target := range []string{"client", "main-router", "side-router"} {
			warns := []string{}
			g := genSingbox(target, v, testNodes(), nil, nil, &warns, ruleMirrorSel{}, bypassSpec{})
			check(ver+"/"+target, g.doc, newFormat)
		}
		// momo 要求 1.12+，旧格式分支没有意义。
		if newFormat {
			warns := []string{}
			g := genMomo(v, testNodes(), nil, nil, &warns, ruleMirrorSel{}, bypassSpec{})
			check(ver+"/openwrt-momo", g.doc, true)
		}
	}
}

// 结构断言只能证明字段没写错位置。这一条用真内核**真正启动**收口：
// 上一条用例发现的缺陷，`sing-box check` 是绿的，只有 run 才会 FATAL。
// 设 TRINE_SINGBOX_BIN 就跑，与 TestMomoConfigPassesRealSingboxCheck 同约定。
func TestSingboxClientConfigStarts(t *testing.T) {
	bin := os.Getenv("TRINE_SINGBOX_BIN")
	if bin == "" {
		t.Skip("未设置 TRINE_SINGBOX_BIN，跳过真机启动校验")
	}
	v, _ := parseSBVersion("1.14")
	warns := []string{}
	g := genSingbox("client", v, testNodes(), nil, nil, &warns, ruleMirrorSel{}, bypassSpec{})

	// 换成空闲端口：本机 7890 很可能被用户自己的代理占着。
	port := freeTCPPort(t)
	if inbounds, ok := g.doc["inbounds"].([]any); ok {
		for _, it := range inbounds {
			if m, ok := it.(map[string]any); ok {
				m["listen_port"] = port
			}
		}
	}
	raw, err := json.MarshalIndent(g.doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "client.json")
	if err := os.WriteFile(file, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	cmd := exec.Command(bin, "run", "-c", file)
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动 sing-box 失败: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		t.Fatalf("sing-box 启动即退出（%v）：\n%s", err, buf.String())
	case <-time.After(3 * time.Second):
		// 还活着：远程规则集可能仍在下载，不作为失败判据。
		_ = cmd.Process.Kill()
		<-done
	}
	if strings.Contains(buf.String(), "FATAL") {
		t.Fatalf("启动日志里有 FATAL：\n%s", buf.String())
	}
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("取空闲端口失败: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
