#!/usr/bin/env bash
# Builds the Windows installer: cross-compiles the agent and packages it
# with wixl (msitools, `dnf install msitools`). No Windows needed.
#
#   build/msi.sh [version]
#
# The exe is stamped with the full `git describe`; the MSI needs a numeric
# x.y.z ProductVersion, so a tag v1.2.3 becomes 1.2.3 and anything else
# becomes 0.0.<commit count> so every dev build still upgrades the last.
set -euo pipefail
cd "$(dirname "$0")/.."
version="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
if [[ $version =~ ^v?([0-9]+\.[0-9]+\.[0-9]+) ]]; then
    msiver="${BASH_REMATCH[1]}"
else
    msiver="0.0.$(git rev-list --count HEAD 2>/dev/null || echo 0)"
fi
mkdir -p dist
exe="dist/fog-agent-windows-amd64.exe"
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
    go build -trimpath -ldflags "-s -w -X main.Version=${version}" -o "$exe" ./cmd/fog-agent
out="dist/fog-agent-${msiver}-x64.msi"
wixl -a x64 -D "Version=${msiver}" -D "Exe=${exe}" -o "$out" build/msi/fog-agent.wxs
echo "$out"
