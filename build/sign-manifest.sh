#!/bin/bash
#
# Build a release manifest from built artifacts and sign it.
#
#   WHAT:  writes manifest.json (channel, sequence, expiry, and every
#          artifact's sha256) and manifest.json.sig (the envelope the agent
#          verifies: leaf certificate, algorithm, signature).
#   WHY:   design 0015 section 3. The manifest is the only thing the
#          signing key ever signs, and a verified manifest plus a matching
#          hash is a verified artifact. The agent checks it against a root
#          compiled into itself, so the server and the mirror are both
#          untrusted.
#   NEEDS: openssl, and a leaf from build/mint-signing-ca.sh.
#   TRAP:  the signature is over the bytes of manifest.json exactly as
#          written. Reformatting it afterwards -- jq, an editor, a CI step
#          that pretty-prints -- invalidates the signature, and the agent
#          will report signature_invalid rather than anything that says so.
#   TRAP:  sequence must never go backwards. The agent refuses a manifest
#          older than the newest it has already accepted, which is what
#          stops a mirror replaying an old release to hold a fleet on a
#          version with a known hole.
#
# Usage:
#   build/sign-manifest.sh --version 0.4.2 --sequence 47 \
#       --url-base https://releases.fogproject.org/agent/v0.4.2 \
#       [--dir DIR] [--out DIR] [--channel stable] [--days 90] [--security]
#       FILE...
#
# Each FILE is named <anything>-<goos>-<goarch>[.exe] or is an .msi, which
# is how build/cross.sh already names them.
#
set -euo pipefail

signing=${FOG_SIGNING_DIR:-$HOME/.fog-agent-signing}
out=dist
channel=stable
days=90
security=false
version=
sequence=
urlbase=

while [ $# -gt 0 ]; do
    case "$1" in
        --version)  version=$2; shift 2 ;;
        --sequence) sequence=$2; shift 2 ;;
        --url-base) urlbase=${2%/}; shift 2 ;;
        --dir)      signing=$2; shift 2 ;;
        --out)      out=$2; shift 2 ;;
        --channel)  channel=$2; shift 2 ;;
        --days)     days=$2; shift 2 ;;
        --security) security=true; shift ;;
        -h|--help)  sed -n '2,30p' "${BASH_SOURCE[0]}"; exit 0 ;;
        -*) echo "unknown argument: $1" >&2; exit 2 ;;
        *) break ;;
    esac
done

[ -n "$version" ]  || { echo "--version is required" >&2; exit 2; }
[ -n "$sequence" ] || { echo "--sequence is required" >&2; exit 2; }
[ -n "$urlbase" ]  || { echo "--url-base is required" >&2; exit 2; }
[ $# -gt 0 ]       || { echo "no artifacts given" >&2; exit 2; }
[ -e "$signing/leaf.key" ] || { echo "no signing leaf at $signing/leaf.key" >&2; exit 2; }

mkdir -p "$out"
manifest=$out/manifest.json
envelope=$out/manifest.json.sig

# platform_of maps a built filename to GOOS/GOARCH. cross.sh names files
# fog-agent-<goos>-<goarch>[.exe]; the MSI is Windows amd64 by
# construction and is named for its architecture instead.
platform_of() {
    local base=${1##*/}
    case "$base" in
        *.msi) echo "windows amd64" ;;
        *)
            base=${base%.exe}
            local arch=${base##*-}
            local rest=${base%-*}
            local os=${rest##*-}
            echo "$os $arch"
            ;;
    esac
}

artifacts=""
for f in "$@"; do
    [ -f "$f" ] || { echo "not a file: $f" >&2; exit 1; }
    read -r os arch <<<"$(platform_of "$f")"
    case "$os" in
        linux|windows|darwin) ;;
        *) echo "cannot tell the platform of $f (read '$os/$arch')" >&2; exit 1 ;;
    esac
    sum=$(sha256sum "$f" | cut -d' ' -f1)
    size=$(stat -c %s "$f")
    entry=$(printf '{"os":"%s","arch":"%s","sha256":"%s","size":%s,"url":"%s/%s"}' \
        "$os" "$arch" "$sum" "$size" "$urlbase" "${f##*/}")
    if [ -n "$artifacts" ]; then artifacts="$artifacts,$entry"; else artifacts="$entry"; fi
    echo "  $os/$arch  $sum  ${f##*/}"
done

expires=$(date -u -d "+$days days" +%Y-%m-%dT%H:%M:%SZ)
notes="https://github.com/FOGProject/fog-agent/releases/tag/v$version"

# Written in one shot and never touched again: the signature below is over
# these exact bytes.
printf '{"channel":"%s","sequence":%s,"expires":"%s","versions":{"%s":{"security":%s,"notes":"%s","artifacts":[%s]}}}' \
    "$channel" "$sequence" "$expires" "$version" "$security" "$notes" "$artifacts" > "$manifest"

sig=$(openssl dgst -sha256 -sign "$signing/leaf.key" "$manifest" | openssl base64 -A)
leaf=$(awk '/BEGIN CERTIFICATE/,/END CERTIFICATE/' "$signing/leaf.crt" | sed ':a;N;$!ba;s/\n/\\n/g')
printf '{"chain":["%s\\n"],"alg":"ecdsa-p256-sha256","sig":"%s"}' "$leaf" "$sig" > "$envelope"

# Prove the signature verifies before handing it over, with openssl rather
# than by assertion. A manifest that does not verify is worse than none:
# it reaches every agent and every one of them reports a signature failure.
if ! openssl dgst -sha256 -verify <(openssl x509 -in "$signing/leaf.crt" -pubkey -noout) \
        -signature <(openssl base64 -d -A <<<"$sig") "$manifest" >/dev/null; then
    echo "the signature just written does not verify" >&2
    exit 1
fi

echo
echo "manifest: $manifest ($(stat -c %s "$manifest") bytes, sequence $sequence, expires $expires)"
echo "envelope: $envelope"
