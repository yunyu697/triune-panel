package webui

import (
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// 主题契约：7 套配色 × 亮暗 14 个组合，必须由生成器展开**同一组**四层令牌。
// 手写 CSS 时最容易漏的就是「某个方案少覆盖几个中性色键、语义色与阴影只挂在
// mode 层」——换配色于是只改了几个按钮。这里从 index.html 的真实内容解析，
// 而不是去读生成器，保证测的是浏览器实际会拿到的那份样式。
func themeBlock(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	i := strings.Index(s, "/* THEME:BEGIN")
	j := strings.Index(s, "/* THEME:END */")
	if i < 0 || j < 0 || j <= i {
		t.Fatal("index.html 找不到 THEME:BEGIN / THEME:END 标记，主题令牌块被删了")
	}
	return s[i:j]
}

var comboRe = regexp.MustCompile(`\[data-mode="(\w+)"\]\[data-scheme="(\w+)"\]\{([^}]*)\}`)
var declRe = regexp.MustCompile(`(--[\w-]+):([^;]+);`)

func parseCombos(t *testing.T) map[string]map[string]string {
	t.Helper()
	out := map[string]map[string]string{}
	for _, m := range comboRe.FindAllStringSubmatch(themeBlock(t), -1) {
		key := m[1] + "/" + m[2]
		if _, dup := out[key]; dup {
			t.Fatalf("组合 %s 定义了两次", key)
		}
		vars := map[string]string{}
		for _, d := range declRe.FindAllStringSubmatch(m[3], -1) {
			vars[d[1]] = d[2]
		}
		out[key] = vars
	}
	return out
}

func TestThemeHasAllFourteenCombos(t *testing.T) {
	combos := parseCombos(t)
	gen, err := os.ReadFile("../../deploy/build-theme.py")
	if err != nil {
		t.Fatal(err)
	}
	// 方案清单必须与生成器、app.js、boot.js 四处同步。新增配色忘了同步首屏
	// 白名单时，深色用户刷新会先画一帧没有样式的一页。
	ids := regexp.MustCompile(`dict\(id="([a-z]+)"`).FindAllStringSubmatch(string(gen), -1)
	want := make([]string, 0, len(ids))
	for _, m := range ids {
		want = append(want, m[1])
	}
	if len(want) != 7 {
		t.Fatalf("生成器里应有 7 套配色，实际 %d", len(want))
	}
	for _, mode := range []string{"light", "dark"} {
		for _, id := range want {
			if _, ok := combos[mode+"/"+id]; !ok {
				t.Errorf("缺少组合 %s/%s", mode, id)
			}
		}
	}
	if len(combos) != len(want)*2 {
		t.Errorf("组合数应为 %d，实际 %d", len(want)*2, len(combos))
	}
	for _, f := range []string{"app.js", "boot.js"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range want {
			if !strings.Contains(string(b), `"`+id+`"`) {
				t.Errorf("%s 缺少配色 %q：首屏白名单或主题弹窗会与 CSS 脱节", f, id)
			}
		}
	}
}

func TestThemeTokenSetsIdentical(t *testing.T) {
	combos := parseCombos(t)
	var base map[string]bool
	var baseKey string
	for k, vars := range combos {
		set := map[string]bool{}
		for name := range vars {
			set[name] = true
		}
		if base == nil {
			base, baseKey = set, k
			continue
		}
		for name := range base {
			if !set[name] {
				t.Errorf("组合 %s 缺少令牌 %s（基准 %s）", k, name, baseKey)
			}
		}
		for name := range set {
			if !base[name] {
				t.Errorf("组合 %s 多出令牌 %s（基准 %s）", k, name, baseKey)
			}
		}
	}
	// 四层里每层至少要有代表键，防止「集合一致但大家都很空」。
	need := []string{
		"--bg", "--side", "--card", "--text", "--muted", "--line", "--hover",
		"--acc", "--acc2", "--acc-btn", "--acc2-btn", "--acc-soft", "--acc-text", "--on-acc",
		"--ok", "--ok-soft", "--err", "--err-soft", "--warn", "--warn-soft",
		"--chart-up", "--chart-down", "--chart-3", "--chart-4", "--chart-5", "--grid",
		"--shadow-sm", "--shadow", "--shadow-lg", "--elev-0", "--elev-1", "--elev-2",
		"--glass", "--mask", "--glow", "--tint", "--focus", "--code-bg", "--zebra",
	}
	sort.Strings(need)
	for _, n := range need {
		if !base[n] {
			t.Errorf("四层令牌契约缺少 %s", n)
		}
	}
}

func hexRGB(s string) (r, g, b float64, ok bool) {
	s = strings.TrimSpace(s)
	if len(s) != 7 || s[0] != '#' {
		return 0, 0, 0, false
	}
	var v [3]int
	for i := 0; i < 3; i++ {
		if _, err := fmt.Sscanf(s[1+i*2:3+i*2], "%02x", &v[i]); err != nil {
			return 0, 0, 0, false
		}
	}
	return float64(v[0]), float64(v[1]), float64(v[2]), true
}

func lum(s string) (float64, bool) {
	r, g, b, ok := hexRGB(s)
	if !ok {
		return 0, false
	}
	f := func(c float64) float64 {
		c /= 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*f(r) + 0.7152*f(g) + 0.0722*f(b), true
}

func contrast(a, b string) (float64, bool) {
	la, ok1 := lum(a)
	lb, ok2 := lum(b)
	if !ok1 || !ok2 {
		return 0, false
	}
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05), true
}

// 正文与卡片、主按钮的文字都要过 WCAG AA 4.5:1。老实现里有个别方案的
// --text 覆盖不全，深色下会落回浅色文字——肉眼可见的一处「主题没换干净」。
func TestThemeContrastWCAG(t *testing.T) {
	for k, vars := range parseCombos(t) {
		for _, pair := range [][2]string{
			{"--text", "--bg"}, {"--text", "--card"},
			{"--text", "--side"},
			// 实心品牌面用专用加深色 --acc-btn：主色本身还要兼顾图表与边框，
			// 不能只为了按钮上的白字压暗。
			{"--on-acc", "--acc-btn"}, {"--on-acc", "--acc2-btn"},
			{"--muted", "--bg"}, {"--muted", "--card"}, {"--muted", "--side"},
		} {
			fg, bg := vars[pair[0]], vars[pair[1]]
			c, ok := contrast(fg, bg)
			if !ok {
				t.Errorf("%s %s/%s 不是纯色值：%q / %q", k, pair[0], pair[1], fg, bg)
				continue
			}
			// muted 是小号辅助文字，同样按正文标准要求。
			if c < 4.5 {
				t.Errorf("%s %s on %s 对比度 %.2f 低于 4.5:1（%s / %s）",
					k, pair[0], pair[1], c, fg, bg)
			}
		}
	}
}

// 配色必须真的改变整套表面与语义层，而不是大家一起指向同一组值。
func TestThemeSchemesAreDistinct(t *testing.T) {
	combos := parseCombos(t)
	seen := map[string]string{}
	for k, vars := range combos {
		sig := strings.Join([]string{
			vars["--bg"], vars["--side"], vars["--card"], vars["--text"],
			vars["--ok-soft"], vars["--err-soft"], vars["--chart-down"],
			vars["--shadow"], vars["--elev-2"], vars["--code-bg"],
		}, "|")
		if prev, ok := seen[sig]; ok {
			t.Errorf("%s 与 %s 的四层令牌完全相同，配色方案只是换了名字", k, prev)
		}
		seen[sig] = k
	}
}

func TestThemeDensityAndRadiusAxes(t *testing.T) {
	blk := themeBlock(t)
	for _, frag := range []string{
		`html[data-density="compact"]`, `html[data-density="comfortable"]`,
		`html[data-radius="sharp"]`, `html[data-radius="soft"]`,
		"--dens-pad", "--r-xl",
	} {
		if !strings.Contains(blk, frag) {
			t.Errorf("主题块缺少密度或圆角轴 %q", frag)
		}
	}
	app, err := os.ReadFile("app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, frag := range []string{"triune_density", "triune_radius", "setDensity", "setRadius"} {
		if !strings.Contains(string(app), frag) {
			t.Errorf("app.js 缺少 %q：密度或圆角档位没有落地入口", frag)
		}
	}
}
