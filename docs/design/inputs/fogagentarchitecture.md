# FOG Agent: Greenfield Architecture Notes

Status: design discussion, not a commitment. Written to give a second session
or another reviewer the full context of a design conversation, so they can
argue with it rather than start over.

Audience for the product: schools and labs first. Small IT staff, Windows
heavy, shared machines with many users, reimaged on a schedule, thin or
nonexistent management stack beyond FOG itself.

---

## 1. Why rethink fog-client at all

Today's fog-client polls `management/index.php` on a timer, performs an RSA
handshake against a per-host certificate, and then roughly fifteen hardwired
modules (hostname changer, snapins, printers, power management, user tracker,
auto logout, directory cleanup, display manager, GreenFOG, ALOBG, updater,
and so on) each receive a blob from the server and act independently.

Structural problems with that shape:

- It is a task runner, not a management agent. Nothing knows whether the
  machine is actually in the requested state, only whether a job ran.
- Modules are not idempotent. Snapins rerun blindly after every image.
- Every module makes its own reboot decisions, so they fight each other.
- The client shares an entry point and code paths with the web UI, which is
  how paths like `authorize()` end up reachable before login.
- The installer and update payload live on every FOG server, so every server
  admin is a software distribution point and the client version is coupled
  to the server version.
- Mono on Linux and Mac has no future.

The rethink: the agent is a state convergence engine. The server holds a
desired state per host. The agent's loop is fetch desired state, compare to
actual, reconcile, report drift and results. Hostname, join, printers,
software, power all become instances of one pattern.

## 2. Two things that must stay separate

| Concern | Where it lives | Notes |
|---|---|---|
| Distribution | Central, project hosted | Binaries, installers, signed release manifest. GitHub Releases behind a project owned domain. |
| Enrollment and control | Customer's FOG server | Device identity, certificates, desired state, credentials. Never central. |

The agent takes binaries from the central manifest and orders only from its
enrolled FOG server. The project never sits between a school and its AD
credentials.

## 3. Core (platform agnostic, small)

- **Identity and enrollment.** Device certificate issued at imaging time or
  first boot via a one time enrollment token. mTLS from then on. FOG already
  runs a CA for the current handshake; the idea survives, the implementation
  does not.
- **Transport.** Outbound only, HTTPS. Poll with a cheap "anything changed"
  endpoint plus a server side nudge flag. Long poll or websockets on PHP
  under Apache is painful; if real time is wanted later, a small sidecar
  gateway daemon alongside the existing PHP daemons is the honest answer.
  Versioned JSON API, completely separate from the UI entry point.
- **Facts and inventory.** Hardware, OS, installed software, logged in user,
  disk encryption state, pending reboot. Reported on change, not every poll.
- **Reboot and user presence coordination.** One subsystem. Maintenance
  windows, deferral, user notification. Every other feature asks it, none
  reboot on their own.
- **Task execution.** Run this script, stream stdout and exit code back. The
  escape hatch that half of today's snapins actually are.
- **Self update.** Signed manifest, channel aware, server pinnable. See
  section 6.
- **Provider model.** Each capability is an interface. Per OS implementations
  are selected at build time. Different binaries per OS and arch is fine and
  simplifies things.

## 4. Capabilities, with the skepticism attached

**Post image provisioning handoff.** The v1 story, not a feature. Imaging
drops the agent binary plus a bootstrap file (server URL, one time enrollment
token) onto the disk before first boot. On first boot the agent enrolls and
converges: name, join, printers, software, before anyone logs in. Nobody else
does this and it is what ties the agent to FOG.

**Software management.** Snapins today are "run this blob with these args."
Redefine software as: detection rule, install action, optional uninstall
action. Prefer native package managers (winget or MSI, apt/dnf/zypper, brew
or pkg) with arbitrary payload as fallback. Detection is what makes it
idempotent and makes reporting truthful.

**Printers.** CUPS covers Linux and Mac with one code path. Windows spooler is
its own provider. Keep the existing assignment plus default printer model.

**Identity provider enrollment** (not "domain join"). Samba AD is just AD to
the client. Windows join is native. Linux is realmd or adcli plus sssd. Mac is
dsconfigad. Plain LDAP is not a join, it is sssd or nslcd config, so it is a
separate provider behind the same interface. Windows AD in v1, the rest later.

**Power scheduling and peer WoL relay.** Energy is a real district line item.
An agent on a subnet can wake its neighbours, which fixes FOG's oldest cross
subnet WoL complaint with almost no code. Both in v1 because they are cheap
once the agent exists.

**User tracking and auto logout.** Shared machines, minors, accountability.
Keep both, they are small.

**System updates.** Linux is one package manager call. Windows Update via the
WUA API is a swamp of feature updates, reboot loops, and driver surprises.
Mac updates are MDM territory. v1 scope: report state, trigger scan or
install, enforce reboot policy. Do not try to be WSUS. It will eat the
project.

**Remote access.** Largest scope and largest security surface. Do not embed a
screen capture stack across three OSes. Two defensible options: a reverse
tunnel broker (agent dials out, admin connects through the server to RDP, VNC,
or SSH already on the box), or install and enrol something that exists
(Veyon, RustDesk, MeshCentral) through the software feature. Schools often
already run Veyon for classroom control. Tunnel broker is v3.

**Also worth having, not on the original list:**

- Disk encryption key escrow (BitLocker, FileVault, LUKS). Imaging shops lose
  recovery keys constantly. Killer feature for this audience.
- Local admin password rotation, LAPS style. Small to build, big value.
- Certificate deployment. Falls out of the trust infrastructure.

**Drop:** display manager, GreenFOG as separate from power management, ALOBG.
These are XP era.

## 5. Platform matrix

Skeptical position: 32 bit Windows is dead as of Windows 11, 32 bit Mac died
with Catalina. The only 32 bit that plausibly matters is armv7 Linux for
older Raspberry Pi class devices, and residual Win10 x86 in schools.

| OS | Targets | Priority |
|---|---|---|
| Windows | x64, arm64, x86 if it costs nothing | v1 |
| Linux | x64, arm64, armv7 | v2 |
| macOS | universal binary | v3 |

Mac is v3 because FOG cannot image Apple Silicon and Apple's management story
is MDM whether we like it or not. If the agent later becomes a standalone
management product, that changes.

## 6. Distribution design

- **Stable project domain**, for example a releases subdomain, that only
  ever redirects into GitHub Releases. That domain is what is baked into the
  agent, never a GitHub URL. Hosting can move later without touching a
  single installed machine.
- **Signed manifest** at a fixed path per channel (stable, beta, nightly).
  Lists versions, per platform artifact URLs, hashes, minimum supported
  server protocol version. Signed with a project key (minisign is simple and
  sufficient), public key compiled into the agent. Agent verifies manifest
  signature, then artifact hash. Nothing else is trusted, including the FOG
  server.
- **Mirror mode on the FOG server.** Server fetches manifest and artifacts on
  its own schedule and serves them at identical paths. Agents try central
  first and fall back to their server, or the admin forces local only. This
  covers filtered and air gapped school networks with one code path.
- **Version pinning per server.** The FOG server can cap the agent version
  for its fleet. Central publishes, local decides.
- **Native installers as artifacts.** MSI for Windows (plays with GPO and
  existing habits), deb and rpm for Linux, pkg for Mac later. Plus a raw
  binary for the post image path.
- **Protocol negotiation is mandatory**, not nice to have. A school with a
  single tech and a three year old FOG server will be running a current
  agent. The agent must run happily against old servers and the manifest's
  minimum server version is how old ones are cut loose deliberately.

Operational costs to accept up front: someone owns the signing key and
release pipeline and it is not a laptop. Windows code signing costs money
yearly and is annoying for an open source nonprofit, but without it
SmartScreen scares every school tech on first install. Apple notarization
needs a paid developer account.

## 7. Language: Go, and what it actually takes

Recommendation is Go. Rust is safer but slower to develop in with a thinner
contributor pool. C# with .NET native AOT is a real option because current
fog-client contributors know it, but 32 bit ARM Linux support is shakier,
Mono is not a future, and the tooling for single static cross platform
binaries is weaker. If the team is C# people who will not touch Go, that
changes the answer. Prior art for Go agents: Tailscale, osquery's Fleet
agent (orbit), Nomad and Consul clients, Grafana Agent, Velociraptor.

### 7.1 Why Go fits this specific problem

- `GOOS`/`GOARCH` cross compilation from one Linux CI runner covers the whole
  matrix. No per platform build hosts unless CGO is pulled in.
- Static binaries, roughly 10 to 15 MB, no runtime to install. Matters on
  freshly imaged machines with nothing on them.
- Build tags (`//go:build windows`) select the per OS provider at compile
  time. The provider model in section 3 maps onto this directly.
- Standard library covers mTLS (`crypto/tls`), HTTP, JSON, structured logging
  (`log/slog`), and process management. Dependency count can stay very low,
  which matters for supply chain review of a binary that runs as SYSTEM.
- `golang.org/x/sys/windows/svc` gives native Windows service support without
  a wrapper. systemd and launchd need only a unit file and a plist.
- Windows 7 and 8 support ended at Go 1.21. Minimum target is Windows 10.
  That is acceptable for schools in 2026 but should be stated in the docs.

### 7.2 Repository layout sketch

```
fog-agent/
  cmd/
    fog-agent/          service entry point
    fog-agent-helper/   per user session helper (see 7.4)
    fog-agent-ctl/      admin CLI: enrol, status, force converge
  internal/
    core/               loop, state store, reboot coordinator, scheduler
    api/                versioned client for the FOG server protocol
    manifest/           signed manifest fetch, verify, self update
    provider/           interfaces only
      hostname/         hostname_windows.go, hostname_linux.go, ...
      identity/         AD join, realmd, dsconfigad
      printer/          winspool, cups
      software/         detection rules, winget, msi, apt, dnf, brew
      power/            schedules, WoL relay
      inventory/        facts collection
      updates/          wua, apt, softwareupdate
  build/
    windows/            WiX source for MSI, service install
    linux/              systemd unit, nfpm config for deb and rpm
    darwin/             launchd plist, pkg scripts
  .goreleaser.yaml
```

### 7.3 Per OS mechanics

Windows, almost all of it is available without CGO by calling DLLs through
`golang.org/x/sys/windows` or `syscall.NewLazyDLL`:

| Need | Mechanism |
|---|---|
| Service lifecycle | `x/sys/windows/svc` |
| Hostname | `SetComputerNameEx` in kernel32 |
| AD join | `NetJoinDomain` in netapi32 |
| Printers | winspool `AddPrinterConnection`, `SetDefaultPrinter`, or PowerShell `Add-Printer` as fallback |
| Inventory | WMI via `go-ole` or the `StackExchange/wmi` package, registry via `x/sys/windows/registry` |
| Software | `winget` exec, `msiexec` exec, uninstall registry keys for detection |
| Updates | WUA COM API via `go-ole`, this is the one genuinely painful provider |
| BitLocker | WMI `Win32_EncryptableVolume` |
| Logged in user, session events | WTS APIs in wtsapi32, or the helper process reports |

Linux, almost everything is exec of the standard tool with parsed output.
That is fine and is what every other agent does:

| Need | Mechanism |
|---|---|
| Service | systemd unit |
| Hostname | `hostnamectl` or D-Bus to hostnamed |
| AD join | `realm join` or `adcli`, then sssd |
| Printers | `lpadmin`, `lpoptions` |
| Software | `apt-get`, `dnf`, `zypper`, `dpkg-query`/`rpm -q` for detection |
| Updates | same package managers |
| Inventory | `/sys`, `/proc`, `dmidecode`, `lsblk` |
| LUKS | `cryptsetup luksDump` |
| Users | `loginctl`, `who`, D-Bus logind |

macOS, also exec heavy: `scutil`, `dsconfigad`, `lpadmin`, `softwareupdate`,
`fdesetup`, `system_profiler -json`. Universal binary via `lipo` over the
amd64 and arm64 builds.

### 7.4 The one design detail people miss: session 0

A Windows service runs in session 0 and cannot show UI or see the desktop.
Anything user facing (reboot warnings, logoff countdown, notifications,
accurate "who is logged in") needs a small per user helper process launched
into each interactive session, talking to the service over a named pipe on
Windows and a Unix socket elsewhere. Today's fog-client has a tray for the
same reason. Design this in from the start: the service owns all decisions,
the helper only displays and reports. The helper must never be trusted to
issue commands.

### 7.5 State, config, and logging

- State store: `bbolt` (pure Go, single file) or `modernc.org/sqlite` (pure
  Go SQLite). Either avoids CGO. Holds last known desired state, last applied
  state, pending reboot reasons, enrollment material.
- Config: one small file, TOML or YAML. Server URL, channel, pin, log level.
  Everything else comes from the server.
- Logging: `log/slog` JSON to a rotating file plus Windows Event Log and
  journald where available. Ship recent log tail to the server on request.
- Secrets: enrollment private key in the OS keystore where possible (DPAPI on
  Windows, keychain on Mac), file with tight permissions on Linux.

### 7.6 Release pipeline

- GitHub Actions on tag. `goreleaser` builds the matrix, produces archives,
  deb and rpm via its built in `nfpm`, checksums, and the GitHub Release.
- MSI via WiX in a Windows job. Authenticode signing via Azure Trusted
  Signing or SignPath (SignPath has a free tier for open source). Never a
  laptop.
- Mac pkg, codesign, and notarization in a macOS job. Paid Apple developer
  account.
- Manifest generation and minisign signing as the final job. Private key in
  a GitHub secret at minimum, a hardware token or KMS if the project can
  manage it.
- Channels map to branches or tag patterns: `v1.2.3` is stable,
  `v1.3.0-beta.1` is beta, nightly from main.

### 7.7 Testing

- Unit tests for core loop and providers behind fake OS shims.
- Provider integration tests in CI on the real OS runners GitHub provides
  (windows-latest, ubuntu-latest, macos-latest). AD join tests need a Samba
  AD container, which is doable in the Linux job.
- End to end: a FOG server in a container plus an agent in a VM, exercising
  enrol, converge, self update, and downgrade refusal.

### 7.8 Rough effort

Honest rough numbers for someone who knows Go and Windows internals, not a
plan:

| Piece | Rough size |
|---|---|
| Core loop, transport, enrollment, state store | 4 to 6 weeks |
| Manifest, self update, signing pipeline | 2 to 3 weeks |
| Windows service plus session helper | 2 to 3 weeks |
| Windows providers for v1 (hostname, AD, printers, software, power, inventory) | 6 to 8 weeks |
| Server side API, schema, UI on working-1.6 | 6 to 8 weeks, PHP |
| Installers and CI | 2 weeks |

Call it two people for a semester to a usable v1 on Windows. Linux providers
after that are quick because they are mostly exec. Updates and remote access
are the two that blow budgets, which is why they are pushed out.

Learning curve for C# developers is low. Go is a smaller language than C#.
The things that bite are error handling style, no exceptions, no generics
heavy code, and thinking in goroutines instead of async/await. A week of
discomfort, not a retraining.

## 8. Phasing

**v1.** Enrollment, transport, inventory, reboot coordination, hostname,
Windows AD join, printers, software with detection rules, power scheduling,
peer WoL relay, user tracking, auto logout, script escape hatch, self update
with signed manifest and server pinning, post image handoff. Windows x64 and
arm64. Server side on `working-1.6` first per the repo's feature rule.

**v2.** Linux providers, update state and reboot policy, disk encryption
escrow, local admin rotation, certificate deployment, mirror mode polish.

**v3.** Tunnel broker for remote access, Mac, Linux and Mac identity
providers, optional real time gateway sidecar.

## 9. Open questions

- Does the existing fog-client team want to write Go? This is the single
  biggest input to the language decision and it has not been asked.
- Should the agent be installable standalone against any FOG server, making
  it a way into FOG for shops that only want management? The distribution
  design allows it. It should be a deliberate decision.
- Who owns the signing keys and the Apple and Windows signing accounts
  organizationally?
- Is the server side API a new PHP router under `working-1.6`, or does the
  gateway sidecar come earlier and own the whole agent API from day one? The
  second is cleaner and means one language for agent and gateway, but it adds
  a new daemon to every FOG install.
