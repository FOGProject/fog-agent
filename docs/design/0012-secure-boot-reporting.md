# 0012: Secure Boot posture as a reported fact

Status: agent side BUILT 2026-09-05 (`internal/secureboot`, gathered in
`cmd/fog-agent/facts.go`); the server's `FACT_REPORTS` entry is not written
yet, so nothing is stored. Verified on both platforms with the shipping code:
this Linux workstation reports `{efi 00 00}` (→ `disabled`, which is right —
Secure Boot is off on it) and `telliottwin11` reports `{efi 01 00}`
(→ `enforcing`, against the ledger's stale `disabled` that motivated the
whole document).

Adds a sixth kind to the fact channel of
[0006](0006-inventory.md): the agent reports what its firmware says about
Secure Boot, so `hosts.hostSbState` stops being only as fresh as the last
netboot.

FOG already has the column, the vocabulary and the reporter. This document
does not propose any of those. It proposes a second reporter for the same
column, because the existing one can only speak at a moment most machines
reach rarely, and the value it leaves behind has no expiry.

---

## 1. What is there today

`hosts.hostSbState` and `hosts.hostSbStateTime` (schema 376) hold what a
machine last said about its own Secure Boot posture and when the server heard
it. `hostSbEnrollCert` and `hostSbEnrollVia` (schema 377) record which
certificate was enrolled and by which route.

The state is one of six names, `FOG\Boot\SecureBootState`'s constants, five
taken verbatim from FOS's own `sbState()` in `secureboot-funcs.sh` so the two
reporters cannot drift into two vocabularies for one fact:

| value | meaning |
|---|---|
| `unknown` | nothing has reported yet — server-side only |
| `nonefi` | booted BIOS/CSM; Secure Boot is not a concept here |
| `noefivars` | UEFI, but the variables could not be read |
| `setup` | Setup Mode — db is writable, enrollment is unattended |
| `enforcing` | User Mode, Secure Boot ON — the enrollment task cannot run |
| `disabled` | User Mode, Secure Boot OFF — the ADR 0008 case |

**It is reported by iPXE, on every PXE boot** — not by FOS, and the schema
comment is explicit about why: FOS runs when someone schedules a task, which
may be months away or never, whereas iPXE runs every time the machine
netboots. The raw observations ride the boot request and
`SecureBootState::fromBootRequest($platform, $secureBoot, $setupMode)` turns
them into one of the six names. `hostSbStateTime` is stamped by the server
from its own clock, never taken from the request.

The column is **advisory only** (ADR 0029). `boot.php` is unauthenticated by
necessity — a booting NIC has no credential — so the value is
attacker-controlled and nothing may read it as a security control. It exists
for targeting, filtering and display.

## 2. The blind spot, measured

iPXE speaks on every netboot. A healthy machine that boots from its own disk
never netboots, so for that machine the ledger is frozen at whatever it said
the last time somebody imaged it.

Measured on the lab install, host 105 `telliottwin11`, 2026-09-04:

```
sbstate      disabled
sbstatetime  2026-09-05 01:01:01
sbenrollvia  db
```

and on the machine itself, the same evening:

```
PS> Confirm-SecureBootUEFI
True
```

Both are correct. At 01:01:01 the enrollment had just written PK, KEK and db,
and the firmware was in User Mode with Secure Boot still off — `disabled` was
the true answer at that instant. Secure Boot was switched on immediately
afterwards, the machine has not netbooted since, and so the server still
believes a host that is enforcing is a valid enrollment target.

That is the failure mode worth naming: **the ledger's error is biased in the
dangerous direction.** `disabled` is the value that makes a host look
eligible, and it is exactly the value a machine leaves behind on the last
netboot before it starts enforcing. The schema comment already refuses to
*default* to `disabled` for this reason; a stale `disabled` gets there by
another road.

## 3. The proposal

A `secureboot` fact kind: one entry in `State::FACT_REPORTS`, one block in the
poll request. No new route — the route rule (`protocol-v1.md`).

It is a fact and not a capability: it is something the host observed about
itself, not something it did with a task, so it rides the poll request
alongside inventory, software, directory, printers and network rather than
appearing in the poll answer.

Hashed and skipped like every other fact, so a fleet whose posture is not
changing costs one hash comparison per host per hour, not a write.

## 4. What the agent sends: observations, not a verdict

The agent sends **the same three raw inputs iPXE sends**, and the server maps
them with the existing `SecureBootState::fromBootRequest()`:

```json
"secureboot": {
  "platform":   "efi",
  "secure_boot": "01",
  "setup_mode":  "00"
}
```

This is the whole point of the design. The alternative — the agent computing
`"enforcing"` itself and sending the name — puts the six-way mapping in two
codebases in two languages, which is the drift the vocabulary was copied
verbatim from FOS to avoid. One mapping, one place, two reporters feeding it.

It also means the agent needs no opinion about precedence between `setup` and
`enforcing`, which is a real subtlety: `SetupMode == '01'` wins over
`SecureBoot == '01'`, because a machine in Setup Mode accepts a db write
whatever the SecureBoot byte says.

## 5. What the agent reads

| platform | source |
|---|---|
| Linux | `/sys/firmware/efi/efivars/SecureBoot-8be4df61-93ca-11d2-aa0d-00e098032b8c` and `SetupMode-…`. `/sys/firmware/efi` absent means `platform=bios` and the other two go empty |
| Windows | `GetFirmwareEnvironmentVariableW` for both variables under `{8be4df61-93ca-11d2-aa0d-00e098032b8c}` |
| macOS | sends nothing |

**The two platforms return different bytes for the same variable, and the
difference is silent.** Measured 2026-09-04:

```
Linux   /sys/.../SecureBoot-8be4df61-…   size=5  bytes=0600000000
Windows GetFirmwareEnvironmentVariableW  len=1   bytes=01
```

efivarfs prepends the 4-byte EFI attribute word; the Win32 call does not.
Taking `b[0]` off a raw read would be right on Windows and wrong on Linux,
where it reads the attribute word's low byte `0x06` — neither `00` nor `01`,
so every Linux host would land in `noefivars`.

That difference is now absorbed in one place: `internal/firmware.ReadVar`
strips the attribute word, so every caller sees the data alone and the byte
wanted is the first one. The package exists precisely so this is handled once
rather than in both `internal/secureboot` and `internal/netboot` (design
0013), which would be two copies of the same trap.

Two corrections to what this document first claimed, both from the same
measurement. Reading these variables needed **no elevation**: the probe ran in
a UAC-filtered ssh session as an ordinary admin account and returned both
values, so `SE_SYSTEM_ENVIRONMENT_NAME` is a requirement for *setting* a
firmware variable, not for getting one. And `Confirm-SecureBootUEFI` is not
needed at all — it is the documented PowerShell route, but shelling out to
PowerShell on every poll to read two bytes is not worth it when the Win32 call
answers directly. The registry value
`HKLM\SYSTEM\CurrentControlSet\Control\SecureBoot\State\UEFISecureBootEnabled`
gives SecureBoot but *not* SetupMode, so it cannot be the primary source.

Still unmeasured: the BIOS/CSM signal on Windows. The API is documented to
fail with `ERROR_INVALID_FUNCTION` on a machine with no UEFI, and there is no
BIOS Windows guest in the lab to confirm it on. Treat any error from the call
as `platform=bios` only after that is checked; until then an error should
produce `noefivars`, which is the answer that cannot make a host look
eligible.

macOS sends nothing rather than sending `nonefi`. Apple's platforms have a
secure boot model that is not UEFI Secure Boot at all, and there is no honest
mapping onto these six values; `nonefi` would assert "Secure Boot is not a
concept here", which is false. Omitting the block leaves the ledger untouched,
which is the truthful outcome.

## 6. Precedence when the two reporters disagree

Last writer wins, on the server's clock, the same rule the column already
uses.

The two reporters see genuinely different instants, not different truths: iPXE
sees the firmware at netboot, the agent sees it from the running OS. When they
disagree, the fresher observation is the better answer to the question the
column exists to answer — "can the enrollment task do anything on this host
today".

One consequence worth stating out loud: an enrollment task's own netboot
writes a *transient* state. During `mode=enrollsb` the machine passes through
Setup Mode, and iPXE records that. The agent's next poll after the machine
comes back up overwrites it with the steady-state answer, which is the one an
admin wants for targeting. Today nothing overwrites it at all — that is
section 2.

## 7. It is still advisory, but now it is attributable

The agent's report does **not** promote `hostSbState` into a security control,
and nothing in this design should be read as arguing that it does. A
compromised operating system can lie about its own firmware exactly as a
spoofed boot request can.

What changes is narrower and still worth having: the boot request is
anonymous, so a false value cannot be pinned on anyone, whereas an agent
report arrives over an enrolled mTLS channel, so the server knows which host's
certificate asserted it. ADR 0029's rule stands unchanged — for targeting,
filtering and display, never for authorization.

## 8. What this does not do

The agent never enrolls keys. Enrollment needs Setup Mode and a pre-OS
environment, and it stays entirely with FOS and the task types that exist
(`ENROLL_SECUREBOOT`, `mode=enrollsb`). This design only closes the reporting
gap that makes an admin choose those tasks blind.

It also does not add a second switch. Secure Boot reporting rides
`FOG_AGENT_INVENTORY_ENABLED` with the other facts; an admin who has turned
fact collection off has said what they want.

## 9. Open

- The BIOS/CSM signal on Windows (section 5) — no BIOS Windows guest in the
  lab to measure it on.
- Whether the host list's Secure Boot filter should distinguish "reported by
  an agent within the hour" from "reported by iPXE eight months ago". The
  data is there — `hostSbStateTime` — and `SecureBootState` already has a
  freshness vocabulary (`FRESH`/`STALE`) for the *enrollment certificate*.
  Reusing that shape for state staleness is the obvious move, but it is a UI
  decision and belongs to whoever owns the host list.
