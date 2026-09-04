#!/usr/bin/env bash
# Builds the Windows installer: cross-compiles the agent and packages it
# with wixl (msitools, `dnf install msitools`). No Windows needed.
#
#   build/msi.sh [version]
#
# The exe is stamped with the full `git describe`; the MSI needs a numeric
# x.y.z ProductVersion, so a tag v1.2.3 becomes 1.2.3 and anything else
# becomes 0.0.<commit count> so every dev build still upgrades the last.
#
# Two things this does beyond a plain wixl run, each a worked-around defect
# proven on a Windows 11 lab host (design 0005 section 4):
#
#  1. A VERSIONINFO resource is compiled into the exe (goversioninfo) AND
#     the File table's Version is set to match (msibuild). The Windows
#     Installer treats a versioned file as newer than an unversioned one,
#     so without this the MSI will NOT overwrite an fog-agent.exe left by a
#     hand install or a rolled-back attempt -- the Setup action then runs
#     the stale binary, which predates `setup`, and the install fails 1603
#     (the deferred action exits 2). Both halves are needed: wixl (msitools
#     0.106) does not read a PE version resource into File.Version, so the
#     stamp alone still leaves the package file unversioned to the engine.
#
#  2. The legacy client's Upgrade row is appended with msibuild AFTER wixl.
#     wixl (msitools 0.106) persists an Upgrade table the Windows engine
#     cannot load (error 2229 at FindRelatedProducts) once it holds more
#     than one UpgradeCode. wixl emits our own UpgradeCode fine; msibuild
#     rewrites the table cleanly with the legacy code added.
#
# Build deps: go, wixl + msibuild + msiinfo (msitools), and goversioninfo
# (`go install github.com/josephspurrier/goversioninfo/cmd/goversioninfo@latest`).
set -euo pipefail
cd "$(dirname "$0")/.."
version="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
if [[ $version =~ ^v?([0-9]+\.[0-9]+\.[0-9]+) ]]; then
    msiver="${BASH_REMATCH[1]}"
else
    msiver="0.0.$(git rev-list --count HEAD 2>/dev/null || echo 0)"
fi

# The legacy "FOG Service" MSI UpgradeCode, retired as a related product.
# Kept here next to the msibuild step that injects it; the wxs explains why
# it is not authored as a <Upgrade> element.
legacy_upgradecode="{1CCFDEAF-53E9-43AC-AE18-F9F86CEFA4EA}"

need() { command -v "$1" >/dev/null 2>&1 || { echo "build/msi.sh: missing $1 ($2)" >&2; exit 1; }; }
need wixl "dnf install msitools"
need msibuild "dnf install msitools"
need msiinfo "dnf install msitools"
goversioninfo="$(command -v goversioninfo || echo "$(go env GOPATH)/bin/goversioninfo")"
[[ -x $goversioninfo ]] || { echo "build/msi.sh: missing goversioninfo (go install github.com/josephspurrier/goversioninfo/cmd/goversioninfo@latest)" >&2; exit 1; }

mkdir -p dist

# 1. Stamp a version resource into the Windows exe. goversioninfo writes a
# .syso into the package dir, which `go build` links in; it is removed after
# so a plain dev `go build` stays unversioned and the tree stays clean.
syso="cmd/fog-agent/resource_windows_amd64.syso"
verjson="$(mktemp)"
IFS=. read -r vmaj vmin vpatch <<<"$msiver"
cat >"$verjson" <<JSON
{
  "FixedFileInfo": {
    "FileVersion": {"Major": ${vmaj}, "Minor": ${vmin}, "Patch": ${vpatch}, "Build": 0},
    "ProductVersion": {"Major": ${vmaj}, "Minor": ${vmin}, "Patch": ${vpatch}, "Build": 0}
  },
  "StringFileInfo": {
    "CompanyName": "FOG Project",
    "FileDescription": "FOG Agent",
    "InternalName": "fog-agent",
    "OriginalFilename": "fog-agent.exe",
    "ProductName": "FOG Agent",
    "ProductVersion": "${version}"
  },
  "VarFileInfo": {"Translation": {"LangID": "0409", "CharsetID": "04B0"}}
}
JSON
"$goversioninfo" -64 -o "$syso" -platform-specific=false "$verjson"
trap 'rm -f "$syso" "$verjson"' EXIT

exe="dist/fog-agent-windows-amd64.exe"
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
    go build -trimpath -ldflags "-s -w -X main.Version=${version}" -o "$exe" ./cmd/fog-agent
rm -f "$syso"

# 2. wixl builds the package; msibuild then rewrites the Upgrade table with
# the legacy row appended to whatever MajorUpgrade emitted.
out="dist/fog-agent-${msiver}-x64.msi"
wixl -a x64 -D "Version=${msiver}" -D "Exe=${exe}" -o "$out" build/msi/fog-agent.wxs

idt="$(mktemp)"
msiinfo export "$out" Upgrade >"$idt"
# The legacy row: detect versions 0.0.0..99.0.0 inclusive-min (256), no
# OnlyDetect so RemoveExistingProducts removes it; ActionProperty is the
# public property FindRelatedProducts sets and RemoveExistingProducts reads.
printf '%s\t0.0.0\t99.0.0\t\t256\t\tLEGACYFOGCLIENT\n' "$legacy_upgradecode" >>"$idt"
msibuild "$out" -i "$idt"
rm -f "$idt"

# Set File.Version to match the exe's stamped resource (x.y.z.0). wixl
# leaves it empty, which makes the engine treat the package exe as
# unversioned and skip overwriting an existing one -- see note 1 above.
fileidt="$(mktemp)"
msiinfo export "$out" File | awk -v v="${msiver}.0" 'BEGIN{FS=OFS="\t"} NR>3 && $1=="AgentExe"{$5=v} {print}' >"$fileidt"
msibuild "$out" -i "$fileidt"
rm -f "$fileidt"

# Prove the table the engine will read actually carries both UpgradeCodes,
# and that the exe is now versioned; a silent msibuild miss would ship the
# broken package again.
if [[ $(msiinfo export "$out" Upgrade | grep -c '{') -lt 2 ]]; then
    echo "build/msi.sh: the Upgrade table did not take the legacy row" >&2
    exit 1
fi
if ! msiinfo export "$out" File | awk 'BEGIN{FS="\t"} $1=="AgentExe"{exit ($5=="" ? 1 : 0)} END{}'; then
    echo "build/msi.sh: File.Version was not set on the agent exe" >&2
    exit 1
fi

echo "$out"
