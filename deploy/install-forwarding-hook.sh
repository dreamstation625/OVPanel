#!/bin/sh
# 安装/移除宿主机 systemd 转发钩子。
#
# 钩子只负责定期执行 ensure-forwarding.sh，让裸 docker compose、1Panel Compose
# 等任意入口创建 ovpanel 网络后也能自动补齐 UFW 转发和 NAT。
#
# 用法：
#   sudo ./deploy/install-forwarding-hook.sh
#   sudo ./deploy/install-forwarding-hook.sh --remove

set -eu

UNIT_DIR="/etc/systemd/system"
LIB_DIR="/usr/local/lib/ovpanel"
SERVICE="ovpanel-forwarding.service"
TIMER="ovpanel-forwarding.timer"

log() { printf '[hook] %s\n' "$*"; }
fail() { printf '[hook][fail] %s\n' "$*" >&2; exit 1; }

if [ "$(id -u)" -ne 0 ]; then
  if command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; then
    exec sudo -n "$0" "$@"
  fi
  fail "安装 systemd 钩子需要 root 或免密 sudo"
fi

command -v systemctl >/dev/null 2>&1 || fail "当前系统没有 systemd"

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
SOURCE="$SCRIPT_DIR/ensure-forwarding.sh"

remove_hook() {
  systemctl disable --now "$TIMER" >/dev/null 2>&1 || true
  systemctl stop "$SERVICE" >/dev/null 2>&1 || true
  rm -f "$UNIT_DIR/$SERVICE" "$UNIT_DIR/$TIMER"
  rm -rf "$LIB_DIR"
  systemctl daemon-reload
  log "已移除 systemd 钩子；已有防火墙/NAT 规则保留"
}

install_workdir() {
  [ -f "$SOURCE" ] || fail "缺少 $SOURCE"
  install -d -m 0755 "$LIB_DIR"
  install -m 0755 "$SOURCE" "$LIB_DIR/ensure-forwarding.sh"

  cat > "$UNIT_DIR/$SERVICE" <<'EOF'
[Unit]
Description=OVPanel Docker forwarding and NAT check
After=docker.service network-online.target
Wants=network-online.target

[Service]
Type=oneshot
Environment=OVPANEL_FORWARD_OPTIONAL=1
Environment=OVPANEL_FORWARD_QUIET=1
Environment=OVPANEL_FORWARD_VERIFY=0
ExecStart=/usr/local/lib/ovpanel/ensure-forwarding.sh
EOF

  cat > "$UNIT_DIR/$TIMER" <<'EOF'
[Unit]
Description=Periodically ensure OVPanel Docker forwarding and NAT

[Timer]
OnBootSec=30s
OnUnitActiveSec=60s
AccuracySec=5s
Persistent=true
Unit=ovpanel-forwarding.service

[Install]
WantedBy=timers.target
EOF

  systemctl daemon-reload
  systemctl enable --now "$TIMER"
  systemctl start "$SERVICE" || log "首次检查未通过，定时器会继续重试"
  log "已安装 $TIMER；开机后及每 60 秒执行一次"
  systemctl --no-pager --full status "$TIMER" | sed -n '1,8p' || true
}

case "${1:-}" in
  "")
    install_workdir
    ;;
  --remove)
    remove_hook
    ;;
  *)
    fail "用法：$0 [--remove]"
    ;;
esac
