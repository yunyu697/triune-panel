#!/usr/bin/env bash
# release.sh --resume 的资产核对桩测（离线，不碰远端 Release）。
# 钉住四种分支：digest 一致跳过 / 不一致覆盖 / digest 缺失保守重传 /
# 名字缺了补传；以及 API 读不到元数据时必须停下，不能盲目前进。
# 这些全局变量是给抽出来的 sync_existing_assets 用的，shellcheck 看不到 source
# shellcheck disable=SC2034
set -euo pipefail
SELF_DIR="$(cd -P "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT="$(cd -P "$SELF_DIR/../.." && pwd -P)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/triune-resume-stub.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT

python3 - "$ROOT/deploy/release.sh" "$TMP/func.sh" <<'PY'
import sys
src = open(sys.argv[1], encoding="utf-8").read().splitlines(keepends=True)
start = next(i for i, l in enumerate(src) if l.startswith("sync_existing_assets()"))
end = next(i for i in range(start, len(src)) if src[i].rstrip("\n") == "}")
open(sys.argv[2], "w", encoding="utf-8").write("".join(src[start:end + 1]))
PY
# shellcheck disable=SC1090
source "$TMP/func.sh"

VER="v1.9.9"
REPO="o/r"
for n in a b c d; do printf 'content-%s' "$n" > "$TMP/$n"; done
ASSETS=("$TMP/a" "$TMP/b" "$TMP/c" "$TMP/d")
REL_ASSETS="a,b,c"
SHA_A="1111111111111111111111111111111111111111111111111111111111111111"
SHA_B="2222222222222222222222222222222222222222222222222222222222222222"
SHA_C="3333333333333333333333333333333333333333333333333333333333333333"
SHA_D="4444444444444444444444444444444444444444444444444444444444444444"
STUB_API_RC=0
STUB_DIGESTS="$(printf 'a\tsha256:%s\nb\tsha256:WRONGVALUE\nc\t\n' "$SHA_A")"
UPLOADS="$TMP/uploads"
: > "$UPLOADS"

gh() {
  if [[ "$1" == "api" ]]; then
    [[ "$STUB_API_RC" -eq 0 ]] || return 1
    printf '%s\n' "$STUB_DIGESTS"
    return 0
  fi
  if [[ "$1" == "release" && "$2" == "upload" ]]; then
    # 第 4 个参数是资产路径；首列记文件名，方便断言哪份被重传/补传
    printf '%s %s\n' "$(basename "$4")" "$*" >> "$UPLOADS"
    return 0
  fi
  return 1
}
shasum() {
  local f="${!#}" s=""
  case "$(basename "$f")" in
    a) s="$SHA_A" ;; b) s="$SHA_B" ;; c) s="$SHA_C" ;; d) s="$SHA_D" ;;
  esac
  printf '%s  %s\n' "$s" "$f"
}
say() { :; }
die() { printf 'DIE %s\n' "$*" >&2; exit 1; }

PASS=0
FAIL=0
ck() {
  if [[ "$2" == "$3" ]]; then
    PASS=$((PASS + 1))
  else
    FAIL=$((FAIL + 1))
    printf '  FAIL %s  期望=[%s] 实际=[%s]\n' "$1" "$2" "$3"
  fi
}

sync_existing_assets
ck "digest 一致的资产不重传" "0" "$(grep -c '^a ' "$UPLOADS" || true)"
ck "digest 不一致触发覆盖重传" "1" "$(grep -c '^b .*--clobber' "$UPLOADS" || true)"
ck "digest 缺失保守覆盖重传" "1" "$(grep -c '^c .*--clobber' "$UPLOADS" || true)"
ck "缺名字的资产补传" "1" "$(grep -c '^d ' "$UPLOADS" || true)"
ck "补传不带 --clobber" "0" "$(grep -c '^d .*--clobber' "$UPLOADS" || true)"
ck "总共重传/补传 3 份" "3" "$(wc -l < "$UPLOADS" | tr -d ' ')"

STUB_API_RC=1
if ( sync_existing_assets ) >/dev/null 2>&1; then RC=0; else RC=1; fi
ck "读不到资产元数据时停下" "1" "$RC"

printf '\n%s：%d 项通过，%d 项失败\n' "$(basename "$0")" "$PASS" "$FAIL"
[[ "$FAIL" -eq 0 ]]
