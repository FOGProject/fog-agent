# 0005: Packaging and the support log

Status: Windows MSI built 2026-09-04, proof in section 4. Linux and macOS
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

Windows lab VM (`telliottwin11`, host 105): with the hand-installed
service removed and the legacy client (FOG Service 0.13) installed first,
the MSI removed the legacy product, installed the agent, ran `setup`
(already enrolled, so no new request), registered and started the service,
and the agent polled again with its existing certificate. Results are
recorded in the commit that closes this section.
