# Installing the agent on Windows

The agent runs as a Windows service named `fog-agent` (display name "FOG
Agent") under the SYSTEM account. The supported install is the MSI; the
binary can also install itself from an administrator prompt.

## The MSI

```
msiexec /i fog-agent-1.2.3-x64.msi /qn SERVER=https://fog.example.org/fog CA=C:\path\ca.cert.pem TOKEN=... /l*v C:\fog-agent-install.log
```

| Property | Meaning |
|---|---|
| `SERVER` | base URL of the FOG server, the same one the web UI uses plus `/fog` |
| `CA` | path to the PEM bundle to trust: the FOG CA (`management/other/ca.cert.pem` on the server) or the public CA the web UI uses. The agent trusts only what it is given |
| `TOKEN` | enrollment token minted by an admin. Optional: without one the host is pending until an admin approves it in Host Management |
| `WEBADDRESS`, `WEBROOT` | the legacy client's names, honored when `SERVER` is absent: `SERVER` becomes `https://WEBADDRESS` + `WEBROOT` |

What the installer does, in order:

- removes the legacy client ("FOG Service", fog-client 0.x) if it is
  installed, as a related product: two agents on one host consume the same
  server rows, and the legacy power module was seen taking an on-demand
  shutdown before the agent could
- puts `fog-agent.exe` in `%ProgramFiles%\FOG`
- runs `fog-agent setup` as SYSTEM, which settles the server URL and CA
  bundle in `%ProgramData%\FOG\agent`, cuts that directory's ACL down to
  SYSTEM and Administrators before the key is generated (ProgramData lets
  every user read what others create), and makes the first enrollment
  request. A server that does not answer at install time is not an install
  failure: the token is kept and the service keeps trying. A bad CA path or
  an unwritable directory fails the install, with the reason in the
  msiexec log
- registers the service (automatic start) and starts it

A newer MSI installed over an older one replaces the binary in place and
keeps the state directory, so the host keeps its identity and does not
re-enroll. On an upgrade the properties are optional. Uninstalling removes
the service and the binary and leaves the state directory (this machine's
key and certificate) in place, so a reinstall picks the same identity up.

The MSI is built on Linux with `build/msi.sh` (needs `wixl` from
msitools); no Windows is needed to produce it.

## Hand install

From an administrator prompt:

```
fog-agent.exe service install --server https://fog.example.org/fog --ca ca.pem [--token T]
```

This does what the MSI does with the binary itself: the `setup` step above,
a copy to `%ProgramFiles%\FOG\fog-agent.exe`, the service registration with
restart on failure (10 s, 1 min, 5 min), an event log source, and start.
`fog-agent setup` alone prepares the state directory without registering
anything, for a service registered some other way.

Other commands: `service status`, `service stop`, `service start`,
`service uninstall`.

## The log

The service writes `C:\ProgramData\FOG\fog-agent.log`, rolled to
`fog-agent.log.1` past 1 MB. This is the file to post on the forums when
something is wrong: it is deliberately outside the locked state directory
so any user of the machine can read and attach it, and it carries no
key, certificate or token. Start, stop and fatal errors also go to the
Application event log under the source `fog-agent`. Install problems are
in the msiexec log (`/l*v`), and `setup`'s own lines appear in
`fog-agent.log` as well.

## Software

A host with software assigned but no Chocolatey reports every entry as
"cannot run" and checks for it at each poll. Set
`FOG_SOFTWARE_CHOCO_BOOTSTRAP_URL` on the server (FOG Configuration, FOG
Client) to have the agent install Chocolatey itself from that script.
