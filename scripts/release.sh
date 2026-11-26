#!/bin/sh
# Build and publish a GitHub release that panels pick up via "Check for updates".
# usage: sh scripts/release.sh "release notes"   (version is internal/version.Version)
set -e
cd "$(dirname "$0")/.."
V=$(sed -n 's/.*Version = "\(.*\)"/\1/p' internal/version/version.go)
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X gpupanel/internal/version.Build=$(git rev-parse --short HEAD)" -o dist/mastergpu-linux-amd64 ./cmd/mastergpu
./dist/mastergpu-linux-amd64 -version
gh release create "v$V" dist/mastergpu-linux-amd64 scripts/install.sh --title "Master GPU v$V" --notes "${1:-Master GPU v$V}"
