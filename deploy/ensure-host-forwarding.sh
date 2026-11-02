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
HOST_NETWORKS_FILE="${OVPANEL_HOST_NETWORKS_FILE:-/control/host-networks.txt}"

log() { printf '[host-forward] %s\n' "$*"; }
warn() { printf '[host-forward][warn] %s\n' "$*" >&2; }

# 写入宿主接口和路由网段清单，供面板/代理拦截与宿主网段重叠的路由通告。
write_host_networks() {
  command -v ip >/dev/null 2>&1 || { warn "缺少 ip，跳过宿主网段清单"; return 0; }
  mkdir -p "$(dirname "$HOST_NETWORKS_FILE")" 2>/dev/null ||
    { warn "无法创建宿主网段目录：$(dirname "$HOST_NETWORKS_FILE")"; return 0; }
  tmp="$(mktemp "${HOST_NETWORKS_FILE}.tmp.XXXXXX" 2>/dev/null)" ||
    { warn "无法创建宿主网段临时文件"; return 0; }
  {
    ip -o -4 addr show | awk '$2 != "lo" && $2 !~ /^(tun|ifb)/ {print $4}'
    ip -4 route show | awk '$1 != "default" && $1 ~ "/" && $0 !~ / dev (tun|ifb)/ {print $1}'
  } | sed '/^$/d' | sort -u > "$tmp" ||
    { rm -f "$tmp"; warn "读取宿主网段失败"; return 0; }
  chmod 0644 "$tmp" 2>/dev/null || true
  mv "$tmp" "$HOST_NETWORKS_FILE" 2>/dev/null ||
    { rm -f "$tmp"; warn "写入宿主网段清单失败：$HOST_NETWORKS_FILE"; return 0; }
  log "已更新宿主网段清单：$HOST_NETWORKS_FILE"
}

refresh_pool() {
  if [ -r /control/bundle.json ]; then
    found="$(sed -n 's/.*"vpnPool"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' /control/bundle.json | head -n 1)"
    [ -n "$found" ] && SUBNET="$found"
  fi
}

write_host_networks

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
  write_host_networks
  apply || warn "应用 host-network 转发规则失败，稍后重试"
done
