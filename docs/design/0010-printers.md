# 0010 — Printers

Status: proposed
Supersedes: the `printermanager` and `defaultprinter` modules of the legacy
client, and the `/service/Printers.php` and `/service/printerlisting.php`
endpoints.

FOG can put a printer on a machine. It cannot tell you whether the printer is
there, it cannot take one off a Linux machine at all, and the four "printer
types" it asks an admin to choose between are not types of printer — they are
the names of four code paths, three of which throw on whichever platform you
are standing on.

This document proposes describing a printer the way both print subsystems
already describe one — a device URI and a driver — reporting what is actually
installed, and saying out loud when an install fails.

---

## 1. What is there today

### 1.1 The type is really a platform switch

`printers.pConfig` holds one of four values, validated in
`packages/web/src/Items/Printer.php:171-186` and offered to the admin as a
dropdown in `buildPrinterTypeSelector()` at `:192`:

| Stored | Shown to the admin |
|---|---|
| `Local` | TCP/IP Port Printer |
| `Network` | Network Printer |
| `iPrint` | iPrint Printer |
| `Cups` | CUPS Printer |

The legacy client dispatches on it (`Modules/PrinterManager/PrintManagerBridge.cs:31-56`),
and here is what each platform does with the four:

| Type | Windows (`WindowsPrinterManager.cs`) | Linux (`UnixPrinterManager.cs`) |
|---|---|---|
| `Local` | `PrintUI /if` with port, INF, model, name (`:89`) | **throws** `NotImplementedException` (`:40`) |
| `Network` | `PrintUI /ga` (`:97`) | **throws** (`:45`) |
| `iPrint` | `iprntcmd.exe` (`:72`) | **throws** (`:35`) |
| `Cups` | **throws** (`:102`) | `lpadmin -p … -E -v lpd://… -P … -D …` (`:50`) |

Three of four throw on each platform. That is the tell: **`pConfig` is not an
attribute of the printer, it is a choice of code path.** "CUPS printer" is not
a kind of printer — CUPS is the print subsystem on the client, and a printer
that is reachable over LPD is reachable over LPD from Windows too. An admin
picking from that dropdown is being asked to name an implementation detail of
a machine they may not have looked at, for a device that has no opinion on the
matter.

The practical consequence is that **a printer entry belongs to one operating
system**. A shop with both has to create the same physical printer twice, and
nothing in FOG says the two rows are the same device.

The column is also misnamed. `pConfig` holds the type; `pConfigFile` holds an
actual configuration file. Two adjacent columns, one of which does not do what
its name says.

### 1.2 Linux removal has never worked, and neither has the default

`UnixPrinterManager.cs:61-64`:

```csharp
public override void Remove(string name, bool verbose = false)
{
    ProcessHandler.Run("lpstat", $"- -P {name}");
}
```

`lpstat` is CUPS' *status query* tool. It has no ability to remove a printer
and never has; removal is `lpadmin -x <name>`. So `Remove()` on Linux runs a
query, discards the output and returns as if it had succeeded.

That is not a cosmetic bug, because removal is the whole of one of FOG's three
modes. `hostPrinterLevel` = 2 is documented as *FOG handles all printers* —
FOG removes any printer it does not know about. On Linux that mode has never
removed anything. An admin who selected it to clean up a lab has been told the
job is done every poll since.

`Default()` throws outright (`:66-69`), so a Linux host cannot be given a
default printer even though `printerAssoc.paIsDefault` exists to say which one
it should be. `Configure()` throws too (`:71`).

One more, in `AddCUPS` (`:52-54`):

```csharp
ProcessHandler.Run("echo", $"{printer.Name} | tr ' ' '_'", true, out stdout);
var portName = stdout[0];
```

That is a subprocess — two, if a shell interprets it — to replace spaces in a
string. Whether `portName` ends up as the slug or as the literal text
`Name | tr ' ' '_'` depends on whether `ProcessHandler.Run` goes through a
shell. Either way the printer's queue name is being computed by `echo`.

### 1.3 The mode is stored as one thing and sent as another

`hosts.hostPrinterLevel` is a `varchar(2)` that stores **0, 1 or 2** — the
radio buttons in `packages/web/src/Pages/HostManagement.php:2829`, `:2857`,
`:2890` compare against those integers, and `:2933` writes one back.

The wire sends something else. `packages/web/src/Client/PrinterClient.php:44-48`:

```php
private static $_modes = [0, 'a', 'ar'];
```

and `json()` emits `self::$_modes[$level]`, so the client receives `0`, `a` or
`ar`. Two vocabularies for one setting, neither of them written down where an
admin can see it, in a column two characters wide holding a single digit.

### 1.4 Nothing records what is actually installed

There is no table, no column and no endpoint that holds the printers a machine
reports having. `GetPrinters()` exists on both platforms — `Win32_Printer` over
WMI on Windows (`WindowsPrinterManager.cs:64`), `lpstat -p` on Linux — but it
is used only inside the client, to decide what to strip in mode 2. All three
call sites are local decisions in `Modules/PrinterManager/PrinterManager.cs`
(`:63`, `:125`, `:140`), and that file contains no `Communication.Post` or any
other transmit. **The result never leaves the machine.**

So FOG cannot answer any of these:

- Did the printer I assigned actually install?
- Which of my machines are missing a printer they should have?
- What is on this machine that I did not put there?
- Why did it fail?

An install that fails fails silently, and the next poll tries the same thing
again forever. This is the same gap design 0009 found for directory
membership: FOG records intent and never observes.

### 1.5 Dead columns

`printers` carries `pAnon2`, `pAnon3`, `pAnon4`, `pAnon5`, mapped in
`Items/Printer.php:51-54`. `printerAssoc` carries `paAnon1` through `paAnon5`,
mapped in `Items/PrinterAssociation.php:45-49`. Nothing reads or writes any of
them anywhere else in the tree. They are `varchar(10)` and `varchar(2)`
leftovers from a schema that pre-allocated spare columns.

FOG already has a move for these. `plugins` carried the same `pAnon1`–`pAnon5`
and they were renamed into real columns — `pIcon`, `pRunfile`, `pLocation` —
through the `renames` block at the top of `packages/web/commons/schema-expected.php:32-50`.
The printer ones were never claimed, so they get dropped rather than renamed.

`printerAssoc.paIsDefault` is a `varchar(2)` holding a boolean;
`groupPrinterAssoc.gpaIsDefault` is a `tinyint(1)`. The same idea, two types,
because they were added years apart.

`Items/Printer.php:179-180` assigns `$curtype` and then immediately overwrites
it — a dead line.

---

## 2. The model: a printer is a URI and a driver

Both print subsystems already agree on how to name a printer. CUPS takes a
**device URI** (`lpadmin -v`) and either a PPD or a driverless model. Windows
takes a port — and a TCP/IP port *is* a device URI written differently — plus a
driver from an INF, or a connection to a shared queue.

So the portable description of a printer is:

| Field | Meaning | Example |
|---|---|---|
| `uri` | how the machine reaches the device | `socket://10.0.4.20:9100`, `ipp://printer.corp/ipp/print`, `lpd://10.0.4.20/queue`, `smb://srv/HP4550` |
| `driver` | what to print with, or empty for driverless | `HP Universal Printing PCL 6`, a `.ppd`, a `.inf` |
| `name` | the queue name the user sees | `Accounts-HP4550` |

**This replaces the four types with one field that describes the device rather
than the code path.** A `socket://` printer is a TCP/IP port on Windows and a
socket device URI on CUPS — the same printer, one row, both platforms. That is
the change that makes a printer entry portable, and it is only possible because
the URI scheme is the thing both spoolers were already built around.

`iPrint` does not fit that and should not be forced to. It is a Novell/Micro
Focus client tool driven by `iprntcmd.exe`, Windows only, and it is expressed
perfectly well as a URI scheme of its own (`iprint://…`) handled by a provider
that only registers on Windows. It keeps working; it stops being one of four
things everybody else has to choose between.

**Driverless is a first-class case.** Modern CUPS and Windows both support IPP
Everywhere, where the printer describes its own capabilities and no driver file
is needed. FOG's model assumes a driver always exists — `pDefFile` and `pModel`
are the only way to describe what to print with. An empty `driver` against an
`ipp://` or `ipps://` URI means driverless, which for a lot of estates is now
the common case and today cannot be expressed at all.

### Why not keep the four types and just fix Linux

Because the types would still be lying. Fixing `UnixPrinterManager` to
implement `AddLocal`/`AddNetwork` would mean deciding what "a Local printer, on
Linux" is — and the answer is "a device URI", which is the proposal. The four
types have no meaning left once you write them down platform by platform.

### Why not a full driver-distribution system

Out of scope. FOG already stores a driver path per printer and already has a
file-serving story (snapins, design 0003). Printers name a driver; getting
driver packages onto machines is the existing problem it has always been.

---

## 3. What the agent reports

A `printers` fact block on the poll (design 0006's shape — a fact kind is a
`State::FACT_REPORTS` entry and a poll block, never a route of its own):

```json
{
  "printers": {
    "subsystem": "cups",
    "default": "Accounts-HP4550",
    "installed": [
      {"name": "Accounts-HP4550", "uri": "socket://10.0.4.20:9100",
       "driver": "HP Universal Printing PCL 6", "shared": false},
      {"name": "Reception", "uri": "ipp://printer.corp/ipp/print",
       "driver": "", "shared": false}
    ]
  }
}
```

`subsystem` is `cups` or `winspool`. It is the honest home for the fact that
`pConfig` was trying to carry, reported by the machine that knows rather than
chosen by an admin who has to guess.

Hash-gated like every fact block, so a machine whose printers have not changed
sends nothing.

**What is deliberately not in it:** per-printer job counts, toner, page counts.
That is print monitoring, it is a different product, and it would put a growing
table behind every poll.

## 4. The tables

Two tables, not one. `hostSpooler` is the per-host anchor — the mirror of
`hostDirectory` in design 0009:

| Column | Type | Note |
|---|---|---|
| `hspID` | int | |
| `hspHostID` | int | FK → `hosts`, CASCADE, unique |
| `hspSubsystem` | varchar(16) | `cups` or `winspool`, empty if the host said something else |
| `hspObservedAt` | datetime | when the machine last answered |

It exists because a machine with CUPS and no queues has **answered**, and a
report that could only see `hostPrinter` rows would show that host as never
having reported — the host most worth looking at being the one that vanishes
from the page. `hostFactState` already records "when did this host last
report kind X", but that table is the poll's hash cache; an admin-facing
report built on it would break the next time the gate changes.

`hostPrinter` is one row per printer observed on a host:

| Column | Type | Note |
|---|---|---|
| `hpID` | int | |
| `hpHostID` | int | FK → `hosts`, CASCADE |
| `hpName` | varchar(255) | the queue name on the machine |
| `hpURI` | varchar(1024) | as reported |
| `hpDriver` | varchar(255) | empty for driverless |
| `hpDefault` | tinyint(1) | the machine's default |
| `hpShared` | tinyint(1) | the machine re-shares this queue |
| `hpObservedAt` | datetime | when the machine last said so |

The default is reported at the block level as a **name** and resolved to this
flag on the way in, so the stored flag and the reported name cannot disagree
— and a default naming a queue that is not installed (left behind by a
removal that did not clear the setting) drops out for free, since no action
could resolve it anyway.

Named for the observation, not the attempt — the `hdObservedAt` rule from 0009.

And the outcome of the last convergence, on the association that asked for it,
so a failure has somewhere to live:

| Column added to `printerAssoc` | Type | Note |
|---|---|---|
| `paAppliedAt` | datetime | last attempt, success or not |
| `paError` | varchar(255) | empty on success |

`paError` is why this design exists as much as anything else. Today a printer
that will not install produces nothing an admin can see.

**The dead columns go.** `pAnon2`–`pAnon5` (`varchar(10)`) and
`paAnon1`–`paAnon5` (`varchar(2)`) are dropped, and `paIsDefault` becomes
`tinyint(1)` to match `gpaIsDefault`. Leaving them would mean writing new code
beside columns that have never held anything. The `plugins` precedent in §1.5
is the same cleanup reached from the other direction.

Neither turned out to be as mechanical as this paragraph first claimed, and
both are worth writing down:

- `paIsDefault` holds `''` on every row nobody ever set, and MariaDB in strict
  mode refuses to convert `''` to an integer. A bare `MODIFY` fails the
  upgrade on essentially every existing install rather than on none of them,
  so a normalizing `UPDATE` has to come first.
- `schema-manifest` could declare a retired **table** and had no way to
  declare a retired **column**, so the nine drops read as permanent
  `MISSING COLUMN` lines in the 1.5 comparison. That is worse than it sounds:
  a report with permanent known differences trains whoever reads it to skim,
  and the next real difference goes with them. `retired` now takes an optional
  `column`, reported rather than silenced exactly as a retired table is. It
  will be needed again the moment design 0007 normalizes the inventory
  columns.

`pConfig` is not dropped in the same step — see §7.

## 5. Desired state, agent side

A `printers` capability alongside `hostname` and `software`, gated on the
existing `printermanager` module so an admin's current per-host and per-group
choices carry over untouched.

The desired state is the resolved assignment set plus the mode:

```json
{
  "printers": {
    "manage": "assigned",
    "default": "Accounts-HP4550",
    "printers": [
      {"id": 12, "name": "Accounts-HP4550",
       "uri": "socket://10.0.4.20:9100",
       "driver": "HP Universal Printing PCL 6"}
    ]
  }
}
```

`manage` names the three modes in words instead of `0`/`a`/`ar`:

| Wire | `hostPrinterLevel` | What it does |
|---|---|---|
| `off` | 0 | FOG does not touch printers on this host |
| `assigned` | 1 | FOG installs and maintains the printers it assigned; anything else on the machine is left alone |
| `exclusive` | 2 | as `assigned`, and printers FOG did not assign are removed |

`exclusive` removes printers from someone's workstation, so it needs to work
correctly or not at all — which is precisely what §1.2 shows has not been true
on Linux. The agent implements removal as `lpadmin -x` on CUPS and
`Remove-Printer` on Windows, and a removal that fails is reported rather than
assumed.

Providers, in the shape design 0001 already uses:

| Platform | Add | Remove | Default | List |
|---|---|---|---|---|
| CUPS | `lpadmin -p <name> -E -v <uri> -m <model>` or `-P <ppd>` | `lpadmin -x <name>` | `lpoptions -d <name>` | `lpstat -v` |
| Windows | `Add-Printer` / `Add-PrinterPort` + `Add-PrinterDriver` | `Remove-Printer` | `(Get-CimInstance Win32_Printer).SetDefaultPrinter()` | `Get-Printer` |

`lpstat -v` rather than `lpstat -p`, because `-v` prints the device URI
alongside the queue name and `-p` does not — and the URI is what the report
needs to compare.

## 6. Reporting

A **Printers** report shaped like Directory Membership: every host with printer
management on, LEFT JOINed to what it reported, so a host that has never
reported is the most visible row rather than an absent one.

| Host | Assigned | Installed | Default | State | Last error | Reported |
|---|---|---|---|---|---|---|

`State` is one of `never reported`, `ok`, `missing`, `extra`, `failed` —
`missing` being a printer FOG assigned that is not on the machine, which is the
question an admin actually arrives with and which has never had an answer.

## 7. Migration, and what is not being broken

Every existing printer row keeps working. `pConfig` is not dropped; it is
**derived from** on upgrade, once, into `pURI`:

| `pConfig` | Existing columns | Becomes |
|---|---|---|
| `Local` | `pIP`, `pPort` | `socket://<pIP>:9100` |
| `Network` | `pPort` (a UNC path) | `smb://<host>/<share>` |
| `Cups` | `pIP` | `lpd://<pIP>/<pAlias>` |
| `iPrint` | `pPort` | `iprint://<pPort>` |

The derivation is best-effort and it is not certain to be right for every row —
`pPort` is a `longtext` that has held whatever an admin typed for a decade. So
**`pConfig` and the old columns stay** after the migration, the UI shows the
derived URI for editing, and a row whose URI could not be derived is listed in
the report rather than silently dropped. Dropping the old columns is a later
step, once installs have been watched converging.

Legacy clients keep their endpoints. `/service/Printers.php` and
`/service/printerlisting.php` are untouched by this design; the agent uses the
poll, and the two coexist exactly as they do for every other capability.

## 8. What this is not

- **Not print monitoring.** No jobs, no queues, no consumables, no page counts.
- **Not driver distribution.** A printer names a driver; getting driver
  packages onto machines is what snapins are for.
- **Not print server management.** FOG configures the client's view of a
  printer. It does not manage the print server the printer lives behind.
- **Not a permissions model.** Who may print is the directory's business.
