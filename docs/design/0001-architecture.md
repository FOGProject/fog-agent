# fog-agent: architecture

Status: PROPOSED, 2026-09-03. Merged from two independent design passes (this
session's first draft and `inputs/fogagentarchitecture.md` from a second
session). Where the two disagreed, the "Divergence log" at the end says which
way it went and why. Decisions 1 to 3 in section 13 were settled with Tom on
2026-09-03; the rest are still his to make.

Audience: schools and labs first. Small IT staff, Windows heavy, shared
machines with many users, reimaged on a schedule, little management tooling
beyond FOG itself.

## 1. Problem

`fog-client` (C# on .NET Framework 4.5.2, Mono on Linux and macOS) is a task
runner, not a management agent, and its structure cannot be patched into one:

| Problem | Evidence |
|---|---|
| Needs a runtime on every machine | Every `.csproj` targets .NET Framework; Linux and macOS run under Mono, which has no future |
| Host identity is MAC only | Every request carries `&mac=`; the server resolves it in `FOGBase::getHostItem()`. Cloned and shared-NIC machines collide, which is what fogproject #198 exists to fix |
| Hand-rolled crypto | RSA-encrypts an AES key that is stored on the host row (`pub_key`); every response is AES-wrapped as `#!en=`. The domain-join password rides in ordinary check-ins |
| Nothing knows the machine's actual state | Modules act on whatever blob they receive. Snapins rerun blindly after every image. Each module reboots on its own, so they fight |
| Client shares the web UI entry point | `management/index.php?sub=requestClientInfo` and `/service/*.php` are inside the same code paths as the UI |
| Silent in-place self-update from every FOG server | `getversion.php` drives `SmartInstaller.exe /upgrade`; every server admin is a software distribution point and client version is coupled to server version |

## 2. The shape of the answer

The agent is a **state convergence engine**. The server holds a desired state
per host. The agent's loop is: fetch desired state, read actual state,
reconcile, report drift and results. Hostname, directory membership, printers,
software, and power are all instances of that one pattern. One-off work (run a
script, reboot into an imaging task) is a second, smaller path.

Written in **Go** as one static binary per OS and architecture. Identified by
the **SMBIOS host identity** fogproject already records at PXE boot, and
**authenticated** by a per-host key and a certificate from FOG's own CA
(mutual TLS). **Outbound only**, HTTPS and JSON, against a versioned
`/agent/v1` on fogproject 1.6. **The agent checks a central signed
manifest for new versions, tells its server when one exists, and applies it
only when an admin says so.**

## 3. Two concerns kept apart

| Concern | Where it lives |
|---|---|
| Distribution: binaries, installers, signed release manifest | Central, project owned. GitHub Releases behind a project domain that only ever redirects there |
| Enrollment and control: identity, certificates, desired state, credentials | The customer's FOG server. Never central |

The agent takes **orders only from its enrolled FOG server**. Its only other
outbound connection is an anonymous, rate-limited fetch of the central release
manifest (section 9), which carries no host identity and whose absence is
never an error. The project never sits between a school and its AD
credentials.

## 4. Identity and enrollment

Two different questions, deliberately answered by two different mechanisms:

- **Which host record is this?** SMBIOS identity plus MAC list. Discoverable by
  anyone, so it may resolve but must never authenticate.
- **Is this really that host?** A private key the host generated and never
  sends, proven with mutual TLS.

### 4.1 Resolution

The agent reports the same four values `IpxeBootMenu::_applySmbiosIdentity()`
and FOS record (fogproject PR #1668). Canonicalization and placeholder
rejection ("To Be Set By OEM", `000000000`, "none") stay **server side** in
`FOG\Base\SmbiosIdentity`, so PXE, FOS and agent obey one rule set.

Verified 2026-09-03 on lab host 105 (`telliottwin11`, a VirtualBox EFI guest
with no DMI overrides): the agent's Windows reader printed
`de96f442-f130-1148-abf7-1d70703855bb`, and a FOS inventory task run minutes
later stored the same string. VirtualBox's own hardware UUID for that VM is
`42f496de-30f1-4811-…`, the first three fields reversed, because VirtualBox
declares SMBIOS 2.5 and every reader in the estate (iPXE, dmidecode, the
Linux kernel, this parser) swaps those fields only from 2.6 on. The agent
therefore matches what FOG stores, not what the hypervisor shows, which is
the right side to be on. On the Precision (real Dell hardware) the Linux
reader returned the kernel's canonical values with the expected Dell traits.
One rendering difference: dmidecode prints `Not Specified` for an absent
string where the agent sends empty; `SmbiosIdentity::PLACEHOLDERS` already
lists `Not Specified`, so both read as absent server side.

| Field | Linux | Windows | macOS |
|---|---|---|---|
| system UUID | `/sys/class/dmi/id/product_uuid` | raw table via `GetSystemFirmwareTable('RSMB')`, parsed by `internal/identity/smbios` with the declared-version byte-order rule | `IOPlatformUUID` |
| system serial | `product_serial` | same table, type 1 | `IOPlatformSerialNumber` |
| board serial | `board_serial` | same table, type 2 | not exposed; send empty |
| chassis asset | `chassis_asset_tag` | same table, type 3 | not exposed; send empty |

Windows reads the raw table rather than WMI so that one parser, with one
byte-order rule, serves every OS and can be unit-tested against a fixture.

### 4.2 Enrollment

| Step | What happens |
|---|---|
| Key | Agent generates an EC P-256 key pair on first run. Private key in DPAPI on Windows, Keychain on macOS, a `0600` root-only file on Linux. The SMBIOS identity it was generated for is stored beside it |
| Request | `POST /agent/v1/enroll` over server-authenticated TLS: SMBIOS values, MAC list, hostname, OS and arch, agent version, a CSR, and an optional **enrollment token** |
| Resolve | Server calls `HostManager::resolveHostBySmbios()`, then MAC, under the existing `FOG_HOST_IDENTIFY_SMBIOS` policy |
| Decide | See table below |
| Issue | Server signs the CSR with the FOG CA already in `/opt/fog/snapins/ssl/` and stores the certificate fingerprint on the host |

| Situation | Outcome |
|---|---|
| Matched host, no bound certificate | Issue and bind |
| Matched host, live certificate, server completed a deploy task on that host within a configurable window | Issue and rebind: this is the **post-image handoff** (4.3) |
| Matched host, live certificate, no recent deploy | Hold for admin approval. A reimaged machine and a stolen identity look identical from here |
| Valid enrollment token, no match | Create the host: agent-based registration |
| No match, no token | Pending list in the web UI; admin approves or denies |

Comparable systems doing exactly this: Puppet (CSR plus autosign policy),
Fleet/osquery (enroll secret then node key), Tailscale (node key plus admin
approval), SCEP and EST under MDM. It is the standard shape.

### 4.3 Post-image handoff

The v1 story, and the thing no other agent does. Imaging drops the agent
binary and a bootstrap file (server URL, CA certificate, optional one-time
token) onto the disk before first boot. On first boot the agent enrolls, is
matched by SMBIOS to the host FOG just imaged, and converges name, directory
membership, printers and software before anyone logs in.

### 4.4 Reimage and clone safety

A captured image may carry an enrolled agent's key. On start the agent
compares the live SMBIOS identity to the one stored beside the key; on
mismatch it discards the key and re-enrolls. A clone of an enrolled machine
therefore cannot present as the original. This is where the unique host
identifier actively prevents a collision rather than just resolving one.

### 4.5 Steady state

Every request is mutual TLS. The server maps certificate fingerprint to host
row. No `mac=` on the wire, no per-host AES key, no `#!en=`. The domain-join
credential travels only inside a task response for a host that has that task,
never on a routine check-in. Certificates have a lifetime and the agent renews
by CSR over its existing mTLS session.

## 5. Transport and server API

Outbound only, HTTPS, JSON. The agent polls a cheap "anything changed since
revision N" endpoint on an interval (default five minutes, server adjustable)
and fetches desired state only when the revision moved. A server-side nudge
flag makes "run this now" arrive at the next poll without a push channel.
Long-poll can be added later without changing the model. Push from server to
client fails through NAT and firewalls, and every comparable agent pulls.

The API is `/agent/v1/*` on fogproject working-1.6, routed by the
existing `FOG\Router\Route` and gated by the existing `Authorization` layer,
with the client certificate as the principal. It sits beside the legacy
`/service/*.php` endpoints so both clients coexist during transition.

### 5.1 The agent owns the contract; the server answers what it can

The agent is released more often than the server and a school runs whatever
FOG it installed three years ago. So the agent, not the server, defines the
protocol, and it must run against a server that is behind it:

- A small **hard floor**: enroll, renew, poll, facts, task result. A server
  without these is not an agent server, and the agent says so in its log and
  keeps retrying on a slow backoff.
- Everything else is a **capability**. On every poll the server returns the
  list it supports (`software.detection`, `directory.ldap`, `wol.relay`,
  `update.pin`, and so on). A capability the server does not list is simply
  not exercised: the provider stays idle, one informative log line per
  change of state, no error, no refusal to run.
- The agent carries the newest schema; the server's `Route` layer versions
  the URL (`/v1`) and only bumps it for breaking shape changes. Additive
  fields are ignored by the side that does not know them.

This replaces "manifest carries a minimum server version and old servers are
cut loose": nothing is cut loose, features light up as servers upgrade.

### 5.2 Security properties of the pull path (decision 3)

Decided: PHP under `Route`. The properties that make the pull secure, all of
which are ordinary TLS and none of which are bespoke:

| Property | How |
|---|---|
| Agent only talks to its own server | Server URL and the CA bundle to trust are written at bootstrap; the agent trusts that bundle, not the OS trust store. The bundle is the FOG CA by default, or the public CA an admin configured for the web UI (`EXTERNAL_CA_AND_LETSENCRYPT.md`) |
| Server knows which host is calling | Apache requires and verifies a client certificate on `/agent/`; `Route` maps the fingerprint to the host. No cookies, no bearer tokens, no `mac=` |
| Nothing secret rides a routine poll | Credentials (directory join) appear only inside a task response for a host that has that task, and the task is marked consumed when the result comes back |
| Payloads are checked before they run | Every downloadable payload carries a sha256 in the desired state; the agent refuses a mismatch |
| Server side is least privilege | The agent principal can read and write its own host's state and nothing else, enforced in `Authorization` the same way API tokens are scoped today |
| Replays and stale state | Desired state carries a revision; results reference the revision they applied. A replayed old response cannot move the agent backward |

## 6. Core subsystems

| Subsystem | Responsibility |
|---|---|
| Identity and enrollment | Section 4 |
| Transport | Section 5 |
| Facts and inventory | Hardware, OS, installed software, logged-in user, disk encryption state, pending reboot. Reported on change, not every poll |
| Reboot and user-presence coordinator | The **only** thing that reboots. Maintenance windows, deferral, user notification, "reboot when nobody is logged in". Every provider asks it; none reboot on their own |
| Convergence loop and state store | Desired state, last applied state, pending reboot reasons, enrollment material. Pure-Go store (`bbolt` or `modernc.org/sqlite`), no CGO |
| Task runner | Run this script, stream stdout and exit code back. Payload hash checked before execution. The escape hatch half of today's snapins actually are |
| Session helper | See 6.1 |
| Update check | Section 9 |
| Providers | One interface per capability, one implementation per OS chosen at build time by `//go:build` tags. "Not supported here" is a compile-time fact |

### 6.1 Session 0 and the per-user helper

A Windows service runs in session 0 and cannot show UI or see the desktop.
Anything user facing (reboot warnings, logoff countdown, notifications,
accurate "who is logged in") needs a small helper launched into each
interactive session, talking to the service over a named pipe on Windows and a
Unix socket elsewhere. Today's client has a tray process for the same reason.
The service owns every decision; the helper only displays and reports, and is
never trusted to issue commands.

## 7. Capabilities

| Capability | Today | Design | Phase |
|---|---|---|---|
| Inventory | MAC and module status | Facts on change (section 6). SMBIOS, hardware, OS, software list, agent version | v1 |
| Task reboot into imaging | `/service/jobs.php` | A one-off task through the reboot coordinator. **Keep.** This is what makes it a FOG agent | v1 |
| Hostname | HostnameChanger | `ensure hostname` provider | v1 |
| Directory membership | AD, Samba, Open Directory on Windows and macOS; nothing on Linux | One `directory` object on the server. Types: `ad` (Samba AD is AD from the client side) and `ldap` (not a join; sssd or nslcd config, so a separate provider behind the same interface). Windows: `NetJoinDomain`. Linux: `realm join` or `adcli` plus sssd. macOS: `dsconfigad` | Windows v1, others v2 |
| Software | Snapins: run a blob with args | Software = detection rule, install action, optional uninstall action. Prefer native managers (winget, MSI, apt/dnf/zypper, brew or pkg); arbitrary payload as fallback. Detection makes it idempotent and makes reporting truthful. Existing snapins map onto "payload with no detection rule" | v1 |
| Power scheduling | Scheduled shutdown, reboot, wake | Keep the server model; execution goes through the coordinator | v1 |
| Peer Wake-on-LAN relay | none | An agent on a subnet wakes its neighbors on the server's behalf. Fixes FOG's oldest cross-subnet WoL complaint with almost no code | v1 |
| User tracking, auto logout | login and logout events, idle logout | Keep both; small, and the audience is shared machines with minors | v1 |
| Printers | Windows: TCP/IP and network. CUPS on Unix | CUPS is one provider for Linux and macOS; Windows spooler is its own (winspool, `Add-Printer` fallback). Keep assignment plus default-printer model | Windows v1, CUPS v2 |
| System updates | none | Report state, trigger scan or install, enforce reboot policy through the coordinator. Linux is one package-manager call. Windows Update Agent is a swamp of feature updates and driver surprises: **do not try to be WSUS**. macOS updates are MDM territory | v2 |
| Remote access | none | v1: none beyond the task runner. Later: a reverse-tunnel broker (agent dials out, admin reaches RDP, VNC or SSH already on the box through the server), or install and enroll an existing tool (Veyon, RustDesk, MeshCentral) through the software provider. Never an embedded screen-capture stack | v3 |
| Disk encryption key escrow | none | BitLocker, FileVault, LUKS recovery keys stored on the server. Imaging shops lose these constantly | v2 |
| Local admin password rotation | none | LAPS style. Small to build | v2 |
| Certificate deployment | none | Falls out of the trust infrastructure | v2 |
| Display manager, GreenFOG, ALOBG, DirCleaner, UserCleanup | string flags, little or no code | **Dropped** | |

## 8. Platforms and phasing

| OS | Targets | Phase |
|---|---|---|
| Windows | x64, arm64; x86 only if it costs nothing | v1 |
| Linux | x64, arm64, armv7 | Core from day one (it is where development and CI happen); providers v2 |
| macOS | universal binary (amd64 plus arm64 via `lipo`) | v3 |

32-bit macOS ended with Catalina. 32-bit Windows is dead as of Windows 11;
residual Windows 10 x86 in schools is the only reason to keep the target. The
32-bit that plausibly matters is armv7 Linux on older Raspberry Pi class
devices. **Go 1.21+ dropped Windows 7, 8, 8.1, Server 2008 and 2012:** the
floor is Windows 10, and the docs say so.

macOS is v3 because FOG cannot image Apple Silicon and Apple's management
story is MDM whether we like it or not. Decided (decision 2): Windows is the
initial target; the core is built and continuously tested on Linux because
that is where the lab is, so Linux providers are never far behind. This is
order of delivery, not scope: all three OSes remain in scope.

## 9. Distribution and update

Decided (decision 1): notify, admin applies. The shape below follows from
Tom's requirement that the FOG server need not exist at any particular
version for the agent to know an update exists.

| Element | Design |
|---|---|
| Release location | GitHub Releases behind a stable project domain that only redirects. The domain, never a GitHub URL, is what the agent knows. A central store anyone can go to |
| Manifest | One signed file per channel (stable, beta, nightly): versions, per-target artifact URLs and hashes, release notes URL. Signed with a project minisign key whose public half is compiled into the agent |
| Who checks | **The agent itself**, the way VirtualBox Guest Additions or a VPN client does. An anonymous GET of a static file, at most once a day with jitter, carrying no host identity, no server identity, no query string |
| If central is unreachable | Nothing happens. One informative log line and a system event ("update check skipped: releases.fogproject.org unreachable"), then the agent carries on exactly as before. Never a warning to the user, never a refusal to run. Air-gapped sites download the installer by hand and push it however they push everything else |
| How the admin finds out | The agent reports `update_available: <version>` as a **fact** on its next check-in. The server stores and displays it like any other fact: host list, host page, dashboard count. The server needs no manifest, no signing key, no knowledge of the release process |
| Who is trusted | **Nobody, including the FOG server.** Before applying, the agent re-verifies the manifest signature and the artifact hash. A compromised or mistaken server cannot ship a bad binary; neither can a compromised CDN |
| Applying | An ordinary task the admin queues for a host or group. The agent never updates on its own. The task can name a version; without one it takes the newest the pin allows |
| Pinning | A capability (`update.pin`) in desired state: the server caps the version the agent will report or apply. An older server that does not send it is treated as "no pin". Central publishes, local decides, and the agent degrades gracefully when local has nothing to say |
| Mirror | Not in v1. The manifest fetch URL is a config value, so a site that wants an internal mirror can point at one later; the signature check makes the mirror untrusted by construction |
| Artifacts | MSI (works with GPO and existing habits), deb and rpm, pkg later, plus a raw binary for the post-image path |

Operational costs to accept up front: someone owns the signing key and the
release pipeline, and it is not a laptop. Windows Authenticode signing costs
money yearly (SignPath has a free tier for open source; Azure Trusted Signing
is the alternative) and without it SmartScreen scares every school tech on
first install. Apple notarization needs a paid developer account.

## 10. Language and mechanics

Go. Rust is safer but slower to develop in with a thinner contributor pool.
C# with .NET native AOT is a real option because current fog-client
contributors know it, but 32-bit ARM Linux support is shakier, Mono is not a
future, and single static cross-platform binaries are weaker. Prior art for Go
agents: Tailscale, Fleet's orbit, Nomad and Consul clients, Grafana Agent,
Velociraptor.

Why it fits: `GOOS`/`GOARCH` cross-compiles the whole matrix from one Linux
runner as long as CGO stays out; static binaries of roughly 10 to 15 MB with
no runtime, which matters on a freshly imaged machine with nothing on it;
build tags select providers at compile time; the standard library covers mTLS,
HTTP, JSON, `log/slog`, and process management, so the dependency count stays
small for a binary that runs as SYSTEM.

Dependencies beyond stdlib, all vendorable: `golang.org/x/sys` (Windows service
via `svc`, registry, DLL calls), `go-ole` plus a WMI wrapper (Windows
inventory, Windows Update Agent), `bbolt` or `modernc.org/sqlite`, `kardianos/service`
if a single lifecycle wrapper across the three OSes proves worth it.

### 10.1 Per-OS mechanics

Windows, without CGO, by calling DLLs through `x/sys/windows` or `NewLazyDLL`:

| Need | Mechanism |
|---|---|
| Service lifecycle | `x/sys/windows/svc` |
| Hostname | `SetComputerNameEx` (kernel32) |
| AD join | `NetJoinDomain` (netapi32) |
| Printers | winspool `AddPrinterConnection`, `SetDefaultPrinter`; PowerShell `Add-Printer` as fallback |
| Inventory | WMI via `go-ole`; registry via `x/sys/windows/registry` |
| Software | `winget`, `msiexec`; uninstall registry keys for detection |
| Updates | Windows Update Agent COM API via `go-ole`. The one genuinely painful provider |
| BitLocker | WMI `Win32_EncryptableVolume` |
| Sessions | WTS APIs (wtsapi32), or the helper reports |

Linux is mostly exec of the standard tool with parsed output, which is what
every other agent does: systemd unit; `hostnamectl` or D-Bus to hostnamed;
`realm join` or `adcli` then sssd; `lpadmin`, `lpoptions`; `apt-get`, `dnf`,
`zypper` with `dpkg-query` or `rpm -q` for detection; `/sys`, `/proc`, `lsblk`
for facts; `cryptsetup luksDump`; `loginctl` or logind over D-Bus.

macOS likewise: `scutil`, `dsconfigad`, `lpadmin`, `softwareupdate`,
`fdesetup`, `system_profiler -json`.

### 10.2 Repository layout

```
fog-agent/
  cmd/
    fog-agent/          service entry point
    fog-agent-helper/   per-user session helper (6.1)
    fog-agent-ctl/      admin CLI: enroll, status, force converge
  internal/
    core/               loop, state store, reboot coordinator, scheduler
    identity/           SMBIOS and MAC readers, one file per OS
    enroll/             keygen, CSR, enrollment, renewal
    api/                versioned client for the FOG server protocol
    manifest/           signed manifest fetch and verify, update apply
    provider/           interfaces only, then one dir per capability:
      hostname/ directory/ printer/ software/ power/ inventory/ updates/
  build/
    windows/            WiX source for MSI, service install
    linux/              systemd unit, nfpm config for deb and rpm
    darwin/             launchd plist, pkg scripts
  docs/design/          this file, inputs/, ADRs as decisions land
  .goreleaser.yaml
```

### 10.3 State, config, logging, secrets

- State store: pure-Go embedded database, one file.
- Config: one small TOML file: server URL, channel, pin, log level.
  Everything else comes from the server.
- Logging: `log/slog` JSON to a rotating file, plus Windows Event Log and
  journald where available. Recent log tail shippable to the server on request.
- Secrets: enrollment key in the OS keystore where one exists; tight file
  permissions on Linux. Never logged.

### 10.4 Release pipeline and testing

GitHub Actions on tag. `goreleaser` builds the matrix, produces archives, deb
and rpm through `nfpm`, checksums and the release. MSI via WiX in a Windows
job with Authenticode signing. macOS pkg, codesign and notarization in a macOS
job. Manifest generation and minisign signing as the final job; private key in
a GitHub secret at minimum, a hardware token or KMS if the project can manage
it. Channels map to tag patterns: `v1.2.3` stable, `v1.3.0-beta.1` beta,
nightly from `main`.

Tests: unit tests for the core loop and providers behind fake OS shims;
provider integration tests on GitHub's real OS runners; AD join against a
Samba AD container in the Linux job; end to end with a FOG server in a
container and an agent in a VM covering enroll, converge, update apply, and
downgrade refusal. Every gate gets mutated red once before it counts.

## 11. What the server needs (fogproject working-1.6)

- `/agent/v1` routes: enroll, renew, poll, desired state, task result,
  facts, log tail.
- Host agent record: certificate fingerprint and expiry, agent version,
  protocol version, last check-in, enrollment state. Pending-enrollment list
  with approve and deny.
- Enrollment token item: mint, expire, single or multi use.
- Directory object (`ad`, `ldap`) replacing the flat AD fields on the host.
- Software object with detection rule; snapins migrate as payload-only rows.
- Version pin capability, and the `update_available` fact shown on host list
  and dashboard. No manifest handling on the server.
- `SmbiosIdentity` reused as the resolver; registration policy shared with PXE.
- Post-image handoff: deploy task writes the bootstrap file into the image
  target (FOS side) and records the deploy completion the enrollment policy
  checks.

## 12. What carries over, what is dropped

Carries over: the FOG CA; the poll model and interval setting; host, group,
printer, power-management and module-toggle tables and UI; the snapin
concept as payload-only software; task reboot into imaging.

Dropped: .NET and Mono; the RSA/AES handshake, `sec_tok`, `prev_sec_tok`,
`#!en=`; `getversion.php` as an update trigger; MAC-only identification;
display manager, GreenFOG, ALOBG, DirCleaner, UserCleanup.

## 13. Open decisions

| # | Decision | Recommendation |
|---|---|---|
| 1 | Update policy | **Decided 2026-09-03: notify, admin applies.** Agent checks central directly, reports availability as a fact, pin is an optional server capability, unreachable central is a log line and nothing more. Section 9 |
| 2 | Platform order | **Decided 2026-09-03: Windows first**, core built and tested on Linux from the first commit, macOS v3. Section 8 |
| 3 | Agent API home | **Decided 2026-09-03: PHP under `Route`**, with the security properties in 5.2 |
| 4 | Standalone use against any FOG server, as a way into FOG for management-only shops | Do not decide now; the distribution design does not preclude it |
| 5 | Who owns the signing keys and the Apple and Windows signing accounts | Project decision, not a design one. Must be answered before the first signed release |
| 6 | Do existing fog-client contributors want to write Go | Ask them. If the answer is a hard no, C# with native AOT is the fallback and the rest of this design survives unchanged |

## 14. Divergence log

Where the two input designs disagreed, and the resolution.

| Topic | Session 1 (this repo's first draft) | Session 2 (`inputs/`) | Resolution |
|---|---|---|---|
| Framing | Task list the agent executes | State convergence engine with idempotent providers | Session 2. It is why snapins rerun after every image today and why modules fight over reboots |
| Host identity | SMBIOS identity resolves the host; certificate authenticates; clone detection by identity mismatch | Certificate at imaging or via token; SMBIOS not mentioned | Session 1. The unique host identifier is a stated requirement and the post-image handoff is stronger with it: the server can auto-rebind because it knows which machine it just imaged |
| Reboot ownership | Not addressed | One coordinator, nothing else reboots | Session 2 |
| Session 0 helper | Missed | Designed in | Session 2 |
| Where agents learn about and fetch releases | From the FOG server's mirrored manifest | Central first, FOG server as fallback | Central only, checked by the agent itself; the server just displays what the agent reports. Tom's call: a server-mirrored manifest would make "update available" depend on the server being new enough to mirror, which is backward. Mirror mode is a config URL for later, not a v1 feature |
| Payload hash | sha256 in the desired state | (same) | **sha512**, decided 2026-09-03 when snapins were built: the server already computes and re-verifies sha512 for every snapin (`SnapinHash` scanner), so the state carries that and no second hashing pass exists. The property -- checked before execution, mismatch refused -- is unchanged |
| Update policy | Notify, admin applies | Self-update, channel aware, server pinnable | Notify (Tom's requirement), pin kept as an optional server capability. Session 2's signing and verification kept in full; only the trigger differs |
| Old servers | Not addressed | Manifest carries a minimum server version; old servers cut loose deliberately | Reversed. The agent owns the contract, keeps a small hard floor, and treats everything else as a capability the server may or may not list (5.1). Nothing is cut loose |
| Agent API home | PHP under `Route` | Leans toward a Go gateway daemon owning the API from day one | PHP. `Route` already has versioned routing, an `Authorization` layer and object-scoped permissions; Apache already terminates TLS and can require client certificates. A Go daemon on every FOG install is a new operational surface, a second language on the server, and a second place access control lives. Revisit only if real-time push becomes a requirement |
| Platforms | All three in v1 | Windows v1, Linux v2, macOS v3 | Session 2's order, with Linux core from day one. OPEN |
| Wall-clock effort estimates | None | Weeks per piece | Removed. Size is stated by content, not time |
| Peer WoL relay, key escrow, LAPS, cert deployment | Not considered | Proposed | Adopted, phased |
| `authorize()` reachable before login | Not claimed | Claimed | Not verified in this pass; left out of the problem table until someone reads `FOGPage::authorize()` for it |
| Agent API path | `/api/agent/v1/*` | -- | `/agent/v1/*` directly under the FOG web root. `Route` serves the web root, not only `/api`, and the enroll route landed there; one path prefix also gives the web server a single `location` to require the client certificate on. Doc updated 2026-09-03 to match the code |
