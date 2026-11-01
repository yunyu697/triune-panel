#!/usr/bin/env bash
# ============================================================
# Triune Panel 一键卸载脚本
#
# 首选（无需联网）——安装时卸载脚本已落地在服务器本地，直接执行:
#   sudo bash /opt/triune-panel/uninstall.sh            # 保留数据
#   sudo bash /opt/triune-panel/uninstall.sh --purge    # 连数据彻底删除
#
# 在线卸载（本地脚本已丢时的备用）：原始地址 + 加速镜像写在同一条命令里，
# 依次尝试自动择优，任一成功即执行:
#   for u in \
#     https://raw.githubusercontent.com/yunyu697/triune-panel/main/deploy/uninstall.sh \
#     https://ghfast.top/https://raw.githubusercontent.com/yunyu697/triune-panel/main/deploy/uninstall.sh \
#     https://gh-proxy.com/https://raw.githubusercontent.com/yunyu697/triune-panel/main/deploy/uninstall.sh \
#     https://gh-proxy.org/https://raw.githubusercontent.com/yunyu697/triune-panel/main/deploy/uninstall.sh \
#     https://ghproxy.cn/https://raw.githubusercontent.com/yunyu697/triune-panel/main/deploy/uninstall.sh; do
#     curl -fsSL "$u" | sudo bash && break
#   done
#
# 注意：以上在线方式要求仓库为 Public；私有仓库匿名拉取一律 404。
# 交互终端下运行时会二次确认是否删除数据；管道/无人值守模式默认保留。
# ============================================================
set -euo pipefail

INSTALL_DIR="${TRIUNE_INSTALL_DIR:-/opt/triune-panel}"
SERVICE=triune-panel.service
PURGE=0
[[ "${1:-}" == "--purge" ]] && PURGE=1

# 权限：删除 /opt 与 systemd 单元需要 root；脚本本体可在普通用户下运行，
# 由 SUDO 逐条提权（与安装脚本保持一致）。
if [[ "$(id -u)" -eq 0 ]]; then
  SUDO=""
elif command -v sudo >/dev/null 2>&1; then
  SUDO="sudo"
else
  SUDO=""
fi

if [[ "$(id -u)" -ne 0 && -z "$SUDO" ]]; then
  echo "✘ 卸载需要 root 权限（当前用户 $(id -un)，且系统无 sudo 命令）"
  echo "  请改用: sudo bash $INSTALL_DIR/uninstall.sh"
  exit 1
fi

echo "=== Triune Panel 卸载 ==="

# ---- 1. 停止并禁用服务（面板退出时会一并停止其托管的内核进程）----
if systemctl list-unit-files 2>/dev/null | grep -q "^$SERVICE"; then
  echo "→ 停止服务 ${SERVICE}（含其托管的 sing-box / xray / mihomo 内核进程）"
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
    echo "→ --purge 模式：删除数据目录 ${DATA_DIR}（含数据库/证书/内核副本/备份）"
    $SUDO rm -rf "$DATA_DIR"
  elif [[ -t 0 ]]; then
    printf "是否同时删除数据目录 %s（数据库、证书、自动备份等，删除后不可恢复）？[y/N] " "$DATA_DIR"
    read -r ans
    if [[ "$ans" == "y" || "$ans" == "Y" ]]; then
      $SUDO rm -rf "$DATA_DIR"
      echo "→ 数据目录已删除"
    else
      echo "→ 数据目录已保留: ${DATA_DIR}（重装脚本会自动复用）"
    fi
  else
    echo "→ 数据目录已保留: ${DATA_DIR}（默认保留，重装可恢复全部数据）"
    echo "  确认不再需要可手动执行: sudo rm -rf $DATA_DIR"
    echo "  或执行彻底卸载: curl -fsSL https://raw.githubusercontent.com/yunyu697/triune-panel/main/deploy/uninstall.sh | sudo bash -s -- --purge"
  fi
fi

# ---- 4. 清理空目录 ----
$SUDO rmdir "$INSTALL_DIR" 2>/dev/null || true

echo ""
echo "✔ Triune Panel 卸载完成"
