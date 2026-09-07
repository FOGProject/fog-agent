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
#       [--merge PREVIOUS.json] FILE...
#
#   --merge PREVIOUS.json  carry the versions already in PREVIOUS forward, so
#          this manifest offers them too. WITHOUT IT A MANIFEST DESCRIBES ONE
#          VERSION, and Manifest.Find() looks the version up by exact key --
#          so a server naming any release but the newest gets no_artifact,
#          and the downgrade that design 0015 sections 9 and 11 make the
#          fleet-wide recovery from a bad build cannot be expressed at all.
#          Needs jq. An absent or empty PREVIOUS is not an error: that is
#          the first release.
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
merge=

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
        --merge)    merge=$2; shift 2 ;;
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
# fog-agent-<goos>-<goarch>[.exe].
#
# The MSI is deliberately NOT here. An artifact in this manifest is the
# thing the agent RENAMES OVER ITS OWN BINARY (update.go swap()), and an
# installer is not that. It used to map to windows/amd64, which put two
# entries under that platform alongside fog-agent-windows-amd64.exe --
# and Manifest.Find() returns the FIRST match, ordered by however the
# caller listed the files. So a Windows amd64 agent could be handed the
# installer, verify its hash perfectly well because the hash was correct,
# swap an 11 MB MSI in as fog-agent.exe, fail to start, and revert. Not a
# hole -- the signature and hash both hold -- but a guaranteed failed
# update on the commonest Windows platform, decided by directory order.
#
# The MSI is still published as a release asset. It is how a machine gets
# the agent in the first place; it is not how an agent replaces itself.
platform_of() {
    local base=${1##*/}
    case "$base" in
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
seen=" "
for f in "$@"; do
    [ -f "$f" ] || { echo "not a file: $f" >&2; exit 1; }
    case "${f##*/}" in
        *.msi)
            echo "  skipping ${f##*/}: an installer is not a self-update artifact"
            continue
            ;;
    esac
    read -r os arch <<<"$(platform_of "$f")"
    case "$os" in
        linux|windows|darwin) ;;
        *) echo "cannot tell the platform of $f (read '$os/$arch')" >&2; exit 1 ;;
    esac
    # One artifact per platform, enforced rather than assumed. Manifest.Find()
    # takes the first match and has no way to prefer one of two, so a
    # duplicate does not fail loudly at the agent -- it silently makes which
    # file a fleet installs depend on the order these arguments arrived in.
    case "$seen" in
        *" $os/$arch "*)
            echo "two artifacts claim $os/$arch; the agent would take whichever came first" >&2
            exit 1
            ;;
    esac
    seen="$seen$os/$arch " 
    sum=$(sha256sum "$f" | cut -d' ' -f1)
    size=$(stat -c %s "$f")
    entry=$(printf '{"os":"%s","arch":"%s","sha256":"%s","size":%s,"url":"%s/%s"}' \
        "$os" "$arch" "$sum" "$size" "$urlbase" "${f##*/}")
    if [ -n "$artifacts" ]; then artifacts="$artifacts,$entry"; else artifacts="$entry"; fi
    echo "  $os/$arch  $sum  ${f##*/}"
done

expires=$(date -u -d "+$days days" +%Y-%m-%dT%H:%M:%SZ)
# When this was signed. The agent checks the signing certificate's validity
# against THIS, not against its own clock, so a manifest keeps verifying for
# its whole life even if the leaf is rotated or expires in the meantime.
signed=$(date -u +%Y-%m-%dT%H:%M:%SZ)
notes="https://github.com/FOGProject/fog-agent/releases/tag/v$version"

# Refuse to sign with a leaf that is dead or nearly so.
#
# Signing with an expired leaf produces a manifest that looks perfect here
# and is refused by every agent as "signature_invalid: not signed by a key
# this build trusts" -- which names the wrong cause and sends whoever is
# debugging it hunting a compromised key. Better to fail in CI, where the
# message can say what actually happened.
#
# The margin is the manifest's own lifetime: a leaf that dies before the
# manifest does still yields a manifest good for its full stated life
# (that is the point of `signed`), but it means the NEXT release cannot be
# signed, and finding that out at release time is too late.
leaf_end=$(openssl x509 -in "$signing/leaf.crt" -noout -enddate | cut -d= -f2)
leaf_end_s=$(date -u -d "$leaf_end" +%s)
now_s=$(date -u +%s)
if [ "$leaf_end_s" -le "$now_s" ]; then
    echo "The signing leaf expired on $leaf_end." >&2
    echo "Reissue it (build/mint-signing-ca.sh --leaf-only), update the two" >&2
    echo "repository secrets, and run this again. Manifests already published" >&2
    echo "are unaffected and keep verifying." >&2
    exit 2
fi
if [ "$leaf_end_s" -le "$(( now_s + days * 86400 ))" ]; then
    echo "  WARNING: the signing leaf expires $leaf_end, inside this" >&2
    echo "  manifest's own ${days}-day life. This manifest is fine; the next" >&2
    echo "  one will not be. Reissue the leaf soon." >&2
fi

# Written in one shot and never touched again: the signature below is over
# these exact bytes. Whichever branch writes it, nothing reformats it after.
entry=$(printf '{"security":%s,"notes":"%s","artifacts":[%s]}' "$security" "$notes" "$artifacts")

if [ -n "$merge" ] && [ -s "$merge" ]; then
    command -v jq >/dev/null 2>&1 || { echo "--merge needs jq" >&2; exit 2; }
    # Every version is kept, not the newest N. A trim needs an ordering, and
    # a string sort puts 0.1.10 below 0.1.9 -- which would silently drop the
    # newest release. Growth is about 1.8 KB per release, so a hundred of
    # them is a 180 KB file fetched once a poll interval by hosts that are
    # behind. A version that must never be installed again is removed by
    # editing the published manifest before the next merge, deliberately and
    # by a person, rather than by a rule in here.
    jq -c -n --slurpfile prev "$merge" \
        --arg channel "$channel" --argjson sequence "$sequence" \
        --arg expires "$expires" --arg signed "$signed" --arg version "$version" \
        --argjson entry "$entry" '
        {channel: $channel, sequence: $sequence, expires: $expires,
         signed: $signed,
         versions: (($prev[0].versions // {}) + {($version): $entry})}
        ' > "$manifest"
    kept=$(jq -r '.versions | keys | length' "$manifest")
    echo "  carried $((kept - 1)) earlier version(s) forward from ${merge##*/}"
else
    printf '{"channel":"%s","sequence":%s,"expires":"%s","signed":"%s","versions":{"%s":%s}}' \
        "$channel" "$sequence" "$expires" "$signed" "$version" "$entry" > "$manifest"
fi

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
