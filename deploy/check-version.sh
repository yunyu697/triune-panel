#!/usr/bin/env bash
# 版本号规则：每段只用 0–9，满 9 进位，任何一段都不出现两位数。
#
#   v1.0.9 → v1.1.0 → … → v1.9.9 → v2.0.0
#
# 为什么要有这条：v0.12.0 这种号读起来像「第十二个小版本」，但排在 v0.9.9
# 后面时人会下意识觉得 12 比 9 大得多，实际只差三个。单位数进位制让「大版本
# 跳了一级」这件事一眼可见，代价是段位容量小——所以进位规则必须写死在这里，
# CI 与发布流程共用同一份判定，别在别处再抄一遍。
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

die() { printf '%s\n' "$*" >&2; exit 1; }

usage() {
  cat <<USAGE
用法:
  ${PROG} <版本>                    校验版本号是否符合规则
  ${PROG} --next <版本> [--patch|--minor|--major]
                                    输出下一个版本（默认 --patch）

规则: 每段 0–9，满 9 进位，任何一段都不出现两位数
      v1.0.9 → v1.1.0 · v1.9.9 → v2.0.0

示例:
  ${PROG} v1.0.0
  ${PROG} --next v1.0.9
  ${PROG} --next v0.12.0        # 越界输入 → 进位到下一个合规号 v1.0.0
USAGE
}

# 合规：v? 后正好三段，每段一位数字
conforming() { [[ "$1" =~ ^v?[0-9]\.[0-9]\.[0-9]$ ]]; }

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

  # 输入本身就越界（如 v0.12.0）时，它后面第一个合规号是下一个主版本——
  # 不能先按末位进位（那会算出 v1.0.1，凭空跳过 1.0.0）。
  if [[ $MAJ -gt 9 || $MIN -gt 9 || $PAT -gt 9 ]]; then
    printf 'v%d.0.0\n' "$((MAJ + 1))"
    exit 0
  fi

  bump=patch
  case "${3:-}" in
    ""|--patch) bump=patch ;;
    --minor)    bump=minor ;;
    --major)    bump=major ;;
    *) die "✘ 未知选项: $3（可用 --patch / --minor / --major）" ;;
  esac

  case "$bump" in
    major) MAJ=$((MAJ + 1)); MIN=0; PAT=0 ;;
    minor) MIN=$((MIN + 1)); PAT=0
           if [[ $MIN -gt 9 ]]; then MAJ=$((MAJ + 1)); MIN=0; fi ;;
    patch) PAT=$((PAT + 1))
           if [[ $PAT -gt 9 ]]; then PAT=0; MIN=$((MIN + 1)); fi
           if [[ $MIN -gt 9 ]]; then MIN=0; MAJ=$((MAJ + 1)); fi ;;
  esac

  # 主版本进位溢出：v9.9.9 已是三段单位数的上限，再往上没有合规号可给。
  # 这里必须硬失败——若顺手输出 v10.0.0/v11.0.0，等于自己破坏刚立的规则。
  if [[ $MAJ -gt 9 ]]; then
    die "✘ 已到 v9.9.9，三段单位数制的上限，没有下一个合规版本号
  要么把段位扩成四段（需同步改本脚本与 CI 判定），要么放宽每段上限"
  fi

  printf 'v%d.%d.%d\n' "$MAJ" "$MIN" "$PAT"
  exit 0
fi

ver="${1#v}"
if conforming "$1"; then
  echo "OK v${ver}（每段 0–9，合规）"
  exit 0
fi

printf '✘ 版本号不合规: %s\n' "$1" >&2
printf '  规则：每段只用 0–9，满 9 进位，任何一段都不出现两位数\n' >&2
if parse "$1"; then
  printf '  这个号的下一段已经越界。下一个合规版本是: ' >&2
  "$0" --next "$1" >&2
else
  printf '  期望形如 v1.0.0（三段数字，可带 v 前缀）\n' >&2
fi
exit 1
