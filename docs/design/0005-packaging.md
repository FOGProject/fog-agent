# 0005: Packaging and the support log

Status: Windows MSI built and proven on a lab host 2026-09-04, section 4; install wizard added 2026-09-06. Linux and macOS
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
| 7 | The MSI carries a wizard, and the binary asks at the prompt | Windows Installer shows no UI of its own: with no Dialog rows a double-click ran `setup` with no server and no CA and failed 1603 with nothing on screen. Anyone who has installed Windows software expects to be asked. wixl ships the standard WiX dialog set as its `ui` extension, so the package authors one page (`FogCfgDlg`) and reuses the rest; `/qn` and `/qb` skip the InstallUISequence, so scripted installs are untouched. `service install` with no flags asks the same questions on the console, gated on stdin being a character device so msiexec and the service never prompt |
| 8 | The CA file is accepted as PEM or DER | The server publishes `management/other/ca.cert.pem` and `ca.cert.der` side by side. A browser saving the `.der` one is a reasonable way to get the file onto a Windows machine, and the only symptom was "CA bundle contains no certificates", which names neither the file nor the format |
| 10 | The wizard fetches the CA and asks the admin to confirm its fingerprint | Almost nobody installing an agent has a PEM file to hand, and telling them to go and find one is where the old flow lost people. Fetching alone would be trust on first use; the confirmation against the web UI's own Certificates tab is what makes it an out-of-band decision (0002) |
| 11 | wixl cannot author the probe custom action either | It accepts `BinaryKey` only with `DllEntry` or `JScriptCall`; `BinaryKey` with `ExeCommand` (type 2) trips an assertion and leaves no row, and Windows Installer treats a `DoAction` naming a missing action as a no-op. `build/msi.sh` appends the row with msibuild, as it already does for the legacy Upgrade row, and `build/check-msi-ui.py` fails the build if any `DoAction` dangles |
| 12 | The wizard has no certificate field, and a certificate the machine already trusts needs no confirmation | An admin whose server runs on Let's Encrypt was asked for a CA file they did not have (2026-09-11). The probe now settles a web UI on a public or corporate certificate on the system trust store and the server name, and the wizard shows `FogTrustedDlg` instead of a fingerprint (0002). `CA=` stays for scripts. The labels also lost their `&`, which `NoPrefix` printed as a character |
| 9 | `AllowSameVersionUpgrades` on the MajorUpgrade | `Product Id="*"` mints a fresh ProductCode on every build while the version only moves on a release, so two builds of 0.1.0 are two products to Windows Installer. Without this the second registers beside the first: the lab host carried two "FOG Agent" rows in Add/Remove Programs on 2026-09-06 |
| 6 | No self-upgrade in the agent yet | A newer MSI over an older one is the upgrade path on Windows for now. The lab needed a snapin plus Task Scheduler to swap a binary, which shows the gap; an agent-driven upgrade is its own slice |

## 3. What a package does not do

It does not carry the CA. The CA is per server, so the package asks the
server at install time what to trust (0002, "Where the server anchor comes
from"). For FOG's own CA, somebody confirms its fingerprint before anything
trusts it. For a web UI on a public or corporate certificate that the machine
already trusts, there is nothing to confirm, and the wizard says so. A CA file
is still accepted as the `CA` property, for a CA the machine does not trust.

The MSI-side plumbing for that is three pieces, because Windows Installer
gives a program no way to hand a value back to the install: a second copy of
the agent in the Binary table (the UI sequence runs before InstallFiles, so
nothing is on disk yet), a custom action running its `ca probe --registry`,
and the `ReadProbe` script custom action lifting the result out of HKCU into
properties. The exe costs the package about 7 MB, taken deliberately
over a second implementation of fetch-and-hash in PowerShell or JScript: the
fingerprint shown to the admin has to be computed exactly the way `setup`
computes and re-checks it.

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

### The wizard, 2026-09-06

Same host. `msiexec /qn` over the wizard-carrying package exited 0, the
service was Running on the new binary, and the Upgrade table -- now four
rows across two UpgradeCodes, one more than the arrangement that produced
2229 -- loaded: `FindRelatedProducts` ran clean. The same install collapsed
the two duplicate "FOG Agent" rows in Add/Remove Programs into one, which
is decision 9 working.

### The certificate fetch, 2026-09-06

Same host, after the wizard learned to fetch. `msiexec /qn` over the
package that carries the agent twice (7.3 MB Binary stream, 3.3 MB package
to 10.7 MB) exited 0 with the service Running and one Add/Remove row, so
neither the Binary stream nor the msibuild-appended CustomAction row upsets
anything the engine loads.

The Go half of the wizard was exercised on the same Windows host directly:
`fog-agent ca probe --server https://10.255.20.1/fog --registry` printed
`50:02:7A:...:C5:C4` and left exactly that under
`HKCU\Software\FOG\Setup`, which is what AppSearch reads. The same value
is what the server's own web UI shows: `fog-pki-admin status` reports it for
the `root` slot, and that is the field FOGConfigurationPage renders in the
Certificates tab's SHA-256 column. Two independent paths to the same 95
characters is the whole basis of the confirmation step, so it is worth
having checked rather than assumed.

Pointed at a server that is not there, the probe deleted the key rather than
leaving it: a stale fingerprint surviving a failed fetch would have put
somebody in front of the previous server's certificate and asked them to
confirm it. That was a real defect, found by running it.

**Not yet proven: the dialogs themselves.** Nobody was logged in to the lab
VM, and with no interactive session msiexec runs at UI level none and skips
the InstallUISequence entirely, so no dialog was ever created. What is
checked instead, on the Linux side, is that every UI reference in the built
package resolves -- `build/check-msi-ui.py`, run by `build/msi.sh`. It found
one real defect on its first run: `VerifyReadyDlg` and `ResumeDlg` both
spawn `OutOfDiskDlg` and `OutOfRbDiskDlg` when the volume is short of space,
wixl includes only what is referenced, and its own `WixUI_Minimal` does not
reference either, so both were missing from the package. That check cannot
stand in for a double-click on a logged-in Windows desktop, which is the one
step left: the dialogs rendering, the AppSearch lift into CAFINGERPRINT, and
the Next-button publish order.
