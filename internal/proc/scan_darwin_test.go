//go:build darwin

package proc

import (
	"encoding/binary"
	"testing"
)

// kern.procargs2 的字节布局要自己拆，拆错的表现是「扫不到任何进程」——
// 清理孤儿静默失效，而单测里的假数据是唯一能在没有 macOS 权限差异时验证它的地方。
func TestParseProcArgs(t *testing.T) {
	build := func(argc uint32, path string, pad int, args ...string) []byte {
		var b []byte
		b = binary.LittleEndian.AppendUint32(b, argc)
		b = append(b, path...)
		b = append(b, 0)
		for i := 0; i < pad; i++ {
			b = append(b, 0)
		}
		for _, a := range args {
			b = append(b, a...)
			b = append(b, 0)
		}
		b = append(b, "HOME=/root"...)
		b = append(b, 0)
		return b
	}

	t.Run("正常", func(t *testing.T) {
		raw := build(3, "/opt/xray", 2, "/opt/xray", "run", "-c")
		path, args := parseProcArgs(raw)
		if path != "/opt/xray" {
			t.Fatalf("路径 = %q", path)
		}
		if len(args) != 3 || args[0] != "/opt/xray" || args[2] != "-c" {
			t.Fatalf("参数 = %q（环境变量不能被算进来）", args)
		}
	})

	t.Run("没有对齐填充", func(t *testing.T) {
		raw := build(2, "/opt/xray", 0, "/opt/xray", "run")
		path, args := parseProcArgs(raw)
		if path != "/opt/xray" || len(args) != 2 {
			t.Fatalf("path=%q args=%q", path, args)
		}
	})

	t.Run("参数少于 argc", func(t *testing.T) {
		// 块被截断时不能越界、也不能崩。argc 说 4 个，块里只剩 1 个参数，
		// 后面紧跟的就是环境变量——分不清 args 与 env 是格式本身的局限，
		// 只要不越界、不多读就行。
		raw := build(4, "/opt/xray", 0, "/opt/xray")
		path, args := parseProcArgs(raw)
		if path != "/opt/xray" {
			t.Fatalf("路径 = %q", path)
		}
		if len(args) == 0 || len(args) > 4 {
			t.Fatalf("参数个数应在 1..4 之间，实际 %q", args)
		}
	})

	t.Run("空块", func(t *testing.T) {
		if p, a := parseProcArgs(nil); p != "" || a != nil {
			t.Fatalf("空输入应返回空，实际 %q %q", p, a)
		}
		if p, a := parseProcArgs([]byte{1, 0}); p != "" || a != nil {
			t.Fatalf("不足 4 字节应返回空，实际 %q %q", p, a)
		}
	})
}
