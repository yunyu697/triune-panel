#!/usr/bin/env bash
# 一条命令走完发版：校验版本号 → 四平台构建 → 生成校验和 → 打 tag → 推送 → 建 Release。
#
# 前置条件：工作区干净（要发的提交必须已经 commit），本地与远端都没有同名 tag。
# 版本号规则见 check-version.sh，发布号必须是它的合规输出。
set -euo pipefail

PROG="$(basename "$0")"
SELF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SELF_DIR/.." && pwd)"
cd "$ROOT"

# 发布带几个平台，这里就是几个；CI 的交叉编译列表必须与之一致
TARGETS="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64"

die() { printf '✘ %s\n' "$*" >&2; exit 1; }
say() { printf '%s\n' "$*"; }
step() { printf '\n\033[1m→ %s\033[0m\n' "$*"; }

usage() {
  cat <<USAGE
用法:
  ${PROG} <版本> [选项]

选项:
  --title <标题>   Release 标题（默认就用版本号）
  --notes <文件>   Release 说明（默认取 CHANGELOG.md 里对应版本的小节）
  --dry-run        只构建并打印将要执行的动作，不动远端
  --yes, -y        跳过二次确认
  -h, --help       显示这段说明

示例:
  ${PROG} v1.0.1
  ${PROG} v1.0.1 --title "v1.0.1 — 修掉 xxx" --dry-run

发布前请先把 CHANGELOG.md 里对应版本的小节写好并 commit——工作区不干净会直接退出。
USAGE
  exit "${1:-0}"
}

VER=""
TITLE=""
NOTES=""
DRY=0
ASSUME_YES=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    -h|--help) usage 0 ;;
    --dry-run) DRY=1; shift ;;
    --yes|-y)  ASSUME_YES=1; shift ;;
    --title)   [[ -n "${2:-}" ]] || die "--title 需要一个值"; TITLE="$2"; shift 2 ;;
    --notes)   [[ -n "${2:-}" ]] || die "--notes 需要一个文件路径"; NOTES="$2"; shift 2 ;;
    -*)        die "未知选项: $1" ;;
    *)         [[ -z "$VER" ]] || die "多余的参数: $1"; VER="$1"; shift ;;
  esac
done

[[ -n "$VER" ]] || usage 2
[[ -n "$TITLE" ]] || TITLE="$VER"

step "校验版本号"
"$SELF_DIR/check-version.sh" "$VER" || exit 1

step "检查工作区与 tag"
[[ "$(git rev-parse --show-toplevel)" == "$ROOT" ]] || die "请在仓库根目录运行"
if [[ -n "$(git status --porcelain)" ]]; then
  die "工作区不干净，先把改动提交掉：
$(git status --short)"
fi
if git rev-parse -q --verify "refs/tags/$VER" >/dev/null; then
  die "本地已有 tag $VER"
fi
if git ls-remote --tags --exit-code origin "refs/tags/$VER" >/dev/null 2>&1; then
  die "远端已有 tag $VER"
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
rm -rf dist && mkdir -p dist
ASSETS=()
for t in $TARGETS; do
  os="${t%/*}"; arch="${t#*/}"
  out="dist/triune-panel-${os}-${arch}"
  say "  ${os}/${arch}"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -trimpath -ldflags "-s -w -X main.Version=$VER" -o "$out" ./cmd/server
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
say "  资产    ${#ASSETS[@]} 个"
say "  说明    $NOTE_SRC"
say "  推送    origin $BRANCH + $VER"

if [[ $DRY -eq 1 ]]; then
  say ""
  say "（--dry-run：到此为止，远端未改动）"
  exit 0
fi

if [[ $ASSUME_YES -eq 0 ]]; then
  printf '\n继续？[y/N] '
  read -r ans
  [[ "$ans" == "y" || "$ans" == "Y" ]] || die "已取消"
fi

step "打 tag 并推送"
git tag -a "$VER" -m "$TITLE"
git push origin "$BRANCH"
git push origin "$VER"

step "创建 Release"
gh release create "$VER" --title "$TITLE" --notes-file "$NOTES" "${ASSETS[@]}"

step "完成"
gh release view "$VER" --json url -q .url
