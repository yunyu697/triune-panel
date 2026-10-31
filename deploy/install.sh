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
# 下载源 = GitHub 原始地址 + 多个加速镜像，写在同一个候选池里；
# 运行时并行探测全部候选，自动选择最快的源下载，失败自动切换次优。
# 也可手动指定镜像跳过探测:  curl -fsSL .../install.sh | sudo GH_PROXY=https://ghfast.top bash
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

# ---- 源探测：并行测试 原始地址 + 各镜像，按响应速度排出可用源顺序 ----
# 探测文件用仓库 README（体积小）；content-type 为 HTML 的视为假响应（反爬/挑战页）剔除。
rank_sources() {
  local probe="https://raw.githubusercontent.com/$REPO/main/README.md"
  local tmp; tmp=$(mktemp -d)
  local bases=("" "${MIRRORS[@]}")
  local i=0
  for b in "${bases[@]}"; do
    (
      local r
      r=$(curl -fsL -o /dev/null --connect-timeout 5 --max-time 8 \
            -w '%{time_total}|%{http_code}|%{content_type}' "${b}${probe}" 2>/dev/null || echo "999|000|")
      local t="${r%%|*}"; local rest="${r#*|}"
      local code="${rest%%|*}"; local ct="${rest#*|}"
      if [[ "$code" == "200" && "$ct" != *html* && "$t" != "999" ]]; then
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

# ---- 下载：按择优顺序尝试；GH_PROXY 手动指定时跳过探测置于首位 ----
dl() { # dl <输出文件> <完整URL...>
  local out="$1"; shift
  local url base
  local ordered=()
  if [[ -n "$GH_PROXY" ]]; then
    ordered=("${GH_PROXY%/}/")
  fi
  # 自动探测择优（GH_PROXY 已在首位，探测结果紧随其后兜底；空行=直连）
  while IFS= read -r b; do
    ordered+=("$b")
  done < <(rank_sources)
  if [[ ${#ordered[@]} -eq 0 ]]; then
    # 探测全部失败（可能断网/仓库不可见）——退回静态顺序兜底
    ordered=("" "${MIRRORS[@]}")
  fi
  for url in "$@"; do
    for base in "${ordered[@]}"; do
      echo "→ 下载 ${base:-GitHub 直连}${url}"
      # --speed-time/--speed-limit: 15 秒内速度低于 10KB/s 判定失败，切换下一源
      if curl -fL --connect-timeout 8 --retry 1 \
           --speed-time 15 --speed-limit 10240 \
           -o "$out" "${base}${url}" 2>/dev/null; then
        return 0
      fi
    done
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
  echo "→ 正在探测下载源（GitHub 原始地址 + ${#MIRRORS[@]} 个镜像，自动择优）..."
  dl "$TMP_BIN" \
     "https://github.com/$REPO/releases/latest/download/$BIN_NAME" \
     "https://raw.githubusercontent.com/$REPO/main/deploy/$BIN_NAME" \
     || { echo "✘ 全部下载源均失败：请检查网络；若仓库为私有，匿名无法拉取，请将仓库设为 Public 或改用离线安装";
          rm -f "$TMP_BIN"; exit 1; }
fi
chmod +x "$TMP_BIN"

# ---- 版本确认（同时校验二进制可执行，架构不匹配/文件损坏时立即中止）----
if ! "$TMP_BIN" -version; then
  echo ""
  echo "✘ 下载的二进制无法执行，可能架构不匹配或文件已损坏"
  echo "  目标架构: $ARCH  （uname -m = $(uname -m)）"
  echo "  文件信息: $(file "$TMP_BIN" 2>/dev/null || ls -la "$TMP_BIN")"
  echo "  请手动指定架构参数重试:  .../install.sh | bash -s -- amd64   或   .../install.sh | bash -s -- arm64"
  echo ""
  rm -f "$TMP_BIN"
  exit 1
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
ExecStart=@EXEC@ -addr :2087 -data @DATA@
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
  -e "s|@DATA@|$(exec_prefix "$INSTALL_DIR/data")|g" "$UNIT_TMP" && rm -f "$UNIT_TMP.bak"
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

# 访问地址探测：优先取本机默认路由源 IP（不受系统代理干扰）；
# 是内网地址时再探测公网出口 IP（--noproxy 防止代理服务器 IP 混入）。
# 每一步都做降级，缺命令（精简容器无 iproute2/curl）也不能让脚本崩掉。
detect_ip() {
  local ip=""
  ip=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++)if($i=="src"){print $(i+1)}}' | head -1) || true
  [[ -z "$ip" ]] && ip=$(ip route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++)if($i=="src"){print $(i+1)}}' | head -1) || true
  [[ -z "$ip" ]] && ip=$(hostname -I 2>/dev/null | awk '{print $1}') || true
  case "$ip" in
    ""|10.*|192.168.*|172.1[6-9].*|172.2[0-9].*|172.3[0-1].*|127.*)
      ip=$(curl --noproxy '*' -s --connect-timeout 3 https://api.ipify.org 2>/dev/null) || true
      [[ -z "$ip" ]] && ip=$(curl -s --connect-timeout 3 https://ifconfig.me 2>/dev/null) || true
      ;;
  esac
  printf '%s' "$ip"
}

if [[ "$SERVICE_STARTED" -eq 0 ]]; then
  # 无 systemd：服务未注册，手动前台/后台启动并给出访问地址
  echo ""
  echo "✔ Triune Panel 已安装完成（当前环境无 systemd，未注册开机自启）"
  echo "  手动启动: $INSTALL_DIR/triune-panel -addr :2087 -data $INSTALL_DIR/data"
  echo "  后台启动: nohup $INSTALL_DIR/triune-panel -addr :2087 -data $INSTALL_DIR/data >/var/log/triune-panel.log 2>&1 &"
  IP=$(detect_ip); [[ -z "$IP" ]] && IP="<服务器IP>"
  echo "  访问:      http://$IP:2087"
  echo "  默认账号:  admin / admin（请立即在设置中修改）"
  echo "  数据目录:  $INSTALL_DIR/data"
  echo "  卸载:      sudo bash $INSTALL_DIR/uninstall.sh （本地执行，无需联网）"
  exit 0
fi

if systemctl is-active --quiet "$SERVICE"; then
  IP=$(detect_ip)
  [[ -z "$IP" ]] && IP="<服务器IP>"
  echo ""
  echo "✔ Triune Panel 安装并启动成功"
  echo "  访问:      http://$IP:2087"
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
