#!/usr/bin/env bash
# 发布后校验：把每次发版完手工敲的那串检查固化成一条命令。
#
#   deploy/verify-release.sh v1.3.1
#
# 查的不是「资产名存在」——GitHub 抽风时（422/500/EOF）报了错的对象可能已经
# 建好了一部分，只列名字会漏掉「草稿没发布」「资产是旧的」这类半成品状态。
# 所以：元数据走匿名 API、索引走匿名 API（面板更新检查用的就是这条）、
# 五个资产全部下载下来与本地 dist/ 逐字节 cmp。
#
# 前置：刚跑完 release.sh，dist/ 或 deploy/ 里还留着那批二进制。
# 选项：
#   --repo O/R        仓库（默认从 git remote origin 推出来）
#   --no-download     跳过资产逐字节比对（快速 / 离线检查元数据）
#   --allow-dirty     工作区有改动时不算失败
set -euo pipefail

PROG="$(basename "$0")"
SELF_DIR="$(cd -P "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT="$(cd -P "$SELF_DIR/.." && pwd -P)"
cd "$ROOT"

die() { printf '✘ %s\n' "$*" >&2; exit 1; }
# 必须自己定义：macOS 有 /usr/bin/say（语音合成），漏定义时脚本不会报错、
# 只会把仓库名念出来；换到 Linux CI 就是 command not found + set -e 直接退出。
say() { printf '%s\n' "$*"; }
step() { printf '\n\033[1m→ %s\033[0m\n' "$*"; }
PASS=0
FAIL=0
ok() { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS + 1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*"; FAIL=$((FAIL + 1)); }
ck() {
  local name="$1" want="$2" got="$3"
  if [[ "$want" == "$got" ]]; then ok "$name"; else bad "$name  期望=[${want}] 实际=[${got}]"; fi
}

usage() {
  cat <<USAGE
用法: ${PROG} <版本> [选项]

  --repo O/R        仓库（默认从 git remote origin 推出来）
  --no-download     跳过资产逐字节比对
  --allow-dirty     工作区有改动时不算失败
  -h, --help        显示这段说明

示例: ${PROG} v1.3.1
USAGE
  exit "${1:-0}"
}

VER=""
REPO=""
NO_DL=0
ALLOW_DIRTY=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    -h|--help) usage 0 ;;
    --no-download) NO_DL=1; shift ;;
    --allow-dirty) ALLOW_DIRTY=1; shift ;;
    --repo) [[ -n "${2:-}" ]] || die "--repo 需要一个值"; REPO="$2"; shift 2 ;;
    -*) die "未知选项: $1" ;;
    *) [[ -z "$VER" ]] || die "多余的参数: $1"; VER="$1"; shift ;;
  esac
done
[[ -n "$VER" ]] || usage 2
[[ "$VER" =~ ^v[0-9]+\.[0-9]\.[0-9]$ ]] || die "版本号格式不对: ${VER}（期望形如 v1.3.1）"

step "预检依赖"
for c in curl python3 git cmp; do
  command -v "$c" >/dev/null 2>&1 || die "缺少 ${c}，先装上再跑"
done
ok "curl / python3 / git / cmp 齐备"

if [[ -z "$REPO" ]]; then
  ORIGIN="$(git remote get-url origin)" || die "读不到 git remote origin，用 --repo 指定"
  REPO="$(printf '%s' "$ORIGIN" | sed -E 's#^(https?://[^/]+/|git@[^:]+:)##; s#\.git$##')"
fi
[[ "$REPO" == */* ]] || die "推不出 owner/repo：${REPO}，用 --repo 指定"
API="https://api.github.com/repos/${REPO}"
UA="triune-panel-verify"
say "  仓库 ${REPO}"

TARGETS="$(sed -n 's/^TARGETS="\(.*\)"/\1/p' "$SELF_DIR/release.sh" | head -1)"
[[ -n "$TARGETS" ]] || die "从 deploy/release.sh 里解析不出 TARGETS"
EXPECT=(sha256sums.txt)
# shellcheck disable=SC2086
for t in $TARGETS; do
  os="${t%/*}"; arch="${t#*/}"
  EXPECT+=("triune-panel-${os}-${arch}")
done
sorted() { printf '%s\n' "$@" | LC_ALL=C sort | tr '\n' ' '; }
WANT_ASSETS="$(sorted "${EXPECT[@]}")"

get() { curl -fsSL --retry 3 --retry-delay 2 -A "$UA" "$@"; }

step "Release 元数据"
if ! META="$(get "$API/releases/tags/$VER" 2>/dev/null)"; then
  bad "拿不到 ${VER} 的 Release"
  cat >&2 <<HINT
  /releases/tags/<tag> 对草稿返 404 —— 很可能是上一轮 gh release create
  报错后留下了一个 draft。任何一步报错都先查远端真实状态再决定是否重试：
    gh release view ${VER} --json isDraft,assets
    gh api repos/${REPO}/releases --paginate -q '.[]|select(.tag_name=="${VER}")'
HINT
  exit 1
fi
PARSED="$(printf '%s' "$META" | python3 -c '
import json, sys
d = json.load(sys.stdin)
a = d.get("assets", [])
print("\t".join([
    str(bool(d.get("draft"))).lower(),
    str(bool(d.get("prerelease"))).lower(),
    ",".join(x["name"] for x in a),
    ",".join(x["name"] for x in a if x.get("state") != "uploaded" or x.get("size", 0) <= 0),
]))
')" || die "解析 Release 元数据失败"
IFS=$'\t' read -r DRAFT PRE ASSET_NAMES BAD_ASSETS <<< "$PARSED"
ck "不是草稿" false "$DRAFT"
ck "不是预发布" false "$PRE"
# 一个资产都没有时 GOT_ASSETS 是空数组：bash 3.2 + set -u 下展开
# "${GOT_ASSETS[@]}" 会直接报 unbound variable，脚本挂在这里而不是打印
# FAIL——可这份检查的用途恰恰是发现「资产没传完」的半成品 Release。
# 按字符串判空走两条分支，别展开空数组。
if [[ -n "$ASSET_NAMES" ]]; then
  IFS=',' read -r -a GOT_ASSETS <<< "$ASSET_NAMES"
  ck "资产清单与 release.sh 的 TARGETS 一致" "$WANT_ASSETS" "$(sorted "${GOT_ASSETS[@]}")"
else
  ck "资产清单与 release.sh 的 TARGETS 一致" "$WANT_ASSETS" ""
fi
ck "所有资产都是 uploaded 且非空" "" "$BAD_ASSETS"

step "匿名版本索引（面板更新检查走的就是这条）"
IDX="$(get "$API/releases?per_page=30")" || die "拉不到匿名 /releases?per_page=30"
IDX_INFO="$(printf '%s' "$IDX" | python3 -c '
import json, sys
rs = json.load(sys.stdin)
vis = [r["tag_name"] for r in rs if not r.get("draft")]
drafts = [r["tag_name"] for r in rs if r.get("draft")]
print("\t".join([vis[0] if vis else "", str(len(vis)), ",".join(drafts)]))
')" || die "解析匿名版本列表失败"
IFS=$'\t' read -r FIRST N_VIS DRAFTS <<< "$IDX_INFO"
ck "匿名列表第一条就是 ${VER}" "$VER" "$FIRST"
if [[ "$N_VIS" -gt 0 ]]; then IDX_OK=true; else IDX_OK=false; fi
ck "匿名列表不为空" "true" "$IDX_OK"
ck "没有残留的草稿 Release" "" "$DRAFTS"
LATEST="$(get "$API/releases/latest" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("tag_name",""))')" \
  || die "拉不到 /releases/latest"
ck "/releases/latest 指向 ${VER}" "$VER" "$LATEST"

step "tag 与提交"
if git rev-parse -q --verify "refs/tags/$VER" >/dev/null; then
  ck "本地 tag 指向 HEAD" "$(git rev-parse HEAD)" "$(git rev-parse "$VER^{commit}")"
else
  ck "本地存在 tag ${VER}" "true" "false"
fi
REMOTE_COMMIT="$(git ls-remote origin "refs/tags/$VER" "refs/tags/$VER^{}" | awk '/\^\{\}$/{print $1}' | head -1)"
if [[ -z "$REMOTE_COMMIT" ]]; then
  REMOTE_COMMIT="$(git ls-remote origin "refs/tags/$VER" | awk '{print $1}' | head -1)"
fi
if [[ -n "$REMOTE_COMMIT" ]]; then RMT_OK=true; else RMT_OK=false; fi
ck "远端存在 tag ${VER}" "true" "$RMT_OK"
if [[ "$RMT_OK" == "true" ]]; then
  ck "远端 tag 指向 HEAD" "$(git rev-parse HEAD)" "$REMOTE_COMMIT"
fi

step "资产逐字节比对"
LOCAL_DIR=""
if [[ -f dist/sha256sums.txt ]]; then LOCAL_DIR="dist"
elif [[ -f deploy/sha256sums.txt ]]; then LOCAL_DIR="deploy"; fi
if [[ $NO_DL -eq 1 ]]; then
  printf '  SKIP --no-download：只核元数据，没比字节\n'
elif [[ -z "$LOCAL_DIR" ]]; then
  bad "dist/ 与 deploy/ 里都没有 sha256sums.txt，没法比对（发版产物被清掉了？）"
else
  TMP="$(mktemp -d -t triune-verify)"
  trap 'rm -rf "$TMP"' EXIT
  say "  本地基准目录 ${LOCAL_DIR}/"
  for name in "${EXPECT[@]}"; do
    local_file="${LOCAL_DIR}/${name}"
    if [[ ! -f "$local_file" ]]; then
      bad "${name}：本地文件不存在"
      continue
    fi
    url="https://github.com/${REPO}/releases/download/${VER}/${name}"
    if ! get "$url" -o "$TMP/$name"; then
      bad "${name}：下载失败"
      continue
    fi
    if cmp -s "$local_file" "$TMP/$name"; then
      ok "${name} 与远端一致"
    else
      bad "${name} 与远端字节不一致（发版中途补传过资产？）"
    fi
  done
fi

step "工作区"
if [[ -n "$(git status --porcelain)" ]]; then
  if [[ $ALLOW_DIRTY -eq 1 ]]; then
    printf '  SKIP 工作区有改动（--allow-dirty）\n'
  else
    ck "工作区干净" "" "$(git status --short | head -3 | tr '\n' ' ')"
  fi
else
  ok "工作区干净"
fi

printf '\n%d 项通过，%d 项失败\n' "$PASS" "$FAIL"
if [[ "$FAIL" -gt 0 ]]; then
  cat >&2 <<HINT
注意：GitHub 报 422/500/EOF 时，那次请求可能已经生效了一部分。
先查远端真实状态（上面的草稿查询 / gh release view），再决定是补资产、
PATCH draft=false，还是补建 Release —— 盲目重跑 release.sh 会撞「tag 已存在」。
HINT
  exit 1
fi
