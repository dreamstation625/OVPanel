#!/bin/sh
# 统一 Compose 入口：优先加载网页更新的 OpenVPN 镜像固定值。
#
# 网页维护成功后写入 <data>/maintenance/openvpn-image.env；
# 直接执行 docker compose up 会回到 .env 的旧镜像，因此手工操作请使用本脚本。
#
# up -d 成功后会自动调用 ensure-forwarding.sh，为当前 ovpanel 网络补齐
# 宿主转发和 NAT；可用 OVPANEL_AUTO_FORWARD=0 关闭。

set -eu

cd "$(dirname "$0")/.."

DATA_ROOT="${OVPANEL_DATA_ROOT:-./data}"
PIN="$DATA_ROOT/maintenance/openvpn-image.env"

compose() {
  if [ -f .env ] && [ -f "$PIN" ]; then
    docker compose --env-file .env --env-file "$PIN" -f compose.yaml "$@"
  elif [ -f .env ]; then
    docker compose --env-file .env -f compose.yaml "$@"
  elif [ -f "$PIN" ]; then
    docker compose --env-file "$PIN" -f compose.yaml "$@"
  else
    docker compose -f compose.yaml "$@"
  fi
}

action="${1:-}"
detach=0
for arg in "$@"; do
  case "$arg" in
    -d | --detach) detach=1 ;;
  esac
done

if compose "$@"; then
  status=0
else
  status=$?
fi

if [ "$status" -eq 0 ] && [ "$action" = "up" ]; then
  if [ "$detach" -eq 1 ]; then
    "$(dirname "$0")/ensure-forwarding.sh"
  else
    printf '[forward][warn] 未使用 -d，跳过自动转发检查；请使用 up -d 部署\n' >&2
  fi
fi

exit "$status"
