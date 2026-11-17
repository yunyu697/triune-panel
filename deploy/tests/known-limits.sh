#!/usr/bin/env bash
# check-known-limits.py 的离线回归。钉住发布门禁的最后一道：
# 允许带「已登记」的限制发布，不允许带「未决」的发布。
#
# 这里的用例都是纯文本，不联网、不跑 go/node，可以直接进 CI。
set -euo pipefail
SELF_DIR="$(cd -P "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT="$(cd -P "$SELF_DIR/../.." && pwd -P)"
CHECK="$ROOT/deploy/check-known-limits.py"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/triune-kl.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT

PASS=0
FAIL=0
ok() { PASS=$((PASS + 1)); printf 'PASS %s\n' "$*"; }
ng() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$*"; }

run() { python3 "$CHECK" "$1"; }

# A 全合法：一条未验证、一条已验证 → 退出 0，两条都解析到
cat >"$TMP/a.md" <<'EOF'
| ID | 条目 | 影响 | 状态 | 验证方式 | 登记于 |
|---|---|---|---|---|---|
| KL-01 | 条目一 | 影响一 | 未验证 | 方式一 | v1 |
| KL-02 | 条目二 | 影响二 | 已验证 | 方式二 | v1 |
EOF
if out="$(run "$TMP/a.md")" && printf '%s\n' "$out" | grep -q '^ROWS	2$'; then
	if printf '%s\n' "$out" | grep -q '^UNVERIFIED	KL-01'; then
		ok "A 全合法：退出 0、解析到 2 条、列出未验证项"
	else
		ng "A 未验证项没被列出"
	fi
else
	ng "A 全合法的登记册被判失败"
fi

# B 「未决」必须阻断（这是「允许带限制发布」的唯一边界）
cat >"$TMP/b.md" <<'EOF'
| ID | 条目 | 影响 | 状态 | 验证方式 | 登记于 |
|---|---|---|---|---|---|
| KL-01 | 条目一 | 影响一 | 未决 | 待补 | v1 |
EOF
if out="$(run "$TMP/b.md")"; then
	ng "B 未决条目没有阻断"
else
	if printf '%s\n' "$out" | grep -q '^UNRESOLVED	KL-01'; then
		ok "B 未决条目阻断且指出是哪一条"
	else
		ng "B 阻断了但没说清是哪一条"
	fi
fi

# C 影响列为空 = 没想清，按未决处理
cat >"$TMP/c.md" <<'EOF'
| ID | 条目 | 影响 | 状态 | 验证方式 | 登记于 |
|---|---|---|---|---|---|
| KL-01 | 条目一 |  | 未验证 | 方式一 | v1 |
EOF
if run "$TMP/c.md" >/dev/null; then
	ng "C 影响列为空却放行了"
else
	ok "C 影响列为空按未决阻断"
fi

# D 只有表头 → 没有条目，不能当作「零限制」放行
cat >"$TMP/d.md" <<'EOF'
| ID | 条目 | 影响 | 状态 | 验证方式 | 登记于 |
|---|---|---|---|---|---|
EOF
if run "$TMP/d.md" >/dev/null; then
	ng "D 空表被放行了（零限制 ≠ 没登记）"
else
	ok "D 空表阻断"
fi

# E 文件不存在 → 阻断，不是崩溃
if run "$TMP/nope.md" >/dev/null 2>&1; then
	ng "E 文件不存在却放行"
else
	ok "E 文件不存在阻断"
fi

# F 真实登记册必须能过（防止改了格式把门禁自己弄红）
if run "$ROOT/docs/known-limits.md" >/dev/null; then
	ok "F 仓库内的登记册格式合法"
else
	ng "F 仓库内的登记册格式不合法"
fi

printf '\n%d PASS / %d FAIL\n' "$PASS" "$FAIL"
[[ $FAIL -eq 0 ]]
