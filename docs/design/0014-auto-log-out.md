# 0014: Auto log out

Status: BUILT 2026-09-05. Agent `internal/provider/autologout`,
`internal/usersession` idle/logoff/notify, `cmd/fog-agent/autologout.go`;
server `FOG\Agent\State` capability `autologout` and schema 432
(`FOG_CLIENT_AUTOLOGOFF_WARN`, and `FOG_CLIENT_AUTOLOGOFF_BGIMAGE` removed).

Auto Log Out is the one legacy client module the rebuild keeps rather than
drops. It answers a real problem — a machine left logged in at a desk holds a
profile, a license seat and, on a shared lab machine, somebody else's turn —
and unlike Display Manager (removed in the same change) it has no better
answer elsewhere in the stack.

---

## 1. What the legacy module did

`FOG\Client\Autologout::json()` is nine lines. It reads `Host::getAlo()`,
refuses anything under five minutes, and returns `{"time": minutes * 60}`.
`getAlo()` is the per-host `hostAutoLogOut.haloTime` falling back to the
global `FOG_CLIENT_AUTOLOGOFF_MIN`, where `0` disables.

The .NET client took that number, watched for that much user inactivity, and
put a countdown window on screen — with a background image, configured by
`FOG_CLIENT_AUTOLOGOFF_BGIMAGE`, pointing by default at
`c:\program files\fog\images\alo-bg.jpg`.

So the server contract is three facts: a per-host time, a global default, and
a five-minute floor. Those carry over unchanged. The countdown window does
not, and section 3 is about why.

## 2. What the agent is told

```json
"autologout": { "minutes": 15, "warn_seconds": 60, "message": "..." }
```

`minutes` under `MinMinutes` (5) disables the capability, exactly as the
legacy floor did, and the floor is enforced **on the agent as well as on the
server**. That duplication is deliberate: a policy that arrived wrong should
not be able to log a whole fleet off, and the cost of the second check is one
comparison.

`warn_seconds` and `message` are new. The legacy module had no way to express
either — the countdown length was baked into the client — and both are things
a site legitimately wants to set.

Absent block means off, and clears whatever was stored. A stored policy that
outlived its removal from the server is the failure mode
`FOG_CLIENT_GREENFOG_ENABLED` is a monument to.

## 3. There is no countdown window, and that is not a regression

The agent is a service in session 0. Since Windows Vista, session 0 has no
desktop a user can see: the whole class of "the service pops up a dialog"
designs stopped working, silently, and a service that draws a window today
draws it where nobody is.

Three ways out, and why this one:

| | cost |
|---|---|
| Ship a tray application | A second binary, a second update path, a second thing to sign, and it must be running for the capability to work at all |
| Draw on the user's desktop from the service | Does not work at all since Vista; the ways to force it are the ones that come with the security holes session 0 isolation closed |
| **`WTSSendMessage`** | One call, no extra process; winlogon renders it inside the target session |

`WTSSendMessage` is the supported way a service talks to a user, it crosses
the isolation boundary by design, and the agent already loads `wtsapi32.dll`
for the session collector (design 0008). It is a message box rather than a
styled countdown with a background image, which is a real loss of polish and
the whole of what is given up.

The reboot capability made this same call already: design 0004 warns through
`shutdown /c`, which is the OS's own countdown, rather than drawing anything.

## 4. Idle time, and the one way this can go badly wrong

The agent asks the OS how long each session has been without input.

- **Windows**: `WTSINFOW.LastInputTime` and `CurrentTime`, from one
  `WTSQuerySessionInformation` call — the same struct the session collector
  already parses for `LogonTime`, and whose 216-byte layout is asserted at
  compile time. The two stamps are subtracted against each other rather than
  against the local clock, so the answer is immune to clock skew and to how
  long the call took.
- **Linux**: logind's `IdleHint` and `IdleSinceHint`. This is the one place
  in `usersession` that shells out to `loginctl`, because the idle hint is
  not in the session files the package otherwise parses. `show-session -p X
  --value` prints the raw property, which is a different thing from the
  column layout `list-sessions` renders and the human-readable `Timestamp`
  that design 0008 refused. `IdleHint` decides: `IdleSinceHint` keeps its
  last value after a user returns, so the timestamp alone would report a
  session being typed into as hours idle.
- **Everywhere else**: unknown.

**Unknown is a distinct answer from zero, and keeping it distinct is the
safety property of this whole capability.** `LastInputTime` is not populated
for every session on every Windows build, and a desktop environment that
never tells logind it is idle reports `IdleHint=no` forever. If unknown
collapsed to a duration, the natural collapse is "idle since the epoch" —
and the agent would log every session on every host off on its first sample.
So `IdleFor` returns `(0, false)`, and the caller skips that session
entirely. The capability doing nothing is always the safe direction.

## 5. What it does not do

**Graphical Linux sessions get no warning.** `notify` writes to the session's
TTY, which a graphical session does not have. Reaching a desktop's
notification daemon means joining the user's own D-Bus session — a dependency
this agent does not carry, and the same one design 0008 turned down for
reading sessions in the first place. The logoff still happens; the warning is
skipped and the agent says so once in its log.

That is recorded as a gap rather than fixed, and the fix if it ever matters is
the same one design 0008 names for session events: talk to logind and to the
session bus properly, once, rather than shelling out in two places.

**No background image.** `FOG_CLIENT_AUTOLOGOFF_BGIMAGE` has nothing to
configure and goes away with the module's other settings.

## 6. When it runs

On the session sampler's tick (`SessionSampleInterval`, 30s), not on the poll.

The policy is in minutes and the poll interval is an admin's choice — five
minutes by default and frequently much more. Evaluating an idle timeout only
on a poll would overshoot it by whatever that choice is, so a fifteen-minute
policy on an hourly poll could log somebody off an hour late, or warn them
sixty seconds before an event that had already happened.

The sampler is already enumerating sessions on that tick for design 0008, so
this adds a per-session idle query and nothing else. The warning set lives in
memory beside the sampler's open set and for the same reason: a restarted
agent has warned nobody, and re-warning is the harmless direction.

## 7. State machine

Per session, per tick, `autologout.Plan` — pure, and tested off-platform:

```
idle >= minutes                 -> log off, forget the warning
idle >= minutes - warn_seconds  -> warn once, remember it
idle <  minutes - warn_seconds  -> forget the warning (the user came back)
session gone                    -> forget the warning
policy disabled                 -> forget every warning
```

The last two are not tidiness. Session ids are reused, so a stale warning
record suppresses the warning for whoever gets that id next; and a set that
only ever grew would warn a user once and then log them off silently for the
rest of the machine's uptime, every time they stepped away again.

A `warn_seconds` longer than the whole timeout is clamped to half of it
rather than refused — the policy still expresses a sane intent, and refusing
it would silently disable the capability on a fat-fingered global setting.

## 8. Server side

`State::CAPABILITIES` gains `'autologout' => 'autologout'`, gated on the
module exactly as every other capability is: the global
`FOG_CLIENT_AUTOLOGOFF_ENABLED` and the host's resolved module set, which are
the same two checks `FOGClient` made for the legacy client. An admin's
existing per-host and per-group choices therefore carry over untouched.

`State::desired()` sends the block from `Host::getAlo()` — the legacy
accessor unchanged — and withholds it entirely below five minutes, so a
policy under the floor clears whatever the agent had stored rather than
sitting there as a number nobody acts on.

`FOG_CLIENT_AUTOLOGOFF_MIN` and the per-host `hostAutoLogOut` row are not
touched; nothing about the admin's mental model changes.

`FOG_CLIENT_AUTOLOGOFF_WARN` is new (schema 432, default 60), edited on the
existing Auto Log Out tab next to the timeout it modifies rather than on a
page of its own.

`FOG_CLIENT_AUTOLOGOFF_BGIMAGE` is removed in the same step — see section 5.
It had a `globalSettings` row, no reader anywhere, and no page that rendered
it, which is the defect the greenfog and `FOG_PLUGINSYS_DIR` removals were
both for.

No `message` is sent. The agent's default text is used, because a string set
on the server is one the server cannot translate into the language of
whoever is sitting at the machine.
