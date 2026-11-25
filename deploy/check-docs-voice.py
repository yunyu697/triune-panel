#!/usr/bin/env python3
"""拦住对外文档里的「内部视角」。

规则见 AGENTS.md A3：CHANGELOG / README / Release notes 的「我」是作者本人，
读者是「你」。所以「我在注释里写了理由」「我是怎么验的」「这个坑这次踩到了」
这类话没有位置 —— 读者没有一个叫「注释」的东西，也不该读开发过程的复盘。

不写成人规矩（写成人规矩等于没有），能机器查的短语就查：
只查**当次发布**（CHANGELOG 第一个版本小节 + README 版本表第一行），
历史小节不动 —— 历史节的写法是既有风格，改它会让已发布的 Release notes
与仓库文本对不上，收益不足。
"""

import re
import sys

# 精确短语，不是正则。误伤的代价是发布被拦一次，漏放的代价是角色穿帮。
BANNED = [
    "我是怎么验的",
    "变异",
    "我原来",
    "我改的",
    "我把它",
    "踩到",
    "注释里",
    "我自己",
    "e2e",
    "preflight",
    "门禁",
    "台账",
    "席位",
    "本轮",
]


def read(path):
    try:
        return open(path, encoding="utf-8").read()
    except OSError as e:
        print(f"ERR 读不到 {path}: {e}")
        sys.exit(1)


def first_changelog_section(src):
    """CHANGELOG 的第一个「## vX.Y.Z」小节，含标题行。"""
    lines = src.split("\n")
    start = None
    for i, ln in enumerate(lines):
        if re.match(r"^## v\d", ln):
            start = i
            break
    if start is None:
        print("ERR CHANGELOG.md 里找不到「## vX.Y.Z」小节")
        sys.exit(1)
    out = [lines[start]]
    for ln in lines[start + 1:]:
        if ln.startswith("## "):
            break
        out.append(ln)
    return out, start + 1


def readme_latest_row(src):
    """README 版本表第一行（表头之后第一条 | vX.Y.Z |）。"""
    lines = src.split("\n")
    for i, ln in enumerate(lines):
        if re.match(r"^\| v\d+\.\d+\.\d+ \|", ln):
            return ln, i + 1
    print("ERR README.md 里找不到版本表第一行")
    sys.exit(1)


def scan(label, text, offset=1):
    bad = 0
    for n, line in enumerate(text if isinstance(text, list) else [text]):
        for phrase in BANNED:
            if phrase in line:
                where = f"{label}:{offset + n}"
                print(f"BAD {where} 出现「{phrase}」——对外文档不写开发过程与内部术语")
                bad += 1
    return bad


def main():
    bad = 0
    sec, off = first_changelog_section(read("CHANGELOG.md"))
    bad += scan("CHANGELOG.md", sec, off)

    row, off = readme_latest_row(read("README.md"))
    bad += scan("README.md", row, off)

    if bad:
        print(f"FAIL 对外文档有 {bad} 处内部视角（规则见 AGENTS.md A3）")
        return 1
    print("OK 当次发布的文档没有内部视角（“我”= 作者本人，读者是“你”）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
