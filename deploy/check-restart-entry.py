#!/usr/bin/env python3
"""拦住「换进程接口的第二条调用路径」。

换进程的接口（清单在 internal/api/api.go 的 RestartEndpoints）请求返回时
进程已经没了，收尾只能是这一串：挂等待层 → 拿接力凭证 → 轮询判活 → 换票
→ reload。这在 app.js 里由 runPanelOp 一处实现。

历史：v2.1.9 把等待层只接在「面板自更新」卡片上，版本列表弹窗和面板回滚
还是 setTimeout(reload, 5000) 猜。三条入口里有两条没接 —— 而用户点的正是
那两条。这种漏法不会报错，另一条路看着还是好的，只有真点下去才知道。

所以判据不写成「记得都接上」，写成结构约束：这些路径字面量在 app.js 里
只允许出现在 runPanelOp 函数体内，且每个恰好一次。

清单从 Go 源码读，不在本文件里另抄一份 —— 新增换进程接口时只改 api.go。
"""

import re
import sys

API_GO = "internal/api/api.go"
APP_JS = "internal/webui/app.js"
ENTRY = "runPanelOp"


def read(path):
    try:
        return open(path, encoding="utf-8").read()
    except OSError as e:
        print(f"ERR 读不到 {path}: {e}")
        sys.exit(1)


def strip_comments(src):
    """把注释替换成等长空白：位置不变，行号仍然可信。

    逐行处理而不是全文件扫描引号 —— 全量扫描要正确区分字符串、模板字符串
    里的 ${}、正则字面量，写错一次就会把整段代码当成字符串跳过，于是注释
    没被清掉、误报到注释里的反例。这里只认两种注释形态（// 与 /* */），
    代价是「同一行里既有 URL 又有换进程路径」会被当成注释（漏报）。这种
    写法在 app.js 里不存在，且漏报也不会放过真正的问题：那条路径仍然必须
    在 runPanelOp 里出现一次。
    """
    out, in_block = [], False
    for line in src.split("\n"):
        if in_block:
            e = line.find("*/")
            if e < 0:
                out.append(" " * len(line))
                continue
            line = " " * (e + 2) + line[e + 2:]
            in_block = False
        k = line.find("/*")
        if k >= 0:
            e = line.find("*/", k + 2)
            if e < 0:
                in_block = True
                line = line[:k]
            else:
                line = line[:k] + " " * (e + 2 - k) + line[e + 2:]
        j = line.find("//")
        if j >= 0:
            line = line[:j]
        out.append(line)
    return "\n".join(out)


def restart_endpoints():
    src = read(API_GO)
    m = re.search(r"var\s+RestartEndpoints\s*=\s*\[\]string\s*\{(.*?)\}", src, re.S)
    if not m:
        print(f"ERR {API_GO} 里找不到 RestartEndpoints 清单")
        sys.exit(1)
    eps = re.findall(r'"([^"]+)"', m.group(1))
    if not eps:
        print(f"ERR {API_GO} 的 RestartEndpoints 是空的")
        sys.exit(1)
    return eps


def entry_span(src):
    """runPanelOp 函数体的 [start, end) 字节区间，用大括号配平。"""
    m = re.search(r"(?:async\s+)?function\s+" + ENTRY + r"\s*\(", src)
    if not m:
        print(f"ERR {APP_JS} 里找不到 {ENTRY}，检查器判不了，别跳过")
        sys.exit(1)
    i = src.find("{", m.end())
    if i < 0:
        print(f"ERR {ENTRY} 后面没有函数体")
        sys.exit(1)
    depth = 0
    for j in range(i, len(src)):
        if src[j] == "{":
            depth += 1
        elif src[j] == "}":
            depth -= 1
            if depth == 0:
                return i, j
    print(f"ERR {ENTRY} 的大括号没配平")
    sys.exit(1)


def main():
    eps = restart_endpoints()
    raw = read(APP_JS)
    src = strip_comments(raw)
    lo, hi = entry_span(src)

    bad = []
    for ep in eps:
        hits = [m.start() for m in re.finditer(re.escape('"' + ep + '"'), src)]
        if not hits:
            bad.append(f"{ep} 在 {APP_JS} 里一次都没出现"
                       f"（清单或前端有一边过时了）")
            continue
        if len(hits) > 1:
            lines = "、".join(str(src.count("\n", 0, h) + 1) for h in hits)
            bad.append(f"{ep} 出现 {len(hits)} 次（行 {lines}）"
                       f"—— 换进程的请求只该有一处，由 {ENTRY} 按 kind 选路径")
            continue
        if not (lo <= hits[0] <= hi):
            line = src.count("\n", 0, hits[0]) + 1
            bad.append(f"{APP_JS}:{line} 的 {ep} 落在 {ENTRY} 外面"
                       f"—— 必须由它收尾（挂层→拿票→轮询→换票→reload），"
                       f"散着调就会漏等待层和接力凭证")

    if bad:
        for b in bad:
            print("BAD " + b)
        return 1

    print(f"OK {APP_JS}：{len(eps)} 个换进程接口，各只有一处调用且都在 {ENTRY} 内")
    return 0


if __name__ == "__main__":
    sys.exit(main())
