#!/usr/bin/env python3
"""拦住「写好了、也放行了、但从来没跑过」的 e2e 脚本。

`.gitignore` 是整个 .e2e 目录全忽略、再逐个白名单放行；放行只说明它进了仓库，
不等于 CI 会跑它。两边各改一处、谁也没错，结果就是脚本在仓库里躺着烂掉——
android-scroll / android-compat 从 v1.6.0 起就对着已经改名的 .topbar 做断言，
两年没人发现，因为 CI 的清单里没有它们。

这里把三份清单对起来看：
  * `.gitignore` 的 `!.e2e/*.js`   —— 入库的脚本
  * `ci.yml` 的 for 循环           —— 每次 CI 会跑的脚本
  * `ci.yml` 里单独 node 的一行    —— 独立步骤（真后端首屏）
入库的必须都被跑；被跑的必须在库。两边只差一个也不放过。

不算「回归脚本」的两个：browser.js 是被 require 的启动封装；
first-paint-server.js 有独立步骤（要现编二进制），不在 for 循环里。
"""

import re
import sys

# 库与独立步骤：入库但不进 for 循环，属于设计如此
NO_SUITE = {"browser", "first-paint-server"}

GITIGNORE = ".gitignore"
CI = ".github/workflows/ci.yml"


def whitelisted() -> set:
    """`.gitignore` 里放行的 .e2e 脚本名（去扩展名）。"""
    out = set()
    for line in open(GITIGNORE, encoding="utf-8"):
        m = re.match(r"^!\.e2e/([\w.-]+)\.js\s*$", line.strip())
        if m:
            out.add(m.group(1))
    return out


def ci_loop() -> set:
    """ci.yml 里 for s in … 循环的脚本名。"""
    src = open(CI, encoding="utf-8").read()
    m = re.search(r"for s in ([^;]*?first-paint-flicker[^;]*?);\s*do", src, re.S)
    if not m:
        raise SystemExit(f"ERR 在 {CI} 里找不到 e2e 的 for 循环")
    return set(m.group(1).replace("\\", " ").split())


def ci_standalone() -> set:
    """ci.yml 里以独立步骤跑的脚本（node .e2e/x.js）。"""
    src = open(CI, encoding="utf-8").read()
    return set(re.findall(r"node\s+\.e2e/([\w.-]+)\.js", src))


def main() -> int:
    try:
        have = whitelisted()
        loop = ci_loop()
        solo = ci_standalone()
    except OSError as e:
        print(f"ERR 读不到清单: {e}")
        return 1

    ran = loop | solo
    # 白名单里有、但 CI 从不执行的：躺着的死脚本
    idle = sorted(have - ran - NO_SUITE)
    # CI 要跑、但没放行的：checkout 之后根本没这个文件
    missing = sorted(ran - have)

    if not idle and not missing:
        print(f"OK {len(have)} 个脚本入库，{len(loop)} 个在循环里跑，"
              f"{len(solo)} 个独立步骤")
        return 0

    for s in idle:
        print(f"IDLE .e2e/{s}.js 已入库但 ci.yml 从不执行（要么加进 for 循环，"
              f"要么说明它为什么不算回归）")
    for s in missing:
        print(f"MISS .e2e/{s}.js 在 ci.yml 里要跑，但 .gitignore 没放行——"
              f"CI checkout 之后没有这个文件")
    return 1


if __name__ == "__main__":
    sys.exit(main())
