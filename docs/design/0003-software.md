# 0003: Software management, Chocolatey first

Status: agreed 2026-09-03 (desired state, no bootstrap in v1, Chocolatey first);
proven 2026-09-03 on the Linux lab with a shim and on `telliottwin11` with real
Chocolatey under the Windows service (pinned, re-pinned, latest, absent,
uninstall, missing backend). Follows the software row in
[0001](0001-architecture.md) section 7: "detection rule, install action,
optional uninstall action; prefer native managers; existing snapins map onto
payload with no detection rule".

## 1. Problem

FOG installs software by running a blob once. A snapin has no idea whether
the thing it installs is already there, whether it is still there a month
later, or what version the host ended up with. Reporting is "the blob exited
0", and drift is invisible. Admins who want "every lab machine has Chrome,
7-Zip and VLC, kept current" script it themselves around `choco`, and 1.6
already ships two snapin templates that call `choco.exe` for exactly that
reason (`SnapinManagement.php`, documented in fog-docs
`kb/how-tos/chocolatey-snapins.md`).

Those templates are the right instinct and the wrong shape: a package
manager already knows what is installed and at what version, so wrapping it
in a run-once task throws away the one thing that makes it better than an
installer blob.

## 2. The shape of the answer

**Software is desired state, not a task.** A software entry says "package X
from backend B should be present (at this version policy) on this host", the
agent converges the host toward it and reports the truth. That is the same
model as the hostname capability, and the opposite of a snapin.

| | Snapin (today) | Software (this design) |
|---|---|---|
| Unit | a file plus args, run once per task | a package id plus a version policy, held continuously |
| Detection | none | the backend's own installed list |
| Drift | invisible | corrected at the next drift check, reported either way |
| Reporting | exit code of one run | installed version, desired version, last outcome, when checked |
| Upgrade | a new snapin task | `version=latest` upgrades at the drift check |
| Remove | a second snapin that uninstalls | `state=absent` |

Snapins stay. They are the escape hatch for anything a package manager
cannot express, and nothing here touches them.

**Chocolatey is the first backend, not the design.** The agent talks to a
`software.Backend` interface (list installed, install, uninstall) and the
server stores a `backend` column. Chocolatey ships in v1 because Tom asked
for it and because it is the one that already has a foothold in FOG. winget,
apt/dnf/zypper and brew are later backends behind the same interface and the
same tables; nothing in the schema is Chocolatey-specific.

**Why Chocolatey before winget.** The agent runs as a Windows service under
SYSTEM. winget ships as an MSIX app, and MSIX packages cannot be registered
for the SYSTEM account, so winget in service context is unsupported by
Microsoft and works only through binary-extraction workarounds (winget-cli
issue 4422, winget-pkgs issue 346975, as of 2025). Chocolatey is a plain
executable built for unattended admin use and runs as SYSTEM by design. It
also has the simpler self-hosting story: a folder or share is a valid
source, which is what offline labs, FOG's home turf, need. Its costs are
real and named: it must be installed on the host (follow-on), and the
community feed rate-limits fleets, which the per-entry `source` answers.
winget stays a later backend, and the natural route for it is the per-user
helper from 0001 section 6.1 rather than the service.

**The backend is the `choco` CLI, not "Windows".** The provider execs a
`choco` binary at a configured path (default
`%ProgramData%\chocolatey\bin\choco.exe` on Windows, `choco` on PATH
elsewhere). That is how the fog-docs templates already call it, for the
reason the docs give (the service's PATH predates Chocolatey's entry). It
also means the whole agent-side pipeline can be proven on the Linux lab VM
with a shim `choco` that speaks the same two commands, before the real run
on `telliottwin11`.

## 3. Server model

Four tables and one module row, one schema version.

| Table | Purpose | Columns (beyond id, name, description, created) |
|---|---|---|
| `software` | the package entry | `backend` (`choco`), `package` (the id the backend knows), `version` (`''` any, `latest`, or an exact version), `state` (`present`/`absent`), `source` (optional, passed to the backend verbatim), `args` (extra backend switches), `timeout`, `returnCodes` (same `code=class` table as snapins), `enabled` |
| `softwareAssoc` | host assignment | `hostID`, `softwareID` |
| `softwareGroupAssoc` | group assignment | `groupID`, `softwareID` |
| `softwareStatus` | what the host reported, one row per host and entry | `hostID`, `softwareID`, `installedVersion`, `status` (`converged`/`installed`/`upgraded`/`removed`/`failed`/`retry`/`reboot`/`cannot_run`), `returnCode`, `details` TEXT, `checked` datetime |
| `modules` | one new row, short name `software` | so per-host and per-group module toggles gate it like every other capability |

Desired set for a host = the host's direct entries, then each group's
entries in group order, deduplicated by software id, the same rule Tom set
for snapins. An entry assigned twice with different version policies is not
a conflict the server resolves: first assignment wins, and the UI says so on
the host's Software tab.

`version` semantics:

| Value | Meaning | What the agent does when the package is present |
|---|---|---|
| `''` | present, any version | nothing |
| `latest` | track the source | `upgrade` at each drift check |
| `1.2.3` | pinned | `install --version 1.2.3 --force` only if the installed version differs |

`state=absent` with the package present runs `uninstall`; with it absent
does nothing and reports `converged`.

## 4. Protocol

One new capability, `software`, alongside `hostname`, `taskreboot` and
`snapin` in `FOG\Agent\State::CAPABILITIES`. The desired state carries:

```json
"software": {
  "drift_interval": 21600,
  "entries": [
    {"id": 3, "backend": "choco", "package": "googlechrome", "version": "latest",
     "state": "present", "source": "", "args": "", "timeout": 900}
  ]
}
```

Results go to `POST /agent/v1/result` with an `item` carrying
`{status, installed_version, exit_code, details}`. The server reads the exit
code against the entry's return-code table exactly as `Snapins::outcome()`
does for snapins (Intune defaults plus Chocolatey's `350` pending-reboot
code mapped to `reboot`), writes the `softwareStatus` row, audits it, and
answers `{"status":"ok","outcome":…}`. `reboot` feeds the reboot coordinator
as a normal (non-forced) reason; `retry` is reported and, for an unflagged entry,
tried again at the next drift check rather than the next poll, because 1618
here usually means another installer holds the MSI mutex and a minute is not
long enough. A `stop_snapins` entry that fails or retries, while snapins that run
after software are queued, holds the revision, so it runs again at the next poll.

The revision changes when the desired set changes. It does **not** change
when a status row is written, so a reporting host does not wake itself.

## 5. Agent

`internal/provider/software/`:

- `Backend` interface: `Installed(ctx) (map[string]string, error)` (package
  id to version), `Install(ctx, Entry) Result`, `Uninstall(ctx, Entry) Result`.
- `choco.go`: `choco list --limit-output --local-only` for detection (one
  call per converge, parsed as `id|version` lines); `choco install <pkg> -y
  -r --no-progress [--version V --force] [--source S] <args>`; `choco
  upgrade` for `latest`; `choco uninstall <pkg> -y -r`. Same process group,
  kill and timeout handling as the snapin runner. Binary missing or not
  executable is `cannot_run` with the path in the details, which is what the
  docs today tell admins to look for.
- Convergence runs when the revision changes and at a drift interval (server
  setting `FOG_SOFTWARE_DRIFT_INTERVAL`, default 6 hours, sent in the
  `software` block so the server owns the cadence). One `Installed()` call,
  then each entry in order, serially, with snapins and software sharing the
  agent's single run loop so two installers never overlap from our side.
  Software runs before snapins in each reconcile, except a snapin marked
  `before_software`, which runs first: the reconcile has three phases
  (those snapins, software, the remaining snapins), and an unfinished first
  phase holds the software and the rest (protocol-v1 "Software"). An entry marked
  `stop_snapins` that fails holds the snapin queue until it succeeds
  (protocol-v1 "Software").
- Reports only on change: a host that is converged and stays converged
  posts once per drift check with `status=converged`, which is also the
  "checked" heartbeat the UI shows.

Chocolatey specifics that shape the code:

- Community source rate limits. `source` is per entry so shops with an
  internal feed set it; the agent adds nothing. FOG hosting a NuGet feed is
  a follow-on (section 8).
- Chocolatey is not bootstrapped unless the server says so. A host without
  it reports `cannot_run` for every entry and the Software tab says
  "Chocolatey is not installed" once, not per row. The agent then looks for
  the binary every poll (a stat) and converges as soon as it appears, rather
  than at the next drift check: installing Chocolatey is the admin's next
  move. With `FOG_SOFTWARE_CHOCO_BOOTSTRAP_URL` set (built after the Windows
  proof; off by default because the fetched script runs as SYSTEM), the
  agent fetches that install script, runs it, reports the attempt on the
  capability, and converges in the same run. `FOG_SOFTWARE_CHOCO_NUPKG_URL`
  points the script at a package the admin hosts, for hosts with no route
  to the community feed. FOG serving the package itself is still section 8. Installing it is a follow-on with a real
  decision in it (community install script over the network, or a nupkg
  the FOG server ships), so it is not decided here.
- `choco` holds its own lock and returns 1618-style codes when it collides
  with a running installer, which the retry outcome already handles.

### Exclusive level

The software policy carries an optional `manage` field. `"exclusive"` adds a
step after the assigned entries of a backend converge: the agent uninstalls
every package that backend lists as installed and that is neither assigned
nor protected. Any other value (`""`, `"assigned"`, a missing field from an
older server) is the behavior above, with no removal.

- `Unwanted(manage, entries, installed, keep)` is pure. It returns nil unless
  the level is exclusive. A package is assigned when ANY entry for the
  backend names it, present or absent. Ids compare in lowercase, as `Decide`
  does. A blank id is never returned.
- `ConvergeExclusive` runs after `Converge`, so it reads the installed list
  the assigned entries left. It does nothing when the backend is unavailable
  or the list fails. A backend that implements `Keeper` returns the ids it
  protects and an optional removal order. When `Keep` fails, nothing is
  removed for that backend.
- A backend with no entries is also cleaned when the level is exclusive, if
  it is available. The Chocolatey bootstrap does not run for it.
- A disabled entry is not removed. The server sends its package in the
  policy's `retain` list (`{backend, package}`). The agent treats a retained
  package as assigned for protection: it is never unwanted, and for choco its
  dependency closure is kept too.
- The agent skips exclusive removal for a backend, and logs why, when any
  `Converge` report for it is `cannot_run`, or when an assigned `present`
  entry is still not installed after the pass. A failed install can leave its
  dependencies behind, and those must not be taken for strays. An `absent`
  entry that is not installed is the goal and does not skip.
- Removals are not reported per item, because the server has no row for a
  package it did not assign. The agent logs each one.
- Protection, per backend:
  - choco keeps `chocolatey`, every `chocolatey-*.extension`, and the
    dependency closure of each installed assigned `present` package, read
    from `lib\<id>\<id>.nuspec`. A missing or unreadable nuspec in the
    closure is an error, except for `chocolatey` and its extensions, which
    are kept by name. A package is removed before its own dependencies.
  - snap keeps every snap whose `snap list` Notes include base, core, snapd,
    kernel or gadget, and the providers of the slots that `snap connections`
    shows for an assigned present snap (content snaps). It also keeps every
    snap named in the snapd seed, `/var/lib/snapd/seed/seed.yaml`: built in
    snaps the OS shipped, such as firefox, snap-store and firmware-updater on
    Ubuntu Desktop, are left alone. A missing seed file means no seeded
    snaps. A seed file that cannot be read or parsed is an error, so nothing
    is removed.
  - flatpak keeps every app id named by a `[Flatpak Preinstall <app-id>]`
    group in a `*.preinstall` file in `/usr/share/flatpak/preinstall.d` or
    `/etc/flatpak/preinstall.d` (built in apps). Ids compare in lowercase. A
    missing directory is fine. A file that cannot be read is an error, so
    nothing is removed. A flatpak that a distro bakes into its image without
    a preinstall.d entry is not recognized, so the admin must check the
    server's preview of the removals. Runtimes are never candidates: the
    installed list is `--app` only. The Flatpak uninstall resolves the real
    case of an application id, because the installed map is lowercase.
  - choco has no built in protection: Windows does not preinstall
    Chocolatey packages.
- The agent also reports each available backend's installed packages in the
  software facts (design 0006), with the backend name as `source`, so the
  server can preview what an exclusive set would remove.

## 6. UI

- **Software** node: list, add, edit, delete, with the same permission
  shape as snapins. Add form: name, backend (one option for now, the field
  exists so the second backend is a row not a redesign), package, version
  policy, state, source, extra args, timeout, return codes.
- **Host** and **Group** pages: a Software tab with assign and remove,
  ordering by assignment like snapins. On the host tab, each row shows the
  reported status, installed version, and when it was checked.
- **Report**: Software status across hosts, filterable by entry and status,
  the "which machines are not on the latest Chrome" view.

## 7. Proof

1. Unit: backend parsing, convergence decisions (present/absent × any,
   latest, pinned × installed/missing/wrong version), outcome handling.
   Each decision table row is a case, and each is made to fail first.
2. Linux VM (`fog-agent-test`): a shim `choco` script that keeps a fake
   installed list in a file and honors `list`, `install`, `upgrade`,
   `uninstall` with configurable exit codes. Proves the whole pipeline end
   to end: desired set order and dedupe, drift interval, status rows,
   outcomes including `reboot` into the coordinator and `retry`, `cannot_run`
   when the binary is missing.
3. Windows VM (`telliottwin11`, host 105): real Chocolatey, one package
   pinned, one `latest`, one `absent`, with the agent running as the
   service. This is the arm only Windows can prove: the ProgramData path,
   the service PATH problem, and real Chocolatey exit codes.

## 8. Follow-ons, deliberately not in this slice

- FOG hosting the Chocolatey package and install script itself, so the
  bootstrap needs no internet (the URL settings already accept any host).
- FOG as a package source (host `.nupkg` files and serve a NuGet v2 feed
  from the storage node), which also gives an offline path.
- flatpak and snap shipped in [0018](0018-software-linux-backends.md). The
  rest remain follow-ons behind the same interface: winget for Windows,
  apt/dnf/zypper for Linux, brew for macOS.
- Migrate the two Chocolatey snapin templates: they keep working, and the
  docs page gains a "prefer Software for this" pointer once this ships.

## 9. Decisions taken here

| # | Decision | Why |
|---|---|---|
| 1 | Desired state, not a task type | Detection and drift are the whole value over a snapin; a task type would rebuild the templates with more moving parts |
| 2 | Backend interface with `choco` as the only v1 implementation | Second backend is a file, not a schema change |
| 3 | The backend is the `choco` binary at a path, not Windows | Provable on the Linux lab first; matches how FOG already calls it |
| 4 | Reuse the snapin return-code pipeline | One outcome model for everything that runs an installer |
| 5 | No bootstrap, no FOG-hosted feed in v1 | Both are real designs of their own; neither blocks the core |
