# 0019: One agent seat per OS on a host

Status: agreed 2026-11-20. Issue: FOGProject/fog-agent#53. Extends
[protocol-v1](protocol-v1.md) enrollment and renewal. The agent needs no code
change. The server holds the change.

## 1. Problem

A dual-boot machine resolves to one FOG host from both operating systems. The
firmware identity and the MAC addresses are the same in each OS.

The host held one agent key. The second OS enrolled, and the request waited as
`rebind`. Approving it replaced the key of the first OS. The first OS then got
401 on its next poll, and it enrolled again. The two OSes took the host from
each other, and only one of them could hold it at a time.

## 2. The seat model

**A host holds one agent seat for each OS.** A seat is one agent key, with its
own certificate, version, update state and last check-in.

**The OS is the `os` value the agent sends at enroll.** It is the Go `GOOS`
value: `windows`, `linux` or `darwin`. The server stores it on the seat. The
agent already sends this value.

**A fingerprint belongs to one seat.** The router binds the certificate
fingerprint to one seat on one live host. So each request reaches the server as
one OS on one host. The agent sees no difference.

**The seat table is the source of truth.** The server also keeps the old
host-level agent columns, as a mirror of the seat that wrote last. The host
list, the reports and the dashboard read the mirror. The agent never reads it.
The server never authenticates from it.

## 3. Enroll reasons

The server picks the reason from the host's seats and the request's OS.

| Reason | The host has | Meaning |
|---|---|---|
| `known-host-no-agent` | no seat | The first agent on a known host |
| `new-seat` | a seat, but none for the request's OS | A second OS on a host that has an agent |
| `rebind` | a seat for the request's OS | A new key for an OS that already has a key |

`unknown-host`, `identity-conflict`, `reissue` and `no-mac` do not change.

Issue of a `new-seat` request adds a seat. It leaves every other seat alone.
Issue of a `rebind` request replaces the key of that one OS seat. It resets the
state of that seat. It leaves the other seats alone.

A seat created from an older install carries the OS of the enrollment row
that holds its key. The OS is empty when no row exists.

A host can have a seat with no OS, from the upgrade. A request from any OS
then pends as `rebind`. Approval gives that seat the request's OS and key.
The old key of that seat then stops authenticating.

## 4. Which approvals apply to `new-seat`

| Path | `new-seat` |
|---|---|
| Token | Approves. A token approves any reason except a conflict |
| Deploy window | Approves. The window also approves `known-host-no-agent` and `rebind` |
| Admin | Approves a request that waits. The admin sees the reason `new-seat` |
| Conflict | Waits for an admin. No token and no window approves a conflict |

A deploy window means the host's last deploy completed within
`FOG_AGENT_ENROLL_DEPLOY_WINDOW` hours. A reimage of the second OS usually
sits inside that window. So the new seat needs no admin.

The server shows the reason `new-seat` in the pending list with the label "New
OS on a host that already has an agent".

## 5. What reaches which seat

### 5.1 The product key and the directory seat

Only the Windows seat gets the product key. The product key is a Windows
feature. A request from another seat gets no `activation` item.

One seat of a host owns the directory. This is the directory seat. The
directory seat is the Windows seat, if the host has one. Otherwise it is the
only seat of the host, if the host has exactly one. Otherwise the host has no
directory seat.

A host has no directory seat when it has two or more seats and none is a
Windows seat. The host page then says: "More than one non-Windows agent seat;
no seat owns the directory."

Only the directory seat gets the `directory` item, the domain join and the
rename. Only the directory seat can start a rejoin plan. Only the directory
seat can claim a rejoin plan. A request from another seat gets none of these.

The server stores directory facts in one row for each host. The server
ignores directory facts from a seat that is not the directory seat. A Linux
seat that reports "not joined" cannot change what the server knows about the
Windows seat.

Every directory operation uses the key of the directory seat. This holds when
another seat sends the poll. A Linux-only host keeps its realm join, because
its one seat is the directory seat.

### 5.2 Snapin target OS

A snapin has a new field, `os`: "Run on". It is empty (any OS), `windows`,
`linux` or `darwin`.

The server skips a queued task when the snapin names an OS and the polling seat
runs another OS. The task stays queued. The seat for the named OS takes it at
its next poll.

An empty `os` keeps the old behavior. The legacy client path ignores the field.

### 5.3 What stays host-level

| Item | Why |
|---|---|
| Name | One machine has one name in FOG |
| Desired agent version | Both seats get the same target version |
| Update ring | The ring belongs to the host |
| Inventory | The hardware is shared. The OS fields show the OS that reported last |

The agent version a seat reports is per seat. The server counts the versions of
every seat when it checks whether a release is still in use.

## 6. Wire changes

[protocol-v1](protocol-v1.md) holds the text.

- The 202 `pending` answer to enroll can carry the reason `new-seat`.
- `rebind` now means that the host has a key for this OS.
- `POST /agent/v1/renew` renews the seat that presents the certificate. It
  never touches another seat.
- A 401 on poll means that the certificate binds to no seat. It says nothing
  about the other seats of the host.

## 7. The host page

The host page lists the seats of the host. Each row shows the OS, the agent
version, the last check-in and the certificate expiry.

## 8. Known limits

- **Two installs of the same OS on one disk share a seat.** Both send the same
  `os` value. The second install replaces the key of the first, as `rebind`.
  A dual-boot of two Windows installs is not a case this design fixes.
- **The agent gets no change.** The agent already sends `os` at enroll. An
  older agent works with a server that has seats.
- **Inventory has no per-seat copy.** The hardware is the same. The OS fields
  show the OS that reported last.
- **The mirror columns show one seat.** A host with two seats shows the data of
  the seat that wrote last. The host page shows both seats.

## 9. Decisions taken here

| # | Decision | Why |
|---|---|---|
| 1 | One seat per host per reported OS | The `os` value is already in the enroll request, so the agent needs no change |
| 2 | `new-seat` is its own reason | An admin must tell a second OS from a replaced key |
| 3 | The deploy window approves `new-seat` | A reimage of the second OS is the usual cause |
| 4 | Conflicts stay admin-only | A conflict means the identity names two hosts |
| 5 | The product key goes to the Windows seat. The directory goes to the directory seat | Other OSes cannot use a product key. One seat must own the one directory row |
| 6 | Snapins get a target OS | A task for one OS must not run on the other |
| 7 | Name, desired version, ring and inventory stay host-level | One machine has one of each |
