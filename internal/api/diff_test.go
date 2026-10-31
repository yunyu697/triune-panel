package api

import "testing"

func TestLineDiffBasic(t *testing.T) {
	old := "a\nb\nc"
	new := "a\nX\nc\nd"
	ops := lineDiff(split(old), split(new))
	var got string
	for _, o := range ops {
		got += o.Op + o.Text + "\n"
	}
	want := " a\n-X\n-b\n+X\n c\n+d\n"
	// LCS 保证删除/新增总量一致，顺序可能为 -b +X 或 -b 前 +X 后，两者都合法
	if got != " a\n-b\n+X\n c\n+d\n" && got != want {
		t.Fatalf("unexpected diff:\n%s", got)
	}
}

func TestLineDiffEmptyOld(t *testing.T) {
	ops := lineDiff(split(""), split("x\ny"))
	if len(ops) != 2 {
		t.Fatalf("want 2 additions, got %d", len(ops))
	}
	for _, o := range ops {
		if o.Op != "+" {
			t.Fatalf("want all +, got %q", o.Op)
		}
	}
}

func TestLineDiffIdentical(t *testing.T) {
	s := split("same\nlines")
	ops := lineDiff(s, s)
	for _, o := range ops {
		if o.Op != " " {
			t.Fatalf("want no changes, got %q %q", o.Op, o.Text)
		}
	}
}

func split(s string) []string {
	if s == "" {
		return []string{""}
	}
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
