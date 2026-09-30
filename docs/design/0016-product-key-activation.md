# 0016: Product-key activation

Status: PROPOSED, 2026-09-30. Answers issue #23. Agreed in outline with Tom
2026-09-30; no code yet.

The legacy client installed the host's product key and activated Windows.
The agent has no equivalent. 0001 §7 has no row for it, and 0009 §9 excluded
it from directory membership: "It belongs with `hostname` or on its own".
This document puts it on its own. The `hostProductKey` field is still in the
UI and in the Product Keys report, but on a 1.6 server nothing reads it for
an agent host.

---

## 1. What the legacy module did

The legacy HostnameChanger module ran `ActivateComputer(key)` on every
check-in (`fog-client` `Modules/HostnameChanger/HostnameChanger.cs:74`,
`Windows/WindowsHostName.cs:297-331`):

- It regrouped a 25-character key into five groups, and refused any key that
  was not then 29 characters long.
- It ran `cscript slmgr.vbs /dli` and parsed "Partial Product Key", the last
  five characters of the installed key (`Windows/WinActivation.cs:68-80`).
- If the key ended with that partial key and `SLIsGenuineLocal` said genuine,
  it stopped.
- Otherwise it ran `slmgr.vbs /ipk <key>`, then `slmgr.vbs /ato`
  (`WinActivation.cs:90-101`).

Three defects carry no weight here:

- **The key was on a command line.** `slmgr /ipk <key>` puts the key where
  every process on the machine can read it.
- **An empty partial key matched every key.** `key.EndsWith("")` is true, so
  a machine with no key installed was reported as already done.
- **It retried forever.** A key that installs but cannot activate ran
  `/ipk` and `/ato` again on every check-in.

## 2. What the agent is told

A new capability, `activation`. Its block is present only when there is a
key to install:

```json
"activation": { "key": "XXXXX-XXXXX-XXXXX-XXXXX-XXXXX" }
```

- `key` is always the hyphenated 29-character form. The server normalizes
  it, so the agent never regroups.
- The agent holds it in a `secret.Secret`, like the directory password
  (0009). It is never logged, never written to `state.json`, and never put in
  a result.
- No key means no block. The agent then does nothing. It never uninstalls a
  key: a machine with no key in FOG keeps what it has.

The block is part of the desired state, so a new key moves the revision and
the agent converges on the next poll.

## 3. How the key is installed

The agent uses the Software Licensing WMI classes, through one PowerShell
script, with the key on **stdin**:

| Step | Call |
|---|---|
| Read the current state | `SoftwareLicensingProduct` for the Windows application ID `55c92734-d682-4d71-983e-d6ec3f16059f` with a `PartialProductKey`: its `PartialProductKey` and `LicenseStatus` |
| Install | `SoftwareLicensingService.InstallProductKey(key)`, then `RefreshLicenseStatus()` |
| Activate | `SoftwareLicensingProduct.Activate()` on the product that now holds the key |

The script is a constant in the agent. The command line holds the script and
no secret. The script reads the key from stdin, does the work, and prints
one JSON line: what it did, the partial key, the license status, and an
error code if any. `slmgr.vbs` calls these same classes.

Alternatives:

| Option | Why not |
|---|---|
| `slmgr.vbs /ipk <key>` (the legacy route) | Puts the key on a command line |
| WMI from Go through COM (`go-ole`) | Adds the agent's first COM dependency for three method calls |
| `slc.dll` directly (`SLInstallProofOfPurchase`) | Needs the product-key config blob that `slmgr` resolves for you; undocumented in practice |

PowerShell ships with every supported Windows, and the agent already runs
external tools for snapins and `dsregcmd`.

## 4. When it counts as done

The agent compares the last five characters of the desired key with the
installed `PartialProductKey`. An empty partial key never matches.

| Installed key | Licensed | Action | Result |
|---|---|---|---|
| Matches | Yes (`LicenseStatus` 1) | None | `unchanged` |
| Matches | No | `Activate()` | `applied` |
| Different or none | — | Install, then `Activate()` | `applied` |
| Install refused | — | None | `failed`, with the error code |

**An activation that fails after the key installed is still `applied`.**
The detail carries the error code, for example `installed; activation
pending (0xC004F074)`. Windows retries activation on its own schedule once
a key is installed. A `failed` result would make the agent install the same
key again on every poll, which is the legacy retry loop.

**An install that fails is `failed`.** The revision stays unapplied, so the
agent tries again at the next poll. That is the contract every provider
has. A wrong-edition key keeps failing until an admin fixes the key, and the
result tells them why.

No reboot is needed.

## 5. What it does not do

- **No drift check.** The agent acts when the revision moves, not on every
  poll. A key that someone changes locally stays changed until the key in
  FOG changes. The legacy client checked on every check-in; that cost a
  `cscript` run a minute for a rare event.
- **No uninstall.** Clearing the key in FOG does nothing to the machine.
- **No KMS host or Active Directory based activation settings.** A KMS
  client key (GVLK) installs like any other key; where it activates from is
  the network's business.
- **Not on Linux or macOS.** The server withholds the block there (§6), and
  the agent reports `failed`, "not supported on this platform", if it gets
  one anyway.

## 6. Server side

`State::CAPABILITIES` gains `'activation' => 'hostnamechanger'`. The legacy
client did activation inside HostnameChanger, so an admin's existing
per-host and per-group module choices carry over unchanged.

`State::desired()` sends the block only when all three hold:

- The host has a product key that `DirectoryPlacement::decodeStored()`
  decodes. That is the one decoder for these legacy fields; older rows may be
  AES or base64 encoded.
- `productKeyIsValid()` accepts it. It is sent as `productKeyFormat()`.
- The host's enrollment recorded `os` as `windows`.

The revision hashes the whole state, key included. That is the same exposure
as the directory password: 64 bits of a SHA-256 over the whole state, which
reveals nothing usable about a 25-character key.

## 7. Protocol

`protocol-v1.md` gains an `### Activation` section with the block in §2 and
the results in §4. 0001 §7 gains a row:

| Capability | Today | Design | Phase |
|---|---|---|---|
| Product-key activation | HostnameChanger | `activation` provider, design 0016 | Windows v1 |
