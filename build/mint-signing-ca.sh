#!/bin/bash
#
# Mint a FOG Agent release signing CA: a root that gets compiled into the
# agent, and a short-lived leaf that actually signs release manifests.
#
#   WHAT:  creates root.key/root.crt and leaf.key/leaf.crt under a signing
#          directory, and optionally installs the root into
#          internal/release/roots.pem so the next build trusts it.
#   WHY:   design 0015 section 3. The agent verifies a release manifest
#          against a root compiled into it, so neither the FOG server that
#          asked for the update nor the mirror that served the bytes has to
#          be trusted. The root is the thing that must be kept safe; the
#          leaf is deliberately short-lived and reissuable, so a leaked
#          leaf costs a reissue and touches no deployed agent.
#   NEEDS: openssl. Nothing else.
#   TRAP:  the leaf MUST carry extendedKeyUsage=codeSigning. The agent pins
#          it, so that a certificate issued under the same root for
#          anything else -- and FOG's PKI issues plenty -- cannot sign a
#          release. A leaf without it verifies nowhere and the failure
#          looks like a bad signature.
#   TRAP:  --install rewrites a tracked file. Do not commit a lab root.
#
# Usage:
#   build/mint-signing-ca.sh [--dir DIR] [--install] [--leaf-only]
#
#   --dir DIR     where the CA lives. Default $FOG_SIGNING_DIR, else
#                 $HOME/.fog-agent-signing
#   --install     write the root into internal/release/roots.pem
#   --leaf-only   reissue the leaf under the existing root, nothing else
#   --install-only  install the existing root and mint nothing
#
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
dir=${FOG_SIGNING_DIR:-$HOME/.fog-agent-signing}
install=0
leaf_only=0
install_only=0

while [ $# -gt 0 ]; do
    case "$1" in
        --dir) dir=$2; shift 2 ;;
        --install) install=1; shift ;;
        --leaf-only) leaf_only=1; shift ;;
        --install-only) install=1; install_only=1; shift ;;
        -h|--help) sed -n '2,30p' "${BASH_SOURCE[0]}"; exit 0 ;;
        *) echo "unknown argument: $1" >&2; exit 2 ;;
    esac
done

mkdir -p "$dir"
chmod 700 "$dir"

# --install-only skips every mint. A release build installs the root it was
# given; it does not get to create one, and coupling the two would mean the
# only way to install a root is to make a new one.
if [ "$install_only" -eq 1 ]; then
    [ -e "$dir/root.crt" ] || { echo "no root at $dir/root.crt" >&2; exit 1; }
    roots=$here/internal/release/roots.pem
    { grep '^#' "$roots" || true; echo; cat "$dir/root.crt"; } > "$roots.new"
    mv "$roots.new" "$roots"
    echo "installed $dir/root.crt into $roots"
    echo "root fingerprint: $(openssl x509 -in "$dir/root.crt" -noout -fingerprint -sha256 | cut -d= -f2)"
    echo "DO NOT COMMIT a lab root."
    exit 0
fi

# Twenty years on the root, one year on the leaf. The root's expiry is
# far out because replacing it means replacing every deployed agent by
# hand; the leaf is short because it lives in CI and expiry is the only
# revocation this PKI has.
#
# It was ninety days until 2026-09-07. That was chosen when an expired
# leaf was believed to cost only a reissue -- but the agent checked the
# certificate chain against its OWN clock, so the day the leaf died every
# manifest it had ever signed died with it, and any host mid rollout
# reported "signature_invalid", naming the wrong cause. The agent now
# judges the chain against the manifest's `signed` time, which makes
# rotation the no-op this file always claimed it was, and makes a longer
# leaf safe: a manifest signed while the leaf was good stays good for its
# own stated life, and nothing NEW can be signed once the leaf is dead.
#
# A year is the balance Tom picked on 2026-09-07: short enough that a
# leaked leaf stops working on a horizon anyone can wait out, long enough
# that reissuing is an annual chore rather than a quarterly one. It pairs
# with maxSignatureAge in internal/release/release.go, which bounds how
# long a leaked leaf can keep being useful by backdating.
root_days=7300
leaf_days=365

if [ "$leaf_only" -eq 0 ]; then
    if [ -e "$dir/root.key" ]; then
        echo "refusing to overwrite $dir/root.key -- use --leaf-only to reissue the leaf" >&2
        exit 1
    fi
    echo "== root, ${root_days} days"
    openssl ecparam -name prime256v1 -genkey -noout -out "$dir/root.key"
    chmod 600 "$dir/root.key"
    openssl req -new -x509 -key "$dir/root.key" -sha256 -days "$root_days" \
        -out "$dir/root.crt" \
        -subj "/O=FOG Project/CN=FOG Agent Signing CA" \
        -addext "basicConstraints=critical,CA:TRUE,pathlen:0" \
        -addext "keyUsage=critical,keyCertSign,cRLSign"
fi

if [ ! -e "$dir/root.key" ]; then
    echo "no root at $dir/root.key -- run without --leaf-only first" >&2
    exit 1
fi

echo "== leaf, ${leaf_days} days"
openssl ecparam -name prime256v1 -genkey -noout -out "$dir/leaf.key"
chmod 600 "$dir/leaf.key"
openssl req -new -key "$dir/leaf.key" -out "$dir/leaf.csr" \
    -subj "/O=FOG Project/CN=fog-agent release signing"
# The extensions are given to the CA, not taken from the CSR: a CSR asks,
# a CA decides. -copy_extensions is deliberately absent.
openssl x509 -req -in "$dir/leaf.csr" -CA "$dir/root.crt" -CAkey "$dir/root.key" \
    -CAcreateserial -sha256 -days "$leaf_days" -out "$dir/leaf.crt" \
    -extfile <(printf '%s\n' \
        "basicConstraints=critical,CA:FALSE" \
        "keyUsage=critical,digitalSignature" \
        "extendedKeyUsage=critical,codeSigning")
rm -f "$dir/leaf.csr"

# Read back what was actually issued rather than what was asked for. A
# leaf missing codeSigning verifies nowhere, and the symptom is a bad
# signature, which names the wrong thing.
if ! openssl x509 -in "$dir/leaf.crt" -noout -text | grep -q "Code Signing"; then
    echo "the issued leaf does not carry codeSigning -- it would be refused" >&2
    exit 1
fi
# Chain only. `-purpose codesign` was here and is NOT portable: the purpose
# name was added in OpenSSL 3.4, so every 3.0 and 3.2 -- which is what
# ubuntu-latest and most distributions still ship -- fails this with
# "Invalid purpose codesign" and reports it as the leaf not verifying,
# naming the wrong thing in exactly the way the comment above warns about.
# Nothing is lost by dropping it: the EKU is asserted directly by the check
# above, on the certificate as issued, and that check works on every
# OpenSSL. The two together say what one flag said.
if ! openssl verify -CAfile "$dir/root.crt" "$dir/leaf.crt" >/dev/null; then
    echo "the issued leaf does not verify against the root" >&2
    exit 1
fi

echo
echo "root:  $dir/root.crt"
echo "leaf:  $dir/leaf.crt  (expires $(openssl x509 -in "$dir/leaf.crt" -noout -enddate | cut -d= -f2))"
echo "root fingerprint: $(openssl x509 -in "$dir/root.crt" -noout -fingerprint -sha256 | cut -d= -f2)"

if [ "$install" -eq 1 ]; then
    roots=$here/internal/release/roots.pem
    # Keep the file's explanatory header; replace only the certificates.
    { grep '^#' "$roots" || true; echo; cat "$dir/root.crt"; } > "$roots.new"
    mv "$roots.new" "$roots"
    echo
    echo "installed into $roots -- the next build will trust this root."
    echo "DO NOT COMMIT a lab root."
fi
