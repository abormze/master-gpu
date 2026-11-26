#!/bin/sh
# Publishes the bundled FFmpeg build (from build-ffmpeg.sh output) as release "ffmpeg-<ver>".
# Usage: scripts/release-ffmpeg.sh <ver> <dir with ffmpeg and ffprobe>
set -e
V=$1; SRC=$2; REPO=abormze/master-gpu
N=ffmpeg-$V-mastergpu-linux-amd64.tar.gz
mkdir -p dist
tar -C "$SRC" -czf "dist/$N" ffmpeg ffprobe
(cd dist && sha256sum "$N" > "$N.sha256")
gh release create "ffmpeg-$V" "dist/$N" "dist/$N.sha256" -R "$REPO" --latest=false \
  --title "FFmpeg $V for Master GPU" --notes "FFmpeg $V built with NVENC/NVDEC (SDK 12.2, NVIDIA driver >= 550), x264, x265, SRT, OpenSSL, libass, drawtext, DASH. Downloaded automatically by Master GPU >= 1.0.7 into /opt/mastergpu/ffmpeg/$V."
