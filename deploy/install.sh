#!/usr/bin/env bash
# Triune Panel 一键安装脚本（在 VPS 上执行）
# 用法: sudo bash install.sh [amd64|arm64]
set -euo pipefail

ARCH="${1:-amd64}"
case "$ARCH" in
  amd64|arm64) ;;
  *) echo "用法: $0 [amd64|arm64]"; exit 1 ;;
esac

INSTALL_DIR=/opt/triune-panel
BIN_NAME="triune-panel-linux-${ARCH}"

if [[ ! -f "$BIN_NAME" ]]; then
  echo "错误: 当前目录找不到 $BIN_NAME（请把 deploy/ 下的文件一起上传）"
  exit 1
fi

mkdir -p "$INSTALL_DIR"
install -m 755 "$BIN_NAME" "$INSTALL_DIR/triune-panel"
install -m 644 triune-panel.service /etc/systemd/system/

systemctl daemon-reload
systemctl enable --now triune-panel

sleep 1
if systemctl is-active --quiet triune-panel; then
  echo "✔ Triune Panel 已启动: http://<服务器IP>:2087 （默认 admin/admin，请立即改密码）"
  echo "  数据目录: $INSTALL_DIR/data"
  echo "  查看日志: journalctl -u triune-panel -f"
else
  echo "✘ 启动失败，日志如下:"
  journalctl -u triune-panel --no-pager -n 20
  exit 1
fi
