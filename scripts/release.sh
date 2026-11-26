#!/bin/sh
# Build and publish a GitHub release that panels pick up via "Check for updates".
# usage: sh scripts/release.sh "release notes"   (version is internal/version.Version)
set -e
cd "$(dirname "$0")/.."
V=$(sed -n 's/.*Version = "\(.*\)"/\1/p' internal/version/version.go)
REPO=abormze/master-gpu
NOTES=${1:-Master GPU v$V}
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X gpupanel/internal/version.Build=$(git rev-parse --short HEAD)" -o dist/mastergpu-linux-amd64 ./cmd/mastergpu
./dist/mastergpu-linux-amd64 -version
SHA=$(sha256sum dist/mastergpu-linux-amd64 | cut -d' ' -f1)
python3 -c 'import json,sys; print(json.dumps({"version":sys.argv[1],"url":sys.argv[2],"sha256":sys.argv[3],"size":int(sys.argv[4]),"notes":sys.argv[5]}, ensure_ascii=False, indent=1))' \
  "$V" "https://github.com/$REPO/releases/download/v$V/mastergpu-linux-amd64" "$SHA" "$(stat -c %s dist/mastergpu-linux-amd64)" "$NOTES" > dist/latest.json
gh release create "v$V" dist/mastergpu-linux-amd64 dist/latest.json scripts/install.sh -R "$REPO" --title "Master GPU v$V" --notes "$NOTES"
