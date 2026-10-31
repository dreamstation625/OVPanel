#!/bin/sh
# 面板容器入口：修正挂载目录所有权后，以非 root 用户运行面板进程。
#
# 容器启动时仅这一步需要 root；面板本身不能读写宿主 Docker Socket，
# 也不以 root 身份处理请求。

set -eu

DATA_DIR="${OVPANEL_DATA_DIR:-/data}"

mkdir -p "$DATA_DIR"

if [ "$(id -u)" = "0" ]; then
  # 绑定目录由 Docker 以 root 创建；只修正数据目录顶层，
  # 避免每次启动递归扫描未来的大型备份目录。
  chown 10001:10001 "$DATA_DIR"
  if [ -n "${OVPANEL_CONTROL_DIR:-}" ]; then
    mkdir -p "$OVPANEL_CONTROL_DIR"
    chown 10001:10001 "$OVPANEL_CONTROL_DIR"
  fi
  exec su-exec ovpanel:ovpanel /usr/local/bin/panel
fi

exec /usr/local/bin/panel
