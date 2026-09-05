# 0008: User tracking as sessions, not events

Status: SHIPPED 2026-09-04. Agent `internal/usersession` and
`cmd/fog-agent/sessions.go`; server item `FOG\Items\HostUserSession`, schema
422 (`hostUserSession`), gated by FOG's existing user-tracking module setting
rather than a second switch. Proven in the lab: 23 session rows recorded.

Supersedes the one-line reservation in `0006-inventory.md` §7, which said
user-tracking login/logout events are "ordered and lossless, a different shape
from an idempotent snapshot". The first half is wrong, and the lab data says
so. This document explains why and what replaces it.

## 1. What is there today, and what it cannot answer

FOG has recorded user tracking since 1.x in `userTracking`: one append-only
row per event, written by `Client/UserTrack.php` from a POST the legacy client
sends at login and logout.

```
utID  utHostID  utUserName  utAction  utDateTime  utDesc  utDate  utAnon3
                                      utCreatedBy  utIP  utHostName
```

`utAction` is `varchar(2)` holding `'1'` (login), `'0'` (logout) or `'99'`
(service start). Four single-column indexes, no composite.

The lab server's 16 rows, all from one host:

| user | logins | logouts | never closed |
|---|---|---|---|
| `tom` | 6 | 4 | 2 |
| `telliott` | 5 | 1 | 4 |

**Six of eleven sessions have no logout.** That is the whole problem, and it
is structural rather than a delivery bug. A logout event requires a network
round trip at the one moment the machine is least able to make one: shutdown,
power loss, a crashed service, a closed lid on a laptop off the network. No
amount of retrying fixes an event that was never generated. An event-only
design will always leak dangling sessions, so every question worth asking
gets an unreliable answer:

- *Who is logged into this machine right now?* Unanswerable — you cannot tell
  an open session from a lost logout.
- *How long was this user on this machine?* Requires pairing consecutive rows
  by host and username in post-processing, which the six dangling rows break.
  Nothing in the codebase does this pairing today.
- *Is anyone using this machine before I image or force-reboot it?* This one
  matters most, because 0004's reboot coordinator should be asking it, and
  today it has nothing to ask.

Beyond pairing, the row is thin. `utUserName` is `varchar(50)` and the
username arrives lowercased with its domain **stripped off**
(`Client/UserTrack.php`), so `CORP\jsmith` and `LAB\jsmith` are the same
person as far as the table is concerned. `utHostName varchar(16)` is a
NetBIOS-era width that truncates a modern hostname. `utDesc` and `utAnon3`
are populated in zero of 16 rows; `utAnon3` is a placeholder column that has
never meant anything. There is no session id, no SID, no console-vs-RDP
distinction, and no notion of an RDP session that is *disconnected but still
logged in* — which on a terminal server is most of them.

## 2. The model: a session is a row with two ends

The agent reports **sessions**, not events. A session is one row that opens
when a user logs on and closes when they log off, carrying both timestamps.
The set of currently-open sessions is state the agent re-reports on a poll,
which is what makes it self-correcting: the machine that lost power emits no
logout, comes back, reports a session set that does not include the old
session, and the server closes it. No event is required at the moment the
machine is dying, which is the moment we cannot rely on.

Precision does not have to be sacrificed for that. Each session carries the
OS-reported logon time, which is **exact** and unaffected by poll timing,
because the OS records it. End times are the agent's own observation, and v1
gets them by sampling the session set on `SessionSampleInterval` (30s) inside
the already-running process rather than the 5-minute poll. So:

- Starts are exact, from the OS.
- Ends the agent observed are accurate to the sample interval — 30 seconds,
  not five minutes, and the deliberate simplification of this version.
- Ends the agent never observed (power loss) are inferred by the server and
  **marked as inferred**, closed at the host's last contact rather than
  silently given a fake timestamp.

Both platforms can do better than sampling later: Windows services can take
`SERVICE_CONTROL_SESSIONCHANGE` notifications and logind publishes D-Bus
signals, either of which would make an observed end exact. That is a strict
refinement of this design — the same rows, a better `husEndedAt` — so it is
deliberately not in v1.

That distinction is the point. The legacy table cannot tell "logged out at
11:54" from "we never found out", and reports built on it silently treat the
second as the first.

### Why not an event queue with durable spooling

The honest alternative is: agent watches OS session events, writes them to a
disk spool, ships the spool on the next poll, survives reboots. It is more
code and it still cannot invent the logout that was never generated. It buys
exact ordering of events that happen between two polls — two logins and a
logout inside five minutes — which the snapshot alone would collapse.

The design below keeps that benefit without the spool: the agent reports
*closed* sessions with their observed timestamps alongside the open set, and
holds unacknowledged closures until a poll returns 200. That is a spool in
the sense that matters (nothing is dropped on a failed poll) without a
separate event store, because a closed session is already the record.

| | event log (legacy) | session snapshot only | **snapshot + observed ends** |
|---|---|---|---|
| "who is on this box now" | no | yes | yes |
| duration | pair rows, breaks on dangling | poll-quantized | exact when observed |
| survives power loss | leaks a dangling row forever | self-heals | self-heals, and says it inferred |
| events between polls | exact | collapsed | preserved as closed rows |
| code | client posts on event | poll block | poll block + local end capture |

## 3. The table

New table `hostUserSession`, prefix `hus`. (`hs` is taken by 0006's
`hostSoftware`.) The legacy `userTracking` table is **not** dropped or
altered — see §6.

```sql
CREATE TABLE `hostUserSession` (
  `husID`         INTEGER NOT NULL AUTO_INCREMENT,
  `husHostID`     INTEGER NOT NULL,
  `husSessionKey` VARCHAR(191) NOT NULL DEFAULT '',
  `husUserName`   VARCHAR(255) NOT NULL DEFAULT '',
  `husDomain`     VARCHAR(255) NOT NULL DEFAULT '',
  `husUserSID`    VARCHAR(191) NOT NULL DEFAULT '',
  `husType`       VARCHAR(32)  NOT NULL DEFAULT '',
  `husState`      VARCHAR(32)  NOT NULL DEFAULT '',
  `husRemoteHost` VARCHAR(255) NOT NULL DEFAULT '',
  `husStartedAt`  DATETIME NOT NULL,
  `husEndedAt`    DATETIME NULL DEFAULT NULL,
  `husEndReason`  VARCHAR(32)  NOT NULL DEFAULT '',
  `husLastSeen`   DATETIME NOT NULL,
  PRIMARY KEY (`husID`),
  UNIQUE KEY `husOpen` (`husHostID`, `husSessionKey`, `husStartedAt`),
  INDEX `husHostOpen` (`husHostID`, `husEndedAt`),
  INDEX `husUser` (`husUserName`),
  INDEX `husStarted` (`husStartedAt`)
) ENGINE=InnoDB;
```

What each column is for, and which legacy gap it closes:

- **`husSessionKey`** — the OS session identifier (Windows WTS session id,
  logind session id). Makes a session addressable, so a second login by the
  same user is a distinct row instead of an ambiguous second event.
- **`husUserName` / `husDomain`** — kept **separate and unmangled**, at 255.
  Legacy lowercases and strips the domain, which merges distinct accounts.
- **`husUserSID`** — the Windows SID or Linux uid. A username can be renamed;
  this is what survives it, and it is what an AD-joined estate reports on.
- **`husType`** — `console`, `remote`, `tty`, `x11`, `wayland`, `unknown`.
  Console versus RDP is the single most-asked distinction and legacy has none.
- **`husState`** — `active`, `disconnected`, `locked`. A disconnected RDP
  session is still a logged-in user holding a profile; today it is invisible.
- **`husRemoteHost`** — for a remote session, where it came from.
- **`husStartedAt` / `husEndedAt`** — `husEndedAt IS NULL` means open. The
  partial index `(husHostID, husEndedAt)` is the "who is on this host now"
  query and the "close everything still open" reconcile, which legacy's four
  single-column indexes cannot serve.
- **`husEndReason`** — `logout`, `disconnect`, `inferred`, `service_stop`.
  `inferred` is the honesty flag: the agent never saw this end.
- **`husLastSeen`** — the last poll that still saw the session open. An
  inferred close uses this as `husEndedAt`, so the duration is a lower bound
  the reports can label rather than a guess.

Duration is then `TIMESTAMPDIFF(SECOND, husStartedAt, husEndedAt)` — a real
column pair, not a pairing heuristic.

## 4. The channel

Sessions ride the **poll request**, like 0006's facts and for the same reason
(they are observations, never desired state). They do **not** use the
hash-on-change gate, which is where 0006 was right: a hash that has not moved
would suppress a resend, and the open set is also the server's evidence the
session is still alive.

The rule instead:

- Include the block when the open set differs from the last **acknowledged**
  one, or when there are closed sessions not yet acknowledged.
- Record acknowledgement only after the poll returns 200, so a failed poll
  resends rather than dropping a closure. Same discipline as `recordFacts`.
- Re-send the full open set every `SessionResyncInterval` (1h) even when
  unchanged, so a server-side row that drifted gets corrected.

Request block:

```json
"sessions": {
  "open": [
    {"key":"2","user":"telliott","domain":"LAB","sid":"S-1-5-21-...-1001",
     "type":"console","state":"active","remote_host":"","started_at":"2026-09-04T09:12:03Z"}
  ],
  "closed": [
    {"key":"3","user":"tom","domain":"LAB","sid":"S-1-5-21-...-1002",
     "type":"remote","state":"active","remote_host":"10.255.25.9",
     "started_at":"2026-09-04T07:01:44Z","ended_at":"2026-09-04T08:55:10Z",
     "end_reason":"logout"}
  ]
}
```

Answer field `collect_sessions` (a `*bool`, absent ≠ false, exactly as
`collect_facts` in 0006) carries the gate.

### The gate is the module that already exists

FOG ships module 12, `usertracker`, default enabled, and admins have been
turning it off for a decade. The server resolves it per host the way it
resolves every other module and sends `collect_sessions: false` when it is
off. **An agent that is told false reports nothing and stops collecting** —
it does not gather and discard, because the privacy-relevant act is reading
who is logged in, not transmitting it.

`husRemoteHost` records where a remote session originated. That is real
additional data about a person, so it is worth saying plainly that it is
covered by the same gate and the same retention as the rest of the row.

## 5. Server ingress and the reconcile

One route, on the existing poll. For a host with the module on:

1. For each entry in `closed`: match the open row by
   `(husHostID, husSessionKey, husStartedAt)` and set `husEndedAt`,
   `husEndReason` from the report. If no open row matches (the agent restarted
   and never reported the open side), insert the row already closed — a
   complete session is worth more than a tidy state machine.
2. For each entry in `open`: `INSERT ... ON DUPLICATE KEY UPDATE` on the same
   unique key, refreshing `husState`, `husRemoteHost` and `husLastSeen`.
3. Close the leftovers: any row for this host still open whose
   `husSessionKey` is not in the reported open set gets
   `husEndedAt = husLastSeen`, `husEndReason = 'inferred'`.

Step 3 is the close-then-reopen shape from 0006 §4.2 turned around, and it is
what makes power loss self-healing: the machine that vanished reports its new
(empty or different) open set on the next boot and its stale rows close at the
time they were last seen, flagged as inferred.

Retention: a `hostUserSession` entry joins `Audit/Retention.php` keyed on
`husStartedAt` with its own `FOG_HOSTUSERSESSION_RETENTION_DAYS`, alongside
the existing `FOG_USERTRACKING_RETENTION_DAYS`.

## 6. The legacy table keeps working

`userTracking` is not dropped, not altered, and still fed. Two reasons: FOG
1.5 clients in the same estate keep posting to
`service/usertracking.report.php`, and the Activity page plus
`History_Report` read it today.

So on each session open and each observed close, the server **also** writes
the equivalent legacy `userTracking` row (action `1`/`0`). A fleet migrating
to fog-agent sees no gap in the page it already uses, and an estate running
both client generations gets one merged view. The shim is one function, it is
marked as a compatibility shim in the source, and
`FOG_USERTRACKING_COMPAT_WRITE` turns it off for an estate that has fully
migrated and does not want the duplicate rows.

Inferred closes write **no** legacy logout row. Writing one would put a
fabricated logout time into a table that cannot express "we inferred this",
which is the defect this whole document exists to stop reproducing.

## 7. Agent side

New package `internal/usersession`, following 0006's split exactly:
`usersession.go` (neutral: the `Session` type, `List()`, and the pure diffing
helpers so they are testable off both platforms), `list_linux.go`,
`list_windows.go`, `list_other.go` returning `(nil, false)`.

- **Linux**: systemd-logind, reading `/run/systemd/sessions/*` — key=value
  files giving id, user, uid, type (`tty`/`x11`/`wayland`), remote host, state
  and realtime start. Falls back to utmp where logind is absent. Not `who`,
  whose output format is a display decision that changes.
- **Windows**: `WTSEnumerateSessionsEx` plus `WTSQuerySessionInformation` for
  username, domain, client address and connect state, and the session's logon
  time from `WTSINFO`. **This must be proved from inside the service before
  the design leans on it**: 0006 §6 recorded that `EnumDisplayDevices`
  silently returns nothing in a service because a service has no window
  station, and it returned success while doing it. The WTS API is documented
  to work from session 0, but "documented to work" is what was believed about
  the display call too. The first implementation step is a probe run as the
  service on the lab host, not a unit test.

State (`internal/enroll/state.go`): `SessionsAcked` (the acknowledged open-set
digest), `SessionsPending` (closed sessions not yet acknowledged) and
`SessionsChecked`. `SessionsDisabled` mirrors `FactsDisabled` for the gate.

## 8. What this is not

Not desired state — the server never tells the agent who may log in. Not
process or application usage tracking; a session is a logon, and per-app
telemetry is a different feature with a different privacy conversation. Not a
replacement for `history`, which records admin actions in the web UI. Not a
security audit log: it records that a session existed, not what was done in
it, and it is not tamper-evident.
