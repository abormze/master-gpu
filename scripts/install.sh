#!/bin/sh
# Master GPU installer — Ubuntu 18.04+
# usage: sudo sh install.sh [path-to-mastergpu-binary] [port]
# without a local binary the latest GitHub release is downloaded
set -e
BIN=${1:-./mastergpu}
PORT=${2:-8080}
DIR=/opt/mastergpu
REL=${MASTERGPU_RELEASE:-https://github.com/abormze/master-gpu/releases/latest/download/mastergpu-linux-amd64}
[ "$(id -u)" = 0 ] || { echo "run as root"; exit 1; }
if [ ! -f "$BIN" ]; then
  echo "downloading $REL"
  BIN=$(mktemp)
  curl -fL --retry 3 -o "$BIN" "$REL" || { echo "download failed: $REL"; exit 1; }
  chmod 755 "$BIN"
fi
if ! command -v ffmpeg >/dev/null 2>&1; then
  apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq ffmpeg
fi
mkdir -p "$DIR/data"
if [ ! -x "$DIR/mediamtx" ]; then
  MTX=v1.9.3
  curl -fsSL "https://github.com/bluenviron/mediamtx/releases/download/$MTX/mediamtx_${MTX}_linux_amd64.tar.gz" | tar xz -C "$DIR" mediamtx \
    || echo "warning: RTMP/RTSP gateway (mediamtx) not installed"
fi
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
