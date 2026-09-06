# Installing the agent on Windows

The agent runs as a Windows service named `fog-agent` (display name "FOG
Agent") under the SYSTEM account. The supported install is the MSI; the
binary can also install itself from an administrator prompt.

## Before you start: get the certificate off the server

The agent trusts one certificate and nothing else — not the machine's
certificate store, not whatever the web browser already accepts. So the
one thing to fetch before installing is the certificate the FOG server
presents. Open this in a browser on the machine you are installing on and
save the file:

```
http://your-fog-server/fog/management/other/ca.cert.pem
```

`ca.cert.der` sits beside it and the installer takes either. If the FOG web
UI already uses a certificate from a public CA (Let's Encrypt, a commercial
one, your own corporate CA), point the installer at that CA's PEM instead —
the agent has to be able to build a chain to whatever the web server hands
it.

Optionally mint an enrollment token in the web UI as well. Without one the
machine shows up in Host Management as pending and waits for an admin to
approve it, which is fine — it just means one more click per machine.

## The MSI, by double-click

Double-click `fog-agent-1.2.3-x64.msi` and the wizard asks for the three
things above:

| The wizard asks | Example |
|---|---|
| Server address | `https://fog.example.org/fog` |
| Certificate file to trust | `C:\Users\you\Downloads\ca.cert.pem` |
| Enrollment token (optional) | leave empty to approve the machine by hand |

The server address is the FOG web UI address with `/fog` on the end — the
same thing you type to reach the management pages. If you reach the UI at
`https://fog.example.org/fog/management/`, the address to give the agent is
`https://fog.example.org/fog`.

The wizard appears only on a first install. An upgrade keeps the state
directory, so it has nothing to ask.

## The MSI, from a script

The same three values as properties. `/qn` skips the wizard entirely, so a
deployment tool behaves exactly as it always did:

```
msiexec /i fog-agent-1.2.3-x64.msi /qn SERVER=https://fog.example.org/fog CA=C:\path\ca.cert.pem TOKEN=... /l*v C:\fog-agent-install.log
```

| Property | Meaning |
|---|---|
| `SERVER` | base URL of the FOG server, the same one the web UI uses plus `/fog` |
| `CA` | path to a saved copy of the certificate to trust, PEM or DER: the FOG CA (`management/other/ca.cert.pem` on the server) or the public CA the web UI uses. The agent trusts only what it is given |
| `TOKEN` | enrollment token minted by an admin. Optional: without one the host is pending until an admin approves it in Host Management |
| `WEBADDRESS`, `WEBROOT` | the legacy client's names, honored when `SERVER` is absent: `SERVER` becomes `https://WEBADDRESS` + `WEBROOT` |

A silent install with no `SERVER` and no `CA` fails, and it should: there is
nothing for the agent to enroll with. The reason is in the msiexec log
(`/l*v`) and in `C:\ProgramData\FOG\fog-agent.log`.

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

Run it with no flags and it asks for the same three values the wizard asks
for, so this works too:

```
fog-agent.exe service install
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
