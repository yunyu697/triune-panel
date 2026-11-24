#!/usr/bin/env bash
# 面板自更新「重启后不用重新登录」的真机回归。
#
# 与 deploy/tests/ 下那批桩测的区别：**它要起两个真进程、占两个端口、替换自己
# 那份临时二进制**，所以放在 deploy/ 根下（与 verify-mihomo.py / verify-orphan-ports.py
# 同属真机回归资产），不进 CI、也不进 preflight。改过下列任何一处之后手动跑一次：
#   internal/api/panelresume.go   (签发 / 换票)
#   internal/api/ops.go           (handleSelfUpdate 里签发的那一步)
#   cmd/server/main.go            (serve 的启动参数)
#
# 它证明的是单测和离线 e2e 都证不了的那一段：
#   exec 换掉进程映像之后，**新进程**还能从同一个 SQLite 里认出那张票，
#   换出一张能用的会话；而旧 token 确实已经失效。
#
# 用法：deploy/verify-selfupdate.sh [监听端口] [文件服务端口]
set -uo pipefail

SELF_DIR="$(cd -P "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT="$(cd -P "$SELF_DIR/.." && pwd -P)"
PORT="${1:-18123}"
HTTP_PORT="${2:-18124}"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/triune-selfupdate.XXXXXX")"
HTTP_PID=""
PANEL_PID=""

cleanup() {
  # 整段重定向：kill 掉 job 时 shell 会往 stderr 打一行 "Terminated: 15"，
  # 那是正常收尾，混在断言结果里会让人以为出错了。
  {
    [[ -n "$PANEL_PID" ]] && kill "$PANEL_PID"
    [[ -n "$HTTP_PID" ]] && kill "$HTTP_PID"
    wait
  } 2>/dev/null
  rm -rf "$WORK"
}
trap cleanup EXIT

PASS=0; FAIL=0
ck() {
  local name="$1" want="$2" got="$3"
  if [[ "$want" == "$got" ]]; then PASS=$((PASS+1)); printf 'PASS %s\n' "$name"
  else FAIL=$((FAIL+1)); printf 'FAIL %s  期望=[%s] 实际=[%s]\n' "$name" "$want" "$got"; fi
}
code() { curl -s --noproxy '*' -o /dev/null -w '%{http_code}' --max-time 3 "$@"; }
jget() { python3 -c 'import sys,json;print(json.load(sys.stdin).get("'"$1"'",""))' 2>/dev/null; }

cd "$ROOT" || exit 1
echo "→ 编译两个版本（当前 = 被测版本，另一个只改版本号）"
go build -ldflags "-X main.Version=v0.0.1-under-test" -o "$WORK/panel-a" ./cmd/server || exit 1
go build -ldflags "-X main.Version=v9.9.9-fake" -o "$WORK/panel-b" ./cmd/server || exit 1

echo "→ 起文件服务与面板（${PORT} / ${HTTP_PORT}）"
( cd "$WORK" && exec python3 -m http.server "$HTTP_PORT" --bind 127.0.0.1 >"$WORK/http.log" 2>&1 ) &
HTTP_PID=$!
"$WORK/panel-a" -addr "127.0.0.1:$PORT" -data "$WORK/data" >"$WORK/panel.log" 2>&1 &
PANEL_PID=$!

BASE="http://127.0.0.1:$PORT"
for _ in $(seq 1 60); do
  curl -s --noproxy '*' -o /dev/null --max-time 1 "$BASE/api/public/info" && break
  sleep 0.25
done

ck "启动版本" '{"version":"v0.0.1-under-test"}' \
  "$(curl -s --noproxy '*' --max-time 3 "$BASE/api/public/info")"

TOK="$(curl -s --noproxy '*' --max-time 5 -X POST "$BASE/api/login" \
  -H 'Content-Type: application/json' -d '{"user":"admin","pass":"admin"}' | jget token)"
[[ ${#TOK} -eq 32 ]] && ck "登录拿到 token" 32 32 || ck "登录拿到 token" 32 "${#TOK}"

RESP="$(curl -s --noproxy '*' --max-time 60 -X POST "$BASE/api/self-update" \
  -H 'Content-Type: application/json' -H "Authorization: Bearer $TOK" \
  -d "{\"url\":\"http://127.0.0.1:$HTTP_PORT/panel-b\"}")"
RESUME="$(printf '%s' "$RESP" | jget resume_token)"
ck "自更新响应带回接力凭证（64 位）" 64 "${#RESUME}"

echo "→ 等新进程（exec 之后版本号必须变）"
VER=""
for _ in $(seq 1 80); do
  sleep 0.5
  V="$(curl -s --noproxy '*' --max-time 2 "$BASE/api/public/info" 2>/dev/null)"
  if printf '%s' "$V" | grep -q 'v9\.9\.9-fake'; then VER="$V"; break; fi
done
ck "重启后跑的是新版本" '{"version":"v9.9.9-fake"}' "$VER"
ck "旧 token 已失效（重启确实清了会话表）" 401 "$(code -H "Authorization: Bearer $TOK" "$BASE/api/me")"

RR="$(curl -s --noproxy '*' --max-time 5 -X POST "$BASE/api/session/resume" \
  -H 'Content-Type: application/json' -d "{\"token\":\"$RESUME\"}")"
NEWTOK="$(printf '%s' "$RR" | jget token)"
ck "换到新会话" 200 "$(code -H "Authorization: Bearer $NEWTOK" "$BASE/api/me")"
ck "同一张票重放被拒" 401 "$(code -X POST "$BASE/api/session/resume" \
  -H 'Content-Type: application/json' -d "{\"token\":\"$RESUME\"}")"
ck "库里已无待恢复会话" 401 "$(code -X POST "$BASE/api/session/resume" \
  -H 'Content-Type: application/json' -d '{"token":"whatever"}')"

printf '\n%s passed, %s failed\n' "$PASS" "$FAIL"
[[ $FAIL -eq 0 ]]
