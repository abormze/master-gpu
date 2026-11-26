#!/bin/bash
# Master GPU installer — Ubuntu 20.04+ (installs a clean, empty panel to /root/mastergpu)
# one command: curl -fsSL https://github.com/abormze/master-gpu/releases/latest/download/install.sh | sudo bash
# usage: bash install.sh [port]
set -e
PORT=${1:-8080}
DIR=/root/mastergpu
REL=${MG_RELEASE:-https://github.com/abormze/master-gpu/releases/latest/download}
MTX=v1.9.3
[ "$(id -u)" = 0 ] || { echo "run as root"; exit 1; }
export DEBIAN_FRONTEND=noninteractive
need=""; for c in ffmpeg curl; do command -v $c >/dev/null 2>&1 || need="$need $c"; done
[ -n "$need" ] && { apt-get update -qq; apt-get install -y -qq $need; }
SRC=$(cd "$(dirname "$0")" 2>/dev/null && pwd)
TMP=$(mktemp -d)
BIN="$SRC/mastergpu"
if [ ! -f "$BIN" ]; then
  echo "downloading $REL/mastergpu-linux-amd64"
  curl -fL --retry 3 -o "$TMP/mastergpu" "$REL/mastergpu-linux-amd64"
  BIN="$TMP/mastergpu"
fi
chmod 755 "$BIN"
"$BIN" -version
systemctl stop mastergpu 2>/dev/null || true
mkdir -p "$DIR/data"
[ "$BIN" = "$DIR/mastergpu" ] || install -m 755 "$BIN" "$DIR/mastergpu"
if [ ! -x "$DIR/mediamtx" ]; then
  if [ -x "$SRC/mediamtx" ]; then install -m 755 "$SRC/mediamtx" "$DIR/mediamtx"
  else curl -fsSL "https://github.com/bluenviron/mediamtx/releases/download/$MTX/mediamtx_${MTX}_linux_amd64.tar.gz" | tar xz -C "$DIR" mediamtx \
    || echo "warning: RTMP/RTSP gateway (mediamtx) not installed"; fi
fi
rm -rf "$TMP"
cat > /etc/systemd/system/mastergpu.service <<UNIT
[Unit]
Description=Master GPU streaming panel
After=network-online.target
Wants=network-online.target

[Service]
Environment=HOME=/root
Environment=CUDA_CACHE_PATH=$DIR/data/cuda-cache
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
systemctl enable --now mastergpu >/dev/null 2>&1
for i in $(seq 1 20); do curl -fs "http://127.0.0.1:$PORT/api/version" >/dev/null && break; sleep 1; done
CJ=$(mktemp); PASS=admin
if curl -fs -c "$CJ" -H 'Content-Type: application/json' -d '{"username":"admin","password":"admin"}' "http://127.0.0.1:$PORT/api/login" >/dev/null; then
  NP=$(tr -dc 'A-Za-z0-9' </dev/urandom | head -c 12)
  curl -fs -b "$CJ" -H 'Content-Type: application/json' -d "{\"current\":\"admin\",\"username\":\"admin\",\"password\":\"$NP\"}" "http://127.0.0.1:$PORT/api/me/password" >/dev/null && PASS=$NP
else
  PASS="(unchanged)"
fi
rm -f "$CJ"
IP=$(curl -fs4 --max-time 5 ifconfig.me 2>/dev/null || hostname -I | awk '{print $1}')
echo "Master GPU: http://$IP:$PORT    user: admin  pass: $PASS"
echo "Updates: Settings > System > Check for updates (GitHub releases)"
