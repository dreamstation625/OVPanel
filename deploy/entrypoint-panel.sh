#!/bin/sh
# 面板容器入口：修正挂载目录所有权后，以不同用户运行 Web 与维护进程。
#
# Web 进程不直接打开 Docker Socket；维护进程单独持有 Socket，只通过
# 容器内 Unix Socket 接受面板的受限更新请求。

set -eu

DATA_DIR="${OVPANEL_DATA_DIR:-/data}"
CONTROL_DIR="${OVPANEL_CONTROL_DIR:-}"
SOCKET="${OVPANEL_MAINTENANCE_SOCKET:-/run/ovpanel/maintain.sock}"
SOCKET_DIR="$(dirname "$SOCKET")"
MAINT_DIR="$DATA_DIR/maintenance"

mkdir -p "$DATA_DIR"

if [ "$(id -u)" = "0" ]; then
  # 绑定目录由 Docker 以 root 创建；只修正必要目录的所有权。
  chown 10001:10001 "$DATA_DIR"
  if [ -n "$CONTROL_DIR" ]; then
    mkdir -p "$CONTROL_DIR"
    chown 10001:10001 "$CONTROL_DIR"
  fi

  mkdir -p "$SOCKET_DIR" "$MAINT_DIR"
  chown 0:10001 "$SOCKET_DIR" "$MAINT_DIR"
  chmod 0770 "$SOCKET_DIR" "$MAINT_DIR"

  # 维护进程需要访问宿主 Docker Socket；容器根文件系统只读，无法在运行期
  # 修改 /etc/group，因此维护进程以容器 root 运行，Web 仍为非 root 用户。
  # Docker Socket 本身就是 root 等价能力，二者通过受权限保护的 Unix Socket 通信。
  /usr/local/bin/ovpanel-maintain &
  MAINT_PID=$!
  trap 'kill -TERM "$MAINT_PID" ${PANEL_PID:-0} 2>/dev/null || true; wait 2>/dev/null || true; exit 0' TERM INT

  su-exec ovpanel:ovpanel /usr/local/bin/panel &
  PANEL_PID=$!
  STATUS=0
  wait "$PANEL_PID" || STATUS=$?
  kill -TERM "$MAINT_PID" 2>/dev/null || true
  wait "$MAINT_PID" 2>/dev/null || true
  exit "$STATUS"
fi

exec /usr/local/bin/panel
