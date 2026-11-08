#!/usr/bin/env bash
# 版本号规则的回归：单数字进位制是 CHANGELOG 里正式发布过的决策，
# 不是随手写的正则。这里钉死「什么合规 / 什么拒绝 / 怎么进位」，并防止
# 别的脚本再抄一份规则（抄了就会和发版脚本错位）。
set -euo pipefail
SELF_DIR="$(cd -P "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT="$(cd -P "$SELF_DIR/../.." && pwd -P)"
CV="$ROOT/deploy/check-version.sh"

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
ok_ver() {
  if bash "$CV" "$1" >/dev/null 2>&1; then printf '0'; else printf '1'; fi
}
next_ver() { bash "$CV" --next "$@" 2>/dev/null; }

# 1) 合规：主版本可多位，后两段各一位；可不带 v 前缀
for v in v1.3.2 v0.0.0 v9.9.9 v10.0.0 v100.2.3 1.3.2; do
  ck "合规 ${v}" "0" "$(ok_ver "$v")"
done
# 2) 拒绝：多位次/修订号、缺段、带后缀、非数字
for v in v1.10.0 v1.3.10 v0.12.0 v1.3 v1.3.2-rc1 v1.3.2.4 v1.3.x abc; do
  ck "拒绝 ${v}" "1" "$(ok_ver "$v")"
done
# 空参数不是版本号，按约定打印用法并退出 0
ck "空参数显示用法" "0" "$(ok_ver "")"
# 3) 进位：patch / minor / major 与「越界输入取下一个合规号」
ck "v1.0.9 --patch" "v1.1.0" "$(next_ver v1.0.9 --patch)"
ck "v1.9.9 --patch" "v2.0.0" "$(next_ver v1.9.9 --patch)"
ck "v9.9.9 --patch" "v10.0.0" "$(next_ver v9.9.9 --patch)"
ck "v1.3.2 --minor" "v1.4.0" "$(next_ver v1.3.2 --minor)"
ck "v1.3.2 --major" "v2.0.0" "$(next_ver v1.3.2 --major)"
ck "v0.12.0 越界进位" "v1.0.0" "$(next_ver v0.12.0)"
# 4) 规则只能有一份：发布校验脚本必须调用 check-version.sh，不许自带正则
if grep -Fq '^v[0-9]+\.[0-9]\.[0-9]$' "$ROOT/deploy/verify-release.sh"; then
  ck "verify-release.sh 没有私抄版本正则" "false" "true"
else
  ck "verify-release.sh 没有私抄版本正则" "false" "false"
fi
ck "verify-release.sh 调用 check-version.sh" "true" \
  "$(grep -Fq 'check-version.sh' "$ROOT/deploy/verify-release.sh" && printf 'true' || printf 'false')"

printf '\n%s：%d 项通过，%d 项失败\n' "$(basename "$0")" "$PASS" "$FAIL"
[[ "$FAIL" -eq 0 ]]
