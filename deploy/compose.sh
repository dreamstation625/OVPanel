#!/bin/sh
# 统一 Compose 入口：优先加载网页更新的 OpenVPN 镜像固定值。
#
# 网页维护成功后写入 <data>/panel/maintenance/openvpn-image.env；
# 直接执行 docker compose up 会回到 .env 的旧镜像，因此手工操作请使用本脚本。

set -eu

cd "$(dirname "$0")/.."

DATA_ROOT="${OVPANEL_DATA_ROOT:-./data}"
PIN="$DATA_ROOT/panel/maintenance/openvpn-image.env"

if [ -f .env ] && [ -f "$PIN" ]; then
  exec docker compose --env-file .env --env-file "$PIN" -f compose.yaml "$@"
elif [ -f .env ]; then
  exec docker compose --env-file .env -f compose.yaml "$@"
elif [ -f "$PIN" ]; then
  exec docker compose --env-file "$PIN" -f compose.yaml "$@"
else
  exec docker compose -f compose.yaml "$@"
fi
