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
the server never sets `want_*` and ignores a block if one arrives, and it
tells the agent so with `collect_facts: false` in the poll answer -- which
is what makes "the agent does not gather" true rather than aspirational.
The field is always sent, because absent and false are the same value to a
JSON decoder and absent has to mean "a server that predates this". Hardware inventory and software share the one gate for
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
`lsblk`/`/sys/block`; macOS `system_profiler`/IOKit.

**Windows uses no WMI.** The agent already pulls the raw SMBIOS structure
table from `GetSystemFirmwareTable('RSMB')` to compute its identity, and
that table carries almost the whole inventory row: types 0, 1, 2 and 3 for
the firmware and enclosure strings, type 4 for the processor, type 17
summed for memory. So `smbios.ParseHardware` is a second view over bytes
the agent has already read, and only two facts need anything else --
the boot disk (`IOCTL_STORAGE_QUERY_PROPERTY` on `\\.\PhysicalDrive0`)
and the display adapters (`EnumDisplayDevices`).

**Types 4 and 17 are optional, and a hypervisor may omit them.** VirtualBox
does: the lab VM's table (captured 2026-09-04, now the fixture in
`internal/identity/smbios/testdata/virtualbox-7.dmi`) carries types 0, 1, 2,
3 and 11 and nothing else. Every firmware and enclosure string resolves, and
the processor and memory come back **empty** -- so a Windows guest
inventoried from SMBIOS alone would report no CPU and no RAM. Real firmware
does emit them, verified against `dmidecode` on a Precision 7550 (3900/5100
MHz, 32768 MB summed from four type-17 structures, two of them empty slots),
which is exactly why only a VM could surface this.

The Windows gatherer therefore falls back, and only when SMBIOS said nothing:
`HKLM\HARDWARE\DESCRIPTION\System\CentralProcessor\0` for the vendor, name
and nominal clock, and `GlobalMemoryStatusEx` for total physical memory. Both
are the same kind of call as the rest of that file -- a registry read and a
kernel call, still no WMI. SMBIOS stays preferred where it speaks: type 4
gives the real current *and* maximum clock where the registry has only the
nominal one, and type 17 reports the modules fitted rather than what the OS
can address. `CPUMax` has no registry equivalent and is deliberately left
blank rather than guessed.

WMI or a PowerShell CIM query would work and would be less code. It is
rejected on two grounds: it means spawning a process or initializing COM
on every collection to read values this process can ask the kernel for
directly, and WMI is the part of Windows most likely to already be broken
on the machine an admin is trying to inventory -- exactly when the
inventory matters. The identity path made the same call for the same
reason (0002).

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

A software block is the host's full current list. On receipt, per host, in
one transaction:

1. Close every open row: `UPDATE hostSoftware SET hsRemovedAt = now WHERE
   hsHostID = ? AND hsRemovedAt IS NULL`.
2. Insert the reported list, `ON DUPLICATE KEY UPDATE` refreshing
   publisher/arch/installDate and `hsLastSeen`, and setting `hsRemovedAt =
   NULL` -- which reopens whatever step 1 just closed and is still there.
   `hsFirstSeen` is deliberately not touched on update: it is when *this
   version* was first seen, not last.

Close-then-reopen rather than the obvious "mark anything not in the list
removed", because the obvious version needs a `NOT IN` carrying all 2800
identities a package-managed Linux host reports, and the statement size then
grows with the host. This way both statements are fixed size and the
chunking is only on the insert. Nothing observes the intermediate
"everything removed" state, because it never commits.

Rows are closed, never deleted, so "which hosts had log4j in March" is
answerable after the estate has been cleaned up. The rows do go with the
host: `hostSoftware.hsHostID` is a declared `satellite` foreign key with
`ON DELETE CASCADE` (schema-constraints.php), the same call FOG already
makes for that host's `inventory` row.

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
entries.

The full poll body for that host measured **397,856 bytes**, and **38,301
bytes** gzipped at `BestSpeed` -- 10.4x. Two decisions follow:

- **The agent gzips a poll body over 16 KB** and sets `Content-Encoding:
  gzip`; the server decodes on that header. 388 KB is uncomfortably close to
  the 1 MB body limit nginx and Apache ship with, and a server with more
  packages than this one would cross it. A poll with no facts is a few
  hundred bytes and is left alone: compressing it would cost a decode for
  nothing. Any compression error falls back to the raw body -- a larger
  request beats a failed poll.
- **The collectors run on an interval, not every poll** (`FactsInterval`,
  one hour). Enumerating 2833 packages every five minutes buys nothing:
  facts move when someone installs a program, not when the poll fires. The
  server can always ask sooner with `want_*`, which outranks the interval.

## 7. What this is not

Not desired state: the server never tells the agent what to install here
(that is 0003). Not a device relation: multiple disks/NICs/GPUs are 0007. Not
an event stream: user-tracking login/logout events are ordered and lossless
(0008), a different shape from an idempotent snapshot, and do not ride this
channel.
