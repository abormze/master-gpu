#!/bin/sh
# Master GPU installer — Ubuntu 18.04+
# usage: sudo sh install.sh [path-to-mastergpu-binary] [port]
set -e
BIN=${1:-./mastergpu}
PORT=${2:-8080}
DIR=/opt/mastergpu
[ "$(id -u)" = 0 ] || { echo "run as root"; exit 1; }
[ -f "$BIN" ] || { echo "binary not found: $BIN"; exit 1; }
if ! command -v ffmpeg >/dev/null 2>&1; then
  apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq ffmpeg
fi
mkdir -p "$DIR/data"
systemctl stop mastergpu 2>/dev/null || true
install -m 755 "$BIN" "$DIR/mastergpu"
cat > /etc/systemd/system/mastergpu.service <<UNIT
[Unit]
Description=Master GPU streaming panel
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=$DIR/mastergpu -dir $DIR/data -listen :$PORT
Restart=always
RestartSec=2
LimitNOFILE=1048576
KillMode=process
TimeoutStopSec=15

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable --now mastergpu
sleep 1
systemctl --no-pager --lines=0 status mastergpu || true
IP=$(hostname -I 2>/dev/null | awk '{print $1}')
echo "Master GPU: http://${IP:-SERVER_IP}:$PORT  (login admin / admin)"
