#!/usr/bin/env python3
"""由 internal/webui/{index.html,qr.js,app.js} 生成 demo.html（离线单文件演示）。

为什么需要这一步，而不是简单的 `cp index.html demo.html`：

CSP 收紧到 `script-src 'self'` 之后，脚本必须作为独立文件从 /assets/*.js 加载
（内联 <script> 与内联事件属性只能靠 'unsafe-inline' 放行，而那一条同时放行了
注入的脚本）。但 demo.html 是直接双击用 file:// 打开的，没有 /assets，所以要把
两份脚本**贴回** HTML 里。

所以 demo.html = index.html + boot.js + qr.js + app.js 的内联版本，内容与面板服务的页面
严格对应，只是脚本位置不同。改了前端就跑一次：

    python3 deploy/build-demo.py

生成后会做两项自检：脚本占位符必须恰好出现一次，且内联后不再包含 src 引用。
"""

import pathlib
import sys

root = pathlib.Path(__file__).resolve().parent.parent
web = root / "internal" / "webui"
html = (web / "index.html").read_text(encoding="utf-8")

for name in ("boot.js", "qr.js", "app.js"):
    tag = f'<script src="/assets/{name}"></script>'
    if html.count(tag) != 1:
        sys.exit(f"expect exactly one {tag!r}, got {html.count(tag)}")
    body = (web / name).read_text(encoding="utf-8")
    html = html.replace(tag, "<script>\n" + body + "</script>")

if 'src="/assets/' in html:
    sys.exit("still references /assets after inlining")

out = root / "demo.html"
out.write_text(html, encoding="utf-8")
print(f"wrote {out.relative_to(root)} ({len(html):,} bytes)")
