#!/usr/bin/env bash
# 版本号规则：次版本与修订号只用 0–9，满 9 进位；主版本不受限，可以两位数。
#
#   v1.0.9 → v1.1.0 → … → v1.9.9 → v2.0.0 → … → v9.9.9 → v10.0.0 → …
#
# 为什么要有这条：v0.12.0 这种号读起来像「第十二个小版本」，但排在 v0.9.9
# 后面时人会下意识觉得 12 比 9 大得多，实际只差三个。让后两段是单位数，
# 「大版本跳了一级」一眼可见。主版本不设限，是留给真正的里程碑用的——
# 到 v9.9.9 之后还能往上走，不必为了凑格式另起炉灶。
#
# 规则必须写死在这里，CI 与发布流程共用同一份判定，别在别处再抄一遍。
#
# 用法：
#   check-version.sh v1.0.0              校验（合规 → 打印 OK 退出 0）
#   check-version.sh --next v1.0.9       下一个版本（1.1.0）
#   check-version.sh --next v1.0.9 --minor   下一个次版本（1.1.0）
#   check-version.sh --next v1.0.9 --major   下一个主版本（2.0.0）
#
# 已发布的历史版本不改号（v0.12.0 就留在那儿），规则从下一个发布号开始生效。
set -euo pipefail

PROG="$(basename "$0")"
# 自引用用绝对路径：通过 PATH 调用时 $0 没有目录部分，直接 "$0" 会找不到自己
SELF="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"

die() { printf '%s\n' "$*" >&2; exit 1; }

usage() {
  cat <<USAGE
用法:
  ${PROG} <版本>                    校验版本号是否符合规则
  ${PROG} --next <版本> [--patch|--minor|--major]
                                    输出下一个版本（默认 --patch）

规则: 主版本任意位数；次版本、修订号 0–9，满 9 进位
      v1.0.9 → v1.1.0 · v1.9.9 → v2.0.0 · v9.9.9 → v10.0.0

示例:
  ${PROG} v1.0.0
  ${PROG} --next v1.0.9
  ${PROG} --next v0.12.0        # 越界输入 → 进位到下一个合规号 v1.0.0
USAGE
}

# 合规：v? 后三段，主版本一位以上数字，后两段各一位数字
conforming() { [[ "$1" =~ ^v?[0-9]+\.[0-9]\.[0-9]$ ]]; }

# 可解析：允许任意位数（用来处理 v0.12.0 这类历史号）
parse() {
  [[ "$1" =~ ^v?([0-9]+)\.([0-9]+)\.([0-9]+)$ ]] || return 1
  MAJ=$((10#${BASH_REMATCH[1]}))
  MIN=$((10#${BASH_REMATCH[2]}))
  PAT=$((10#${BASH_REMATCH[3]}))
}

case "${1:-}" in
  ""|-h|--help) usage; exit 0 ;;
esac

if [[ "${1:-}" == "--next" ]]; then
  [[ -n "${2:-}" ]] || { usage >&2; exit 2; }
  parse "$2" || die "✘ 无法解析版本号: $2（期望形如 v1.0.0）"

  # 后两段越界（如 v0.12.0、v1.0.10）时，它后面第一个合规号就是进位后的值——
  # 不能再额外 +1（那会把 v0.12.0 算成 v1.0.1，凭空跳过 1.0.0）。
  if [[ $MIN -gt 9 || $PAT -gt 9 ]]; then
    if [[ $PAT -gt 9 ]]; then PAT=0; MIN=$((MIN + 1)); fi
    if [[ $MIN -gt 9 ]]; then MIN=0; MAJ=$((MAJ + 1)); fi
    printf 'v%d.%d.%d\n' "$MAJ" "$MIN" "$PAT"
    exit 0
  fi

  bump=patch
  case "${3:-}" in
    ""|--patch) bump=patch ;;
    --minor)    bump=minor ;;
    --major)    bump=major ;;
    *) die "✘ 未知选项: $3（可用 --patch / --minor / --major）" ;;
  esac

  # 主版本不设上限，只有后两段会溢出进位
  case "$bump" in
    major) MAJ=$((MAJ + 1)); MIN=0; PAT=0 ;;
    minor) MIN=$((MIN + 1)); PAT=0
           if [[ $MIN -gt 9 ]]; then MAJ=$((MAJ + 1)); MIN=0; fi ;;
    patch) PAT=$((PAT + 1))
           if [[ $PAT -gt 9 ]]; then PAT=0; MIN=$((MIN + 1)); fi
           if [[ $MIN -gt 9 ]]; then MIN=0; MAJ=$((MAJ + 1)); fi ;;
  esac

  printf 'v%d.%d.%d\n' "$MAJ" "$MIN" "$PAT"
  exit 0
fi

ver="${1#v}"
if conforming "$1"; then
  echo "OK v${ver}（次版本与修订号 0–9，合规）"
  exit 0
fi

printf '✘ 版本号不合规: %s\n' "$1" >&2
printf '  规则：主版本任意位数；次版本、修订号只用 0–9，满 9 进位\n' >&2
if parse "$1"; then
  printf '  这个号的后两段已经越界。下一个合规版本是: ' >&2
  "$SELF" --next "$1" >&2
else
  printf '  期望形如 v1.0.0（三段数字，可带 v 前缀）\n' >&2
fi
exit 1
