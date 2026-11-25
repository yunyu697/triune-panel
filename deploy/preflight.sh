#!/usr/bin/env bash
# 发布门禁：release.sh 在构建前调用它；也可以单独跑（只体检、不发布）。
#
# 为什么要有它：v2.1.0 之前的验收是「人记得该验什么」，而 v2.0.9 用
# `sing-box check` 当收口，漏掉了只在 start service 阶段才报的 detour 错误。
# 单人不该靠记性做质量——每一条都必须由脚本判定，失败即阻断，不留跳过开关。
#
# 允许带「已登记的已知限制」发布，不允许带「未登记的缺陷」发布：
# 缺内核二进制时，只有 docs/known-limits.md 里有对应条目才放行（WARN），
# 否则直接 FAIL。
#
# 用法：deploy/preflight.sh v2.1.0
# 退出码：0 可发布；1 有阻断项（看 FAIL 行）。
set -euo pipefail
SELF_DIR="$(cd -P "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT="$(cd -P "${SELF_DIR}/.." && pwd -P)"
cd "$ROOT"

PASS=0
FAIL=0
WARNS=()

step() { printf '\n\033[1m→ %s\033[0m\n' "$*"; }
pass() { printf '  PASS %s\n' "$*"; PASS=$((PASS + 1)); }
fail() { printf '  FAIL %s\n' "$*" >&2; FAIL=$((FAIL + 1)); }
warn() { printf '  WARN %s\n' "$*"; WARNS+=("$*"); }

VER="${1:-}"
if [[ -z "$VER" ]]; then
	printf '用法: deploy/preflight.sh vX.Y.Z\n' >&2
	exit 2
fi

# 后台 shell 不加载交互 profile：go / node 常不在 PATH 里。
if ! command -v go >/dev/null 2>&1 && [[ -x "${HOME}/sdk/go/bin/go" ]]; then
	export PATH="${HOME}/sdk/go/bin:${PATH}"
fi

# ============ 1. 版本号合规 ============
step "版本号"
if "${SELF_DIR}/check-version.sh" "$VER" >/dev/null 2>&1; then
	pass "版本号 ${VER} 合规（次/修订位 0–9）"
else
	fail "版本号 ${VER} 不合规，用 deploy/check-version.sh --next 取号"
fi

# ============ 2. build 标记与 demo 同步 ============
# 标记是「升级后要不要覆盖内嵌界面」的唯一依据；demo.html 是离线回归的载体，
# 它过期时浏览器测的是旧界面且不会报错，只能靠标记和 TestDemoHTMLMatchesIndex。
step "build 标记"
MARK="triune-panel-web/${VER}"
if grep -q "$MARK" internal/webui/index.html; then
	pass "index.html 标记 ${MARK}"
else
	fail "index.html 不是 ${MARK}（改了界面忘了改标记，老用户升级后看到的还是旧界面）"
fi
if [[ -f demo.html ]] && grep -q "$MARK" demo.html; then
	pass "demo.html 已重建且标记一致"
else
	fail "demo.html 缺失或标记不一致，跑 python3 deploy/build-demo.py"
fi
if go test ./internal/webui/ -run TestDemoHTMLMatchesIndex -count=1 >/dev/null 2>&1; then
	pass "demo.html 与 index.html 内容同步"
else
	fail "demo.html 与 index.html 不同步，重新 build-demo"
fi

# ============ 3. CHANGELOG 小节 ============
# release.sh 用 awk index($0,"## vX")==1 抽说明，小节不存在就会发一个空 Release。
step "CHANGELOG"
if [[ -f CHANGELOG.md ]] && awk -v v="$VER" 'index($0, "## " v) == 1 { f = 1 } END { exit !f }' CHANGELOG.md; then
	pass "存在「## ${VER}」小节"
else
	fail "CHANGELOG.md 缺少「## ${VER}」小节，发布说明会为空"
fi

# ============ 4. 全量单测 ============
step "单测"
if go vet ./... >/dev/null 2>&1; then
	pass "go vet"
else
	fail "go vet 有告警"
fi
TEST_OUT="$(go test ./... -count=1 2>&1 || true)"
if printf '%s\n' "$TEST_OUT" | grep -qE '^(FAIL|--- FAIL)'; then
	printf '%s\n' "$TEST_OUT" | grep -E '^(FAIL|--- FAIL)' | head -10 >&2
	fail "单测有失败"
else
	PKGS="$(printf '%s\n' "$TEST_OUT" | grep -cE 'ok[[:space:]]+triune' || true)"
	pass "全量单测绿（${PKGS} 个包 ok）"
fi

# ============ 5. e2e ============
# 清单从 ci.yml 的循环里解析——抄一份到脚本就一定会漂移，而漏跑的脚本
# 不会报错，只会静默地不测。
step "e2e（离线首屏 / 主题 / 分组）"
if [[ ! -d .e2e/node_modules ]]; then
	fail "缺 .e2e/node_modules，先跑 npm ci --prefix .e2e"
else
	E2E_LIST="$(python3 - .github/workflows/ci.yml <<'PY'
import re, sys
src = open(sys.argv[1], encoding="utf-8").read()
m = re.search(r"for s in ([^;]*?first-paint-flicker[^;]*?);\s*do", src, re.S)
if not m:
    sys.exit(1)
print("\n".join(m.group(1).replace("\\", " ").split()))
PY
)" || E2E_LIST=""
	if [[ -z "$E2E_LIST" ]]; then
		fail "无法从 ci.yml 解析 e2e 脚本清单"
	else
		E2E_FAILED=0
		E2E_COUNT=0
		while IFS= read -r s; do
			[[ -n "$s" ]] || continue
			E2E_COUNT=$((E2E_COUNT + 1))
			if ! node ".e2e/${s}.js" >"/tmp/preflight-e2e-${s}.log" 2>&1; then
				printf '  失败脚本：%s（日志 /tmp/preflight-e2e-%s.log）\n' "$s" "$s" >&2
				E2E_FAILED=$((E2E_FAILED + 1))
			fi
		done <<<"$E2E_LIST"
		if [[ $E2E_FAILED -eq 0 ]]; then
			pass "${E2E_COUNT} 个 e2e 脚本全部通过"
		else
			fail "${E2E_FAILED}/${E2E_COUNT} 个 e2e 脚本失败"
		fi
	fi
	# 真后端首屏（/api/status 延迟、401、网络错误）：脚本自己构建二进制。
	if node .e2e/first-paint-server.js >/tmp/preflight-e2e-server.log 2>&1; then
		pass "真后端首屏回归"
	else
		fail "真后端首屏回归失败（日志 /tmp/preflight-e2e-server.log）"
	fi
fi

# ============ 6. 真内核 ============
# check 只证明「能加载」，不证明「能启动」——这里调的都是 run 判活的测试。
step "真内核（启动判活）"
find_bin() {
	local envv="$1" name="$2" v=""
	v="${!envv:-}"
	if [[ -n "$v" ]]; then
		printf '%s' "$v"
		return 0
	fi
	command -v "$name" 2>/dev/null || return 1
}

# kernel_gate <显示名> <环境变量> <known-limits 关键词> <包> <测试正则>
KERNEL_OK=0

kernel_gate() {
	local label="$1" envv="$2" kw="$3" pkg="$4" pat="$5" bin=""
	bin="$(find_bin "$envv" "$label")" || bin=""
	if [[ -n "$bin" && ! -x "$bin" ]]; then
		fail "${label} 的二进制不可执行：${bin}"
		return
	fi
	if [[ -z "$bin" ]]; then
		# 没有二进制不等于可以跳过：要么登记过，要么就是未登记缺陷。
		if grep -qi "$kw" docs/known-limits.md 2>/dev/null; then
			warn "${label} 无内核二进制，按 known-limits 已登记条目放行"
		else
			fail "${label} 无内核二进制（设 \$${envv} 或装到 PATH），且 known-limits 未登记"
		fi
		return
	fi
	# 路径必须加引号：label 一旦含空格（如「sing-box 服务端」），未加引号的
	# 重定向会被 bash 拆成两个词，报 ambiguous redirect 并让这一条恒失败。
	if env "$envv=$bin" go test "./${pkg}/" -run "$pat" -count=1 >"/tmp/preflight-kernel-${label}.log" 2>&1; then
		KERNEL_OK=$((KERNEL_OK + 1))
		pass "${label} 启动判活通过"
	else
		printf '  日志 /tmp/preflight-kernel-%s.log\n' "$label" >&2
		tail -5 "/tmp/preflight-kernel-${label}.log" >&2 || true
		fail "${label} 启动判活失败"
	fi
}

kernel_gate "sing-box 客户端" TRINE_SINGBOX_BIN "sing-box" internal/api 'TestSingboxClientConfigStarts|TestSingboxClientConfigStartsWithLANResolver|TestMomoConfigPassesRealSingboxCheck'
kernel_gate "sing-box 服务端" TRINE_SINGBOX_BIN "sing-box" internal/livecheck 'TestSingboxServerConfigStarts'
kernel_gate "mihomo" TRINE_MIHOMO_BIN "mihomo" internal/api 'TestMihomoClientConfigStarts|TestMihomoUsesLANResolverPolicy'
kernel_gate "xray" TRINE_XRAY_BIN "xray" internal/livecheck 'TestXrayConfigStarts'

# 三个内核全缺时不能只是 WARN：那等于产物一次都没真跑过，
# 「已登记」也不该兜住这种环境缺失。
if [[ $KERNEL_OK -eq 0 ]]; then
	fail "没有任何真内核可用，产物未做启动验证（设 TRINE_SINGBOX_BIN / TRINE_MIHOMO_BIN / TRINE_XRAY_BIN）"
fi

# ============ 7. 已知限制登记册 ============
# 规则：允许带「已登记」的限制发布，不允许带「未决」的发布。
# 未决 = 状态写成未决、或影响/验证方式为空、或状态不在允许集合里。
step "已知限制登记册"
if [[ ! -f docs/known-limits.md ]]; then
	fail "缺 docs/known-limits.md"
else
	REG_OUT="$(python3 "${SELF_DIR}/check-known-limits.py" docs/known-limits.md 2>/dev/null)" || true

	UNRESOLVED="$(printf '%s\n' "$REG_OUT" | grep '^UNRESOLVED' || true)"
	UNVERIFIED="$(printf '%s\n' "$REG_OUT" | grep '^UNVERIFIED' || true)"
	REG_ROWS="$(printf '%s\n' "$REG_OUT" | sed -n 's/^ROWS\t//p')"

	if [[ -n "$UNRESOLVED" ]]; then
		printf '%s\n' "$UNRESOLVED" | cut -f2,3 | sed 's/^/  未决：/' >&2
		fail "登记册有未决条目，先写清影响与验证方式（或改成未验证）"
	elif [[ -z "$REG_ROWS" || "$REG_ROWS" -eq 0 ]]; then
		fail "登记册没有解析到条目（表头或分隔行格式不对）"
	else
		pass "登记册 ${REG_ROWS} 条，无未决"
	fi
	if [[ -n "$UNVERIFIED" ]]; then
		while IFS=$'\t' read -r _ rid item; do
			warn "未验证 ${rid}：${item}"
		done <<<"$UNVERIFIED"
	fi
fi

# ============ 8. 演示页路由 ============
# mockRoute 把 /api/panel/rollback 切成 api="panel" + rest="/rollback"，
# 写成 api === "panel/rollback" 的分支永不命中；走不到只是 return {}，
# 不报错也不告警，症状是「演示页那块一直空着」。曾真实存在五条。
step "演示页路由（mock 分支必须真的能命中）"
if MOCK_OUT="$(python3 "${SELF_DIR}/check-mock-routes.py" internal/webui/app.js 2>&1)"; then
	pass "mock 分支没有带斜杠的 api 常量比较"
else
	printf '%s\n' "$MOCK_OUT" | sed 's/^/  /' >&2
	fail "演示页存在永不命中的 mock 分支（api 只到第一个斜杠，子路径要用 rest.startsWith）"
fi

# ============ 9. 换进程接口的调用点 ============
# 请求返回时进程已经没了，收尾只能是「挂层→拿票→轮询→换票→reload」。
# v2.1.9 只把等待层接在三条入口中的一条上，另外两条还是 setTimeout 猜，
# 用户点的正是那两条。所以判据写成结构约束：这些路径只允许出现在
# runPanelOp 里，每个恰好一次；清单以 api.go 的 RestartEndpoints 为准。
step "换进程接口（只能有一条调用路径）"
if ENTRY_OUT="$(python3 "${SELF_DIR}/check-restart-entry.py" 2>&1)"; then
	pass "换进程接口各只有一处调用，都在统一收尾函数内"
else
	printf '%s\n' "$ENTRY_OUT" | sed 's/^/  /' >&2
	fail "换进程接口出现了第二条调用路径（漏等待层/接力凭证的那个入口看着也是好的）"
fi

# ============ 汇总 ============
printf '\n'
if [[ ${#WARNS[@]} -gt 0 ]]; then
	printf '带 %d 条已登记限制发布（明细见 docs/known-limits.md）\n' "${#WARNS[@]}"
fi
if [[ $FAIL -gt 0 ]]; then
	printf '\033[31m%s 项通过，%s 项失败——不可发布\033[0m\n' "$PASS" "$FAIL" >&2
	exit 1
fi
printf '\033[32m%s 项通过，0 项失败——可以发布\033[0m\n' "$PASS"
