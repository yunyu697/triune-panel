#!/usr/bin/env bash
# 一条命令走完发版：预检依赖 → 校验版本号 → 四平台构建 → 生成校验和 →
# 打 tag → 推送 → 建 Release。
#
# 前置条件：工作区干净（要发的提交必须已经 commit），本地与远端都没有同名 tag。
# 版本号规则见 check-version.sh，发布号必须是它的合规输出。
#
# 中途断在 push / 建 Release 时用 --resume 续跑：脚本会先查远端真实状态
# （tag 在不在、Release 是不是草稿、缺哪几个资产），只补没做完的那几步。
# **不要**直接重跑原命令：tag 已经建好的话会死在「本地已有 tag」。
set -euo pipefail

PROG="$(basename "$0")"
# 一律取物理路径：macOS 的 /tmp 是 /private/tmp 的符号链接，而 git rev-parse
# --show-toplevel 返回物理路径、pwd 默认返回逻辑路径。两者不相等会让下面的
# 「请在仓库根目录运行」误伤放在符号链接目录下的克隆（CI 临时工作区常见）。
SELF_DIR="$(cd -P "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT="$(cd -P "$SELF_DIR/.." && pwd -P)"
cd "$ROOT"

# 发布带几个平台，这里就是几个；CI 的交叉编译列表必须与之一致
TARGETS="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64"

die() { printf '✘ %s\n' "$*" >&2; exit 1; }
say() { printf '%s\n' "$*"; }
step() { printf '\n\033[1m→ %s\033[0m\n' "$*"; }

# 续跑时 dist/ 可能还是上一次构建出来的完整产物；校验和过了就别重编四份，
# 尤其断线时往往连 Go 工具链都不在当前环境里。
dist_ok() {
  [[ -f dist/sha256sums.txt ]] || return 1
  # shellcheck disable=SC2086
  for t in $TARGETS; do
    [[ -f "dist/triune-panel-${t%/*}-${t#*/}" ]] || return 1
  done
  ( cd dist && shasum -a 256 -c sha256sums.txt >/dev/null 2>&1 )
}

usage() {
  cat <<USAGE
用法:
  ${PROG} <版本> [选项]

选项:
  --title <标题>   Release 标题（默认就用版本号）
  --notes <文件>   Release 说明（默认取 CHANGELOG.md 里对应版本的小节）
  --dry-run        只构建并打印将要执行的动作，不动远端
  --resume         断点续跑：跳过已完成的部分，补齐没做完的远端步骤
  --yes, -y        跳过二次确认
  -h, --help       显示这段说明

示例:
  ${PROG} v1.0.1
  ${PROG} v1.0.1 --title "v1.0.1 — 修掉 xxx" --dry-run
  ${PROG} v1.0.1 --resume --notes /tmp/notes.md   # 上一步建 Release 时断线了

发布前请先把 CHANGELOG.md 里对应版本的小节写好并 commit——工作区不干净会直接退出。
USAGE
  exit "${1:-0}"
}

VER=""
TITLE=""
NOTES=""
DRY=0
ASSUME_YES=0
RESUME=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    -h|--help) usage 0 ;;
    --dry-run) DRY=1; shift ;;
    --resume)  RESUME=1; shift ;;
    --yes|-y)  ASSUME_YES=1; shift ;;
    --title)   [[ -n "${2:-}" ]] || die "--title 需要一个值"; TITLE="$2"; shift 2 ;;
    --notes)   [[ -n "${2:-}" ]] || die "--notes 需要一个文件路径"; NOTES="$2"; shift 2 ;;
    -*)        die "未知选项: $1" ;;
    *)         [[ -z "$VER" ]] || die "多余的参数: $1"; VER="$1"; shift ;;
  esac
done

[[ -n "$VER" ]] || usage 2
[[ -n "$TITLE" ]] || TITLE="$VER"

step "预检依赖"
# 后台 shell 不加载交互 profile，go / gh 常常不在 PATH 里：原来要到「构建
# 四平台二进制」才炸，那时版本材料和确认都已经走完了，白等一轮。前移到这里。
for c in go git gh shasum sed awk cp; do
  command -v "$c" >/dev/null 2>&1 || die "缺少 ${c}（后台任务记得显式 export PATH）"
done
go version >/dev/null 2>&1 || die "go 不能执行: $(command -v go)"
# 续跑要查远端真实状态，即使 --dry-run 也需要 gh 能读。
if [[ $DRY -eq 0 || $RESUME -eq 1 ]]; then
  gh auth status >/dev/null 2>&1 || die "gh 未登录（gh auth login，或设置 GH_TOKEN）"
fi
say "  $(go version | head -1)"

step "校验版本号"
"$SELF_DIR/check-version.sh" "$VER" || exit 1

step "校验 Web UI 的 build 标记"
# index.html 头部的 <!-- triune-panel-web/<ver> --> 是「升级后要不要覆盖
# data/web/index.html」的唯一依据：标记与二进制里的不一致才会覆盖。
# 改了 UI 却忘了改标记，老用户升级后打开还是旧界面——v1.0.0 修的就是这个坑，
# 而它只会在"升级"这一条路径上出现，日常开发和 CI 都碰不到。这里硬性卡住。
WEB_MARK="$(sed -n 's/.*triune-panel-web\/\([^ >]*\).*/\1/p' internal/webui/index.html | head -1)"
if [[ "$WEB_MARK" != "$VER" ]]; then
  die "internal/webui/index.html 的 build 标记是「${WEB_MARK}」，与发布版本 ${VER} 不一致。
    改成 ${VER} 后重新提交（记得同步 demo.html），否则升级上来的用户会继续看到旧界面。"
fi
say "  标记一致：triune-panel-web/${WEB_MARK}"

step "检查工作区与 tag"
[[ "$(git rev-parse --show-toplevel)" == "$ROOT" ]] || die "请在仓库根目录运行"
if [[ -n "$(git status --porcelain)" ]]; then
  die "工作区不干净，先把改动提交掉：
$(git status --short)"
fi
ORIGIN="$(git remote get-url origin)" || die "读不到 git remote origin"
REPO="$(printf '%s' "$ORIGIN" | sed -E 's#^(https?://[^/]+/|git@[^:]+:)##; s#\.git$##')"
[[ "$REPO" == */* ]] || die "推不出 owner/repo：${REPO}"
if [[ $RESUME -eq 0 ]]; then
  if git rev-parse -q --verify "refs/tags/$VER" >/dev/null; then
    die "本地已有 tag ${VER}（断在半路的话用 --resume 续跑）"
  fi
  if git ls-remote --tags --exit-code origin "refs/tags/$VER" >/dev/null 2>&1; then
    die "远端已有 tag ${VER}（断在半路的话用 --resume 续跑）"
  fi
else
  # 续跑最重要的不是「敢重试」，而是先看清远端已经生效了多少。
  # GitHub 抽风时报了 422/500/EOF 的那个请求可能已经建好了 draft + 部分资产。
  step "续跑：探测远端状态"
  REL_LINE="$(gh release view "$VER" --json databaseId,isDraft,assets \
    --jq '[(.databaseId|tostring), (.isDraft|tostring), ([.assets[].name] | join(","))] | @tsv' \
    2>/dev/null || true)"
  if [[ -n "$REL_LINE" ]]; then
    IFS=$'\t' read -r REL_ID REL_DRAFT REL_ASSETS <<< "$REL_LINE"
    say "  Release 已存在：id=${REL_ID} draft=${REL_DRAFT}"
    say "  现有资产：${REL_ASSETS}"
  else
    REL_ID=""; REL_DRAFT=""; REL_ASSETS=""
    say "  远端还没有 ${VER} 的 Release（含草稿）"
  fi
  if git ls-remote --tags --exit-code origin "refs/tags/$VER" >/dev/null 2>&1; then
    say "  远端已有 tag ${VER}"
  else
    say "  远端还没有 tag ${VER}"
  fi
fi
BRANCH="$(git rev-parse --abbrev-ref HEAD)"
[[ "$BRANCH" == "main" ]] || say "! 当前分支是 ${BRANCH}，不是 main"

if [[ -n "$NOTES" ]]; then
  [[ -f "$NOTES" ]] || die "找不到说明文件: $NOTES"
  NOTE_SRC="文件 $NOTES"
else
  # 没指定就取 CHANGELOG 里「## <版本>」到下一个二级标题之间的内容
  NOTES="$(mktemp -t triune-notes)"
  trap 'rm -f "$NOTES"' EXIT
  awk -v v="## $VER" 'index($0,v)==1{f=1;next} f&&/^## /{exit} f' CHANGELOG.md > "$NOTES"
  [[ -s "$NOTES" ]] || die "CHANGELOG.md 里没有「## ${VER}」小节，也没有用 --notes 指定说明"
  NOTE_SRC="CHANGELOG.md 的「## ${VER}」小节"
fi

step "构建四平台二进制"
ASSETS=()
REUSE=0
if [[ $RESUME -eq 1 ]] && dist_ok; then
  REUSE=1
  say "  dist/ 里四份产物齐全且校验和通过，复用（--resume）"
else
  rm -rf dist && mkdir -p dist
fi
# shellcheck disable=SC2086
for t in $TARGETS; do
  os="${t%/*}"; arch="${t#*/}"
  out="dist/triune-panel-${os}-${arch}"
  if [[ $REUSE -eq 0 ]]; then
    say "  ${os}/${arch}"
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
      go build -trimpath -ldflags "-s -w -X main.Version=$VER" -o "$out" ./cmd/server
  fi
  ASSETS+=("$out")
done

# 本机架构能直接跑，顺手核一下版本串有没有注入进去
HOST_BIN="dist/triune-panel-$(go env GOOS)-$(go env GOARCH)"
if [[ -x "$HOST_BIN" ]]; then
  GOT="$("$HOST_BIN" -version | awk '{print $2}')"
  [[ "$GOT" == "$VER" ]] || die "二进制自报 ${GOT}，期望 ${VER}（ldflags 没生效？）"
  say "  自检通过：$("$HOST_BIN" -version)"
fi

step "生成校验和"
( cd dist && shasum -a 256 triune-panel-* > sha256sums.txt && shasum -a 256 -c sha256sums.txt )
ASSETS+=("dist/sha256sums.txt")

# 离线安装要用：install.sh 在同目录找到二进制就不联网
cp dist/triune-panel-* dist/sha256sums.txt deploy/

step "将执行"
say "  版本    ${VER}（标题 ${TITLE}）"
say "  tag     $VER → $(git rev-parse --short HEAD)"
say "  资产    ${#ASSETS[@]} 个$(if [[ $REUSE -eq 1 ]]; then printf '（复用 dist/）'; fi)"
say "  说明    $NOTE_SRC"
say "  推送    origin $BRANCH + $VER"
if [[ $RESUME -eq 1 ]]; then say "  模式    断点续跑（按远端已有状态逐项补齐）"; fi

if [[ $DRY -eq 1 ]]; then
  say ""
  if [[ $RESUME -eq 1 ]]; then
    say "（--resume --dry-run：下面是探测到的续跑计划，远端未改动）"
    if [[ -z "${REL_ID:-}" ]]; then
      say "  · Release 不存在，续跑时会新建"
    elif [[ "$REL_DRAFT" == "true" ]]; then
      say "  · Release 已存在但仍是草稿：只补缺的资产，最后 PATCH draft=false 发布"
    else
      say "  · Release 已存在且已发布：只补缺的资产，不动草稿状态"
    fi
    if [[ -n "$(git ls-remote --tags origin "refs/tags/$VER" 2>/dev/null)" ]]; then
      say "  · 远端 tag 已存在，跳过推送"
    else
      say "  · 远端 tag 不存在，续跑时会推送"
    fi
  else
    say "（--dry-run：到此为止，远端未改动）"
  fi
  exit 0
fi

if [[ $ASSUME_YES -eq 0 ]]; then
  printf '\n继续？[y/N] '
  read -r ans
  [[ "$ans" == "y" || "$ans" == "Y" ]] || die "已取消"
fi

step "打 tag 并推送"
if git rev-parse -q --verify "refs/tags/$VER" >/dev/null; then
  [[ $RESUME -eq 1 ]] || die "本地已有 tag $VER"
  say "  ! 本地已有 tag ${VER}，跳过创建"
else
  git tag -a "$VER" -m "$TITLE"
fi
git push origin "$BRANCH"
# 远端 tag 可能已经推上去了。先查再推：盲目 push 会撞 already exists，
# 更要紧的是远端 tag 指向别的提交时必须停下来，别把版本挂错地方。
REMOTE_TAG="$(git ls-remote --tags origin "refs/tags/$VER" | awk '{print $1}' | head -1)"
LOCAL_TAG="$(git rev-parse "$VER")"
if [[ -n "$REMOTE_TAG" ]]; then
  [[ "$REMOTE_TAG" == "$LOCAL_TAG" ]] || \
    die "远端 tag $VER 指向 ${REMOTE_TAG}，本地是 ${LOCAL_TAG}，先查清楚再续跑"
  say "  ! 远端已有 tag $VER 且一致，跳过推送"
else
  git push origin "$VER"
fi

step "创建 Release"
# gh release view 能找到草稿；/releases/tags/<tag> 对草稿返 404，不能用它判存不存在。
if [[ -z "${REL_ID:-}" ]]; then
  REL_LINE="$(gh release view "$VER" --json databaseId,isDraft,assets \
    --jq '[(.databaseId|tostring), (.isDraft|tostring), ([.assets[].name] | join(","))] | @tsv' \
    2>/dev/null || true)"
  if [[ -n "$REL_LINE" ]]; then
    IFS=$'\t' read -r REL_ID REL_DRAFT REL_ASSETS <<< "$REL_LINE"
  else
    REL_ID=""; REL_DRAFT=""; REL_ASSETS=""
  fi
fi
if [[ -z "$REL_ID" ]]; then
  gh release create "$VER" --title "$TITLE" --notes-file "$NOTES" "${ASSETS[@]}"
else
  [[ $RESUME -eq 1 ]] || die "远端已有 ${VER} 的 Release（断在半路的话用 --resume 续跑）"
  say "  ! Release 已存在（id=${REL_ID}），只补缺的资产"
  for a in "${ASSETS[@]}"; do
    name="$(basename "$a")"
    case ",${REL_ASSETS}," in
      *",${name},"*) ;;
      *) say "  + 补传 ${name}"; gh release upload "$VER" "$a" ;;
    esac
  done
  if [[ "$REL_DRAFT" == "true" ]]; then
    say "  · 发布草稿"
    printf '{"draft":false}' | gh api --method PATCH "repos/${REPO}/releases/${REL_ID}" --input - >/dev/null
  fi
fi

step "完成"
gh release view "$VER" --json url -q .url
say "  发完建议跑一次 ./deploy/verify-release.sh $VER"
