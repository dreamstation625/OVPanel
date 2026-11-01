#!/bin/sh
# 确保 OVPanel 的 Compose 网络可以通过宿主机转发并完成 NAT。
#
# 由 compose.sh 在 up 成功后调用；只处理当前项目网络，不修改其他 Docker 网络。
# 默认启用，可用 OVPANEL_AUTO_FORWARD=0 关闭。
#
# 支持的自动配置：UFW 已启用。Docker 自身已提供 NAT 且 UFW 未启用时跳过。
# 其他防火墙组合会明确报错，要求管理员手动配置。

set -eu

NETWORK="${OVPANEL_NETWORK:-ovpanel}"
AUTO="${OVPANEL_AUTO_FORWARD:-1}"
OPTIONAL="${OVPANEL_FORWARD_OPTIONAL:-0}"
QUIET="${OVPANEL_FORWARD_QUIET:-0}"
VERIFY="${OVPANEL_FORWARD_VERIFY:-1}"

log() { printf '[forward] %s\n' "$*"; }
info() { [ "$QUIET" = "1" ] || printf '[forward] %s\n' "$*"; }
warn() { printf '[forward][warn] %s\n' "$*" >&2; }
fail() { printf '[forward][fail] %s\n' "$*" >&2; exit 1; }

[ "$AUTO" = "0" ] && { info "OVPANEL_AUTO_FORWARD=0，跳过"; exit 0; }

if [ "$(id -u)" -ne 0 ]; then
  if command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; then
    exec sudo -n env OVPANEL_NETWORK="$NETWORK" OVPANEL_AUTO_FORWARD="$AUTO" "$0" "$@"
  fi
  fail "修改防火墙需要 root 或免密 sudo"
fi

command -v docker >/dev/null 2>&1 || fail "缺少 docker 命令"
if ! docker network inspect "$NETWORK" >/dev/null 2>&1; then
  if [ "$OPTIONAL" = "1" ]; then
    info "Docker 网络 $NETWORK 还不存在，等待下次检查"
    exit 0
  fi
  fail "找不到 Docker 网络 $NETWORK；请先启动 Compose"
fi

raw_subnets="$(docker network inspect "$NETWORK" --format '{{range .IPAM.Config}}{{.Subnet}} {{end}}' 2>/dev/null || true)"
ipv4=""
for subnet in $raw_subnets; do
  case "$subnet" in
    *:*) continue ;;
  esac
  ipv4="$ipv4 $subnet"
done
ipv4="$(printf '%s' "$ipv4" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')"
[ -n "$ipv4" ] || fail "网络 $NETWORK 没有 IPv4 子网"
info "网络 $NETWORK 的 IPv4 子网：$ipv4"

command -v iptables >/dev/null 2>&1 || fail "缺少 iptables，无法检查和配置转发"

nat_rules="$(iptables -t nat -S POSTROUTING 2>/dev/null || true)"
forward_rules="$(iptables -S ufw-user-forward 2>/dev/null || true)"
need_nat=""
for subnet in $ipv4; do
  case "$nat_rules" in
    *"-s $subnet"*MASQUERADE*) ;;
    *) need_nat="$need_nat $subnet" ;;
  esac
done

ufw_active=0
if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | head -n 1 | grep -qi '^Status: active'; then
  ufw_active=1
fi

if [ "$ufw_active" -eq 0 ]; then
  if [ -z "$need_nat" ]; then
    info "UFW 未启用，检测到现有 NAT；跳过"
    exit 0
  fi
  fail "UFW 未启用且缺少 NAT：$need_nat；请启用 Docker iptables 或手动配置防火墙"
fi

for subnet in $ipv4; do
  case "$forward_rules" in
    *"-s $subnet -j ACCEPT"*) info "UFW 转发已存在：$subnet" ;;
    *)
      log "添加 UFW 转发：$subnet"
      ufw route allow from "$subnet" >/dev/null || fail "ufw route allow 失败：$subnet"
      ;;
  esac
done

before="/etc/ufw/before.rules"
[ -f "$before" ] || fail "未找到 $before"

block="$(mktemp)"
work="$(mktemp)"
current="$(mktemp)"
backup=""
cleanup() { rm -f "${block:-}" "${work:-}" "${current:-}"; }
trap cleanup EXIT

{
  echo "# BEGIN OVPanel forwarding"
  echo "*nat"
  echo ":POSTROUTING ACCEPT [0:0]"
  for subnet in $ipv4; do
    echo "-A POSTROUTING -s $subnet ! -o br-+ -j MASQUERADE"
  done
  echo "COMMIT"
  echo "# END OVPanel forwarding"
} > "$block"

awk '
  /^# BEGIN OVPanel forwarding$/ { inside=1 }
  inside { print }
  /^# END OVPanel forwarding$/ { inside=0 }
' "$before" > "$current"

if cmp -s "$block" "$current" && ! grep -q '^# OVPanel Docker NAT' "$before"; then
  info "UFW 持久化 NAT 已是最新"
else
  backup="/etc/ufw/before.rules.ovpanel-bak-$(date +%Y%m%d%H%M%S)"
  cp -a "$before" "$backup"
  awk -v blockfile="$block" '
    function emit(   line) {
      while ((getline line < blockfile) > 0) print line
      close(blockfile)
    }
    /^# BEGIN OVPanel forwarding$/ { skip=1; next }
    /^# END OVPanel forwarding$/ { skip=0; next }
    /^# OVPanel Docker NAT/ { legacy=1; next }
    legacy && /^COMMIT$/ { legacy=0; next }
    skip || legacy { next }
    !inserted && /^\*filter$/ { emit(); inserted=1 }
    { print }
    END { if (!inserted) { print ""; emit() } }
  ' "$before" > "$work"

  if ! iptables-restore --test "$work" >/dev/null 2>&1; then
    cp -a "$backup" "$before"
    fail "UFW before.rules 校验失败，已恢复原文件"
  fi
  cat "$work" > "$before"
  log "已写入持久化 NAT：$before（备份 $backup）"
fi

for subnet in $need_nat; do
  if ! iptables -t nat -C POSTROUTING -s "$subnet" ! -o br-+ -j MASQUERADE 2>/dev/null; then
    iptables -t nat -A POSTROUTING -s "$subnet" ! -o br-+ -j MASQUERADE || fail "添加 NAT 失败：$subnet"
    log "已添加 NAT：$subnet"
  fi
done

if [ "$VERIFY" = "1" ] && docker ps --format '{{.Names}}' 2>/dev/null | grep -qx 'ovpanel-openvpn'; then
  if docker exec ovpanel-openvpn wget -qO- -T 5 http://www.baidu.com >/dev/null 2>&1; then
    info "OpenVPN 容器公网出口正常"
  else
    warn "OpenVPN 容器公网出口测试失败；检查上游网络或 DNS"
  fi
fi

info "完成"
