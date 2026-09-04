# 0006: Inventory and the installed-software report

Status: PROPOSED 2026-09-04. The agent reports facts about the host it runs
on: hardware inventory into FOG's existing table, and installed software into
a new one built for reporting. Follows the desired-state model (0001, 0003)
in reverse: the server pulls facts, it does not push them.

## 1. The split

Two kinds of fact, two homes, one channel.

| Fact | Storage | Why there |
|---|---|---|
| Hardware inventory | the existing `inventory` table (one row per host) | The Host "Inventory" tab and the Hardware report already read it; reusing it means both work with no UI change. The legacy transport (FOS imaging posting base64 form fields authenticated by MAC) is not reused -- the agent sends structured JSON over its mTLS channel |
| Installed software | a new `hostSoftware` table | FOG 1.6 has no per-host installed-programs store anywhere. The `software`/`softwareStatus` tables are the desired-state install capability (0003), a different thing: what an admin *wants*, not what *is* |

Neither is desired state, so neither belongs in the poll answer. Both are
**facts on change**, carried up in the poll request.

## 2. The channel: facts on change, in the poll

The poll request already carries what the agent has applied. It gains what
the agent has observed, sent only when it changes:

```json
{"agent_version": "1.2.3", "applied_revision": "…", "want_state": false,
 "inventory": { … },            // present only when changed or asked for
 "software": [ … ]}             // present only when changed or asked for
```

The agent hashes each fact set and keeps the last hash it successfully sent
in its state file. When the current hash differs, it includes the full block
in the next poll; on a 200 it records the new hash. This is the desired-state
mechanism (0001 §5) run in reverse: there, the server sends `state` when the
agent's `applied_revision` is stale; here, the agent sends facts when its own
content hash moved.

The server can force a resend. The poll answer gains:

```json
{"…": "…", "want_inventory": true, "want_software": false}
```

The server sets `want_<kind>` when it holds no current hash for that host --
a fresh enrollment, a restored database, an admin who cleared the row. The
agent then includes the block on its next poll regardless of local change.
The server remembers the last applied hash per host per kind in a small
`hostFactState` table, which also answers "when did we last hear facts from
this host".

This obeys the route rule (protocol-v1.md): a new kind of report is data on
an existing route, never a new path.

### Gate

A global setting `FOG_AGENT_INVENTORY_ENABLED` (default on). An installed
program list is mildly sensitive; a site can turn collection off. When off,
the server never sets `want_*` and ignores a block if one arrives, and the
agent does not gather. Hardware inventory and software share the one gate for
v1; they can split later if a site wants one without the other.

## 3. Hardware inventory

The agent already writes four SMBIOS identity fields to the `inventory` row
at enrollment (`Enrollment::_createPendingHost`). The inventory capability
extends that to the full row and keeps it current.

Fields gathered, per platform, mapped onto the existing `inventory` columns
(property names as FOG uses them): `sysman, sysproduct, sysversion,
sysserial, sysuuid, systype, biosvendor, biosversion, biosdate, mbman,
mbproductname, mbversion, mbserial, mbasset, cpuman, cpuversion, cpucurrent,
cpumax, mem, hdmodel, hdserial, hdfirmware, caseman, casever, caseserial,
caseasset, gpuvendors, gpuproducts`.

Sources: Linux `/sys/class/dmi/id` and `/proc/cpuinfo`, `/proc/meminfo`,
`lsblk`/`/sys/block`; Windows the SMBIOS table already parsed plus WMI/CIM
(`Win32_Processor`, `Win32_PhysicalMemory`, `Win32_DiskDrive`,
`Win32_VideoController`); macOS `system_profiler`/IOKit.

**Known limit, kept on purpose.** The `inventory` table holds one disk and
folds GPUs into two comma strings. The agent populates the primary disk and
joins GPU names, matching what the table and its UI expect. Real
normalization -- `hostDisk`, `hostNic`, `hostGpu` relations with a row per
device -- is a separate enhancement (0007, proposed), not folded into getting
inventory reporting working, because it rewrites the Inventory tab.

## 4. Installed software

### 4.1 The table

`hostSoftware`, one row per observed `(host, name, source, version)`:

| Column | Type | Meaning |
|---|---|---|
| `hsID` | int PK | |
| `hsHostID` | int | the host |
| `hsName` | varchar(255) | display name as the OS reports it |
| `hsVersion` | varchar(128) | version string, '' if the OS gives none |
| `hsPublisher` | varchar(255) | vendor, '' if none |
| `hsSource` | varchar(16) | `registry`, `winget`, `dpkg`, `rpm`, `flatpak`, `snap`, `pkgutil`, `brew` |
| `hsArch` | varchar(16) | `x64`, `x86`, `arm64`, '' |
| `hsInstallDate` | date NULL | OS-reported install date where it exists |
| `hsFirstSeen` | datetime | first poll this (name,source,version) was seen |
| `hsLastSeen` | datetime | most recent poll it was seen |
| `hsRemovedAt` | datetime NULL | when it stopped being reported; NULL = installed now |

`UNIQUE (hsHostID, hsName, hsSource, hsVersion)`, `KEY (hsName)` for
fleet queries, `KEY (hsHostID, hsRemovedAt)` for the per-host current list.

Identity includes the version deliberately: OS package lists enumerate each
installed `(name, version)` separately, two versions can coexist, and an
upgrade reads naturally as one version removed and another added -- which is
the history a report wants. Keeping every version ever seen, with its
`hsFirstSeen`/`hsRemovedAt`, is the reportability Tom asked for; the current
truth is the `hsRemovedAt IS NULL` slice of the same table.

### 4.2 The reconcile

A software block is the host's full current list. On receipt, per host:

- each incoming `(name, source, version)`: insert if new
  (`hsFirstSeen = hsLastSeen = now`, `hsRemovedAt = NULL`); if it exists,
  set `hsLastSeen = now` and clear `hsRemovedAt` if it had been removed;
  refresh publisher/arch/installDate if they moved.
- each row currently installed (`hsRemovedAt IS NULL`) for this host that is
  **not** in the incoming list: set `hsRemovedAt = now`.

One transaction, so a host is never seen half-updated. `Software.php`'s own
`destroy()` cascade is the model for keeping the rows tied to the host's life.

### 4.3 Reporting

- **Host tab "Software":** the current list, `WHERE hsHostID = ? AND
  hsRemovedAt IS NULL ORDER BY hsName`.
- **Fleet report "Installed Software":** `SELECT hsName, hsVersion,
  COUNT(DISTINCT hsHostID) … WHERE hsRemovedAt IS NULL GROUP BY hsName,
  hsVersion` -- which hosts have what, and the version spread. History drops
  the `hsRemovedAt IS NULL` filter.

## 5. Server ingress

`State::result()` is the single ingress for what the agent reports, but facts
ride the poll, not `result`. The poll handler (`Route::agentPoll`) gains a
step after it answers state: if a block is present and the gate is on, hand
it to a writer and update `hostFactState`.

New writers under `packages/web/src/Agent/`, mirroring `Snapins`/`SoftwareSet`:

- `Inventory::report(Host, array $fields)` -> upsert the one `inventory` row.
- `InstalledSoftware::report(Host, array $list)` -> the §4.2 reconcile.

Both audit (`host.inventory` already exists; add `host.software`) so the
existing "what changed on this host" view shows fact changes too.

## 6. Agent side

- `internal/inventory`: per-OS gatherers returning a struct that marshals to
  the inventory field names; `identity` already has the SMBIOS half.
- `internal/software`: per-OS listers returning `[]InstalledProgram`.
- The poll loop hashes each, compares to the stored hash, includes the block
  on change or when `want_*` came back, records the new hash on a 200.
- Gathering the software list every poll is cheap on Linux (dpkg/rpm query)
  and acceptable on Windows (registry walk); if it proves heavy, a slower
  gather cadence than the poll is a config value, not a design change.
- A collector that cannot run must report *nothing*, never an empty list.
  `inventory.Gather()` and `software.List()` each return a bool for this.
  The server treats a reported list as complete and marks everything absent
  from it as removed, so a platform with no collector sending `[]` would wipe
  a host's whole software history, and an empty inventory block would blank a
  good row.

### Measured on a real Linux host (2026-09-04)

A Fedora workstation reports **2833** packages, because a package-managed
system's "installed software" is every library, not just the things a user
would call programs. That is the right answer for reporting -- "which hosts
still run openssl 3.0.x" is exactly the question a fleet admin has -- but it
makes the software block far larger than Windows' few hundred registry
entries. The block only moves on change, so the cost is bounded, but the
poll body must be measured and compressed (Content-Encoding: gzip) rather
than shipping a few hundred KB uncompressed. Decided at wiring time against a
real measurement, not a guess.

## 7. What this is not

Not desired state: the server never tells the agent what to install here
(that is 0003). Not a device relation: multiple disks/NICs/GPUs are 0007. Not
an event stream: user-tracking login/logout events are ordered and lossless
(0008), a different shape from an idempotent snapshot, and do not ride this
channel.
