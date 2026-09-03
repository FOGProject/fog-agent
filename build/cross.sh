#!/usr/bin/env bash
# Builds fog-agent for every target in the design's platform matrix
# (docs/design/0001-architecture.md section 8) into ./dist. CGO is off so a
# single Linux box produces all of them; if a target ever needs CGO this
# script is where that would show up as a failure.
set -euo pipefail
cd "$(dirname "$0")/.."
version="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
targets=(
  windows/amd64 windows/arm64 windows/386
  linux/amd64 linux/arm64 linux/arm
  darwin/amd64 darwin/arm64
)
mkdir -p dist
for t in "${targets[@]}"; do
  os="${t%/*}"; arch="${t#*/}"
  out="dist/fog-agent-${os}-${arch}"
  [[ $os == windows ]] && out+=".exe"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" GOARM=7 \
    go build -trimpath -ldflags "-s -w -X main.Version=${version}" -o "$out" ./cmd/fog-agent
  printf '%-16s %8s bytes\n' "$t" "$(stat -c %s "$out")"
done
