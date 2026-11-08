#!/usr/bin/env python3
"""变异验证：把本轮四处修复逐条退回去，Go 静态测试 / 浏览器实测必须变红。

用法：python3 .e2e/mutate-no-flicker.py
任何一步失败（变异后仍全绿 = 在测空气）都以非 0 退出。
不管结果如何，finally 里恢复源文件并重建 demo.html。
"""
import os
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
APP = os.path.join(ROOT, "internal", "webui", "app.js")
HTML = os.path.join(ROOT, "internal", "webui", "index.html")
SRC = {p: open(p, encoding="utf-8").read() for p in (APP, HTML)}
GO = os.path.join(os.path.expanduser("~"), "sdk", "go", "bin", "go")
if not os.path.exists(GO):
    GO = "go"

GO_TEST = [GO, "test", "./internal/webui/", "-count=1",
           "-run", "TestFirstPaintSettingsSection|TestThemeSwitchNoFlicker"]
NODE = ["node", ".e2e/theme-switch-flicker.js"]
NODE_SECT = ["node", ".e2e/section-restore.js"]

CSS_BLOCK = """/* 首屏设置页分组恢复（配合 boot.js 的 data-boot-sect）：HTML 里三组卡片
   默认全可见、「常用」按钮写死 .on，app.js 的 setSectSaved() 原来还排在
   goto() 的 30ms 定时器里——停在「运维」刷新就会先画一帧「常用」。
   这里在首帧只留目标组；setSect() 一接手就撤掉属性，恢复由 [hidden] 控制。
   必须撤：setSectUsable() 要临时展开所有卡片量 offsetParent，被 !important
   压住的话会误判成「只有当前这一组有内容」。 */
html[data-boot-sect="common"] #v-settings .card[data-sect]:not([data-sect="common"]),
html[data-boot-sect="security"] #v-settings .card[data-sect]:not([data-sect="security"]),
html[data-boot-sect="maintain"] #v-settings .card[data-sect]:not([data-sect="maintain"]){display:none!important}
/* 按钮高亮：先把 HTML 写死的「常用 .on」还原成未选中，再给目标组套上
   .seg-lg button.on 的同一套观感。两条同权重，靠源序决定，别调换。 */
html[data-boot-sect] #set_sect_seg button.on{background:none;color:var(--muted);box-shadow:none}
html[data-boot-sect="common"] #set_sect_seg button[data-sect="common"],
html[data-boot-sect="security"] #set_sect_seg button[data-sect="security"],
html[data-boot-sect="maintain"] #set_sect_seg button[data-sect="maintain"]{
  background:var(--acc-soft);color:var(--acc-text);
  box-shadow:inset 0 0 0 1px color-mix(in srgb,var(--acc) 42%,transparent)}
"""

SYNC_SECT = """    if (sect && SET_SECTS[sect]) setSect(sect, true);   // 深链接：用户点的，记住它
    else setSectSaved();
    setTimeout(() => {
"""
ASYNC_SECT = """    setTimeout(() => {
    if (sect && SET_SECTS[sect]) setSect(sect, true);   // 深链接：用户点的，记住它
    else setSectSaved();
"""

INCREMENTAL = """  const host = $("schemes");
  if (!host.children.length) host.innerHTML = SCHEMES.map(schemeMarkup).join("");
  [...host.querySelectorAll(".scheme")].forEach(btn => {
    const s = SCHEMES.find(x => x[0] === btn.dataset.s);
    if (s) paintScheme(btn, s[0], s[1]);
  });
"""
REBUILD = """  $("schemes").innerHTML = SCHEMES.map(([id, name]) => {
    const c = schemeVars(themeMode, id);
    return `<button class="scheme${id === themeScheme ? " on" : ""}" data-s="${id}">
      <span class="hex">${c.acc} · ${c.acc2}</span></button>`;
  }).join("");
"""

MUTATIONS = [
    ("删掉首屏分组 CSS", HTML, CSS_BLOCK, "", GO_TEST + NODE_SECT),
    ("分组恢复退回 30ms 定时器", APP, SYNC_SECT, ASYNC_SECT, GO_TEST + NODE_SECT),
    ("applyTheme 不再挂禁过渡标记", APP,
     "  markThemeSwitch();   // 必须排在改 data-mode/scheme 之前，同一个 JS 任务里\n", "", GO_TEST + NODE),
    ("预览网格改回 innerHTML 重建", APP, INCREMENTAL, REBUILD, GO_TEST + NODE),
]


def run(cmd):
    env = dict(os.environ)
    env["NODE_PATH"] = os.path.join(os.path.expanduser("~"),
                                    ".npm/_npx/e41f203b7505f1fb/node_modules")
    r = subprocess.run(cmd, cwd=ROOT, env=env, capture_output=True, text=True)
    out = (r.stdout + r.stderr).strip().splitlines()
    return r.returncode, (out[-1] if out else "")


def rebuild_demo():
    r = subprocess.run(["python3", "deploy/build-demo.py"], cwd=ROOT,
                       capture_output=True, text=True)
    if r.returncode:
        print("重建 demo.html 失败:", r.stderr, file=sys.stderr)
        sys.exit(1)


def main():
    bad = 0
    try:
        for name, path, old, new, cmds in MUTATIONS:
            src = SRC[path]
            if old not in src:
                print(f"SKIP {name}: 找不到变异锚点（脚本需要同步更新）")
                bad += 1
                continue
            open(path, "w", encoding="utf-8").write(src.replace(old, new, 1))
            rebuild_demo()
            # cmds 里 NODE 是多个元素，按 GO_TEST + NODE 拼成一条；拆开执行
            checks = [GO_TEST]
            if NODE_SECT[1] in cmds:
                checks.append(NODE_SECT)
            if NODE[1] in cmds:
                checks.append(NODE)
            results = []
            for c in checks:
                rc, tail = run(c)
                results.append(("go" if c is GO_TEST else "browser", rc, tail))
            # 变异后至少要有一条检查变红
            red = [r for r in results if r[1] != 0]
            detail = " | ".join(f"{k}={'红' if rc else '绿'} [{tail}]" for k, rc, tail in results)
            if red:
                print(f"PASS {name} → {detail}")
            else:
                print(f"FAIL {name} → 全部仍绿，说明没测到: {detail}")
                bad += 1
            open(path, "w", encoding="utf-8").write(src)
            rebuild_demo()
    finally:
        for p, s in SRC.items():
            open(p, "w", encoding="utf-8").write(s)
        rebuild_demo()
    rc, tail = run(GO_TEST)
    print("恢复后 Go 测试:", "绿" if rc == 0 else "红 " + tail)
    if rc:
        bad += 1
    print(f"\n{bad} 个问题")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
