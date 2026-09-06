# Installing the agent on Windows

The agent runs as a Windows service named `fog-agent` (display name "FOG
Agent") under the SYSTEM account. The supported install is the MSI; the
binary can also install itself from an administrator prompt.

## The MSI, by double-click

Double-click `fog-agent-1.2.3-x64.msi`. The wizard asks for the server
address, offers a box for an optional enrollment token, then fetches the
certificate that server publishes and shows you its fingerprint to confirm.

| The wizard asks | Example |
|---|---|
| Server address | `https://fog.example.org/fog` |
| Enrollment token (optional) | leave empty to approve the machine by hand |
| Certificate file to trust | leave empty for almost every server |

The server address is the FOG web UI address with `/fog` on the end — the
same thing you type to reach the management pages. If you reach the UI at
`https://fog.example.org/fog/management/`, the address to give the agent is
`https://fog.example.org/fog`.

### Confirming the certificate

The agent trusts one certificate authority and nothing else — not the
machine's certificate store, not whatever the browser already accepts. So
the wizard's second page shows you what the server offered:

```
Issued to:  CN=FOG Server CA
Expires:    2035-05-28
SHA-256 fingerprint:
50:02:7A:40:9A:B0:F4:B9:57:CE:BF:94:26:21:31:E1:38:42:F1:8E:A6:0F:24:D8:64:D3:F5:5D:0F:EB:C5:C4
```

**Check that fingerprint against your own server before clicking Next.**
Sign in to the FOG web UI, open **FOG Configuration**, then the
**Certificates** tab: the **Root** row's **SHA-256** column holds the same
value. If the two do not match character for character, something other
than your server answered — click Cancel.

This is the whole point of the step. Downloading the certificate over the
connection it is meant to verify proves nothing on its own; you comparing it
against a value you fetched a different way is what makes it trustworthy.
The old fog-client downloaded the certificate and simply trusted it.

The wizard appears only on a first install. An upgrade keeps the state
directory, so it has nothing to ask.

### When the fetch is not the answer

If your FOG web UI runs on a certificate from a public or corporate CA
(Let's Encrypt, a commercial CA, your own AD CS), the certificate FOG
publishes is not the one that signed it, and the wizard says so. Put that
CA's file in the **Certificate file to trust** box instead — the agent has
to be able to build a chain to whatever the web server presents. You can
also fetch FOG's own copy by hand from
`http://your-fog-server/fog/management/other/ca.cert.pem`; `ca.cert.der`
sits beside it and the installer takes either.

## The MSI, from a script

`/qn` skips the wizard entirely, so a deployment tool behaves exactly as it
always did. Give it the fingerprint instead of a file and there is nothing
to copy to each machine:

```
msiexec /i fog-agent-1.2.3-x64.msi /qn SERVER=https://fog.example.org/fog CAFINGERPRINT=50:02:7A:... TOKEN=... /l*v C:\fog-agent-install.log
```

Read the fingerprint once, from the Certificates tab of the web UI or with
the agent itself on any machine:

```
fog-agent.exe ca probe --server https://fog.example.org/fog
```

| Property | Meaning |
|---|---|
| `SERVER` | base URL of the FOG server, the same one the web UI uses plus `/fog` |
| `CAFINGERPRINT` | SHA-256 of the certificate the server publishes. The agent fetches that certificate itself and refuses anything whose fingerprint is not this one. Colons, spaces and case do not matter |
| `CA` | path to a saved copy of the certificate to trust, PEM or DER — the alternative to `CAFINGERPRINT`, and the only option when the web UI uses a public or corporate CA |
| `TOKEN` | enrollment token minted by an admin. Optional: without one the host is pending until an admin approves it in Host Management |
| `WEBADDRESS`, `WEBROOT` | the legacy client's names, honored when `SERVER` is absent: `SERVER` becomes `https://WEBADDRESS` + `WEBROOT` |

A silent install with no `CA` and no `CAFINGERPRINT` fails, and it should:
there is nothing to say which certificate the machine should trust, and
nobody there to be asked. The reason is in the msiexec log (`/l*v`) and in
`C:\ProgramData\FOG\fog-agent.log`.

## What the installer does, in order

- removes the legacy client ("FOG Service", fog-client 0.x) if it is
  installed, as a related product: two agents on one host consume the same
  server rows, and the legacy power module was seen taking an on-demand
  shutdown before the agent could
- puts `fog-agent.exe` in `%ProgramFiles%\FOG`
- runs `fog-agent setup` as SYSTEM, which settles the server URL and CA
  bundle in `%ProgramData%\FOG\agent`, cuts that directory's ACL down to
  SYSTEM and Administrators before the key is generated (ProgramData lets
  every user read what others create), and makes the first enrollment
  request. Where a fingerprint was confirmed rather than a file given, this
  step fetches the certificate again itself and refuses anything that does
  not match: the wizard runs unelevated and hands across the fingerprint
  only, never the certificate. A server that does not answer at install
  time is not an install failure: the token is kept and the service keeps
  trying. A bad CA path, a fingerprint that does not match, or an
  unwritable directory fails the install, with the reason in the msiexec
  log
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

Run it with no flags and it asks the same questions the wizard asks,
including showing the fingerprint for you to confirm, so this works too:

```
fog-agent.exe service install
```

Unattended, `--ca-fingerprint` replaces `--ca` exactly as `CAFINGERPRINT`
replaces `CA` in the MSI. Naming a `--server` on the command line turns the
questions off: a script that gets asked a question hangs, so anything
missing becomes an error instead — except the certificate confirmation,
which is a decision and is still put to whoever is at the terminal.

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
