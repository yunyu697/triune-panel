#!/usr/bin/env bash
# 四个静态检查器（演示页路由 / 换进程接口 / 对外文档口吻 / e2e 登记）的回归。
#
# 为什么要有这个文件：检查器只在 preflight 里跑一次，而 preflight 只看
# 「这一项绿不绿」。哪天有人把正则放松了、把跳过注释的逻辑写宽了，检查器
# 自己不会报错——它会安静地开始什么都查不出来，门禁照样全绿。
# 所以每个检查器都要有一组「该红的变异」和一两个「不该误伤的对照」。
#
# 全部在临时目录里做：只复制检查器要读的那几个文件，改副本，不碰工作区。
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/triune-static.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT

FAILED=0
PASSED=0

ok() { printf '  \033[32m✓\033[0m %s\n' "$1"; PASSED=$((PASSED + 1)); }
bad() { printf '  \033[31m✗\033[0m %s\n' "$1"; FAILED=$((FAILED + 1)); }

# new_case <名字>：准备一个干净目录，复制检查器要读的文件
new_case() {
	CASE="$TMP/$1"
	mkdir -p "$CASE/internal/api" "$CASE/internal/webui" "$CASE/deploy" "$CASE/.github/workflows"
	cp "$ROOT/internal/api/api.go" "$CASE/internal/api/"
	cp "$ROOT/internal/webui/app.js" "$CASE/internal/webui/"
	cp "$ROOT/CHANGELOG.md" "$ROOT/README.md" "$CASE/"
	cp "$ROOT/.gitignore" "$CASE/"
	cp "$ROOT/.github/workflows/ci.yml" "$CASE/.github/workflows/"
	cp "$ROOT/deploy/check-mock-routes.py" "$ROOT/deploy/check-restart-entry.py" \
		"$ROOT/deploy/check-docs-voice.py" "$ROOT/deploy/check-e2e-registry.py" "$CASE/deploy/"
}

# run <期望 0|1> <说明> <命令...>：在 $CASE 里跑，比对退出码
run() {
	local want="$1" label="$2"
	shift 2
	local out rc
	out="$(cd "$CASE" && "$@" 2>&1)"
	rc=$?
	if [[ "$rc" -eq "$want" ]]; then
		ok "$label"
	else
		bad "${label}（期望退出 ${want}，实际 ${rc}）"
		printf '%s\n' "$out" | head -6 | sed 's/^/      /'
	fi
}

# edit <相对 $CASE 的路径> <老串> <新串>：精确替换一次，找不到就报错
# （避免变异悄悄没生效，把「该红」测成「绿」而看不出原因）
#
# 路径一律拼上 $CASE：`run` 的 cd 只在子 shell 里生效，早先这里用相对路径，
# 变异全打在了工作区的真实文件上——自测把仓库改坏，还顺带让后面的用例互相污染。
edit() {
	local file="$CASE/$1" old="$2" new="$3"
	case "$file" in
	"$TMP"/*) ;;
	*)
		printf '  \033[31m✗\033[0m 拒绝写临时目录之外的文件：%s\n' "$file"
		FAILED=$((FAILED + 1))
		return 1
		;;
	esac
	python3 - "$file" "$old" "$new" <<'PY'
import sys
path, old, new = sys.argv[1], sys.argv[2], sys.argv[3]
src = open(path, encoding="utf-8").read()
if old not in src:
    print("MUTATION NOT APPLIED:", old[:60], file=sys.stderr)
    sys.exit(3)
open(path, "w", encoding="utf-8").write(src.replace(old, new, 1))
PY
	if [[ $? -ne 0 ]]; then
		printf '  \033[31m✗\033[0m 变异没落到文件上：%s\n' "$file"
		FAILED=$((FAILED + 1))
		return 1
	fi
}

echo "→ 演示页路由（check-mock-routes.py）"
new_case mock
run 0 "真实 app.js 通过" python3 deploy/check-mock-routes.py internal/webui/app.js
edit internal/webui/app.js 'async function selfUpdate() {' \
	'if (api === "panel/rollback") { return {}; }
async function selfUpdate() {' || true
run 1 "带斜杠的 api 比较会红" python3 deploy/check-mock-routes.py internal/webui/app.js

new_case mock-comment
edit internal/webui/app.js 'async function selfUpdate() {' \
	'// 反例：别写 api === "panel/rollback"
async function selfUpdate() {' || true
run 0 "注释里的反例不误伤" python3 deploy/check-mock-routes.py internal/webui/app.js

echo "→ 换进程接口（check-restart-entry.py）"
new_case entry
run 0 "真实代码通过" python3 deploy/check-restart-entry.py
edit internal/webui/app.js 'async function selfUpdate() {' \
	'async function rogue() { return fetch("/api/self-update", { method: "POST" }); }
async function selfUpdate() {' || true
run 1 "入口外再发一次请求会红" python3 deploy/check-restart-entry.py

new_case entry-comment
edit internal/webui/app.js 'async function selfUpdate() {' \
	'// 别在这里直接 fetch("/api/self-update")
/* 也别用 /api/panel/rollback */
async function selfUpdate() {' || true
run 0 "注释里的路径不误伤" python3 deploy/check-restart-entry.py

new_case entry-stale
edit internal/api/api.go '	"/api/panel/rollback",
}' '	"/api/panel/rollback",
	"/api/panel/self-update",
}' || true
run 1 "清单里多一条不存在的路径会红" python3 deploy/check-restart-entry.py

echo "→ 对外文档口吻（check-docs-voice.py）"
new_case voice
run 0 "真实文档通过" python3 deploy/check-docs-voice.py
edit CHANGELOG.md '### 更新的时候，看到的是一层模糊遮罩' \
	'### 更新的时候，看到的是一层模糊遮罩

**我是怎么验的**：八组变异全部转红。' || true
run 1 "当次小节混入开发过程会红" python3 deploy/check-docs-voice.py

new_case voice-history
edit CHANGELOG.md '## v2.1.9' '## v2.1.9

历史遗留措辞：我是怎么验的、五组变异。' || true
run 0 "历史小节不误伤（只查当次发布）" python3 deploy/check-docs-voice.py

echo "→ e2e 登记（check-e2e-registry.py）"
new_case e2e
run 0 "真实清单通过" python3 deploy/check-e2e-registry.py
edit .github/workflows/ci.yml 'sni-ui toast; do' '; do' || true
run 1 "入库但 CI 不跑会红" python3 deploy/check-e2e-registry.py

new_case e2e-missing
edit .gitignore '!.e2e/toast.js' '' || true
run 1 "CI 要跑但没放行会红" python3 deploy/check-e2e-registry.py

echo
if [[ "$FAILED" -gt 0 ]]; then
	printf '\033[31m%s 项失败，%s 项通过\033[0m\n' "$FAILED" "$PASSED" >&2
	exit 1
fi
printf '\033[32m%s 项全部通过\033[0m\n' "$PASSED"
