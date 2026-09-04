# 0005: Packaging and the support log

Status: Windows MSI built and proven on a lab host 2026-09-04, section 4. Linux and macOS
packages are later slices.

## 1. Shape

The package owns what a package manager is good at and hands the rest to
the agent through one command:

| Owner | Does |
|---|---|
| Package (MSI) | files under Program Files, the service registration, start, stop, removal, replacing an older version, retiring the legacy client |
| `fog-agent setup` | the state directory, its ACL, the server URL and CA, the first enrollment request |

`setup` is the install-time half of `service install`; both call the same
`prepareState`. A hand install adds the binary copy and the service
registration itself.

## 2. Decisions

| # | Decision | Why |
|---|---|---|
| 1 | Built on Linux with wixl, not WiX on Windows | The release pipeline is Linux. msitools compiles ServiceInstall, ServiceControl, deferred exe custom actions and MajorUpgrade, which is all this package needs |
| 2 | The legacy client is retired through its UpgradeCode (`{1CCFDEAF-53E9-43AC-AE18-F9F86CEFA4EA}`, "FOG Service") as a related product | Two agents on one host consume the same server rows: its power module took an on-demand shutdown before the agent saw it (0004). Windows Installer already knows how to remove a related product cleanly; nothing in the agent has to find or run the old uninstaller |
| 3 | A server that does not answer at install time is not an install failure | Deployment tools install the MSI on machines that are off the network at that moment. The token is kept in the state directory and the service's first enrollment uses it; a bad CA path or an unwritable directory still fails the install |
| 4 | The legacy property names `WEBADDRESS` and `WEBROOT` are honored | Deployment scripts written for the old client keep working with `SERVER` derived from them |
| 5 | The log is `C:\ProgramData\FOG\fog-agent.log`, beside the state directory, not in it | The state directory is locked to SYSTEM and Administrators because it holds the key. The log must be readable by whoever is asked to post it on the forums, and this is the successor to `C:\fog.log`. Nothing secret is written to it |
| 6 | No self-upgrade in the agent yet | A newer MSI over an older one is the upgrade path on Windows for now. The lab needed a snapin plus Task Scheduler to swap a binary, which shows the gap; an agent-driven upgrade is its own slice |

## 3. What a package does not do

It does not carry the CA. The CA is per server; the installer takes a path
to it. Fetching it from the server with a fingerprint pin is a possible
later convenience, not a v1 requirement.

## 4. Proof

Windows lab VM (`telliottwin11`, host 105), 2026-09-04: with the legacy
client (FOG Service 0.13) installed first, `msiexec /i fog-agent-x64.msi
/qn SERVER=... CA=...` exited 0, the legacy product was gone from Add/Remove
Programs, FOG Agent 0.0.29 was present, the `fog-agent` service was RUNNING,
the on-disk exe reported the built version, and `setup` found the host
already enrolled and started polling on the existing certificate. The
server saw poll, `payload/snapin/{id}` and `result` requests from the host,
all 200.

Two defects were found and fixed getting there, both in the packaging and
neither in the agent (this is what the proof was for):

- **wixl cannot persist a multi-UpgradeCode Upgrade table the Windows engine
  will load.** With MajorUpgrade's own rows plus the legacy `<Upgrade>` row,
  `FindRelatedProducts` failed with error 2229 ("could not load table
  Upgrade") and the install rolled back 1603 -- before touching a file.
  Each UpgradeCode alone loaded; the two together did not. `build/msi.sh`
  now appends the legacy row with `msibuild` after wixl, which rewrites the
  table cleanly.
- **an unversioned exe is not overwritten, so `setup` ran a stale binary.**
  The Go exe had no version resource and wixl left `File.Version` empty, so
  over a prior install the engine kept the old `fog-agent.exe` -- which
  predated the `setup` command -- and the deferred Setup action exited 2
  (1603). `build/msi.sh` now stamps a VERSIONINFO resource (goversioninfo)
  and sets `File.Version` to match, so the package file is always newer than
  an unversioned one on disk.

Both were invisible to the Linux-side `msiinfo`, which reads back tables
wixl wrote even when the Windows engine rejects them; only installing on a
real Windows host surfaced them.
