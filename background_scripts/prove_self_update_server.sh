#!/bin/bash
#
# The half of self-update that prove_self_update.sh cannot reach: the FOG
# server naming a version, and probation clearing on a real authenticated
# poll.
#
#   COSTS YOU NOTHING TO RUN, BUT IT IS BLOCKED UNTIL YOU DO ONE THING:
#   open https://10.255.20.1/fog/management/index.php?node=schema, sign in,
#   and run the updater once. Schema step 434 adds hostAgentDesiredVersion
#   and the two FOG_AGENT_* settings, and until it runs every FOG page on
#   this box redirects to that updater. I could not run it myself -- the
#   deploy script, the UI click and the direct database write were all
#   refused, and this install has no FOG_SCHEMA_INSTALL_TOKEN to use the
#   third door. This script checks for the column and stops if it is absent.
#
#   WHAT:  enrolls a real agent against the local FOG server, approves it,
#          sets a desired version on that host, and watches the agent poll,
#          receive the update block, verify, swap, restart, and CLEAR its
#          probation on the next successful poll.
#   WHERE: the local 1.6 lab server (10.255.20.1) and a systemd user
#          service on this box. It creates ONE host row and one agent
#          certificate; both are safe to delete afterwards in Host
#          Management, and --clean does the local half.
#   WHY:   design 0015. prove_self_update.sh already proved verification,
#          the swap, the restart, the replay floor and the unattended
#          revert on real artifacts. The one thing it could not prove is
#          the SUCCESS path of probation -- that a good update is not
#          reverted fifteen minutes later -- because clearing needs a poll
#          that actually authenticates. Tests pin ClearProbation itself;
#          only this proves the run loop calls it at the right moment.
#   NEEDS:  the mirror on :8099 (serve_lab_mirror below), the lab-signed
#           binaries under /images/claude-lab/self-update/serve, and the
#           lab root compiled into them:
#             build/mint-signing-ca.sh --dir /images/claude-lab/self-update/ca --install
#   TRAP:  those binaries trust a LAB root and are not releasable.
#          internal/release/roots.pem in git must stay empty of it.
#
#   HAS NEVER BEEN RUN. Everything above the schema step is proven; this
#   script itself is unexercised, because the column it needs does not
#   exist yet. Treat a failure here as likely mine.
#
# Usage: background_scripts/prove_self_update_server.sh [--clean]
#
set -uo pipefail

LAB=/images/claude-lab/self-update
RIG=$LAB/rig-server
SERVE=$LAB/serve
STATE=$RIG/state
BIN=$RIG/bin/fog-agent
UNIT=fog-agent-server-lab.service
FOG=https://10.255.20.1/fog
MIRROR=http://10.255.20.1:8099
pass=0; fail=0
say() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
ok()  { pass=$((pass+1)); printf '   \033[32mPASS\033[0m %s\n' "$*"; }
bad() { fail=$((fail+1)); printf '   \033[31mFAIL\033[0m %s\n' "$*"; }
sha() { sha256sum "$1" | cut -d' ' -f1; }
fog() { sudo php /tmp/fogq.php "$@"; }

if [ "${1:-}" = "--clean" ]; then
    systemctl --user stop "$UNIT" 2>/dev/null
    rm -f "$HOME/.config/systemd/user/$UNIT"; systemctl --user daemon-reload
    rm -rf "$RIG"; echo "local rig removed; delete the host row in Host Management yourself"
    exit 0
fi

# One helper for every database touch, so the credential is read by PHP out
# of FOG's own config and never reaches a command line. QUERY_STRING is set
# before the bootstrap because DatabaseManager redirects everything to the
# schema page while the database is behind, and a query string mentioning
# `schema` is FOG's own documented way past that.
cat > /tmp/fogq.php <<'PHP'
<?php
$_SERVER['QUERY_STRING'] = 'node=schema';
$_SERVER['REQUEST_URI'] = '/fog/management/index.php?node=schema';
$_SERVER['REQUEST_METHOD'] = 'GET';
$_GET['node'] = 'schema';
chdir('/var/www/html/fog/management');
require_once '/var/www/html/fog/commons/base.inc.php';
$DB = \FOG\Base\FOGBase::getClass('DatabaseManager');
switch ($argv[1]) {
case 'has-column':
    $DB->query("SHOW COLUMNS FROM `hosts` LIKE 'hostAgentDesiredVersion'");
    exit($DB->fetch()->get() ? 0 : 1);
case 'pending-host':   // newest pending enrollment's host id
    $DB->query("SELECT `aeHostID` FROM `agentEnrollment` WHERE `aeState`='pending' ORDER BY `aeID` DESC LIMIT 1");
    echo (int)$DB->fetch()->get('aeHostID');
    break;
case 'set-desired':    // <hostID> <version>
    // Checked rather than escaped. sanitize() lives on PDODB and I have
    // not confirmed the manager proxies it; a version is a closed
    // character set, so refusing anything else is both safer and one
    // fewer API to be wrong about.
    if (!preg_match('/^[0-9A-Za-z.+-]{1,50}$/', $argv[3])) {
        fwrite(STDERR, "not a version: {$argv[3]}\n");
        exit(2);
    }
    $DB->query("UPDATE `hosts` SET `hostAgentDesiredVersion`='" . $argv[3]
        . "' WHERE `hostID`=" . (int)$argv[2]);
    break;
case 'agent-version':  // <hostID> -- what the server believes it is running
    $DB->query("SELECT `hostAgentVersion` FROM `hosts` WHERE `hostID`=" . (int)$argv[2]);
    echo (string)$DB->fetch()->get('hostAgentVersion');
    break;
case 'set-mirror':
    // setSetting(), never a raw UPDATE: globalSettings is cached, and a
    // direct write changes nothing any reader sees.
    \FOG\Base\FOGBase::setSetting('FOG_AGENT_UPDATE_MANIFEST_URL', $argv[2]);
    break;
}
PHP

say "0. the schema step"
if ! fog has-column; then
    bad "hostAgentDesiredVersion is absent -- run the schema updater first (see the header)"
    echo "   https://10.255.20.1/fog/management/index.php?node=schema"
    exit 1
fi
ok "schema step 434 is applied"

say "1. a real agent, enrolled against the real server"
systemctl --user stop "$UNIT" 2>/dev/null
rm -rf "$RIG"; mkdir -p "$RIG/bin" "$STATE"
cp "$SERVE/fog-agent-0.2.0-linux-amd64" "$BIN"
"$BIN" enroll --once --dir "$STATE" --server "$FOG" \
    --ca /var/www/html/fog/management/other/ca.cert.pem 2>&1 | sed 's/^/   | /'
host=$(fog pending-host)
[ "${host:-0}" -gt 0 ] && ok "pending as host $host" || { bad "no pending enrollment"; exit 1; }
sudo php /home/telliott/scripts/background_scripts/approve_agent_enrollment.php "$host" 2>&1 | sed 's/^/   | /'

fog set-mirror "$MIRROR/manifest.json"
mkdir -p "$HOME/.config/systemd/user"
cat > "$HOME/.config/systemd/user/$UNIT" <<UNITEOF
[Unit]
Description=FOG Agent (self-update server proof)
[Service]
ExecStart=$BIN run --dir $STATE
Restart=always
RestartSec=2
UNITEOF
systemctl --user daemon-reload && systemctl --user start "$UNIT"
sleep 20
[ "$(fog agent-version "$host")" = "0.2.0" ] \
    && ok "the server sees this host running 0.2.0" \
    || bad "the server has not recorded an agent version yet"

say "2. the server names a version and the agent obeys"
fog set-desired "$host" 0.3.0
echo "   set hostAgentDesiredVersion=0.3.0 on host $host; waiting for a poll..."
for i in $(seq 1 60); do
    sleep 10
    [ "$(sha "$BIN")" = "$(sha "$SERVE/fog-agent-0.3.0-linux-amd64")" ] && break
done
if [ "$(sha "$BIN")" = "$(sha "$SERVE/fog-agent-0.3.0-linux-amd64")" ]; then
    ok "updated to 0.3.0 from the server's instruction alone after $((i*10))s"
else
    bad "still on 0.2.0 after $((i*10))s"
fi

say "3. probation clears on a successful poll -- the thing only this can prove"
# The failure being excluded: probation NOT clearing means every good
# update in the fleet reverts itself fifteen minutes later, forever.
echo "   waiting for the restarted agent to complete one authenticated poll..."
for i in $(seq 1 60); do
    sleep 10
    [ -f "$STATE/update-probation.json" ] || break
done
if [ -f "$STATE/update-probation.json" ]; then
    bad "probation still armed after $((i*10))s -- this update will revert itself"
    cat "$STATE/update-probation.json" | sed 's/^/   | /'
else
    ok "probation cleared after $((i*10))s; 0.3.0 is now simply the installed version"
fi
journalctl --user -u "$UNIT" --no-pager -o cat | grep -i 'update:' | tail -8 | sed 's/^/   | /'

[ "$(fog agent-version "$host")" = "0.3.0" ] \
    && ok "the server now sees 0.3.0, so the rollout is observable from the host list" \
    || bad "the server still reports the old version"

say "result: $pass passed, $fail failed"
echo "clean up with: $0 --clean   (and delete host $host in Host Management)"
[ "$fail" -eq 0 ]
