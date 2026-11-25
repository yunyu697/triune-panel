#!/usr/bin/env python3
"""拦住演示路由里的「死分支」。

演示页的 mockRoute 把路径切成两段：

    /api/panel/rollback  ->  api = "panel"   rest = "/rollback"

`api` 只到**第一个**斜杠为止。所以 `if (api === "panel/rollback")` 永远
不成立，而走不到分支只是 `return {}`——不报错、不告警，症状是「演示页那块
一直空着」，或者演示出来的行为与线上不一致。

这类死分支真实存在过五条（traffic/daily、traffic/live、certs/issue、
panel/backups、panel/rollback）。其中 panel/rollback 那条让「回滚面板」
在演示页里从来没真正跑过，于是它缺等待层、缺接力凭证这件事一直没被发现。

只检查 `api === "..."` 里带斜杠的常量比较；`rest.startsWith("/x")` 是
正确写法，不在此列。
"""

import re
import sys

PAT = re.compile(r"""api\s*===\s*["']([^"']*/[^"']*)["']""")

def main() -> int:
    path = sys.argv[1] if len(sys.argv) > 1 else "internal/webui/app.js"
    try:
        src = open(path, encoding="utf-8").read()
    except OSError as e:
        print(f"ERR 读不到 {path}: {e}")
        return 1

    bad = []
    for m in PAT.finditer(src):
        # 注释里举反例也会命中（本文件开头就写着一条），跳过整行注释、
        # 以及行内 // 之后的部分，只看真正会执行的代码
        ls = src.rfind("\n", 0, m.start()) + 1
        le = src.find("\n", m.start())
        text = src[ls:le if le != -1 else len(src)]
        if text.lstrip().startswith(("//", "*", "/*")):
            continue
        if m.start() - ls >= len(text.split("//")[0]):
            continue
        bad.append((src.count("\n", 0, m.start()) + 1, m.group(1)))

    if not bad:
        print(f"OK {path}：没有带斜杠的 api 常量比较")
        return 0

    for line, lit in bad:
        print(f"BAD {path}:{line}  api === \"{lit}\" 永远不会命中"
              f"（api 只到第一个斜杠，子路径要用 rest.startsWith）")
    return 1

if __name__ == "__main__":
    sys.exit(main())
