#!/usr/bin/env bash
# ============================================================
# Triune Panel 一键安装 / 升级脚本
#
# 在线一键安装（VPS 上执行）:
#   curl -fsSL https://raw.githubusercontent.com/yunyu697/triune-panel/main/deploy/install.sh | sudo bash
#
# 忘记 sudo 也能装（脚本会自动用 sudo 执行特权步骤）:
#   curl -fsSL https://raw.githubusercontent.com/yunyu697/triune-panel/main/deploy/install.sh | bash
#
# 指定架构（默认自动检测）:
#   curl -fsSL .../install.sh | sudo bash -s -- arm64
#
# 安装到自定义目录（无需 root，写到用户目录）:
#   TRIUNE_INSTALL_DIR=~/.local/triune-panel bash install.sh
#
# 指定版本（默认自动取最新）:
#   curl -fsSL .../install.sh | bash -s -- amd64 v0.8.0
#
# 自定义面板监听端口（默认 10010）:
#   curl -fsSL .../install.sh | sudo TRIUNE_PANEL_PORT=10010 bash
#
# 安装时自动放行防火墙端口（ufw / firewalld / iptables 有则自动配，
# 云服务器还需在厂商控制台安全组放行同一端口）。
#
# 下载源 = GitHub 原始地址 + 多个加速镜像，写在同一个候选池里；
# 运行时并行探测全部候选，自动选择最快的源下载，失败自动切换次优。
# 也可手动指定镜像跳过探测:  curl -fsSL .../install.sh | sudo GH_PROXY=https://ghfast.top bash
#
# ★ 防"装到旧版本"三件套（重要）:
#   1) 先向 GitHub 解析出真实的最新 tag，再按 tag 拼固定版本 URL 下载
#      —— 不用 releases/latest，避免镜像/代理缓存了旧的 latest 指向。
#   2) 下载后比对 sha256（与 GitHub API / sha256sums.txt 一致），不匹配即换源重下。
#   3) 校验二进制自报版本必须等于目标 tag，且比已安装版本更新，否则中止。
#      —— 即使某个镜像返回 HTTP 200 但内容是旧的/截断的，也装不上。
#
# 离线模式：脚本同目录存在 triune-panel-linux-<arch> 时直接使用，不联网。
# 升级安全：已有安装时只替换面板二进制，数据目录 /opt/triune-panel/data 原样保留。
# 卸载：安装完成后卸载脚本落地在 /opt/triune-panel/uninstall.sh，直接本地执行，无需联网。
# ============================================================
set -euo pipefail

REPO="yunyu697/triune-panel"
INSTALL_DIR="${TRIUNE_INSTALL_DIR:-/opt/triune-panel}"
SYSTEMD_DIR="${TRIUNE_SYSTEMD_DIR:-/etc/systemd/system}"
SERVICE=triune-panel.service
PANEL_PORT="${TRIUNE_PANEL_PORT:-10010}"   # 面板监听端口，可用环境变量覆盖
GH_PROXY="${GH_PROXY:-}"
RAW_BASE="https://raw.githubusercontent.com/$REPO/main"

# ---- 权限处理：特权步骤单独用 sudo 执行，脚本主体可在普通用户下运行 ----
# 背景：常见踩坑是「目录已存在但属主是 root，普通用户重装时
#   install: cannot create regular file '/opt/triune-panel/triune-panel': Permission denied」。
#   这里不让脚本整体要求 root，而是把写 /opt、写 systemd、发指令这些动作
#   逐条交给 sudo；已经以 root 运行时则直接执行，不多套一层。
if [[ "$(id -u)" -eq 0 ]]; then
  SUDO=""
elif command -v sudo >/dev/null 2>&1; then
  SUDO="sudo"
else
  SUDO=""
fi

# 需要 root 才能完成的操作；无 sudo 且非 root 时给出可执行的指引
need_root() {
  if [[ -n "$SUDO" || "$(id -u)" -eq 0 ]]; then
    return 0
  fi
  echo ""
  echo "✘ 该操作需要 root 权限（当前用户: $(id -un)，且系统无 sudo 命令）"
  echo "  可选方案："
  echo "    1) 用 root 执行本脚本:  curl -fsSL $RAW_BASE/deploy/install.sh | sudo bash"
  echo "    2) 安装到用户目录（无需 root）:  TRIUNE_INSTALL_DIR=\$HOME/triune-panel bash install.sh"
  echo ""
  exit 1
}

# 候选镜像池（与 GitHub 原始地址同池竞争，运行时按实测速度自动择优）
MIRRORS=(
  "https://ghfast.top/"
  "https://gh-proxy.com/"
  "https://gh-proxy.org/"
  "https://ghproxy.cn/"
  "https://gh-proxy.net/"
  "https://ghproxy.net/"
  "https://gh.ddlc.top/"
  "https://ghproxy.link/"
)

# ---- 架构检测 ----
ARCH="${1:-}"
WANT_TAG="${2:-}"
if [[ -z "$ARCH" ]]; then
  MACHINE="$(uname -m 2>/dev/null || true)"
  if [[ -z "$MACHINE" ]]; then
    echo "✘ 无法检测系统架构（uname 命令不可用）"
    echo "  请手动指定架构:  bash install.sh amd64   或   bash install.sh arm64"
    exit 1
  fi
  case "$MACHINE" in
    x86_64|amd64)  ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *) echo "✘ 不支持的架构: $MACHINE，可用参数指定 amd64/arm64"; exit 1 ;;
  esac
fi
case "$ARCH" in amd64|arm64) ;; *) echo "✘ 架构参数只能是 amd64 或 arm64"; exit 1 ;; esac

BIN_NAME="triune-panel-linux-${ARCH}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd || pwd)"

# ============================================================
# 版本解析：先确定"要装哪个版本"，再按 tag 拼固定 URL。
#
# 为什么不用 releases/latest：
#   releases/latest 是一条 302，很多加速镜像/CDN 会缓存这条重定向，
#   缓存期内即使仓库已经发了新 Release，镜像仍会把你导向旧 tag，
#   于是"装到的还是旧版本"。所以这里先解析出真实 tag，
#   再拼 releases/download/<tag>/<file> 这种不可变 URL。
# ============================================================
resolve_latest_tag() {
  # 1) GitHub API（最权威，带 tag_name）
  local tag
  tag=$(curl -fsSL --connect-timeout 8 --max-time 20 \
        -H "Accept: application/vnd.github+json" \
        "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null \
        | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/') || true
  if [[ -n "$tag" ]]; then printf '%s' "$tag"; return 0; fi

  # 2) releases/latest 的 302 Location（API 不可用时的退路）
  tag=$(curl -fsSI --connect-timeout 8 --max-time 20 \
        "https://github.com/$REPO/releases/latest" 2>/dev/null \
        | tr -d '\r' | grep -i '^location:' | sed -E 's#.*/releases/tag/([^/[:space:]]+).*#\1#' | head -1) || true
  if [[ -n "$tag" ]]; then printf '%s' "$tag"; return 0; fi

  return 1
}

# 读取目标版本对应资产的 sha256（优先 API，其次 sha256sums.txt 资产）
resolve_sha256() { # resolve_sha256 <tag> <bin_name>
  local tag="$1" bin="$2" sha=""
  sha=$(curl -fsSL --connect-timeout 8 --max-time 25 \
        -H "Accept: application/vnd.github+json" \
        "https://api.github.com/repos/$REPO/releases/tags/$tag" 2>/dev/null \
        | tr '{' '\n' | grep -F "\"name\": \"$bin\"" \
        | grep -m1 '"digest"' | sed -E 's/.*"sha256:([0-9a-f]{64})".*/\1/') || true
  if [[ -n "$sha" ]]; then printf '%s' "$sha"; return 0; fi

  # 退路：Release 附带的 sha256sums.txt
  local sums
  sums=$(curl -fsSL --connect-timeout 8 --max-time 20 \
         "https://github.com/$REPO/releases/download/$tag/sha256sums.txt" 2>/dev/null) || true
  sha=$(printf '%s' "$sums" | awk -v b="$bin" '$2==b{print $1; exit}')
  if [[ -n "$sha" ]]; then printf '%s' "$sha"; return 0; fi

  return 1
}

# 计算本地文件 sha256（兼容 sha256sum / shasum / openssl）
file_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" 2>/dev/null | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" 2>/dev/null | awk '{print $1}'
  elif command -v openssl >/dev/null 2>&1; then
    openssl dgst -sha256 "$1" 2>/dev/null | awk '{print $NF}'
  else
    return 1
  fi
}

# ---- 源探测：并行测试 原始地址 + 各镜像，按响应速度排出可用源顺序 ----
# 探测用 Range 请求只取前 1KB，既快又能验证"该源真能吐出二进制"：
#   - 必须返回 206/200 且 content-type 不是 html（html = 反爬/挑战页假响应）
#   - 实测 ghproxy.cn / ghproxy.link 会返回 200 + HTML 挑战页，必须剔除
rank_sources() { # rank_sources <探测URL>
  local probe="$1"
  local tmp; tmp=$(mktemp -d)
  local bases=("" "${MIRRORS[@]}")
  local i=0
  for b in "${bases[@]}"; do
    (
      local r
      r=$(curl -fsL -r 0-1023 -o /dev/null --connect-timeout 5 --max-time 10 \
            -w '%{time_total}|%{http_code}|%{content_type}' "${b}${probe}" 2>/dev/null || echo "999|000|")
      local t="${r%%|*}"; local rest="${r#*|}"
      local code="${rest%%|*}"; local ct="${rest#*|}"
      # 只认 200/206 + 非 HTML + 有实际字节
      if [[ ("$code" == "200" || "$code" == "206") && "$ct" != *html* && "$t" != "999" ]]; then
        printf '%s\t%s\n' "$t" "$b" > "$tmp/$i"
      fi
    ) &
    i=$((i+1))
  done
  wait
  # 输出按耗时升序的 base 列表（空串=直连）
  sort -n "$tmp"/* 2>/dev/null | cut -f2 || true
  rm -rf "$tmp"
}

# ---- 下载：按择优顺序尝试；任一源失败（下载失败/校验不过）自动换下一个 ----
# 用法: dl <输出文件> <期望sha256> <URL>
dl() { # dl <输出文件> <期望sha256> <完整URL>
  local out="$1" want_sha="$2" url="$3"
  local base ordered=()
  if [[ -n "$GH_PROXY" ]]; then
    ordered=("${GH_PROXY%/}/")
  fi
  # 自动探测择优（GH_PROXY 已在首位，探测结果紧随其后兜底；空行=直连）
  while IFS= read -r b; do
    ordered+=("$b")
  done < <(rank_sources "$url")
  if [[ ${#ordered[@]} -eq 0 ]]; then
    # 探测全部失败（可能断网/仓库不可见）——退回静态顺序兜底
    ordered=("" "${MIRRORS[@]}")
  fi
  for base in "${ordered[@]}"; do
    local label="${base:-GitHub 直连}"
    echo "→ 下载 ${label}${url}"
    rm -f "$out"
    # --speed-time/--speed-limit: 15 秒内速度低于 10KB/s 判定失败，切换下一源
    if ! curl -fL --connect-timeout 8 --retry 1 \
         --speed-time 15 --speed-limit 10240 \
         -o "$out" "${base}${url}" 2>/dev/null; then
      echo "  ✗ 下载失败，尝试下一个源"
      continue
    fi
    # ---- 完整性校验：sha256 不符说明拿到的是损坏/旧内容，必须换源 ----
    if [[ -n "$want_sha" ]]; then
      local got; got=$(file_sha256 "$out" || true)
      if [[ -z "$got" ]]; then
        echo "  ✗ 无法计算 sha256（缺少 sha256sum/shasum/openssl），跳过校验"
      elif [[ "$got" != "$want_sha" ]]; then
        echo "  ✗ sha256 不匹配（期望 ${want_sha:0:16}… 实得 ${got:0:16}…）—— 该源返回的不是当前版本，换下一个源"
        continue
      else
        echo "  ✓ sha256 校验通过"
      fi
    fi
    return 0
  done
  return 1
}

# ---- 获取二进制 ----
TMP_BIN=$(mktemp)
if [[ -f "$SCRIPT_DIR/$BIN_NAME" ]]; then
  echo "→ 离线模式：使用本地 $SCRIPT_DIR/$BIN_NAME"
  cp "$SCRIPT_DIR/$BIN_NAME" "$TMP_BIN"
else
  echo "→ 检测架构: $ARCH"

  # ---- 步骤1：确定目标版本 ----
  if [[ -n "$WANT_TAG" ]]; then
    TAG="$WANT_TAG"
    echo "→ 指定版本: $TAG"
  else
    echo "→ 正在解析最新版本..."
    TAG=$(resolve_latest_tag || true)
    if [[ -z "$TAG" ]]; then
      echo ""
      echo "✘ 无法解析最新版本（GitHub API 与 releases/latest 均不可达）"
      echo "  可能原因：网络受限、仓库为私有、或代理未放行 api.github.com"
      echo "  解决办法："
      echo "    · 指定版本重试:  .../install.sh | bash -s -- $ARCH v0.8.0"
      echo "    · 手动指定镜像:  .../install.sh | bash -s -- $ARCH   （配合 GH_PROXY=）"
      echo ""
      rm -f "$TMP_BIN"
      exit 1
    fi
    echo "→ 最新版本: $TAG"
  fi

  # ---- 步骤2：取期望 sha256 ----
  echo "→ 正在获取 $TAG 的校验和..."
  WANT_SHA=$(resolve_sha256 "$TAG" "$BIN_NAME" || true)
  if [[ -n "$WANT_SHA" ]]; then
    echo "  sha256: ${WANT_SHA:0:16}…"
  else
    echo "  (未获取到 sha256，将只做版本号校验)"
  fi

  # ---- 步骤3：按固定 tag URL 下载（不用 latest，避免镜像缓存旧版本）----
  ASSET_URL="https://github.com/$REPO/releases/download/$TAG/$BIN_NAME"
  echo "→ 正在探测下载源（GitHub 原始地址 + ${#MIRRORS[@]} 个镜像，自动择优）..."
  dl "$TMP_BIN" "$WANT_SHA" "$ASSET_URL" \
     || { echo "✘ 全部下载源均失败或校验不通过";
          echo "  请检查网络；若仓库为私有，匿名无法拉取，请将仓库设为 Public 或改用离线安装";
          rm -f "$TMP_BIN"; exit 1; }
fi
chmod +x "$TMP_BIN"

# ---- 版本确认（同时校验二进制可执行，架构不匹配/文件损坏时立即中止）----
if ! BIN_VER_OUT=$("$TMP_BIN" -version 2>/dev/null); then
  echo ""
  echo "✘ 下载的二进制无法执行，可能架构不匹配或文件已损坏"
  echo "  目标架构: $ARCH  （uname -m = $(uname -m)）"
  echo "  文件信息: $(file "$TMP_BIN" 2>/dev/null || ls -la "$TMP_BIN")"
  echo "  请手动指定架构参数重试:  .../install.sh | bash -s -- amd64   或   .../install.sh | bash -s -- arm64"
  echo ""
  rm -f "$TMP_BIN"
  exit 1
fi
# 输出形如: triune-panel v0.8.0 (linux/arm64)
GOT_VER=$(printf '%s' "$BIN_VER_OUT" | awk '{print $2}')
echo "→ 下载完成: $BIN_VER_OUT"
# ---- 版本必须等于目标 tag —— 挡住"镜像返回旧版本"的最后一道闸 ----
if [[ -n "${TAG:-}" && -n "$GOT_VER" && "$GOT_VER" != "$TAG" ]]; then
  echo ""
  echo "✘ 版本校验失败：期望 $TAG，实际 $GOT_VER"
  echo "  该源返回的是旧版本（可能是镜像缓存未更新），请重试或指定其它镜像："
  echo "    GH_PROXY=https://ghfast.top bash install.sh $ARCH $TAG"
  echo ""
  rm -f "$TMP_BIN"
  exit 1
fi
# ---- 不允许降级：目标版本必须不低于已安装版本 ----
OLD_VER=""
if [[ -x "$INSTALL_DIR/triune-panel" ]]; then
  OLD_VER=$("$INSTALL_DIR/triune-panel" -version 2>/dev/null | awk '{print $2}' || true)
fi
if [[ -n "$OLD_VER" && -n "$GOT_VER" && "$GOT_VER" != "$OLD_VER" ]]; then
  # 仅做字符串比较：vX.Y.Z 数字段逐段比大小
  ver_lt() {
    local a="${1#v}" b="${2#v}"
    local IFS=.
    local -a A=($a) B=($b)
    for i in 0 1 2; do
      local x="${A[$i]:-0}" y="${B[$i]:-0}"
      x=$((10#${x:-0})); y=$((10#${y:-0}))
      [[ "$x" -lt "$y" ]] && return 0
      [[ "$x" -gt "$y" ]] && return 1
    done
    return 1
  }
  if ver_lt "$GOT_VER" "$OLD_VER"; then
    echo ""
    echo "✘ 拒绝降级：已安装版本 $OLD_VER 高于本次获取到的 $GOT_VER"
    echo "  如确认需要降级，请手动指定版本:  .../install.sh | bash -s -- $ARCH $GOT_VER"
    echo ""
    rm -f "$TMP_BIN"
    exit 1
  fi
fi

# ---- 停旧服务（升级场景）----
if systemctl list-unit-files 2>/dev/null | grep -q "^$SERVICE"; then
  echo "→ 检测到已有安装，停止旧服务（数据目录保留）"
  $SUDO systemctl stop "$SERVICE" 2>/dev/null || true
fi

# ---- 安装目录准备：不存在则创建，存在但不可写则修正属主 ----
if [[ ! -d "$INSTALL_DIR" ]]; then
  need_root
  echo "→ 创建安装目录 $INSTALL_DIR"
  $SUDO mkdir -p "$INSTALL_DIR"
elif [[ ! -w "$INSTALL_DIR" ]]; then
  # 目录已存在但当前用户写不进去（多半是之前用 root 装的）
  if [[ -n "$SUDO" ]]; then
    echo "→ 安装目录 $INSTALL_DIR 已存在且当前用户无写权限，使用 sudo 修正"
    $SUDO mkdir -p "$INSTALL_DIR"
  else
    echo ""
    echo "✘ 安装目录 $INSTALL_DIR 已存在，但当前用户 $(id -un) 无写权限"
    echo "  目录信息: $(ls -ld "$INSTALL_DIR" 2>/dev/null | awk '{print $1, $3":"$4}')"
    echo "  请用 root 安装:  curl -fsSL $RAW_BASE/deploy/install.sh | sudo bash"
    echo "  或装到用户目录:  TRIUNE_INSTALL_DIR=\$HOME/triune-panel bash install.sh"
    echo ""
    exit 1
  fi
fi

# ---- 安装二进制 ----
if ! $SUDO install -m 755 "$TMP_BIN" "$INSTALL_DIR/triune-panel"; then
  echo ""
  echo "✘ 安装二进制失败：无法写入 $INSTALL_DIR/triune-panel"
  echo "  诊断信息："
  echo "    当前用户: $(id -un) (uid=$(id -u))"
  echo "    目录权限: $(ls -ld "$INSTALL_DIR" 2>/dev/null | awk '{print $1, $3":"$4}' || echo '目录不存在')"
  echo "    磁盘空间: $(df -h "$INSTALL_DIR" 2>/dev/null | awk 'NR==2{print $4" 可用 / "$2" 总计"}' || echo '未知')"
  echo "  常见原因与处理："
  echo "    · 目录属主为 root → 用 sudo 安装:  curl -fsSL $RAW_BASE/deploy/install.sh | sudo bash"
  echo "    · 磁盘空间不足 → 清理后重试:  df -h / && du -sh $INSTALL_DIR"
  echo "    · 文件系统只读 → 检查挂载:  mount | grep ' / '"
  echo ""
  exit 1
fi
rm -f "$TMP_BIN"

need_root
# systemd 单元目录通常已存在，但自定义 SYSTEMD_DIR 时可能缺失 —— 先确保存在，
# 否则 tee 写不进去会因 set -e 静默退出，用户看不到原因。
if [[ ! -d "$SYSTEMD_DIR" ]]; then
  echo "→ 创建 systemd 单元目录 $SYSTEMD_DIR"
  $SUDO mkdir -p "$SYSTEMD_DIR"
fi
# systemd ExecStart 的路径必须跟随实际安装目录；路径含空格时需双引号包裹
exec_prefix() {
  case "$1" in
    *[[:space:]]*) printf '"%s"' "$1" ;;
    *) printf '%s' "$1" ;;
  esac
}
UNIT_TMP=$(mktemp)
cat > "$UNIT_TMP" <<'UNIT'
[Unit]
Description=Triune Panel (mihomo/sing-box/xray 管理面板)
After=network.target
Wants=network.target

[Service]
Type=simple
ExecStart=@EXEC@ -addr :@PORT@ -data @DATA@
Restart=on-failure
RestartSec=3
User=root
LimitNOFILE=65535
NoNewPrivileges=false
PrivateTmp=false

[Install]
WantedBy=multi-user.target
UNIT
sed -i.bak \
  -e "s|@EXEC@|$(exec_prefix "$INSTALL_DIR/triune-panel")|g" \
  -e "s|@DATA@|$(exec_prefix "$INSTALL_DIR/data")|g" \
  -e "s|@PORT@|$PANEL_PORT|g" "$UNIT_TMP" && rm -f "$UNIT_TMP.bak"
if ! $SUDO tee "${SYSTEMD_DIR%/}/$SERVICE" >/dev/null < "$UNIT_TMP"; then
  echo ""
  echo "✘ 写入 systemd 单元失败: ${SYSTEMD_DIR%/}/$SERVICE"
  echo "  目录信息: $(ls -ld "$SYSTEMD_DIR" 2>/dev/null | awk '{print $1, $3":"$4}' || echo '目录不存在')"
  echo "  请检查该目录是否存在且可写，或用 TRIUNE_SYSTEMD_DIR 指定其他目录"
  echo ""
  rm -f "$UNIT_TMP"
  exit 1
fi
rm -f "$UNIT_TMP"

# ---- 启停服务（无 systemd 的环境跳过，给出手动启动指引）----
if command -v systemctl >/dev/null 2>&1; then
  $SUDO systemctl daemon-reload
  $SUDO systemctl enable --now "$SERVICE"
  SERVICE_STARTED=1
else
  SERVICE_STARTED=0
  echo "→ 未检测到 systemd，跳过服务注册（当前环境可能使用 openrc 等其他 init）"
fi

# ---- 卸载脚本落地：卸载从此不依赖网络 ----
$SUDO tee "$INSTALL_DIR/uninstall.sh" >/dev/null <<'UNINSTALL'
#!/usr/bin/env bash
# Triune Panel 卸载脚本（安装时自动落地，本地执行，无需联网）
#   保留数据:   sudo bash /opt/triune-panel/uninstall.sh
#   彻底删除:   sudo bash /opt/triune-panel/uninstall.sh --purge
set -euo pipefail

INSTALL_DIR="${TRIUNE_INSTALL_DIR:-/opt/triune-panel}"
SERVICE=triune-panel.service
PURGE=0
[[ "${1:-}" == "--purge" ]] && PURGE=1

if [[ "$(id -u)" -ne 0 ]]; then
  if command -v sudo >/dev/null 2>&1; then
    SUDO="sudo"
  else
    echo "✘ 卸载需要 root 权限（当前用户 $(id -un)）"
    echo "  请改用: sudo bash $INSTALL_DIR/uninstall.sh"
    exit 1
  fi
else
  SUDO=""
fi

echo "=== Triune Panel 卸载 ==="

# ---- 1. 停止并禁用服务（面板退出时会一并停止其托管的内核进程）----
if systemctl list-unit-files 2>/dev/null | grep -q "^$SERVICE"; then
  echo "→ 停止服务 $SERVICE（含其托管的 sing-box / xray / mihomo 内核进程）"
  $SUDO systemctl disable --now "$SERVICE" 2>/dev/null || $SUDO systemctl stop "$SERVICE" 2>/dev/null || true
  sleep 1
else
  echo "→ 未检测到 systemd 服务（可能已卸载）"
fi

# 兜底：确认没有残留的面板进程
if pgrep -f "$INSTALL_DIR/triune-panel" >/dev/null 2>&1; then
  echo "→ 结束残留面板进程"
  $SUDO pkill -f "$INSTALL_DIR/triune-panel" 2>/dev/null || true
  sleep 0.5
fi

# ---- 2. 删除服务单元与二进制 ----
if [[ -f "/etc/systemd/system/$SERVICE" ]]; then
  $SUDO rm -f "/etc/systemd/system/$SERVICE"
  $SUDO systemctl daemon-reload
  echo "→ 已删除服务单元 /etc/systemd/system/$SERVICE"
fi
if [[ -f "$INSTALL_DIR/triune-panel" ]]; then
  $SUDO rm -f "$INSTALL_DIR/triune-panel"
  echo "→ 已删除二进制 $INSTALL_DIR/triune-panel"
fi

# ---- 3. 数据目录处理 ----
DATA_DIR="$INSTALL_DIR/data"
if [[ -d "$DATA_DIR" ]]; then
  if [[ $PURGE -eq 1 ]]; then
    echo "→ --purge 模式：删除数据目录 $DATA_DIR（含数据库/证书/内核副本/备份）"
    $SUDO rm -rf "$DATA_DIR"
  elif [[ -t 0 ]]; then
    printf "是否同时删除数据目录 %s（数据库、证书、自动备份等，删除后不可恢复）？[y/N] " "$DATA_DIR"
    read -r ans
    if [[ "$ans" == "y" || "$ans" == "Y" ]]; then
      $SUDO rm -rf "$DATA_DIR"
      echo "→ 数据目录已删除"
    else
      echo "→ 数据目录已保留: $DATA_DIR（重装脚本会自动复用）"
    fi
  else
    echo "→ 数据目录已保留: $DATA_DIR（默认保留，重装可恢复全部数据）"
    echo "  确认不再需要可手动执行: sudo rm -rf $DATA_DIR"
  fi
fi

# ---- 4. 清理脚本自身与空目录 ----
$SUDO rm -f "$INSTALL_DIR/uninstall.sh"
$SUDO rmdir "$INSTALL_DIR" 2>/dev/null || true

echo ""
echo "✔ Triune Panel 卸载完成"
UNINSTALL
$SUDO chmod 755 "$INSTALL_DIR/uninstall.sh"

# ---- 健康检查 ----
sleep 1.5

# 访问地址探测：
#   1) 先取本机默认路由源 IP（不受系统代理干扰）——云服务器上这通常是内网 IP；
#   2) 源 IP 是内网/回环地址（或取不到）时，再探测公网出口 IP。
# 公网探测必须全部加 --noproxy '*'：否则系统里配置的代理（http_proxy 等）
# 会让"公网 IP"显示成代理节点的 IP，而不是本机真实 IP——这是此前版本的 bug。
# 探测源覆盖国际与国内可达服务，任一成功即返回；全部失败则输出空串，
# 由调用方给出自查指引，绝不显示代理 IP 或错误 IP。
detect_ip() {
  local ip=""
  ip=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++)if($i=="src"){print $(i+1)}}' | head -1) || true
  [[ -z "$ip" ]] && ip=$(ip route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++)if($i=="src"){print $(i+1)}}' | head -1) || true
  [[ -z "$ip" ]] && ip=$(hostname -I 2>/dev/null | awk '{print $1}') || true
  case "$ip" in
    ""|10.*|192.168.*|172.1[6-9].*|172.2[0-9].*|172.3[0-1].*|127.*)
      local pub c
      for c in "https://api.ipify.org" "https://ifconfig.me/ip" "https://4.ipw.cn" \
               "https://ip.3322.net" "https://api-ipv4.ip.sb/ip" "https://ipv4.icanhazip.com"; do
        pub=$(curl --noproxy '*' -4 -s --connect-timeout 2 --max-time 4 "$c" 2>/dev/null | tr -d '[:space:]') || true
        # 只接受形如 IPv4 的返回，防止代理/镜像返回 HTML 或代理自身 IP 以外的垃圾内容
        case "$pub" in
          [1-9]*.[0-9]*.[0-9]*.[0-9]*)
            case "$pub" in
              10.*|192.168.*|172.1[6-9].*|172.2[0-9].*|172.3[0-1].*|127.*|100.6[4-9].*|100.1[0-1][0-9].*|100.12[0-7].*)
                continue ;;  # 返回的还是内网/CGNAT 地址，换下一个源
              *) printf '%s' "$pub"; return 0 ;;
            esac ;;
        esac
      done
      printf '%s' "$ip"  # 公网探测全部失败：回退显示内网地址，由调用方提示
      ;;
    *) printf '%s' "$ip"; return 0 ;;
  esac
}

# is_private_ip 判断给定地址是否内网/回环。
is_private_ip() {
  case "$1" in
    ""|10.*|192.168.*|172.1[6-9].*|172.2[0-9].*|172.3[0-1].*|127.*) return 0 ;;
    *) return 1 ;;
  esac
}

# ensure_port_open 安装时检查面板端口是否已放行，未放行则自动放行。
# 只在检测到已启用的防火墙时才动配置：
#   - ufw / firewalld：查询状态 → 未放行则 add；
#   - iptables：仅当 INPUT 链默认策略为 DROP/REJECT 时才插入放行规则
#     （避免在无防火墙的机器上无谓改动）；
#   - 都没有：跳过并提示云服务器安全组需要放行（安全组无法在机器内修改）。
ensure_port_open() {
  local port="$1"
  echo "→ 检查防火墙对端口 $port/tcp 的放行状态"
  # 1) ufw
  if command -v ufw >/dev/null 2>&1; then
    if $SUDO ufw status 2>/dev/null | grep -qi "status: active"; then
      if $SUDO ufw status 2>/dev/null | grep -q "$port/tcp"; then
        echo "  ✔ ufw 已放行 $port/tcp"
      else
        if $SUDO ufw allow "$port/tcp" >/dev/null 2>&1; then
          echo "  ✔ 已在 ufw 放行 $port/tcp"
        else
          echo "  ⚠ ufw 放行失败，请手动执行: sudo ufw allow $port/tcp"
        fi
      fi
    else
      echo "  · ufw 未启用，跳过"
    fi
    return 0
  fi
  # 2) firewalld
  if command -v firewall-cmd >/dev/null 2>&1 && $SUDO firewall-cmd --state >/dev/null 2>&1; then
    if $SUDO firewall-cmd --query-port="$port/tcp" 2>/dev/null | grep -q yes; then
      echo "  ✔ firewalld 已放行 $port/tcp"
    else
      if $SUDO firewall-cmd --permanent --add-port="$port/tcp" >/dev/null 2>&1 \
         && $SUDO firewall-cmd --reload >/dev/null 2>&1; then
        echo "  ✔ 已在 firewalld 放行 $port/tcp"
      else
        echo "  ⚠ firewalld 放行失败，请手动执行:"
        echo "      sudo firewall-cmd --permanent --add-port=$port/tcp && sudo firewall-cmd --reload"
      fi
    fi
    return 0
  fi
  # 3) iptables（仅默认策略为 DROP/REJECT 时才需要放行）
  if command -v iptables >/dev/null 2>&1; then
    if $SUDO iptables -S INPUT 2>/dev/null | grep -Eq -- "-P INPUT (DROP|REJECT)"; then
      if $SUDO iptables -C INPUT -p tcp --dport "$port" -j ACCEPT 2>/dev/null; then
        echo "  ✔ iptables 已放行 $port/tcp"
      elif $SUDO iptables -I INPUT -p tcp --dport "$port" -j ACCEPT 2>/dev/null; then
        echo "  ✔ 已在 iptables 放行 $port/tcp（注意：该规则重启后失效，建议持久化）"
      else
        echo "  ⚠ iptables 放行失败，请手动执行: sudo iptables -I INPUT -p tcp --dport $port -j ACCEPT"
      fi
    else
      echo "  · iptables INPUT 默认策略非 DROP/REJECT，跳过"
    fi
    return 0
  fi
  echo "  · 未检测到防火墙工具，跳过"
}

if [[ "$SERVICE_STARTED" -eq 0 ]]; then
  # 无 systemd：服务未注册，手动前台/后台启动并给出访问地址
  echo ""
  echo "✔ Triune Panel 已安装完成（当前环境无 systemd，未注册开机自启）"
  echo "  已安装版本: $GOT_VER${TAG:+  (目标版本 $TAG)}"
  echo "  手动启动: $INSTALL_DIR/triune-panel -addr :$PANEL_PORT -data $INSTALL_DIR/data"
  echo "  后台启动: nohup $INSTALL_DIR/triune-panel -addr :$PANEL_PORT -data $INSTALL_DIR/data >/var/log/triune-panel.log 2>&1 &"
  ensure_port_open "$PANEL_PORT"
  IP=$(detect_ip)
  if is_private_ip "$IP" || [[ -z "$IP" ]]; then
    echo "  访问:      http://<服务器公网IP>:$PANEL_PORT"
    echo "  （未能自动取得公网 IP。内网网卡地址: ${IP:-未检测到}；"
    echo "    可手动执行 'curl -4 ip.3322.net' 查询，或在云控制台查看公网 IP）"
  else
    echo "  访问:      http://$IP:$PANEL_PORT"
  fi
  echo "  默认账号:  admin / admin（请立即在设置中修改）"
  echo "  数据目录:  $INSTALL_DIR/data"
  echo "  卸载:      sudo bash $INSTALL_DIR/uninstall.sh （本地执行，无需联网）"
  exit 0
fi

ensure_port_open "$PANEL_PORT"

if systemctl is-active --quiet "$SERVICE"; then
  IP=$(detect_ip)
  echo ""
  echo "✔ Triune Panel 安装并启动成功"
  echo "  已安装版本: $GOT_VER${TAG:+  (目标版本 $TAG)}"
  if is_private_ip "$IP" || [[ -z "$IP" ]]; then
    echo "  访问:      http://<服务器公网IP>:$PANEL_PORT"
    echo "  （未能自动取得公网 IP。内网网卡地址: ${IP:-未检测到}；"
    echo "    可手动执行 'curl -4 ip.3322.net' 查询，或在云控制台查看公网 IP）"
  else
    echo "  访问:      http://$IP:$PANEL_PORT"
  fi
  echo "  云服务器请在控制台安全组放行 $PANEL_PORT/tcp（机器内无法代改）"
  echo "  默认账号:  admin / admin（请立即在设置中修改）"
  echo "  数据目录:  $INSTALL_DIR/data"
  echo "  查看日志:  journalctl -u triune-panel -f"
  echo "  卸载:      sudo bash $INSTALL_DIR/uninstall.sh （本地执行，无需联网）"
  echo ""
  echo "  下一步：设置 → 组件更新与版本管理 → 一键安装内核（sing-box / xray / mihomo）"
else
  echo "✘ 启动失败，最近日志:"
  journalctl -u "$SERVICE" --no-pager -n 20 2>/dev/null || echo "  (无法读取 journalctl，请手动执行: journalctl -u $SERVICE -n 50)"
  exit 1
fi
