#!/usr/bin/env bash
# OVPanel 部署前宿主检查。
#
# 在目标 Linux 主机上以普通用户运行即可完成大部分检查；
# 加载内核模块和列出 nftables 规则需要 root 或 sudo。
#
# 用法：
#   ./check-host.sh [--data-dir /srv/ovpanel]
#
# 退出码：0 表示必需项全部通过；1 表示存在阻断项。
# 本脚本只读取信息，除按需加载 ifb/sch_htb/act_mirred 模块外不修改宿主。

set -u

DATA_DIR="/srv/ovpanel"
if [ "${1:-}" = "--data-dir" ] && [ -n "${2:-}" ]; then
  DATA_DIR="$2"
fi

failures=0
warnings=0

ok() { printf '[ok]   %s\n' "$*"; }
warn() { printf '[warn] %s\n' "$*"; warnings=$((warnings + 1)); }
fail() { printf '[fail] %s\n' "$*"; failures=$((failures + 1)); }

is_root() { [ "$(id -u)" -eq 0 ]; }

have() { command -v "$1" >/dev/null 2>&1; }

module_loaded() {
  [ -d "/sys/module/$1" ] || grep -qE "^$1[ _]" /proc/modules 2>/dev/null
}

try_module() {
  local name="$1"
  if module_loaded "$name"; then
    ok "内核模块 $name 已加载"
    return
  fi
  if have modprobe && is_root && modprobe "$name" >/dev/null 2>&1; then
    ok "内核模块 $name 已加载"
    return
  fi
  if have modprobe && modprobe -n "$name" >/dev/null 2>&1; then
    warn "内核模块 $name 未加载但可加载；请以 root 执行：modprobe $name"
    return
  fi
  fail "内核模块 $name 不可用（可尝试：sudo modprobe $name）"
}

printf 'OVPanel 宿主检查\n'
printf '数据目录：%s\n\n' "$DATA_DIR"

# 平台与架构
kernel="$(uname -s)"
arch="$(uname -m)"
if [ "$kernel" = "Linux" ]; then
  ok "操作系统：Linux $(uname -r)"
else
  fail "当前系统是 $kernel；生产仅支持 Linux，不支持 Docker Desktop"
fi

case "$arch" in
  x86_64 | amd64 | aarch64 | arm64) ok "架构：$arch" ;;
  *) fail "架构 $arch 不在已验证范围（amd64/arm64）" ;;
esac

# Docker 与 Compose
if have docker; then
  if docker info >/dev/null 2>&1; then
    server_version="$(docker version --format '{{.Server.Version}}' 2>/dev/null || echo unknown)"
    ok "Docker Engine：$server_version"
    security_options="$(docker info --format '{{.SecurityOptions}}' 2>/dev/null || true)"
    case "$security_options" in
      *rootless*) fail "检测到 rootless Docker；当前基线是 rootful Docker Engine" ;;
      *) ok "Docker 模式：rootful" ;;
    esac
  else
    fail "无法连接 Docker Engine（检查服务状态或当前用户权限）"
  fi
else
  fail "缺少 docker 命令"
fi

if docker compose version >/dev/null 2>&1; then
  ok "Docker Compose：$(docker compose version --short 2>/dev/null || echo v2)"
elif have docker-compose; then
  warn "仅找到旧版 docker-compose；建议使用 Docker Compose v2"
else
  fail "缺少 docker compose"
fi

# TUN 与网络工具
if [ -c /dev/net/tun ]; then
  ok "/dev/net/tun 存在"
else
  fail "缺少 /dev/net/tun 字符设备，OpenVPN TUN 模式无法运行"
fi

# nft/tc/ip 由 OpenVPN 镜像自带，宿主缺少它们不影响容器运行；
# 这里只用于宿主侧诊断，最终以容器的 /readyz 与 /v1/environment 为准。
for bin in nft tc ip; do
  if have "$bin"; then
    ok "宿主命令 $bin 可用"
  else
    warn "宿主缺少 $bin；容器镜像自带，以容器内检查为准"
  fi
done

if have nft && is_root; then
  if nft list tables >/dev/null 2>&1; then
    ok "nftables 可读取规则表"
  else
    warn "nftables 无法读取规则表（检查内核 nf_tables 支持）"
  fi
elif have nft; then
  warn "非 root，跳过 nftables 规则表读取"
fi

# 数据面内核模块。模块可加载即可，容器启动后会再次检查。
try_module ifb
try_module sch_htb
try_module act_mirred

# 转发开关。Compose 会尝试设置容器网络命名空间内的值。
forward="$(cat /proc/sys/net/ipv4/ip_forward 2>/dev/null || echo unknown)"
if [ "$forward" = "1" ]; then
  ok "宿主 net.ipv4.ip_forward=1"
else
  warn "宿主 net.ipv4.ip_forward=$forward；Compose 需要允许设置该 sysctl"
fi

# 磁盘空间
if have df; then
  parent="$DATA_DIR"
  while [ ! -d "$parent" ] && [ "$parent" != "/" ]; do
    parent="$(dirname "$parent")"
  done
  available_kb="$(df -Pk "$parent" 2>/dev/null | awk 'NR==2 {print $4}')"
  if [ -n "${available_kb:-}" ]; then
    if [ "$available_kb" -lt 1048576 ]; then
      warn "数据目录所在分区可用空间低于 1 GB（${available_kb} KB）"
    else
      ok "数据目录可用空间：$((available_kb / 1024)) MB"
    fi
  else
    warn "无法读取数据目录所在分区的剩余空间"
  fi
fi

printf '\n结论：%d 个阻断项，%d 个警告。\n' "$failures" "$warnings"
if [ "$failures" -gt 0 ]; then
  printf '请先处理阻断项，再启动 OVPanel。\n'
  exit 1
fi
printf '宿主必需项已通过；容器内数据面能力请查看 vpn-agent 的 /v1/environment。\n'
