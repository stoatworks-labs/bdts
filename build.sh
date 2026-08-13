#!/usr/bin/env bash
# Cross-compile bdts for the BirdDog PLAY (aarch64 Linux, Debian 10).
#
# Like bdplay and unlike bdkvm/bdcam, this needs no cgo and therefore no zig:
# bdts dlopens nothing, it drives the tailscale CLI and systemctl as
# subprocesses. So CGO_ENABLED=0 gives a static binary that runs on the device's
# 2019-vintage glibc with no toolchain pinning at all.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$HERE"

command -v go >/dev/null || { echo "error: go not found" >&2; exit 1; }

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"

# The panel is embedded into the binary, so a stale asset is not a possible
# failure mode — but a broken one is, and it is cheaper to find out here.
go vet ./...
go test ./...

# The panel is a plain script with no build step, so nothing else would catch a
# syntax error in it until a browser silently rendered an empty section on a
# device. Check the RENDERED asset, not the source: the port substitution is
# what turns __BDTS_API_PORT__ into something a parser will accept.
if command -v node >/dev/null; then
  RENDERED="$(mktemp -t bdts-panel).js"
  trap 'rm -f "$RENDERED"' EXIT
  sed 's/__BDTS_API_PORT__/8092/g' web/bdts-tailscale.js > "$RENDERED"
  node --check "$RENDERED" || { echo "error: the panel script does not parse" >&2; exit 1; }
  echo "panel:   web/bdts-tailscale.js parses"
else
  echo "note: node not found, skipping the panel syntax check" >&2
fi

mkdir -p dist
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
  -o dist/bdts-linux-arm64 .

echo "built:   dist/bdts-linux-arm64"
echo "version: ${VERSION}"
file dist/bdts-linux-arm64
echo "size:    $(du -h dist/bdts-linux-arm64 | cut -f1)"
