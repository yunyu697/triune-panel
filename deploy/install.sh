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
# GitHub 直连慢时用镜像（环境变量）:
#   curl -fsSL .../install.sh | sudo GH_PROXY=https://ghfast.top bash
#
# 离线模式：脚本同目录存在 triune-panel-linux-<arch> 时直接使用，不联网。
# 升级安全：已有安装时只替换面板二进制，数据目录 /opt/triune-panel/data 原样保留。
# ============================================================
set -euo pipefail

REPO="yunyu697/triune-panel"
INSTALL_DIR=/opt/triune-panel
SERVICE=triune-panel.service
GH_PROXY="${GH_PROXY:-}"

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

dl() { # dl <输出文件> <完整URL...>  依次尝试：镜像(若设置) → 直连
  local out="$1"; shift
  local url
  for url in "$@"; do
    for base in ${GH_PROXY:+"$GH_PROXY/"} ""; do
      echo "→ 下载 ${base}${url}"
      if curl -fL --connect-timeout 8 --retry 2 -o "$out" "${base}${url}" 2>/dev/null; then
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
  dl "$TMP_BIN" \
     "https://github.com/$REPO/releases/latest/download/$BIN_NAME" \
     "https://raw.githubusercontent.com/$REPO/main/deploy/$BIN_NAME" \
     || { echo "✘ 下载失败：请检查网络，或设置 GH_PROXY 镜像重试（国内: GH_PROXY=https://ghfast.top）";
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

# ---- 健康检查 ----
sleep 1.5
if systemctl is-active --quiet "$SERVICE"; then
  IP=$(curl -s --connect-timeout 3 https://api.ipify.org 2>/dev/null || hostname -I 2>/dev/null | awk '{print $1}' || echo "<服务器IP>")
  echo ""
  echo "✔ Triune Panel 安装并启动成功"
  echo "  访问:      http://$IP:2087"
  echo "  默认账号:  admin / admin（请立即在设置中修改）"
  echo "  数据目录:  $INSTALL_DIR/data"
  echo "  查看日志:  journalctl -u triune-panel -f"
  echo ""
  echo "  下一步：设置 → 组件更新与版本管理 → 一键安装内核（sing-box / xray / mihomo）"
else
  echo "✘ 启动失败，最近日志:"
  journalctl -u "$SERVICE" --no-pager -n 20 || true
  exit 1
fi
