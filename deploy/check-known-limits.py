#!/usr/bin/env python3
"""校验已知限制登记册（docs/known-limits.md）。

发布门禁的规则：允许带「已登记」的限制发布，不允许带「未决」的发布。
「未决」= 状态不在 未验证/已验证/已缓解 里，或条目/影响/验证方式有空列。

输出（tab 分隔，供 preflight.sh 解析）：
    UNRESOLVED  <ID>  <原因>
    UNVERIFIED  <ID>  <条目>
    ROWS        <解析到的条目数>

退出码：0 无未决；1 有未决或表格式不对。
"""
import sys

ALLOWED = {"未验证", "已验证", "已缓解"}


def main(path: str) -> int:
    try:
        lines = open(path, encoding="utf-8").read().splitlines()
    except OSError as e:
        print("无法读取登记册: %s" % e, file=sys.stderr)
        return 1

    unverified = []
    unresolved = []
    rows = 0
    for line in lines:
        line = line.strip()
        if not line.startswith("|"):
            continue
        cells = [c.strip() for c in line.strip("|").split("|")]
        # 表头、分隔行、列数不足的都不是数据行
        if len(cells) < 6 or cells[0] in ("ID", ""):
            continue
        if set(cells[1]) <= set("-"):
            continue
        rid, item, impact, status, how = cells[0], cells[1], cells[2], cells[3], cells[4]
        rows += 1
        if status not in ALLOWED:
            unresolved.append((rid, "状态为「%s」，允许的是 未验证/已验证/已缓解" % status))
            continue
        if not item or not impact or not how:
            unresolved.append((rid, "条目/影响/验证方式有空列"))
            continue
        if status == "未验证":
            unverified.append((rid, item))

    for rid, reason in unresolved:
        print("UNRESOLVED\t%s\t%s" % (rid, reason))
    for rid, item in unverified:
        print("UNVERIFIED\t%s\t%s" % (rid, item))
    print("ROWS\t%d" % rows)
    return 1 if unresolved or rows == 0 else 0


if __name__ == "__main__":
    if len(sys.argv) != 2:
        print("用法: check-known-limits.py docs/known-limits.md", file=sys.stderr)
        sys.exit(2)
    sys.exit(main(sys.argv[1]))
