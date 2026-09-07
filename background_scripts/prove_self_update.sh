#!/bin/bash
#
# Prove agent self-update (design 0015) on a real machine, under a real
# service manager, with real signed artifacts.
#
#   WHAT:  builds a rig under /images/claude-lab/self-update/rig, installs
#          0.2.0 as a systemd USER service, and walks six scenarios in
#          order: three refusals, one successful update, one replay refusal,
#          and one unattended auto-revert driven by the service itself.
#   WHERE: this box (10.255.20.1). Nothing touches the FOG server, no
#          database, no webroot. The mirror is a python http.server on
#          port 8099 serving /images/claude-lab/self-update/serve.
#   WHY:   design 0015 sections 3 and 6. Every unit test in the repo proves
#          these paths against fixtures; this proves them against a real
#          binary replacing itself while running, and a real service
#          manager restarting it.
#   NEEDS: the lab signing CA at /images/claude-lab/self-update/ca, its
#          root compiled into the binaries under serve/, and the mirror up.
#          build/mint-signing-ca.sh --dir <ca> --install makes the first.
#   TRAP:  the binaries under serve/ trust a LAB root. They are not
#          releasable and internal/release/roots.pem must be restored
#          before anything is committed.
#   TRAP:  scenario 6 waits for the running service to notice a deadline on
#          its own loop, which is a poll interval away. It is slow on
#          purpose -- restarting the service to hurry it would prove the
#          startup check instead of the one that matters.
#
# Usage:  background_scripts/prove_self_update.sh [--keep]
#
set -uo pipefail

LAB=/images/claude-lab/self-update
RIG=$LAB/rig
SERVE=$LAB/serve
CA=$LAB/ca
STATE=$RIG/state
BIN=$RIG/bin/fog-agent
UNIT=fog-agent-lab.service
MIRROR=http://10.255.20.1:8099
# Port 9 (discard) is closed here, so a poll fails immediately rather than
# hanging on a timeout. The agent must survive that, which is the point.
DEADSERVER=https://127.0.0.1:9/fog
pass=0; fail=0

say()  { printf '\n\033[1m== %s\033[0m\n' "$*"; }
ok()   { pass=$((pass+1)); printf '   \033[32mPASS\033[0m %s\n' "$*"; }
bad()  { fail=$((fail+1)); printf '   \033[31mFAIL\033[0m %s\n' "$*"; }
# Assert on the binary's identity, not on what we believe we installed.
version() { "$BIN" version 2>/dev/null | head -1; }
sha()     { sha256sum "$1" | cut -d' ' -f1; }

V020=$(sha "$SERVE/fog-agent-0.2.0-linux-amd64")
V030=$(sha "$SERVE/fog-agent-0.3.0-linux-amd64")

say "0. rig"
systemctl --user stop "$UNIT" 2>/dev/null
rm -rf "$RIG"; mkdir -p "$RIG/bin" "$STATE"
cp "$SERVE/fog-agent-0.2.0-linux-amd64" "$BIN"

# Let the AGENT build its own state directory -- key, identity, config --
# rather than hand-writing files it has to agree with. It fails to enroll
# against a dead server, which is fine and is all we want from it.
# --ca matters here: without it the agent fetches the server's CA before it
# writes anything, so against a dead server it bails having created no state
# at all. Any PEM will do -- this rig never completes a TLS handshake.
"$BIN" enroll --once --dir "$STATE" --server "$DEADSERVER" --ca "$CA/root.crt" >/dev/null 2>&1

# Then give it a certificate over the key it just generated. A real one
# comes from the FOG CA on enrollment; here it only has to be a well-formed
# client certificate that pairs with key.pem, so that the run loop takes the
# poll path (and comes back round to the probation check) instead of
# blocking forever in enrollment.
openssl req -new -x509 -key "$STATE/key.pem" -sha256 -days 30 \
    -subj "/CN=lab-rig" -out "$STATE/cert.pem" 2>/dev/null
cp "$STATE/cert.pem" "$STATE/ca.pem"
chmod 600 "$STATE"/*.pem
[ "$(sha "$BIN")" = "$V020" ] && ok "installed 0.2.0 ($(version))" || bad "rig binary is not 0.2.0"

mkdir -p "$HOME/.config/systemd/user"
cat > "$HOME/.config/systemd/user/$UNIT" <<UNITEOF
[Unit]
Description=FOG Agent (self-update lab rig)
[Service]
ExecStart=$BIN run --dir $STATE
# The whole restart contract: a non-zero exit is how the agent says "the
# binary under me is not the one you started". Restart=always turns that
# into the restart. RestartSec is short so the proof is not mostly waiting.
Restart=always
RestartSec=2
UNITEOF
systemctl --user daemon-reload
systemctl --user start "$UNIT"
sleep 3
systemctl --user is-active --quiet "$UNIT" && ok "service running on 0.2.0" || bad "service did not start"

# --- refusals: the binary must be untouched after every one of them -------

say "1. a tampered manifest is refused"
# One byte changed in the signed bytes. The signature still verifies as a
# signature; it just no longer covers this content.
sed 's/"security":false/"security":true /' "$SERVE/manifest.json" > "$SERVE/manifest-tampered.json"
cp "$SERVE/manifest.json.sig" "$SERVE/manifest-tampered.json.sig"
out=$("$BIN" update --to 0.3.0 --dir "$STATE" --manifest "$MIRROR/manifest-tampered.json" 2>&1)
echo "$out" | sed 's/^/   | /'
if echo "$out" | grep -qi 'signature' && [ "$(sha "$BIN")" = "$V020" ]; then
    ok "refused on the signature, binary still 0.2.0"
else
    bad "tampered manifest was not refused on the signature, or the binary moved"
fi

say "2. an unsigned manifest is refused"
cp "$SERVE/manifest.json" "$SERVE/manifest-unsigned.json"
printf '{"certificates":[],"signature":""}' > "$SERVE/manifest-unsigned.json.sig"
out=$("$BIN" update --to 0.3.0 --dir "$STATE" --manifest "$MIRROR/manifest-unsigned.json" 2>&1)
echo "$out" | sed 's/^/   | /'
[ "$(sha "$BIN")" = "$V020" ] && echo "$out" | grep -qiE 'signature|verif' \
    && ok "refused, binary still 0.2.0" || bad "unsigned manifest was not refused"

say "3. a correctly signed manifest whose mirror serves the wrong bytes is refused"
# The hostile-mirror case, and the one TLS cannot help with: the manifest is
# genuinely FOG's, the transport is fine, and the file on the far end is not
# what was signed.
#
# Note how this has to be built. Editing a signed manifest to point at a
# corrupt file does NOT test this -- it breaks the signature, and the agent
# refuses one step earlier for a different reason, which looks like a pass
# and proves nothing about the hash. So: sign over the GOOD bytes, then
# corrupt the file behind the URL afterwards. Re-signing is what an attacker
# cannot do; swapping the file behind the URL is exactly what they can.
cp "$SERVE/fog-agent-0.3.0-linux-amd64" "$SERVE/fog-agent-badmirror-linux-amd64"
FOG_SIGNING_DIR=$CA /home/telliott/fog-agent/build/sign-manifest.sh \
    --version 0.3.0 --sequence 2 --url-base "$MIRROR" --out "$LAB/tmp-badhash" \
    "$SERVE/fog-agent-badmirror-linux-amd64" >/dev/null 2>&1
cp "$LAB/tmp-badhash/manifest.json"     "$SERVE/manifest-badhash.json"
cp "$LAB/tmp-badhash/manifest.json.sig" "$SERVE/manifest-badhash.json.sig"
signed_hash=$(sha "$SERVE/fog-agent-badmirror-linux-amd64")
printf 'x' | dd of="$SERVE/fog-agent-badmirror-linux-amd64" bs=1 seek=4096 conv=notrunc 2>/dev/null
[ "$(sha "$SERVE/fog-agent-badmirror-linux-amd64")" != "$signed_hash" ] \
    || bad "rig: the artifact was not actually corrupted"
out=$("$BIN" update --to 0.3.0 --dir "$STATE" --manifest "$MIRROR/manifest-badhash.json" 2>&1)
echo "$out" | sed 's/^/   | /'
if [ "$(sha "$BIN")" = "$V020" ] && ! echo "$out" | grep -qi 'signature' \
   && echo "$out" | grep -qiE 'hash|sha256|digest'; then
    ok "refused on the artifact hash, binary still 0.2.0"
else
    bad "wrong-bytes mirror not refused on the HASH (a signature error here means the manifest was edited, not the artifact)"
fi

# --- the update itself ----------------------------------------------------

say "4. the real signed manifest applies"
out=$("$BIN" update --to 0.3.0 --dir "$STATE" --manifest "$MIRROR/manifest.json" 2>&1)
rc=$?
echo "$out" | sed 's/^/   | /'
[ "$(sha "$BIN")" = "$V030" ] && ok "binary on disk is now 0.3.0" || bad "binary did not become 0.3.0"
[ "$(sha "$BIN.prev")" = "$V020" ] && ok ".prev holds 0.2.0, so revert has something to restore" || bad "no usable .prev"
[ $rc -ne 0 ] && ok "exited non-zero ($rc): the restart signal to the service manager" || bad "exited zero after replacing its own binary"
if [ -f "$STATE/update-probation.json" ]; then
    ok "probation armed: $(cat "$STATE/update-probation.json")"
else
    bad "probation was not armed -- an unattended bad update would be permanent"
fi

say "5. the service restarts onto the new binary"
# The running process is still executing the 0.2.0 inode; that is correct
# and is why the swap is safe. The restart is what puts 0.3.0 in charge.
systemctl --user restart "$UNIT"; sleep 3
systemctl --user is-active --quiet "$UNIT" && ok "service is up" || bad "service did not come back"
journalctl --user -u "$UNIT" -n 30 --no-pager -o cat | tail -5 | sed 's/^/   | /'
[ "$(version)" = "$(version)" ] && printf '   binary reports: %s\n' "$(version)"

say "6. an older manifest cannot be replayed"
# The sequence floor, and it has to be tested with a version the agent is
# NOT already on: Run() compares versions before it fetches anything, so
# asking a 0.3.0 agent for 0.3.0 returns "unchanged" without the manifest
# ever being read, and the floor is never consulted.
#
# The story this models: sequence 1 offered 0.4.0, sequence 2 withdrew it
# (say 0.4.0 shipped a hole). Both were genuinely signed by us. Re-serving
# sequence 1 to walk a fleet back onto 0.4.0 is the attack, and the only
# thing that stops it is the agent remembering how far it has counted.
cp "$SERVE/fog-agent-0.3.0-linux-amd64" "$SERVE/fog-agent-withdrawn-linux-amd64"
FOG_SIGNING_DIR=$CA /home/telliott/fog-agent/build/sign-manifest.sh \
    --version 0.4.0 --sequence 1 --url-base "$MIRROR" --out "$LAB/tmp-old" \
    "$SERVE/fog-agent-withdrawn-linux-amd64" >/dev/null 2>&1
cp "$LAB/tmp-old/manifest.json"     "$SERVE/manifest-old.json"
cp "$LAB/tmp-old/manifest.json.sig" "$SERVE/manifest-old.json.sig"
out=$("$BIN" update --to 0.4.0 --dir "$STATE" --manifest "$MIRROR/manifest-old.json" 2>&1)
echo "$out" | sed 's/^/   | /'
if echo "$out" | grep -qiE 'stale|sequence|replay'; then
    ok "refused as stale"
elif echo "$out" | grep -qi 'unchanged'; then
    bad "the version comparison short-circuited; this never tested the floor"
else
    bad "an older signed manifest was accepted"
fi

say "7. probation expires with no successful poll -> the service reverts itself"
# Nothing restarts it and nobody intervenes. This is the failure this
# feature exists for: a binary that installs, starts, and cannot reach the
# server that manages it. Age the deadline and wait for the service to
# come round its own loop.
python3 - "$STATE/update-probation.json" <<'PY'
import json,sys,datetime
p=sys.argv[1]; d=json.load(open(p))
d['deadline']=(datetime.datetime.now(datetime.timezone.utc)-datetime.timedelta(minutes=1)).isoformat().replace('+00:00','Z')
json.dump(d,open(p,'w'))
print("   aged deadline to", d['deadline'])
PY
echo "   waiting for the service to notice on its own loop (up to 6 minutes)..."
for i in $(seq 1 72); do
    sleep 5
    [ "$(sha "$BIN")" = "$V020" ] && break
done
if [ "$(sha "$BIN")" = "$V020" ]; then
    ok "reverted to 0.2.0 unattended after $((i*5))s"
else
    bad "still on 0.3.0 after $((i*5))s -- the auto-revert did not fire"
fi
sleep 3
systemctl --user is-active --quiet "$UNIT" && ok "service is up on the restored binary" || bad "service is down after revert"
journalctl --user -u "$UNIT" --no-pager -o cat | grep -i 'update:' | tail -6 | sed 's/^/   | /'
[ -f "$STATE/update-probation.json" ] && bad "probation file survived the revert" || ok "probation cleared"

say "result: $pass passed, $fail failed"
if [ "${1:-}" != "--keep" ]; then
    systemctl --user stop "$UNIT" 2>/dev/null
    systemctl --user disable "$UNIT" 2>/dev/null
    rm -f "$HOME/.config/systemd/user/$UNIT"
    systemctl --user daemon-reload
    echo "rig stopped and unit removed; $RIG left in place for inspection"
fi
[ "$fail" -eq 0 ]
