# Installing the agent on Windows

The agent runs as a Windows service named `fog-agent` (display name "FOG
Agent") under the SYSTEM account. The supported install is the MSI; the
binary can also install itself from an administrator prompt.

## The MSI, by double-click

Double-click `fog-agent-1.2.3-x64.msi`. The wizard asks for the server
address and an optional enrollment token. Then it asks the server what to
trust and shows you the answer.

| The wizard asks | Example |
|---|---|
| Server address | `https://fog.example.org/fog` |
| Enrollment token (optional) | leave empty to approve the machine by hand |

The server address is the FOG web UI address with `/fog` on the end — the
same thing you type to reach the management pages. If you reach the UI at
`https://fog.example.org/fog/management/`, the address to give the agent is
`https://fog.example.org/fog`.

### What the agent trusts

The agent does not trust whatever a browser would accept. The wizard's
second page is one of two, and your server's certificate decides which.

**Your server uses FOG's own certificates.** This is the default. The page
shows the certificate authority the server publishes:

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

**Your web UI uses a public or corporate certificate** (Let's Encrypt, a
commercial CA, your own AD CS). The page says this computer already trusts
the certificate, and shows who issued it. There is no fingerprint to
compare. The agent checks the server the way a browser does: against this
computer's trust store, for the server name in the address. A renewed
certificate keeps working. Two things follow:

- Put the name on the certificate in the server address. An IP address
  fails, because a public certificate does not carry one.
- The computer must trust the issuer. Windows trusts the public CAs. A
  corporate CA has to be in the machine's store, which Group Policy
  normally does.

If neither page fits, such as an offline corporate CA the computer does not
trust, give the CA file on the command line as the `CA` property (below).
The wizard has no box for it. FOG's own copy is at
`http://your-fog-server/fog/management/other/ca.cert.pem`; `ca.cert.der`
sits beside it and the installer takes either.

## The MSI, from a script

`/qn` skips the wizard entirely, so a deployment tool behaves exactly as it
always did. `ca probe`, run on any machine, tells you which of the two cases
your server is:

```
fog-agent.exe ca probe --server https://fog.example.org/fog
```

For a server on FOG's own certificates, give the install the fingerprint.
There is then nothing to copy to each machine. Read the fingerprint once,
from `ca probe` or from the Certificates tab of the web UI:

```
msiexec /i fog-agent-1.2.3-x64.msi /qn SERVER=https://fog.example.org/fog CAFINGERPRINT=50:02:7A:... TOKEN=... /l*v C:\fog-agent-install.log
```

For a web UI on a public or corporate certificate the machines already
trust, the server address is enough:

```
msiexec /i fog-agent-1.2.3-x64.msi /qn SERVER=https://fog.example.org/fog TOKEN=... /l*v C:\fog-agent-install.log
```

| Property | Meaning |
|---|---|
| `SERVER` | base URL of the FOG server, the same one the web UI uses plus `/fog` |
| `CAFINGERPRINT` | SHA-256 of the certificate the server publishes, for a server on FOG's own certificates. The agent fetches that certificate itself and refuses anything whose fingerprint is not this one. Colons, spaces and case do not matter. Given for a server on a machine-trusted certificate, the install fails rather than ignore it |
| `CA` | path to a saved CA certificate to pin, PEM or DER, instead of asking the server. It is for a CA the machine does not trust |
| `TOKEN` | enrollment token minted by an admin. Optional: without one the host is pending until an admin approves it in Host Management |
| `WEBADDRESS`, `WEBROOT` | the legacy client's names, honored when `SERVER` is absent: `SERVER` becomes `https://WEBADDRESS` + `WEBROOT` |

A silent install for a server on FOG's own certificates, with no `CA` and no
`CAFINGERPRINT`, fails, and it should: nothing says which certificate to
trust, and nobody is there to ask. The reason is in the msiexec log (`/l*v`)
and in `C:\ProgramData\FOG\fog-agent.log`.

## What the installer does, in order

- removes the legacy client ("FOG Service", fog-client 0.x) if it is
  installed, as a related product: two agents on one host consume the same
  server rows, and the legacy power module was seen taking an on-demand
  shutdown before the agent could
- puts `fog-agent.exe` in `%ProgramFiles%\FOG`
- runs `fog-agent setup` as SYSTEM, which settles the server URL and what
  to trust in `%ProgramData%\FOG\agent`, cuts that directory's ACL down to
  SYSTEM and Administrators before the key is generated (ProgramData lets
  every user read what others create), and makes the first enrollment
  request. Where a fingerprint was confirmed rather than a file given, this
  step fetches the certificate again itself and refuses anything that does
  not match: the wizard runs unelevated and hands across the fingerprint
  only, never the certificate. For a certificate the computer already
  trusts, this step makes that check again itself. A server that does not answer at install
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

## Capturing an image with the agent installed

Capture the machine as it is. Sysprep is not needed, and it changes nothing
the agent uses.

The image carries the state directory: the key, the certificate, and the
host they were issued to. On every start the agent compares the machine's
SMBIOS identity (UUID, system serial, board serial, asset tag) with the
identity the key was made for. On another machine they differ. The agent
then discards the key, the certificate and that host's settings, and
enrolls as itself. The server finds the host by SMBIOS, then by MAC. If this
server deployed to that host in the last `FOG_AGENT_ENROLL_DEPLOY_WINDOW`
hours (24 by default), the request is approved with no click. Otherwise it
waits under Hosts > Pending Agents.

Before v0.1.7 the agent made this check only when it had no certificate. So
every clone polled as the captured machine and took its name.

The check cannot separate machines whose firmware has no UUID or serial of
its own: all empty, or all the same placeholder. For those, stop the
service and delete `key.pem` and `cert.pem` from `%ProgramData%\FOG\agent`
immediately before capture.

The MSI is built on Linux with `build/msi.sh` (needs `wixl` from
msitools); no Windows is needed to produce it.

## Hand install

From an administrator prompt:

```
fog-agent.exe service install --server https://fog.example.org/fog [--token T]
```

Run it with no flags and it asks the same questions the wizard asks, so this
works too:

```
fog-agent.exe service install
```

Either way, the agent then asks the server what to trust, as the wizard
does. For FOG's own certificates it shows the fingerprint for you to
confirm. For a certificate this computer already trusts, it needs nothing
more.

Unattended, `--ca-fingerprint` does what `CAFINGERPRINT` does in the MSI,
and `--ca FILE` does what `CA` does. Naming a `--server` on the command line turns the
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
