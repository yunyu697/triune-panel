#!/usr/bin/env bash
# install.sh 下载链桩测（离线、不联网、不需要 root）。
#
#   deploy/tests/install-download-fallback.sh [install.sh 路径]
#
# macOS 跑不了 install.sh 全流程（它会照 uname -m 下 linux 二进制再拒绝执行），
# 所以按项目规矩只抽函数 + 桩 curl，钉住这条分支：
#   官方 releases/download 504 + 所有镜像失败 → 匿名 API asset 直连兜底，
#   仍然过 sha256；兜底返回坏字节 / 没有兜底地址时必须失败。
# 第一个参数可以是「变异版 install.sh」（去掉兜底），此时用例应当转红。
set -euo pipefail
SELF_DIR="$(cd -P "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT="$(cd -P "$SELF_DIR/../.." && pwd -P)"
INSTALL="${1:-$ROOT/deploy/install.sh}"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/triune-install-stub.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT

# 只抽这几个函数；函数体以顶格 } 结束，不执行 install.sh 的主流程。
python3 - "$INSTALL" "$TMP/funcs.sh" <<'PY'
import sys
src = open(sys.argv[1], encoding="utf-8").read().splitlines(keepends=True)
names = ["resolve_sha256", "resolve_asset_id", "file_sha256", "dl_verify_sha", "dl"]
out = []
for name in names:
    start = next(i for i, l in enumerate(src) if l.startswith(name + "()"))
    end = next(i for i in range(start, len(src)) if src[i].rstrip("\n") == "}")
    out.append("".join(src[start:end + 1]))
open(sys.argv[2], "w", encoding="utf-8").write("\n".join(out))
PY
# shellcheck disable=SC1090
source "$TMP/funcs.sh"

REPO="o/r"
# 下面两个变量只在抽出来的函数里用，shellcheck 看不到 source 的内容
# shellcheck disable=SC2034
MIRRORS=("https://mirror.invalid/")
# shellcheck disable=SC2034
GH_PROXY=""
BIN="triune-panel-linux-amd64"
STUB_BAD=0
STUB_PUBLIC_OK=0
API_CALLS=0
TAG_CALLS=0
printf 'REAL-BINARY-BYTES' > "$TMP/good.bin"
printf 'BROKEN-BYTES' > "$TMP/bad.bin"
SHA="$(file_sha256 "$TMP/good.bin")"
JSON="$(printf '{"assets": [{"url": "x", "id": 123456, "name": "%s", "digest": "sha256:%s"}]}' "$BIN" "$SHA")"
PUBLIC_URL="https://github.com/${REPO}/releases/download/v1.0.0/${BIN}"
API_URL="https://api.github.com/repos/${REPO}/releases/assets/123456"

# 桩 curl：解析 -o 与 URL，按场景返回固定内容。
curl() {
  local out="" url="" prev=""
  for a in "$@"; do
    [[ "$prev" == "-o" ]] && out="$a"
    prev="$a"
    case "$a" in http*) url="$a" ;; esac
  done
  case "$url" in
    */releases/tags/*)
      TAG_CALLS=$((TAG_CALLS + 1))
      printf '%s' "$JSON"
      return 0
      ;;
    */releases/assets/*)
      API_CALLS=$((API_CALLS + 1))
      local src="$TMP/good.bin"
      if [[ "$STUB_BAD" == "1" ]]; then src="$TMP/bad.bin"; fi
      if [[ -n "$out" ]]; then cp "$src" "$out"; else cat "$src"; fi
      return 0
      ;;
    *sha256sums.txt)
      printf '%s  %s\n' "$SHA" "$BIN"
      return 0
      ;;
    */releases/download/*)
      if [[ "$STUB_PUBLIC_OK" == "1" ]]; then
        if [[ -n "$out" ]]; then cp "$TMP/good.bin" "$out"; else cat "$TMP/good.bin"; fi
        return 0
      fi
      [[ -n "$out" ]] && printf '<html>504 Gateway Time-out</html>' > "$out"
      return 56
      ;;
  esac
  return 1
}
# 探测全部失败 → dl 回退到静态候选（直连 + 那个假镜像），两者都会被桩成 504。
rank_sources() { :; }

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
# 命令输出一律丢掉，只留退出码；dl 必须在当前 shell 执行，
# 否则 $(...) 子 shell 里的 API_CALLS 计数传不回来。
run() { if "$@" >/dev/null 2>&1; then printf '0'; else printf '1'; fi; }
RC=0
run_dl() { if dl "$@" >/dev/null 2>&1; then RC=0; else RC=1; fi; }

# 1) 一次 JSON 同时解析出 digest 与 asset id；缺 id 时不能瞎兜底。
ck "从 Release JSON 取 sha256" "$SHA" "$(resolve_sha256 v1.0.0 "$BIN" "$JSON")"
ck "从同一份 JSON 取 asset id" "123456" "$(resolve_asset_id "$BIN" "$JSON")"
ck "无 id 时 resolve_asset_id 失败" "1" "$(run resolve_asset_id "$BIN" '{"assets": []}')"
# 2) 调用方传了第三参数（哪怕是空串）就不该再打一次 tags API。
ck "空元数据走 sha256sums 退路" "$SHA" "$(resolve_sha256 v1.0.0 "$BIN" "")"
ck "没有重复请求 tags API" "0" "$TAG_CALLS"
ck "dl_verify_sha 正确字节通过" "0" "$(run dl_verify_sha "$TMP/good.bin" "$SHA")"
ck "dl_verify_sha 坏字节拒绝" "1" "$(run dl_verify_sha "$TMP/bad.bin" "$SHA")"

# 3) 公开 URL 504 + 镜像全挂 → API 兜底成功，内容逐字节一致。
API_CALLS=0
run_dl "$TMP/out1" "$SHA" "$PUBLIC_URL" "$API_URL"
ck "兜底下载成功" "0" "$RC"
ck "兜底内容正确" "$(cat "$TMP/good.bin")" "$(cat "$TMP/out1" 2>/dev/null || true)"
ck "确实调用了 API 通道" "1" "$API_CALLS"
# 4) 兜底返回坏字节：sha256 必须挡住，不能因为换了通道就放行。
STUB_BAD=1
run_dl "$TMP/out2" "$SHA" "$PUBLIC_URL" "$API_URL"
ck "兜底坏字节被 sha256 拒绝" "1" "$RC"
STUB_BAD=0
# 5) 没有兜底地址时，公开 URL 挂了就是失败。
API_CALLS=0
run_dl "$TMP/out3" "$SHA" "$PUBLIC_URL" ""
ck "无兜底地址时下载失败" "1" "$RC"
ck "没兜底时不调用 API" "0" "$API_CALLS"
# 6) 公开 URL 本来就好：不该无谓地走 API（多一次匿名请求）。
STUB_PUBLIC_OK=1
API_CALLS=0
run_dl "$TMP/out4" "$SHA" "$PUBLIC_URL" "$API_URL"
ck "主地址可用时下载成功" "0" "$RC"
ck "主地址可用时不触发兜底" "0" "$API_CALLS"

printf '\n%s：%d 项通过，%d 项失败\n' "$(basename "$0")" "$PASS" "$FAIL"
[[ "$FAIL" -eq 0 ]]
