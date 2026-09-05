# Privacy statement — fog-agent

`fog-agent` sends data to exactly one place: **the FOG server that the machine
is enrolled with**, which is a server the operator of the machine runs
themselves, on their own network.

It contacts no other host. It sends nothing to FOG Project, to the
maintainers, or to any third party, and it contains no analytics, telemetry
or crash-reporting service. There is no FOG Project-operated endpoint for it
to talk to, and adding one would be a change to this document as much as to
the code.

## Who is the data controller

The organization running the FOG server. FOG Project supplies the software;
it never receives the data. If you are an end user of a machine managed by
FOG, the party accountable for what is collected and how long it is kept is
whoever administers your FOG server — typically your own IT department.

## What is sent to your FOG server

The agent is a fleet management tool, so it reports what a fleet management
tool reports. All of it goes only to the enrolled server.

| Category | Contents |
|---|---|
| Machine identity | hostname, the machine's SMBIOS UUID, serial numbers and MAC addresses |
| Hardware inventory | manufacturer, model, BIOS vendor/version/date, CPU, memory, motherboard, chassis, disk model and serial, GPU |
| Installed software | package or product name, version, publisher, install date |
| Logon sessions | the account name and domain, the OS security identifier (Windows SID or Linux uid), session type (console, remote, tty, X11, Wayland), session state, the remote host for a remote session, and logon and logoff times |
| Firmware posture | Secure Boot state, boot mode, and the current boot entry configuration |
| Task results | the exit code and output of snapins and software operations the server asked for |
| Agent state | the agent version and the time of each check-in |

**Logon sessions identify people.** The account name, the domain and the
remote host of an RDP session are personal data in most jurisdictions,
including under the GDPR. That reporting exists so administrators can tell
which machine a user is on and which machines are in use, and it is gated by
the per-host and per-group "User Tracker" module switch on the server: an
administrator who turns that module off for a host receives no session data
from it.

## What is never sent

- Passwords, and no credential the agent is given. The domain-join account
  password is received from the server, held in memory only for the duration
  of the join, and never written to disk or logged.
- File contents, documents, browser history, keystrokes, screen contents,
  clipboard contents or any form of user activity beyond the session records
  described above.
- The agent's own private key, which is generated on the machine and never
  leaves it. Enrollment sends a certificate signing request — a public key —
  not the private half.

## What is stored on the machine

The agent's state directory (`%ProgramData%\FOG\fog-agent` on Windows,
`/var/lib/fog-agent` on Linux) holds its private key, its certificate, and a
small amount of state used to avoid re-reporting unchanged information. Logs
record what the agent did, not what the user did.

## Uninstalling

Uninstalling through the platform's normal mechanism (Add/Remove Programs on
Windows, the package manager elsewhere) stops all reporting immediately and
removes the agent. The record already held by your FOG server is your
administrator's to delete; FOG Project has no copy of it and no ability to
delete it for you.

## Changes

This file is versioned in the repository, so its history is the change log.
