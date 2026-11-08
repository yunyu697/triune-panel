#!/usr/bin/env bash
# verify-release.sh 的完全离线回归：桩掉 curl 和 git，自己造一个假仓库 + 假 dist。
# 不联网、不耗 GitHub API 配额、不需要真实 tag 或发布产物，因此可以直接进 CI。
#
# 钉住五件事：
#   A 公开下载 504 → 5 次 API 回退，校验全绿
#   E 公开下载正常 → 一次都不回退
#   B 回退通道返回坏字节 → 5 项「字节不一致」（回退不等于放宽校验）
#   C 去掉回退的变异版 → 5 项「下载失败」（回滚修复必须转红）
#   D 匿名版本索引被改成带 token → 桩直接拒绝（面板更新检查必须匿名）
set -euo pipefail
SELF_DIR="$(cd -P "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT="$(cd -P "$SELF_DIR/../.." && pwd -P)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/triune-verify-offline.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
REPO="$TMP/repo"
mkdir -p "$REPO/deploy" "$REPO/dist" "$TMP/bin"
cp "$ROOT/deploy/verify-release.sh" "$ROOT/deploy/release.sh" "$ROOT/deploy/check-version.sh" "$REPO/deploy/"
export STUB_DIR="$TMP"
export STUB_SHA="1111111111111111111111111111111111111111"
# 这两个要 export：桩 curl 是被校验脚本拉起的子进程，普通赋值传不进去
export STUB_PUBLIC_OK=0 STUB_BAD=0
VER="v9.9.9"

# 造 4 份假二进制 + sha256sums.txt + 固定 API 响应，asset id 由脚本生成，
# 不硬编码任何真实 Release 的 id。
python3 - "$REPO" <<'PY'
import hashlib, json, os, re, sys
repo = sys.argv[1]
rel = open(os.path.join(repo, "deploy", "release.sh"), encoding="utf-8").read()
targets = re.search(r'^TARGETS="(.*)"', rel, re.M).group(1).split()
bins = ["triune-panel-%s-%s" % tuple(t.split("/")) for t in targets]
names = ["sha256sums.txt"] + bins
for n in bins:
    with open(os.path.join(repo, "dist", n), "w", encoding="utf-8") as f:
        f.write("FAKE-BINARY-" + n)
lines = []
for n in bins:
    p = os.path.join(repo, "dist", n)
    lines.append("%s  %s" % (hashlib.sha256(open(p, "rb").read()).hexdigest(), n))
open(os.path.join(repo, "dist", "sha256sums.txt"), "w", encoding="utf-8").write("\n".join(lines) + "\n")
assets = [{"name": n, "id": i, "state": "uploaded",
           "size": os.path.getsize(os.path.join(repo, "dist", n))}
          for i, n in enumerate(names, 1)]
json.dump({"draft": False, "prerelease": False, "assets": assets},
          open(os.path.join(os.path.dirname(repo), "meta.json"), "w", encoding="utf-8"))
json.dump([{"tag_name": "v9.9.9", "draft": False}],
          open(os.path.join(os.path.dirname(repo), "index.json"), "w", encoding="utf-8"))
json.dump({"tag_name": "v9.9.9", "draft": False, "prerelease": False},
          open(os.path.join(os.path.dirname(repo), "latest.json"), "w", encoding="utf-8"))
with open(os.path.join(os.path.dirname(repo), "idmap"), "w", encoding="utf-8") as f:
    for i, n in enumerate(names, 1):
        f.write("%d %s\n" % (i, n))
PY

cat > "$TMP/bin/curl" <<'STUB'
#!/usr/bin/env bash
out=""; kind=""; url=""; prev=""; has_auth=0
for a in "$@"; do
  [[ "$prev" == "-o" ]] && out="$a"
  prev="$a"
  case "$a" in
    Authorization:*) has_auth=1 ;;
    https://github.com/*/releases/download/*) kind="public"; url="$a" ;;
    */releases/assets/*) kind="asset"; url="$a" ;;
    */releases/tags/*) kind="meta" ;;
    *per_page=30*) kind="idx" ;;
    */releases/latest) kind="latest" ;;
  esac
done
emit() { if [[ -n "$out" ]]; then cat "$1" > "$out"; else cat "$1"; fi; }
# 匿名版本索引 / latest 绝不能带认证头：带了就说明有人把 get_anon 改成 get。
if [[ "$kind" == "idx" || "$kind" == "latest" ]] && [[ "$has_auth" -eq 1 ]]; then
  printf 'leak\n' >> "$STUB_DIR/auth_leak"
  printf 'stub: 匿名索引请求带了认证头\n' >&2
  exit 90
fi
case "$kind" in
  meta)   emit "$STUB_DIR/meta.json"; exit 0 ;;
  idx)    emit "$STUB_DIR/index.json"; exit 0 ;;
  latest) emit "$STUB_DIR/latest.json"; exit 0 ;;
  public)
    name="${url##*/}"
    if [[ "${STUB_PUBLIC_OK:-0}" == "1" ]]; then
      emit "$STUB_DIR/repo/dist/$name"; exit 0
    fi
    [[ -n "$out" ]] && printf '<html>504 Gateway Time-out</html>' > "$out"
    printf 'curl: (56) The requested URL returned error: 504\n' >&2
    exit 56
    ;;
  asset)
    id="${url##*/}"
    name="$(awk -v i="$id" '$1==i{print $2; exit}' "$STUB_DIR/idmap")"
    if [[ "${STUB_BAD:-0}" == "1" ]]; then
      if [[ -n "$out" ]]; then printf 'BROKEN-ASSET' > "$out"; else printf 'BROKEN-ASSET'; fi
      exit 0
    fi
    emit "$STUB_DIR/repo/dist/$name"
    exit 0
    ;;
esac
exit 1
STUB

cat > "$TMP/bin/git" <<'STUB'
#!/usr/bin/env bash
case "${1:-}" in
  remote) printf 'https://github.com/o/r.git\n' ;;
  ls-remote)
    printf '%s\trefs/tags/v9.9.9^{}\n%s\trefs/tags/v9.9.9\n' "$STUB_SHA" "$STUB_SHA"
    ;;
  status) : ;;
  rev-parse)
    case "$*" in *-q*) exit 0 ;; esac
    printf '%s\n' "$STUB_SHA"
    ;;
  *) exit 1 ;;
esac
STUB
cat > "$TMP/bin/gh" <<'STUB'
#!/usr/bin/env bash
# 桩：让校验器一定拿到 token。CI runner 没有 gh 登录时，变异版「索引带 token」
# 恰好也不带 token，守卫就测不出问题——回归不能依赖运行环境有没有凭证。
if [[ "${1:-}" == "auth" && "${2:-}" == "token" ]]; then
  printf 'stub-token-for-verify\n'
  exit 0
fi
exit 1
STUB
chmod +x "$TMP/bin/curl" "$TMP/bin/git" "$TMP/bin/gh"

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
run_case() { # <标签> <脚本> <期望失败数> <期望回退次数> [额外关键字] [期望出现次数]
  local label="$1" script="$2" want_fail="$3" want_fb="$4" extra="${5:-}" want_extra="${6:-0}"
  local out="$TMP/out.txt" got_fail got_fb got_extra
  PATH="$TMP/bin:$PATH" bash "$script" "$VER" > "$out" 2>&1 || true
  got_fail="$(sed -n -E 's/.*，([0-9]+) 项失败.*/\1/p' "$out" | tail -1)"
  got_fb="$(grep -c '已改走匿名 API 通道' "$out" || true)"
  ck "${label}：失败数" "$want_fail" "${got_fail:-<none>}"
  ck "${label}：回退次数" "$want_fb" "${got_fb:-0}"
  if [[ -n "$extra" ]]; then
    got_extra="$(grep -c "$extra" "$out" || true)"
    ck "${label}：${extra}" "$want_extra" "${got_extra:-0}"
  fi
}

# 两个变异版：禁用回退 / 让匿名索引带 token
python3 - "$REPO/deploy/verify-release.sh" "$REPO/deploy/mutant-no-fallback.sh" <<'PY'
import sys
s = open(sys.argv[1], encoding="utf-8").read()
old = 'get "$API/releases/assets/$asset_id"'
assert old in s
open(sys.argv[2], "w", encoding="utf-8").write(s.replace(old, "false"))
PY
python3 - "$REPO/deploy/verify-release.sh" "$REPO/deploy/mutant-idx-token.sh" <<'PY'
import sys
s = open(sys.argv[1], encoding="utf-8").read()
old = 'IDX="$(get_anon "$API/releases?per_page=30" 2>/dev/null)"'
assert old in s
open(sys.argv[2], "w", encoding="utf-8").write(s.replace(old, old.replace("get_anon", "get")))
PY

ORIG="$REPO/deploy/verify-release.sh"
STUB_PUBLIC_OK=0 STUB_BAD=0
run_case "A 504 回退全绿" "$ORIG" 0 5
STUB_PUBLIC_OK=1
run_case "E 主地址正常不回退" "$ORIG" 0 0
STUB_PUBLIC_OK=0 STUB_BAD=1
run_case "B 坏字节被拒" "$ORIG" 5 5 "字节不一致" 5
STUB_BAD=0
# FAIL 行带 ANSI 颜色码，别用 "FAIL .*" 去匹配；中文冒号只出现在那 5 行里
run_case "C 变异：禁用回退" "$REPO/deploy/mutant-no-fallback.sh" 5 0 "：下载失败" 5
rm -f "$TMP/auth_leak"
run_case "D 变异：索引带 token" "$REPO/deploy/mutant-idx-token.sh" 3 5 "" 
ck "D：匿名索引守卫确实拦截" "1" "$(if [[ -f "$TMP/auth_leak" ]]; then printf '1'; else printf '0'; fi)"

printf '\n%s：%d 项通过，%d 项失败\n' "$(basename "$0")" "$PASS" "$FAIL"
[[ "$FAIL" -eq 0 ]]
