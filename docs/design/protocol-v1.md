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

The first authenticated call, the hard floor's heartbeat, and the one
place the agent learns what it should look like. Every route under
`/agent/v1/` other than enroll is reachable only with the client
certificate enrollment issued: the web server verifies it against the agent
CA bundle on the TLS handshake (`ssl_verify_client optional` /
`SSLVerifyClient optional`, so a browser is never asked for one), and the
router re-verifies the chain against the same bundle and binds the key
fingerprint to exactly one live, non-pending host before any route matches.
No token, no session, no `mac=`.

Request:

```json
{"agent_version": "1.2.3", "applied_revision": "3f1c9a0b2d4e5f60", "want_state": false}
```

`applied_revision` is the revision this agent last converged, empty on a
fresh agent and on a build that has just learned a capability. `want_state`
asks for the state even when that revision is current: the software drift
check needs the set without anything having moved.

The request also carries **facts** -- what the agent has observed about its
own host, as opposed to what it has applied:

```json
{"agent_version": "1.2.3", "applied_revision": "3f1c9a0b…", "want_state": false,
 "inventory": {"sysman": "Dell Inc.", "…": "…"},
 "software": [{"name": "openssh-server", "version": "9.6p1", "source": "rpm", "…": "…"}]}
```

A fact block is present only when the agent's own content hash for it moved,
or when the previous answer asked with `want_inventory` / `want_software`.
This is the conditional fetch above run in reverse, and it has the same
consequence: **absent is "nothing new", never "nothing there"**. An agent
whose collector cannot run on this platform sends no block rather than an
empty one, because the server treats a reported list as complete.

**The poll request may be gzipped.** When the body exceeds 16 KB the agent
compresses it and sets `Content-Encoding: gzip`; the server decodes on that
header alone. A measured Linux host's software list is 388 KB of JSON and
37 KB gzipped, which is the difference between fitting the 1 MB body limit
nginx and Apache ship with and not. No other route compresses: nothing else
the agent sends is big enough to be worth a decode.

Response `200`:

```json
{
  "status": "ok",
  "protocol": 1,
  "host": {"id": 231, "name": "7550precision"},
  "revision": "3f1c9a0b2d4e5f60",
  "poll_interval": 300,
  "server_time": "2026-09-03T12:43:21-05:00",
  "state": {"revision": "3f1c9a0b2d4e5f60", "capabilities": ["hostname"], "hostname": {"name": "lab-01", "enforce": true}},
  "want_inventory": false,
  "want_software": false
}
```

`state` is present when `revision` is not the `applied_revision` the agent
sent, or when it asked for it; otherwise the answer stops at `server_time`
and the agent has nothing to do but sleep `poll_interval`. This is a
conditional fetch on a POST, the way an ETag works on a GET, done as a POST
because the poll also writes: the server records `hostAgentCheckin` and
`hostAgentVersion` on every one.

`want_inventory` and `want_software` ask for a fact block the server holds
no current hash for -- a fresh enrollment, a restored database, an admin who
cleared the row. They are the server's half of the same conditional: it can
always force a resend, and the agent honors the request on its next poll
whether or not anything changed locally.

**The revision is opaque.** The agent compares it with the one it applied,
for equality, and does nothing else with it: never parses it, never orders
two of them, never treats it as a version number or a time. Today it is a
digest of the desired state; tomorrow it may be a cheaper thing the server
can compute without building the state. An agent that reads anything into
it closes that door for every server it will ever talk to.

| Status | Meaning | Agent does |
|---|---|---|
| 200 | Bound host, answered | Converge `state` if present, then sleep `poll_interval` and poll again |
| 401 | No verified certificate, or one bound to no live host (deleted, re-enrolled elsewhere, still pending) | Drop the certificate, keep the key, enroll again |

The 401 is the reimage/rebind path in practice: a host deleted from FOG and
its agent left running comes back as `unknown-host` pending, with the same
key, and waits for an admin like any new machine.

## The desired state

What the host should look like, carried in the poll answer as `state`.
`capabilities` is the list from design 0001 5.1; empty is a valid answer and
the agent idles on it. A capability is listed when its legacy module is on for
the host (the global `FOG_CLIENT_*_ENABLED` setting and the host's resolved
module set, the same two checks the old client's endpoints make), so existing
per-host and per-group module choices carry over. Blocks appear only for the
capabilities listed; a server that does not offer something never describes
it.

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

### Snapins

Capability `snapin` (module `snapinclient`). The block is the host's
snapin queue exactly as the server tasked it: `snapinTasks` rows in
`sequence` order, which the server wrote from its resolver -- the host's
own associations first, then its groups' grants in group order,
deduplicated. The agent runs them in that order and never re-sorts.

```json
{
  "capabilities": ["snapin"],
  "snapins": [
    {"task": 41, "snapin": 7, "name": "Install 7-Zip", "file": "7z.msi",
     "size": 1834496, "sha512": "…", "args": "/qn", "run_with": "msiexec.exe",
     "run_with_args": "/i", "timeout": 600, "action": "", "abort_on_fail": false}
  ]
}
```

For each task, in order:

| Step | Route | Server side |
|---|---|---|
| fetch | `GET /agent/v1/payload/snapin/{task}` | the task must belong to this host's own job (404 otherwise, the same message as "no such task"); marks the task, the job and the host's task in progress; streams the bytes from the storage node over the web tier's own FTP session, so the agent trusts only the server's certificate |
| verify | | the agent hashes as it downloads and refuses a sha512 that is not the one declared. That refusal is reported as status `hash_mismatch`, not retried: the file the server has is not the file it described, and the admin needs to see it |
| run | | `run_with run_with_args file args`, or the file itself when `run_with` is empty (made executable on Unix); `timeout` seconds then the whole process group is killed, status `timeout`; a payload that could not start is `cannot_run` |
| report | `POST /agent/v1/result` with `item: {id: task, status, exit_code, details}` | `status` is `ran` or one of the three above; `exit_code` is the program's own, untouched, meaningful only for `ran`; `details` the last 4 KB of output. The server answers `{"status":"ok","outcome":…}` |

**Outcome, decided by the server.** A raw exit code says nothing by
itself: an MSI answers 3010 for "installed, reboot to finish" and 1618
for "another installer is running, try later", and treating either as a
failure aborts jobs that did not fail. Each snapin carries a return-code
table (lines of `code=class`, the Intune defaults when empty: 0 and 1707
success, 3010 and 1641 reboot, 1618 retry, anything else failed) and the
server reads the code against it, for this agent and for the legacy
client alike:

| Outcome | Server | Agent |
|---|---|---|
| `success` | task complete | next task |
| `reboot` | task complete | forced reason for the reboot coordinator, then next task |
| `retry` | task back to queued, details kept | stops the queue for this poll; the next poll starts again here |
| `failed` | task complete; cancels the rest of the job when it aborts on failure | stops when the job aborts on failure, else next task |

A status other than `ran` is always `failed`. The task row keeps the raw
code, the status and the output; the outcome is what the UI shows.

The defaults are Windows codes. Linux and macOS hand the parent only the
low 8 bits of an exit status, so a script there cannot answer 3010 or
1618 (they arrive as 194 and 82); a non-Windows snapin that needs the
reboot or retry outcome maps the code it can actually return.

A fetch that fails (network, 503 from a node that is down) leaves the task
open and the revision unapplied; the next poll runs the queue again from
the first open task.

`action` (`reboot` or `shutdown`, from the snapin's flags) is a forced
reason for the reboot coordinator, the way the legacy client's post-snapin
restart was forced; every shutdown reason together means a shutdown, one
reason that needs the machine back means a reboot.

Payloads land under the state directory in a root-only directory named
by task id and are deleted after the run, whatever happened.

### Power

Capability `power` (module short name `powermanagement`), design 0004. The
block is the host's resolved shutdown and reboot schedules (its own rows and
its groups' grants, `wol` left out because the server sends that packet
itself) plus any on-demand action an admin has clicked:

```json
"power": {
  "schedules": [{"cron": "30 22 * * 1-5", "action": "shutdown"}],
  "ondemand":  [{"id": 41, "action": "reboot"}]
}
```

`cron` is the five-field form FOG stores (`minute hour day-of-month month
day-of-week`; `*`, lists, ranges and `/step`; day-of-month and day-of-week
both restricted means either matches). The agent fires schedules itself,
sleeping until the earlier of its next poll and its next firing, and each
firing is a forced reason to the reboot coordinator in that action's mode.

Each on-demand entry is handed to the coordinator as a forced reason and
acknowledged at once with `POST /agent/v1/result` `{capability: "power",
status: "applied", detail: "on-demand reboot accepted (id 41)"}`; that
report is what deletes the host's on-demand rows on the server, so an admin's
request stands until an agent has actually taken it. The coordinator's own
decision follows as a `reboot` result like any other.

### Software

Capability `software` (module short name `software`), design 0003. The
block is the desired package set in run order, resolved the way the snapin
queue is (the host's direct assignments, then its groups' grants in group
order, deduplicated), plus the server's drift interval:

```json
"software": {
  "drift_interval": 21600,
  "bootstrap": {"url": "", "nupkg_url": ""},
  "entries": [
    {"id": 3, "backend": "choco", "package": "googlechrome", "version": "latest",
     "state": "present", "source": "", "args": "", "timeout": 900}
  ]
}
```

`version` is `""` (any), `latest` (upgrade at every check) or an exact
version (pinned; installed with `--force` when the host has another).
`state` is `present` or `absent`. A disabled entry is left out, not sent as
absent: turning an entry off stops managing the package, it does not
remove it.

`bootstrap` is what the agent does when Chocolatey is missing. An empty
`url` (the default, `FOG_SOFTWARE_CHOCO_BOOTSTRAP_URL`) means nothing: every
entry reports `cannot_run` and the agent looks for the binary at each poll.
With a `url` the agent fetches that install script (trusting the system
roots plus the FOG CA, so the FOG server can host it) into its state
directory and runs it with PowerShell as SYSTEM, `nupkg_url`
(`FOG_SOFTWARE_CHOCO_NUPKG_URL`) becoming the script's
`chocolateyDownloadUrl` when set. The attempt is reported as a result on
the `software` capability, `applied` or `failed`, and the entries run right
after. A failed bootstrap is tried again at the next drift check, not every
poll. Windows only: elsewhere the bootstrap reports `failed` and says so.

The agent converges the set when the revision changes and again whenever
`drift_interval` seconds have passed since its last check, fetching the
state for that. One installed-list call from the backend, then each entry
in order, then a second list when anything ran so every report carries the
version the host ended with. Outcomes never hold the revision: a failed
install is reported and tried again at the next drift check, not every
poll. Only a report that did not reach the server keeps the revision
unapplied.

| Step | Route | Notes |
|---|---|---|
| report | `POST /agent/v1/result` with `item: {id, status, installed_version, exit_code, details}` | `status` is `converged`, `installed`, `upgraded`, `removed`, `timeout` or `cannot_run`; `exit_code` is the package manager's own, meaningful for the three action statuses; `details` the last 4 KB of output. The server answers `{"status":"ok","outcome":…}` |

The outcome is read the way a snapin's is, against the entry's return-code
table, with the snapin defaults plus Chocolatey's `350` (pending reboot
detected) as `reboot`. `converged` is always `success`; `timeout` and
`cannot_run` always `failed`. `reboot` becomes a non-forced reason for the
coordinator, so an installer that wants a reboot to finish waits for the
logged-in user, unlike a task or a snapin. The status row on the server
keeps the action word when it succeeded, else the outcome, else the
never-ran status verbatim, so the host's Software tab and the snapin
history read alike.

The Chocolatey backend is the `choco` executable at a path
(`%ProgramData%\chocolatey\bin\choco.exe` on Windows, `choco` on PATH
elsewhere), which is how FOG's own snapin templates call it and why the
pipeline can be proven with a shim on Linux. Detection is `choco list -r`
(`--local-only` added on 1.x, which 2.x rejects). A host without the
binary reports `cannot_run` for every entry.

## POST /agent/v1/result

What one provider did at one revision, or what happened to one thing
under it. Same gate as poll. One route for every kind of report.

```json
{"revision": "3f1c9a0b2d4e5f60", "capability": "hostname", "status": "applied", "detail": "old-name -> lab-01"}
```

`status` is one of `applied`, `unchanged`, `pending_reboot`, `failed`. The
agent records the revision as applied only when no provider failed, so a
failure is retried on the next poll rather than forgotten. A report without
an item is recorded on the host as an `agent.result` audit row, which is
where FOG already shows what happened to a host, and answered `{"status":
"ok"}`.

A report **with an item** is about one server-owned row under the
capability, and the server answers with an outcome the agent acts on:

```json
{"revision": "3f1c9a0b2d4e5f60", "capability": "snapin", "status": "applied",
 "item": {"id": 41, "status": "ran", "exit_code": 3010, "details": "…"}}
```

```json
{"status": "ok", "outcome": "reboot"}
```

`item.id` names the row (a snapin task, a software entry); the rest of
`item` is what that capability's report class reads, in its own status
vocabulary (Snapins, Software above). The server dispatches on
`capability` to the report class, which keeps the row, reads the exit code
against its return-code table and decides the outcome. The per-item
identity and the two-way answer are real; a route per artifact type is
not, so a capability that gains item reports gains an entry in the
server's dispatch map and nothing on the path. A capability with no item
reports answers 400 to an item; the report classes' own codes (404 for a
row that is not this host's, 409, 503) pass through.

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

## The route rule

The agent surface is five routes, and the number is meant to stay small:

| Route | Transport shape |
|---|---|
| `POST /agent/v1/enroll` | no certificate yet; the request is the claim |
| `POST /agent/v1/poll` | JSON in, JSON out, conditional on a revision |
| `POST /agent/v1/result` | JSON in, a one-word answer |
| `GET /agent/v1/payload/{capability}/{id}` | bytes out, streamed |
| `POST /agent/v1/renew` | a CSR in, a certificate out |

**A new agent route is justified only by a new transport shape, never by a
new subject**: a new verb, a binary payload, or a different trust boundary.
A new capability, artifact type or report kind is a new value of
`capability` in the existing routes and a new block in the desired state.
The tell: if the proposed path contains a noun from the data model, it
fails. `snapin/{id}/result` and `software/{id}/result` were that shape, and
they are now one `item` on `result`; `snapin/{id}/file` is now
`payload/{capability}/{id}`. The server gates the rule with
`tests/agent-route-nouns.test.php`, which fails when a literal segment of
an `/agent/v1/` path names a class in `Route::$validClasses`.

**`/agent/v1/` is the agent surface; `/agent/` without `/v1/` is the admin
surface, and the two have different trust boundaries.** The agent surface
is gated by the client certificate (or, for enroll, by nothing but the
claim itself) and answers only about the caller's own host. The admin
surface (`/agent/enrollments`, `/agent/enrollment/{id}/{action}`,
`/agent/tokens`, `/agent/token`) is gated by an API token and the host
permissions, and acts on any host. A route that an admin calls "about
agents" belongs under `/agent/`, never under `/agent/v1/`, however much it
is about agents; the version segment marks the contract the agent binary
is built against, and nothing else.

**What the rule does not cover.** It is a rule about paths on the agent
surface. It does not say anything about the shape of `state` in the poll
answer, which grows a block per capability by design; about the admin
surface, whose routes name nouns because they are about the model; or
about a second protocol version, which is `/agent/v2/` and a new contract,
not a new subject. And a new transport shape is still a decision, not a
loophole: "it needs a different verb" is the start of a design note, not a
license to add the route.
