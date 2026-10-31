#!/usr/bin/env bash
# ============================================================
# Triune Panel 一键安装 / 升级脚本
#
# 在线一键安装（VPS 上执行）:
#   curl -fsSL https://raw.githubusercontent.com/yunyu697/triune-panel/main/deploy/install.sh | sudo bash
#
# 指定架构（默认自动检测）:
#   curl -fsSL .../install.sh | sudo bash -s -- arm64
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
INSTALL_DIR=/opt/triune-panel
SERVICE=triune-panel.service
GH_PROXY="${GH_PROXY:-}"

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
  case "$(uname -m)" in
    x86_64)  ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *) echo "✘ 不支持的架构: $(uname -m)，可用参数指定 amd64/arm64"; exit 1 ;;
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

# ---- 版本确认 ----
"$TMP_BIN" -version || true

# ---- 停旧服务（升级场景）----
if systemctl list-unit-files 2>/dev/null | grep -q "^$SERVICE"; then
  echo "→ 检测到已有安装，停止旧服务（数据目录保留）"
  systemctl stop "$SERVICE" 2>/dev/null || true
fi

# ---- 安装 ----
mkdir -p "$INSTALL_DIR"
install -m 755 "$TMP_BIN" "$INSTALL_DIR/triune-panel"
rm -f "$TMP_BIN"

cat > "/etc/systemd/system/$SERVICE" <<'UNIT'
[Unit]
Description=Triune Panel (mihomo/sing-box/xray 管理面板)
After=network.target
Wants=network.target

[Service]
Type=simple
ExecStart=/opt/triune-panel/triune-panel -addr :2087 -data /opt/triune-panel/data
Restart=on-failure
RestartSec=3
User=root
LimitNOFILE=65535
NoNewPrivileges=false
PrivateTmp=false

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
systemctl enable --now "$SERVICE"

# ---- 卸载脚本落地：卸载从此不依赖网络 ----
cat > "$INSTALL_DIR/uninstall.sh" <<'UNINSTALL'
#!/usr/bin/env bash
# Triune Panel 卸载脚本（安装时自动落地，本地执行，无需联网）
#   保留数据:   sudo bash /opt/triune-panel/uninstall.sh
#   彻底删除:   sudo bash /opt/triune-panel/uninstall.sh --purge
set -euo pipefail

INSTALL_DIR=/opt/triune-panel
SERVICE=triune-panel.service
PURGE=0
[[ "${1:-}" == "--purge" ]] && PURGE=1

echo "=== Triune Panel 卸载 ==="

# ---- 1. 停止并禁用服务（面板退出时会一并停止其托管的内核进程）----
if systemctl list-unit-files 2>/dev/null | grep -q "^$SERVICE"; then
  echo "→ 停止服务 $SERVICE（含其托管的 sing-box / xray / mihomo 内核进程）"
  systemctl disable --now "$SERVICE" 2>/dev/null || systemctl stop "$SERVICE" 2>/dev/null || true
  sleep 1
else
  echo "→ 未检测到 systemd 服务（可能已卸载）"
fi

# 兜底：确认没有残留的面板进程
if pgrep -f "$INSTALL_DIR/triune-panel" >/dev/null 2>&1; then
  echo "→ 结束残留面板进程"
  pkill -f "$INSTALL_DIR/triune-panel" 2>/dev/null || true
  sleep 0.5
fi

# ---- 2. 删除服务单元与二进制 ----
if [[ -f "/etc/systemd/system/$SERVICE" ]]; then
  rm -f "/etc/systemd/system/$SERVICE"
  systemctl daemon-reload
  echo "→ 已删除服务单元 /etc/systemd/system/$SERVICE"
fi
if [[ -f "$INSTALL_DIR/triune-panel" ]]; then
  rm -f "$INSTALL_DIR/triune-panel"
  echo "→ 已删除二进制 $INSTALL_DIR/triune-panel"
fi

# ---- 3. 数据目录处理 ----
DATA_DIR="$INSTALL_DIR/data"
if [[ -d "$DATA_DIR" ]]; then
  if [[ $PURGE -eq 1 ]]; then
    echo "→ --purge 模式：删除数据目录 $DATA_DIR（含数据库/证书/内核副本/备份）"
    rm -rf "$DATA_DIR"
  elif [[ -t 0 ]]; then
    printf "是否同时删除数据目录 %s（数据库、证书、自动备份等，删除后不可恢复）？[y/N] " "$DATA_DIR"
    read -r ans
    if [[ "$ans" == "y" || "$ans" == "Y" ]]; then
      rm -rf "$DATA_DIR"
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
rm -f "$INSTALL_DIR/uninstall.sh"
rmdir "$INSTALL_DIR" 2>/dev/null || true

echo ""
echo "✔ Triune Panel 卸载完成"
UNINSTALL
chmod 755 "$INSTALL_DIR/uninstall.sh"

# ---- 健康检查 ----
sleep 1.5
if systemctl is-active --quiet "$SERVICE"; then
  # 访问地址：优先取本机默认路由源 IP（不受系统代理干扰）；
  # 是内网地址时再探测公网出口 IP（--noproxy 防止代理服务器 IP 混入）。
  IP=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++)if($i=="src"){print $(i+1)}}' | head -1)
  case "$IP" in
    ""|10.*|192.168.*|172.1[6-9].*|172.2[0-9].*|172.3[0-1].*|127.*)
      IP=$(curl --noproxy '*' -s --connect-timeout 3 https://api.ipify.org 2>/dev/null || true) ;;
  esac
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
  journalctl -u "$SERVICE" --no-pager -n 20 || true
  exit 1
fi
