#!/bin/bash
# Builds the bundled FFmpeg (NVENC SDK 12.2 => driver >= 550) inside ubuntu:22.04:
#   docker run --rm -v $PWD/ffbuild:/b ubuntu:22.04 bash /b/build-ffmpeg.sh
# /b must contain ffmpeg-<ver>/ (release tarball) and nvh122/ (nv-codec-headers sdk/12.2).
set -e
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq && apt-get install -y -qq build-essential nasm pkg-config clang llvm libx264-dev libx265-dev libnuma-dev libmp3lame-dev libopus-dev libssl-dev libsrt-openssl-dev libfreetype-dev libass-dev libfontconfig-dev libfribidi-dev zlib1g-dev >/dev/null
cd /b/nvh122 && make install PREFIX=/usr >/dev/null
L=/usr/lib/x86_64-linux-gnu; for l in x264 x265 srt-openssl numa; do rm -f $L/lib$l.so; done
cd /b/ffmpeg-9.1
./configure --prefix=/b/out --extra-version=mastergpu --pkg-config-flags=--static --extra-ldflags="-static-libgcc -static-libstdc++" --enable-gpl --enable-version3 --disable-doc --disable-ffplay --disable-debug \
 --enable-libx264 --enable-libx265 --enable-libmp3lame --enable-libopus --enable-libsrt --enable-openssl \
 --enable-libfreetype --enable-libass --enable-libfontconfig --enable-libfribidi \
 --enable-ffnvcodec --enable-nvenc --enable-nvdec --enable-cuvid --enable-cuda-llvm > /b/configure.log 2>&1 || { tail -30 /b/configure.log ffbuild/config.log; exit 1; }
make -j3 >/b/make.log 2>&1 && make install >/dev/null && strip /b/out/bin/ffmpeg /b/out/bin/ffprobe
ldd /b/out/bin/ffmpeg | awk '{print $1}' | tr '\n' ' '; echo; /b/out/bin/ffmpeg -hide_banner -encoders | grep -E "nvenc|libx26" ; echo BUILD-DONE
