#!/usr/bin/env bash
# ============================================================
# Triune Panel 一键卸载脚本
#
# 在线一键卸载（默认保留数据，可随时重装恢复）:
#   curl -fsSL https://raw.githubusercontent.com/yunyu697/triune-panel/main/deploy/uninstall.sh | sudo bash
#
# raw 直连慢时，可改用加速镜像拉取本脚本（实测可用，任选其一）:
#   curl -fsSL https://ghfast.top/https://raw.githubusercontent.com/yunyu697/triune-panel/main/deploy/uninstall.sh | sudo bash
#   curl -fsSL https://gh-proxy.com/https://raw.githubusercontent.com/yunyu697/triune-panel/main/deploy/uninstall.sh | sudo bash
#   curl -fsSL https://ghproxy.net/https://raw.githubusercontent.com/yunyu697/triune-panel/main/deploy/uninstall.sh | sudo bash
#
# 连数据一起彻底删除（不可恢复，请谨慎）——在以上任一命令后加 -s -- --purge:
#   ... | sudo bash -s -- --purge
#
# 交互终端下运行时会二次确认是否删除数据；管道/无人值守模式默认保留。
# ============================================================
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
    echo "  或执行彻底卸载: curl -fsSL https://raw.githubusercontent.com/yunyu697/triune-panel/main/deploy/uninstall.sh | sudo bash -s -- --purge"
  fi
fi

# ---- 4. 清理空目录 ----
rmdir "$INSTALL_DIR" 2>/dev/null || true

echo ""
echo "✔ Triune Panel 卸载完成"
