#!/bin/sh
# OpenVPN 容器入口。bridge 模式直接启动代理；
# host-network 模式先启动宿主转发维护进程，再启动代理。
set -eu

if [ "${OVPANEL_HOST_NETWORK:-0}" = "1" ]; then
  /usr/local/bin/ensure-host-forwarding &
fi

exec /usr/local/bin/vpn-agent
