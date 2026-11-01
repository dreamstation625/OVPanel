#!/bin/sh
# 仅用于 host-network 模式：让宿主机放行 VPN 客户端转发，并保留数据端口。
#
# OpenVPN 容器使用 host 网络时，运行代理的 nft 表也位于宿主网络命名空间，
# 但 UFW 的 FORWARD 默认拒绝仍需额外放行。这里只插入项目自己的跳转链，
# 不改变 UFW 的其他规则；UFW reload 后会由循环重新补上。

set -eu

PORT="${OVPANEL_VPN_PORT:-1194}"
PROTO="$(printf '%s' "${OVPANEL_VPN_PROTO:-udp}" | tr '[:upper:]' '[:lower:]')"
SUBNET=""

log() { printf '[host-forward] %s\n' "$*"; }
warn() { printf '[host-forward][warn] %s\n' "$*" >&2; }

refresh_pool() {
  if [ -r /control/bundle.json ]; then
    found="$(sed -n 's/.*"vpnPool"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' /control/bundle.json | head -n 1)"
    [ -n "$found" ] && SUBNET="$found"
  fi
}

while [ -z "$SUBNET" ]; do
  refresh_pool
  [ -n "$SUBNET" ] && break
  sleep 5
done
log "VPN 池：$SUBNET；数据端口：$PORT/$PROTO"

if [ "$(cat /proc/sys/net/ipv4/ip_forward 2>/dev/null || echo 0)" != "1" ]; then
  sysctl -w net.ipv4.ip_forward=1 >/dev/null 2>&1 || warn "无法自动开启宿主机 net.ipv4.ip_forward=1"
fi

apply() {
  refresh_pool
  [ -n "$SUBNET" ] || return 0
  if ! nft list table ip filter >/dev/null 2>&1; then
    return 0
  fi

  nft list chain ip filter ovpanel_host_forward >/dev/null 2>&1 || nft add chain ip filter ovpanel_host_forward
  nft flush chain ip filter ovpanel_host_forward
  nft add rule ip filter ovpanel_host_forward ip saddr "$SUBNET" accept
  nft add rule ip filter ovpanel_host_forward ip daddr "$SUBNET" ct state established,related accept
  if ! nft list chain ip filter FORWARD 2>/dev/null | grep -q 'jump ovpanel_host_forward'; then
    nft insert rule ip filter FORWARD jump ovpanel_host_forward
  fi

  if nft list chain ip filter ufw-user-input >/dev/null 2>&1; then
    case "$PROTO" in
      tcp) match="tcp dport $PORT" ;;
      udp) match="udp dport $PORT" ;;
      *) match="" ;;
    esac
    if [ -n "$match" ] && ! nft list chain ip filter ufw-user-input | grep -q "$match"; then
      nft insert rule ip filter ufw-user-input $match accept
    fi
  fi
}

apply || warn "应用 host-network 转发规则失败，稍后重试"
while :; do
  sleep 30
  apply || warn "应用 host-network 转发规则失败，稍后重试"
done
