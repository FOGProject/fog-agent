# Agent protocol v1: enrollment

Status: DRAFT 2026-09-03. The agent owns this contract (design doc 5.1); the
server implements it under `/agent/v1`. JSON both ways, UTF-8, `Content-Type:
application/json`.

## POST /agent/v1/enroll

Server-authenticated TLS only (the agent has no certificate yet). The agent
trusts the CA bundle written at bootstrap, never the OS store. Idempotent: the
agent repeats the identical request until it gets a terminal answer.

Request:

```json
{
  "protocol": 1,
  "agent_version": "0.1.0",
  "os": "windows", "arch": "amd64",
  "hostname": "LAB-PC-07",
  "identity": {
    "system_uuid": "…", "system_serial": "…", "board_serial": "…",
    "chassis_asset": "…", "smbios_version": "3.2",
    "macs": ["cc:48:3a:5e:11:aa"]
  },
  "csr_pem": "-----BEGIN CERTIFICATE REQUEST-----…",
  "token": "optional enrollment token"
}
```

The CSR's subject is ignored; the server sets the issued subject
(`CN=fog-agent host <id>`) so an agent cannot choose its own identity. Only
the public key and the proof of possession matter.

Responses:

| HTTP | `status` | Meaning | Agent behavior |
|---|---|---|---|
| 200 | `issued` | Approved. Body carries `host_id`, `certificate_pem` (leaf followed by its issuing chain), `not_after` | Store cert, switch to mTLS, done |
| 202 | `pending` | Waiting for an admin, a token, or a deploy. Body carries `reason` (`unknown-host`, `known-host-no-agent`, `rebind`) and `retry_after` seconds | Log once per reason change, sleep, repeat |
| 403 | `denied` | An admin denied this key. Body carries `reason` | Log, back off to hourly, repeat (a later approval must still be reachable) |
| 400 | `error` | The request is malformed (`reason`: `csr`) | Log, back off, repeat |
| 503 | `error` | The server accepted the request but could not sign (`reason`: `signing`); the row is kept | Log, retry after `retry_after` |
| 426 | `unsupported` | Server does not speak protocol 1 | Log, back off, repeat |

Approval paths on the server, in order of evaluation:

| Path | Condition | Outcome |
|---|---|---|
| Token | `token` present, valid, unspent (or multi-use and unexpired) | Issue; an unknown machine was already created as a pending host and is un-pended by the issue; audit `agent.enroll` via `token` |
| Post-image | identity resolves to a host with no conflict, and the host's last deploy completed within `FOG_AGENT_ENROLL_DEPLOY_WINDOW` hours (default 24, 0 disables) | Issue and (re)bind; audit via `deploy` |
| Admin | everything else | Pending row; on approve issue and audit via `admin` with the user; on deny record the key fingerprint so the same key returns `denied` |

An unknown machine becomes a **pending host** immediately (description
"Pending Registration created by FOG_AGENT"), with an inventory row carrying
its firmware identity, so it appears where admins already look for pending
registrations. Approving the enrollment un-pends the host. The agent never
learns a host id until issue, so nothing about the pending host reaches it.

Reasons a request waits: `unknown-host`, `known-host-no-agent`, `rebind`
(the host already has an agent key), `identity-conflict` (firmware and MACs
name different hosts; the firmware's host is kept and the admin sees the
conflict), `reissue` (same key, certificate already collected), `no-mac`.

A pending row holds the request verbatim (identity, hostname, key
fingerprint, CSR) so approval signs exactly what was presented. A host with a
live certificate that receives a new enrollment from the same identity pends
with reason `rebind`; approving it revokes the old fingerprint.

## What a pending agent may do

Nothing. No other route accepts a request without a client certificate the
server issued, so "pending" is enforced by TLS, not by application checks.

## POST /agent/v1/poll

The first authenticated call, and the hard floor's heartbeat. Every route
under `/agent/v1/` other than enroll is reachable only with the client
certificate enrollment issued: the web server verifies it against the agent
CA bundle on the TLS handshake (`ssl_verify_client optional` /
`SSLVerifyClient optional`, so a browser is never asked for one), and the
router re-verifies the chain against the same bundle and binds the key
fingerprint to exactly one live, non-pending host before any route matches.
No token, no session, no `mac=`.

Request:

```json
{"agent_version": "1.2.3"}
```

Response `200`:

```json
{
  "status": "ok",
  "protocol": 1,
  "host": {"id": 231, "name": "7550precision"},
  "capabilities": ["hostname"],
  "state_revision": "3f1c9a0b2d4e5f60",
  "poll_interval": 300,
  "server_time": "2026-09-03T12:43:21-05:00"
}
```

`capabilities` is the list from design 0001 5.1; empty is a valid answer and
the agent idles on it. A capability is listed when its legacy module is on for
the host (the global `FOG_CLIENT_*_ENABLED` setting and the host's resolved
module set, the same two checks the old client's endpoints make), so existing
per-host and per-group module choices carry over. `state_revision` is a
digest of the desired state; the agent fetches it only when this differs from
the revision it last applied. The server records `hostAgentCheckin` and
`hostAgentVersion` on every poll.

| Status | Meaning | Agent does |
|---|---|---|
| 200 | Bound host, answered | Sleep `poll_interval`, poll again |
| 401 | No verified certificate, or one bound to no live host (deleted, re-enrolled elsewhere, still pending) | Drop the certificate, keep the key, enroll again |

The 401 is the reimage/rebind path in practice: a host deleted from FOG and
its agent left running comes back as `unknown-host` pending, with the same
key, and waits for an admin like any new machine.

## GET /agent/v1/state

The desired state, fetched when `state_revision` moved. Same gate as poll.
Blocks appear only for the capabilities listed; a server that does not offer
something never describes it.

```json
{
  "revision": "3f1c9a0b2d4e5f60",
  "capabilities": ["hostname"],
  "hostname": {"name": "lab-01", "enforce": true}
}
```

| Block | Source | Provider |
|---|---|---|
| `hostname` | the host record's name; `enforce` is the host's "Enforce Hostname / AD Join Reboots" flag, the admin's permission to reboot to finish a rename | `ensure hostname`: compares case-insensitively, sets only on a difference. Linux `hostnamectl` (no reboot), Windows `SetComputerNameEx` (reboot pending), macOS `scutil` |

Two more blocks serve the reboot coordinator (design 0001 section 6):

```json
{
  "capabilities": ["hostname", "taskreboot"],
  "task": {"id": 41, "type": "Deploy", "force": false},
  "reboot": {"grace": 60}
}
```

| Block | Source | Consumer |
|---|---|---|
| `task` | capability `taskreboot` (module `taskreboot`): the task waiting for this host in a state that needs it to boot into FOS, exactly what `Client\Jobs` answers the old client. `null` while none waits, so queueing or canceling a task moves the revision. `force` is FOG_TASK_FORCE_REBOOT | the coordinator records a reboot reason for the task, or withdraws it |
| `reboot` | sent with any non-empty capability list. `grace` is FOG_GRACE_TIMEOUT, seconds | the countdown logged-in users get before a forced reboot |

### The reboot coordinator

The only thing in the agent that reboots. Providers never do; a provider
whose change needs one reports `pending_reboot` and the coordinator
records a reason for it, forced if the host's `enforce` flag is set. A
waiting task is a reason too, forced if FOG_TASK_FORCE_REBOOT is on.
Reasons persist in the state file across restarts.

Every poll, with reasons outstanding, the coordinator counts logged-in
users (logind or `who` on Linux, WTS sessions on Windows, `who` on macOS)
and decides:

| Users | Any reason forced | Decision |
|---|---|---|
| none | either | reboot now |
| some | no | wait; reported as `reboot pending_reboot` with the count |
| some | yes | reboot after `grace` seconds, message shown to users |

The decision is reported as a result with capability `reboot`
(`applied` when a reboot was requested, `pending_reboot` when it waited),
so the host's audit shows why the machine went down or why it did not.

**Where this diverges from the old client:** the agent reboots **once per
task id**. A machine that comes back with the same task still queued did
not boot into FOS (boot order, no PXE on that segment), and the old
client's answer, rebooting it again every check-in, takes the machine
away from its user without fixing that. Cancel and re-queue the task to
ask again; a new task is a new id.

Two server-side facts that decide whether a capability appears at all:

- The capability list is the host's *resolved* modules intersected with the
  global `FOG_CLIENT_*_ENABLED` switches. FOG has no default tier in module
  resolution, so a host with no module rows has no capabilities. Hosts the
  agent creates get the `isDefault` modules at creation, the same as boot
  registration and the Host Management form (fogproject PR #1707).
- A host record whose name fails FOG's own rule (1 to 15 characters of the
  allowed set) is invalid, and an invalid host does not authenticate: the
  agent gets 401 and re-enrolls as `rebind`. The API refuses such a name, so
  this only bites on a name written straight into the database.

Proven 2026-09-03 on a throwaway Debian VM: rename in FOG, next poll saw the
revision move, `hostname: applied (fogagent-test -> fog-renamed)`, the
following run reported `unchanged`, both audited as `agent.result` with
auth source `agent`.

## POST /agent/v1/result

What one provider did at one revision. Same gate as poll. Recorded on the
host as an `agent.result` audit row, which is where FOG already shows what
happened to a host; no table until inventory needs one.

```json
{"revision": "3f1c9a0b2d4e5f60", "capability": "hostname", "status": "applied", "detail": "old-name -> lab-01"}
```

`status` is one of `applied`, `unchanged`, `pending_reboot`, `failed`. The
agent records the revision as applied only when no provider failed, so a
failure is retried on the next poll rather than forgotten.

## POST /agent/v1/renew

Same gate as poll: the caller is already bound to its host by the certificate
it presents, and the body carries a request for the **same key**. The answer
is the enroll `issued` shape and the agent stores it the same way. Renewal is
same-key only; a new key is a new claim on the machine and goes through
enroll, where it pends as `rebind` for an admin. The old certificate is not
revoked: it binds to the same key and lapses on its own.

Request:

```json
{"csr_pem": "-----BEGIN CERTIFICATE REQUEST-----\n..."}
```

Response `200`:

```json
{"status": "issued", "host_id": 231, "certificate_pem": "...", "not_after": "2027-09-03 14:44:06"}
```

| Status | Meaning | Agent does |
|---|---|---|
| 200 | Renewed; `hostAgentNotAfter` updated, audited | Store it, present it from the next connection |
| 400 | Not a CSR, or one for a different key | Report; keep the current certificate |
| 401 | As for poll | Drop the certificate, keep the key, enroll again |
| 503 | Signing helper unavailable on this server | Report; retry next poll |

The agent renews inside `RenewLead` (120 days) of expiry, checked after each
successful poll, and `fog-agent renew` forces one at any time. A machine that
misses the window entirely falls through to the 401 path and an admin.

Server files behind this: `Agent\Principal` (verification and fingerprint),
the gate in `Route` before dispatch, and the installer publishing
`management/other/agent-ca-bundle.pem` (agent CA + root) for both the vhost
and PHP to verify against.
