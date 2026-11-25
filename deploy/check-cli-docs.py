#!/usr/bin/env python3
"""拦住「命令存在、文档里没有」。

`tp` 的子命令表在 cmd/server/tp.go（`tpCommands`），面向用户的说明在 docs/cli.md。
两处各改一处不算谁的错，结果就是命令能用、文档里一条也搜不到——`tp ui` 就这样
从 v1.5.0 一直缺到 v2.2.2：升级完浏览器还是旧界面时，用户没有任何地方能查到它。

判据：命令表里每个非 hidden 的名字，docs/cli.md 里都要出现 ``tp <名字>` ``。
hidden 的是兼容旧名（reset-password / reset），帮助里都不列，文档也不该列。
"""

import re
import sys

TP_GO = "cmd/server/tp.go"
DOC = "docs/cli.md"

# {name: "x", desc: "...", run: ...}  —— 只取带 run 的完整条目
ENTRY = re.compile(r'\{\s*name:\s*"([\w-]+)"[^}]*?\brun:\s*\w+[^}]*?\}')


def commands(src: str):
    """返回 (可见命令名集合, hidden 命令名集合)。"""
    visible, hidden = [], []
    for m in ENTRY.finditer(src):
        name = m.group(1)
        (hidden if re.search(r"hidden:\s*true", m.group(0)) else visible).append(name)
    return visible, hidden


def main() -> int:
    try:
        go = open(TP_GO, encoding="utf-8").read()
        doc = open(DOC, encoding="utf-8").read()
    except OSError as e:
        print(f"ERR 读不到文件: {e}")
        return 1

    visible, hidden = commands(go)
    if not visible:
        print(f"ERR 在 {TP_GO} 里没解析到任何命令（表结构变了？）")
        return 1

    missing = [n for n in visible if f"`tp {n}`" not in doc and f"`tp {n} " not in doc]
    # 兼容旧名出现在文档里是噪音：文档描述的是当前用法
    leaked = [n for n in hidden if f"`tp {n}`" in doc]

    if not missing and not leaked:
        print(f"OK {len(visible)} 个命令都在 {DOC} 里，隐藏旧名未出现")
        return 0

    for n in missing:
        print(f"MISS 命令 {n} 在 {TP_GO} 里有，{DOC} 里搜不到 `tp {n}`"
              f"（用户查不到就等于没有）")
    for n in leaked:
        print(f"LEAK 隐藏的旧名 {n} 出现在 {DOC} 里（帮助里本来就不列它）")
    return 1


if __name__ == "__main__":
    sys.exit(main())
