#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""生成 index.html 的主题令牌块。

为什么要有生成器：以前 7 套配色 × 亮暗两层是手写在 CSS 里的，mode 层管
中性色/语义色/阴影，scheme 层只管强调色与图表色。结果「换配色」时语义色、
阴影、玻璃层、图表辅助色都不动，颜色方案看起来只是换了几个按钮；不同方案
覆盖的中性色键还参差不齐。

现在一份配色表派生出**同一组**四层令牌（表面 / 品牌 / 语义 / 图表与景深），
14 个组合的变量名集合完全一致，由 internal/webui/theme_contract_test.go
逐项钉住。改配色改这张表，然后跑本脚本，不要手改生成的 CSS。

用法：
  python3 deploy/build-theme.py           写回 index.html
  python3 deploy/build-theme.py --stdout  只打印生成的 CSS 块
  python3 deploy/build-theme.py --check   检查 index.html 是否与生成器一致
"""
import argparse
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
INDEX = ROOT / "internal" / "webui" / "index.html"
BEGIN = "/* THEME:BEGIN 由 deploy/build-theme.py 生成，勿手改 */"
END = "/* THEME:END */"

# 每套方案给出亮暗两套基色。语义三色显式列出（不从主色派生）：绿色的「成功」
# 必须还是绿色，不能因为换到玫瑰配色就变成粉色状态灯。但 soft 底色、图表
# 色板、阴影染色、焦点环都跟着这套色走，整体才是一套配色而不是几个按钮。
SCHEMES = [
    dict(id="azure", name="蔚蓝",
         light=dict(bg="#f4f6fa", side="#ffffff", card="#ffffff", text="#141a22",
                    muted="#68727f", line="#e5eaf1", hover="#f1f4f9",
                    acc="#3b82f6", acc2="#8b5cf6", acc_text="#2563eb", on_acc="#ffffff",
                    ok="#15803d", err="#dc2626", warn="#b45309",
                    chart_down="#22c55e", chart_4="#f59e0b"),
         dark=dict(bg="#0d1016", side="#11151c", card="#151a22", text="#e6eaf0",
                   muted="#8b94a3", line="#232a34", hover="#1b222c",
                   acc="#5b9cf8", acc2="#a78bfa", acc_text="#7cabf9", on_acc="#0b1017",
                   ok="#2fca7f", err="#f06a6a", warn="#eab308",
                   chart_down="#2fca7f", chart_4="#fbbf24")),
    dict(id="indigo", name="靛夜",
         light=dict(bg="#f5f6fd", side="#ffffff", card="#ffffff", text="#191c2e",
                    muted="#6b7194", line="#e4e7f5", hover="#eef0fb",
                    acc="#6366f1", acc2="#0891b2", acc_text="#4f46e5", on_acc="#ffffff",
                    ok="#0f8f63", err="#d63d63", warn="#b45309",
                    chart_down="#10b981", chart_4="#f59e0b"),
         dark=dict(bg="#080d1a", side="#0c1324", card="#0f1729", text="#e0e7f5",
                   muted="#7e8cab", line="#1c2740", hover="#152038",
                   acc="#818cf8", acc2="#22d3ee", acc_text="#a5b0fb", on_acc="#080d1a",
                   ok="#34d399", err="#f472b6", warn="#fbbf24",
                   chart_down="#4ade80", chart_4="#fbbf24")),
    dict(id="forest", name="森野",
         light=dict(bg="#f3f8f5", side="#ffffff", card="#ffffff", text="#14251b",
                    muted="#5f7d6b", line="#dcebe2", hover="#e9f4ee",
                    acc="#16a34a", acc2="#65a30d", acc_text="#15803d", on_acc="#ffffff",
                    ok="#15803d", err="#c2410c", warn="#a16207",
                    chart_down="#65a30d", chart_4="#d97706"),
         dark=dict(bg="#0a120e", side="#0f1a14", card="#111e17", text="#e3ece5",
                   muted="#83a08d", line="#1e3228", hover="#17271e",
                   acc="#34d399", acc2="#a3e635", acc_text="#5fe0b0", on_acc="#0a120e",
                   ok="#34d399", err="#fb7185", warn="#facc15",
                   chart_down="#a3e635", chart_4="#fbbf24")),
    dict(id="rose", name="玫瑰",
         light=dict(bg="#fdf5f7", side="#ffffff", card="#ffffff", text="#2b1620",
                    muted="#8a6a76", line="#f2dfe5", hover="#fbebef",
                    acc="#e11d48", acc2="#a855f7", acc_text="#be123c", on_acc="#ffffff",
                    ok="#0f766e", err="#be123c", warn="#b45309",
                    chart_down="#0d9488", chart_4="#f59e0b"),
         dark=dict(bg="#120e13", side="#181219", card="#1d151c", text="#efe4e9",
                   muted="#a58a96", line="#2c212a", hover="#261b23",
                   acc="#fb7185", acc2="#c084fc", acc_text="#fc93a2", on_acc="#120e13",
                   ok="#4ade80", err="#fda4af", warn="#fbbf24",
                   chart_down="#2dd4bf", chart_4="#fbbf24")),
    dict(id="solar", name="暖阳",
         light=dict(bg="#f7f2e9", side="#fffdf8", card="#fffdf8", text="#3b3026",
                    muted="#8e7f6c", line="#e9dfcd", hover="#f3ebdb",
                    acc="#e08a2e", acc2="#c2410c", acc_text="#bf6f1c", on_acc="#ffffff",
                    ok="#4d7c0f", err="#b91c1c", warn="#a16207",
                    chart_down="#5a9d3f", chart_4="#b45309"),
         dark=dict(bg="#17110a", side="#1e1610", card="#251b12", text="#f0e6d8",
                   muted="#a3907a", line="#3a2c1e", hover="#2c2116",
                   acc="#e8a04a", acc2="#f97316", acc_text="#f0b76a", on_acc="#17110a",
                   ok="#4ade80", err="#f87171", warn="#fbbf24",
                   chart_down="#86efac", chart_4="#fbbf24")),
    dict(id="steel", name="雾蓝",
         light=dict(bg="#eceff4", side="#ffffff", card="#ffffff", text="#1f2732",
                    muted="#69758a", line="#dbe2ea", hover="#e8edf4",
                    acc="#5470a8", acc2="#3f7fa0", acc_text="#415c8c", on_acc="#ffffff",
                    ok="#1b7f5a", err="#c24545", warn="#9a6b0b",
                    chart_down="#2e8f5e", chart_4="#b07d1a"),
         dark=dict(bg="#0e1319", side="#131a22", card="#18202a", text="#dfe6ef",
                   muted="#8593a5", line="#26313d", hover="#1e2833",
                   acc="#7f9bd1", acc2="#5fa8c9", acc_text="#9db4e0", on_acc="#0e1319",
                   ok="#34d399", err="#f87171", warn="#eab308",
                   chart_down="#5eead4", chart_4="#eab308")),
    dict(id="ocean", name="海盐",
         light=dict(bg="#eef6f7", side="#ffffff", card="#ffffff", text="#12303a",
                    muted="#5b7f8a", line="#d7e6ea", hover="#e5f0f3",
                    acc="#0f9db8", acc2="#0d9488", acc_text="#0a7f97", on_acc="#ffffff",
                    ok="#0f766e", err="#be3b4a", warn="#a16207",
                    chart_down="#1f9d61", chart_4="#d98b1f"),
         dark=dict(bg="#061417", side="#0a1c20", card="#0d2429", text="#d9eef2",
                   muted="#7fa3ab", line="#17363d", hover="#103038",
                   acc="#22b8d4", acc2="#2dd4bf", acc_text="#4ecfe6", on_acc="#061417",
                   ok="#34d399", err="#fb7185", warn="#fbbf24",
                   chart_down="#67e8f9", chart_4="#fbbf24")),
]


def rgb(h):
    h = h.lstrip("#")
    return tuple(int(h[i:i + 2], 16) for i in (0, 2, 4))


def mix(a, b, t):
    """把 b 按 t 的比例混进 a，返回不透明 hex（老内核对 rgba 更挑）。"""
    ra, rb = rgb(a), rgb(b)
    return "#%02x%02x%02x" % tuple(round(x * (1 - t) + y * t) for x, y in zip(ra, rb))


def alpha(h, a):
    r, g, b = rgb(h)
    return "rgba(%d,%d,%d,%s)" % (r, g, b, a)


def luminance(h):
    def f(c):
        c /= 255
        return c / 12.92 if c <= 0.03928 else ((c + 0.055) / 1.055) ** 2.4
    r, g, b = rgb(h)
    return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b)


def contrast(a, b):
    la, lb = luminance(a), luminance(b)
    if la < lb:
        la, lb = lb, la
    return (la + 0.05) / (lb + 0.05)


def ensure_contrast(fg, bgs, target=4.6, toward=None):
    """把前景色朝 toward 逐步推，直到在所有底色上都过 WCAG AA。

    手写 7 套配色时，浅色方案的 muted 小字普遍只有 3.5～4.4:1；逐套手调
    既容易漏，新增配色又会重犯。交给生成器算，颜色仍沿原本色相走。
    """
    toward = toward or fg
    cur = fg
    for _ in range(80):
        if all(contrast(cur, bg) >= target for bg in bgs):
            break
        cur = mix(cur, toward, .04)
    return cur


def button_color(acc, on_acc):
    """实心品牌面（主按钮 / 图标底）的专用色，保证上面的文字达标。

    品牌主色本身要兼顾边框、图表与渐变，不能为了按钮文字一味压暗；所以
    拆出 --acc-btn / --acc2-btn 这一层，视觉仍是同一色相。
    """
    toward = "#000000" if luminance(on_acc) > .5 else "#ffffff"
    cur = acc
    for _ in range(80):
        if contrast(cur, on_acc) >= 4.6:
            break
        cur = mix(cur, toward, .04)
    return cur


def tokens(s, mode):
    dark = mode == "dark"
    muted = ensure_contrast(s["muted"], [s["bg"], s["card"], s["side"]], toward=s["text"])
    surfaces = {
        "--bg": s["bg"], "--side": s["side"], "--card": s["card"],
        "--text": s["text"], "--muted": muted,
        "--line": s["line"], "--hover": s["hover"],
    }
    brand = {
        "--acc": s["acc"], "--acc2": s["acc2"],
        "--acc-soft": mix(s["bg"], s["acc"], .16 if dark else .11),
        "--acc-text": s["acc_text"], "--on-acc": s["on_acc"],
        "--acc-btn": button_color(s["acc"], s["on_acc"]),
        "--acc2-btn": button_color(s["acc2"], s["on_acc"]),
    }
    # 语义色的 soft 底跟随本方案的背景，而不是所有方案共用一块固定浅灰。
    soft_t = .17 if dark else .11
    semantic = {
        "--ok": s["ok"], "--ok-soft": mix(s["bg"], s["ok"], soft_t),
        "--err": s["err"], "--err-soft": mix(s["bg"], s["err"], soft_t),
        "--warn": s["warn"], "--warn-soft": mix(s["bg"], s["warn"], soft_t),
    }
    ink = (0, 0, 0) if dark else (16, 24, 40)
    sh = "rgba(%d,%d,%d," % ink
    ar, ag, ab = rgb(s["acc"])
    charts = {
        "--chart-up": s["acc"], "--chart-down": s["chart_down"],
        "--chart-3": s["acc2"], "--chart-4": s["chart_4"], "--chart-5": s["ok"],
        "--grid": "rgba(255,255,255,.07)" if dark else alpha(s["text"], .075),
    }
    depth = {
        "--shadow-sm": "0 1px 2px " + sh + (".4)" if dark else ".05)"),
        "--shadow": ("0 1px 2px " + sh + (".35)" if dark else ".04)") +
                     ",0 12px 30px -16px " + sh + (".7)" if dark else ".20)")),
        "--shadow-lg": "0 30px 70px -28px " + sh + (".85)" if dark else ".34)"),
        # 景深三层：静止 / 悬浮 / 浮层。悬浮层带主色染色，换配色时一起变。
        "--elev-0": "0 1px 2px " + sh + (".4)" if dark else ".05)"),
        "--elev-1": ("0 1px 2px " + sh + (".35)" if dark else ".04)") +
                     ",0 10px 26px -14px " + sh + (".55)" if dark else ".20)")),
        "--elev-2": ("0 2px 5px " + sh + (".45)" if dark else ".07)") +
                     ",0 20px 42px -20px rgba(%d,%d,%d,.30)" % (ar, ag, ab)),
        "--glass": alpha(s["bg"], .72) if dark else "rgba(255,255,255,.74)",
        "--mask": "rgba(0,0,0,.58)" if dark else "rgba(15,20,28,.44)",
        "--glow": "0 10px 24px -10px rgba(%d,%d,%d,.55)" % (ar, ag, ab),
        "--tint": "rgba(%d,%d,%d,.08)" % (ar, ag, ab),
        "--focus": "rgba(%d,%d,%d,.45)" % (ar, ag, ab),
    }
    extras = {
        # 代码块 / 表格斑马纹 / 焦点环也归配色方案管，不再写死灰色。
        "--code-bg": mix(s["bg"], s["hover"], .7),
        "--zebra": mix(s["card"], s["hover"], .55),
    }
    out = {}
    for d in (surfaces, brand, semantic, charts, depth, extras):
        out.update(d)
    return out


def css_block():
    lines = [BEGIN, "", "/* 四层令牌：表面 / 品牌 / 语义 / 图表与景深。每个组合都由",
             "   tokens() 展开同一组变量，集合完全一致（Go 守卫逐项比）。 */"]
    # 兜底：scheme 不合法时仍有一套完整变量，避免整页没有样式。
    for mode in ("light", "dark"):
        base = SCHEMES[0][mode]
        decl = "".join("%s:%s;" % (k, v) for k, v in tokens(base, mode).items())
        lines.append('[data-mode="%s"]{%s}' % (mode, decl))
    lines.append("")
    for sc in SCHEMES:
        lines.append("/* --- %s %s --- */" % (sc["id"], sc["name"]))
        for mode in ("light", "dark"):
            decl = "".join("%s:%s;" % (k, v) for k, v in tokens(sc[mode], mode).items())
            lines.append('[data-mode="%s"][data-scheme="%s"]{%s}' % (mode, sc["id"], decl))
        lines.append("")
    lines += [
        "/* 密度与圆角是独立于明暗与配色的第三、第四根轴；外观偏好只存本机。 */",
        ":root{--dens-pad:20px;--dens-gap:18px;--dens-font:14px;--r-xl:20px;--r-lg:14px;--r:10px;--r-sm:7px}",
        'html[data-density="compact"]{--dens-pad:13px;--dens-gap:12px;--dens-font:13px;--r-xl:14px;--r-lg:10px;--r:7px;--r-sm:5px}',
        'html[data-density="comfortable"]{--dens-pad:24px;--dens-gap:22px;--dens-font:14px}',
        'html[data-radius="sharp"]{--r-xl:8px;--r-lg:6px;--r:4px;--r-sm:3px}',
        'html[data-radius="soft"]{--r-xl:26px;--r-lg:20px;--r:15px;--r-sm:11px}',
        "", END,
    ]
    return "\n".join(lines)


def render(current):
    block = css_block()
    if BEGIN in current and END in current:
        head, rest = current.split(BEGIN, 1)
        _, tail = rest.split(END, 1)
        return head + block + tail
    # 首次迁移：把旧的手写主题段落整体换掉。
    start = current.find("/* ============ 主题：亮暗（mode）× 色彩方案（scheme） ============")
    stop = current.find("/* ============ 基础 ============ */")
    if start < 0 or stop < 0 or stop <= start:
        raise SystemExit("找不到旧主题段落边界，拒绝猜测着改 index.html")
    return current[:start] + block + "\n\n" + current[stop:]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--stdout", action="store_true", help="只打印生成的 CSS 块")
    ap.add_argument("--check", action="store_true", help="检查 index.html 是否已同步")
    args = ap.parse_args()
    if args.stdout:
        sys.stdout.write(css_block())
        return 0
    cur = INDEX.read_text(encoding="utf-8")
    want = render(cur)
    if args.check:
        if want != cur:
            print("index.html 的主题令牌块与生成器不一致，请跑 python3 deploy/build-theme.py")
            return 1
        print("主题令牌块已同步：14 组合 / 四层令牌")
        return 0
    if want != cur:
        INDEX.write_text(want, encoding="utf-8")
        print("已重建 index.html 主题令牌块（%d 组合）" % (len(SCHEMES) * 2))
    else:
        print("主题令牌块无变化")
    return 0


if __name__ == "__main__":
    sys.exit(main())
