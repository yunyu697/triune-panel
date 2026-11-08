#!/usr/bin/env bash
# 主题令牌块必须与配色表生成器一致。
#
# 手改 index.html 的 THEME 块、或者改了配色表却忘了重建，浏览器拿到的样式
# 就和 Go 守卫测的那份不是一回事：新增配色可能缺中性色键、语义色又退回
# mode 层。这里在 CI 里用 --check 兜住，纯离线、不联网。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "${ROOT}"

python3 deploy/build-theme.py --check

# 顺带确认 demo.html 里的令牌块与 index.html 同步：离线演示页内嵌的是同一份
# <style>，两者不一致时浏览器回归测的就可能是过期的页面。
python3 - <<'PY'
import sys

BEGIN = "/* THEME:BEGIN"
END = "/* THEME:END */"

def block(path):
    s = open(path, encoding="utf-8").read()
    i, j = s.find(BEGIN), s.find(END)
    if i < 0 or j < 0 or j <= i:
        print("找不到主题令牌块：%s" % path)
        sys.exit(1)
    return s[i:j]

a, b = block("internal/webui/index.html"), block("demo.html")
if a != b:
    print("demo.html 的主题令牌块与 index.html 不一致，请跑 python3 deploy/build-demo.py")
    sys.exit(1)
print("主题生成器一致性：14 组合已同步，demo 与 index 一致")
PY
