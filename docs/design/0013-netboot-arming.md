# 0013: A task reboot has to land in the network

Status: BUILT 2026-09-05, arming not yet proven on a live task. Agent
`internal/netboot` (the firmware half) and `planReboot` in
`cmd/fog-agent/main.go` (the withholding policy). Fixes the hole in the task
reboot of [0001](0001-architecture.md) section 6: the agent reboots a machine
for a queued FOG task and the firmware boots the local disk, so the task is
still queued and nothing happened.

`Find()` is verified against real firmware — on a Dell Precision 7550 whose
`BootOrder` is `0001,0000,0003,0008,0002` it returns
`Boot0003 (Onboard NIC(IPV4))`, correctly stepping over two disk entries that
come first and preferring the active IPv4 NIC entry to the active IPv6 one
that follows it. The parser's test fixtures are that machine's own
`Boot####` bytes.

`Arm()` and `Disarm()` are verified against real efivarfs too — writing
`BootNext` produced `07 00 00 00 03 00` on disk (the NV|BS|RT attribute word
then the option number little-endian), read back correctly, and deleted
cleanly including the immutable-flag handling. No reboot happened between the
write and the delete, so the firmware never acted on it.

What is **not** proven is the last step: a machine actually coming up in FOS
because `BootNext` was set. That is a UEFI guarantee rather than anything in
this package, and testing it means rebooting a real machine into PXE. Both
VirtualBox guests in the lab are the case section 3 describes — no network
`Boot####` to point at — so they exercise the refusal path and not the
success path.

---

## 1. The problem, stated plainly

`coordinate()` in `cmd/fog-agent/main.go` is the agent's only caller of
`reboot.Execute`. When FOG has a task waiting, the poll records a reason with
`reboot.SourceTask` and the coordinator reboots the machine.

Nothing in that path tells the firmware to boot from the network.

On a machine whose boot order starts with its own disk — which is every
machine anybody has ever finished deploying — the reboot returns straight to
the installed OS, the agent polls again, sees the same waiting task, and
reboots again. The task cannot run, and the only visible symptom is a machine
that reboots on a loop.

The legacy fog-client had the same hole and the same workaround: an admin set
the firmware to network-first on every machine in the estate, permanently, and
relied on iPXE chaining to the local disk when there was no task. That works,
it is what most FOG sites do, and it is also why FOG has `hostBiosExit` and
`hostEfiExit` to configure. It should not be a *requirement*.

## 2. The mechanism: BootNext

UEFI already has exactly this. `BootNext` is an EFI global variable holding
one UINT16 that names a `Boot####` option. The firmware boots that option
once, and **deletes the variable itself before handing over**. There is no
cleanup to do and no state to get stuck in: if the machine loses power before
the boot, if the task is cancelled, if FOS crashes — the worst case is one
unnecessary netboot, and iPXE with no task chains to the local disk, which is
the behaviour FOG already relies on.

That "the firmware clears it" property is why `BootNext` and not `BootOrder`.
Rewriting `BootOrder` means the agent has taken ownership of a machine-wide
firmware setting that it must remember to put back, on a machine that may
never boot again. `BootNext` is a request for one boot, and it expires by
itself.

| | `BootNext` | rewrite `BootOrder` | leave it to the admin |
|---|---|---|---|
| survives a failed task | expires by itself | agent must restore it | n/a |
| machine-wide side effect | none | permanent until undone | permanent by design |
| works on a normally-configured machine | yes | yes | only if pre-configured |
| needs firmware writes | yes | yes | no |

## 3. Refusing is the feature

The reason this is a design document and not a two-line change is the failure
case, and it is the whole of the complaint that prompted it: **a reboot that
cannot reach the task must not happen.**

If the firmware has no network boot option, arming is impossible. Rebooting
anyway takes the machine away from whoever is using it and achieves exactly
nothing. So:

> When a task reason is pending and the agent cannot arm a network boot, it
> does not reboot for that task. It reports why, and the task stays queued.

This turns the worst failure mode FOG has here — "I queued an image and
nothing ever happened" — into a sentence on the host: *this machine has no
network boot entry in its firmware*. That is a thing an admin can act on. A
reboot loop is not.

Measured, and the reason this case is not hypothetical: VirtualBox's EDK2
firmware offers `UEFI PXEv4 (MAC:...)` in its interactive Boot Manager menu
but persists **no `Boot####` entry for it**. A VirtualBox guest is therefore a
machine where `BootNext` has nothing to point at, and it is what the lab runs.
Real firmware from major vendors does persist these entries, but "PXE is
disabled in firmware setup" produces the same absence on real hardware.

### When other reasons are also pending

A reboot may be wanted by more than one thing at once — a software install
needs one, and a task is queued. If arming fails, the task reason alone is
withheld: it stays pending, its failure is reported, and the coordinator
proceeds with whatever reasons remain. A machine is not held hostage by an
un-netbootable task, and the task is not silently satisfied by a reboot that
could never have served it.

## 4. Identifying a network boot option

Each `Boot####` holds an `EFI_LOAD_OPTION`:

```
UINT32           Attributes
UINT16           FilePathListLength
CHAR16           Description[]        // NUL-terminated
EFI_DEVICE_PATH  FilePathList[]       // FilePathListLength bytes
UINT8            OptionalData[]
```

An option is a network boot when its device path contains a Messaging node
(`Type 0x03`) with SubType `0x0b` (MAC address), `0x0c` (IPv4) or `0x0d`
(IPv6). Every network boot path carries a MAC node, so that is the marker.
Nodes are `UINT8 Type, UINT8 SubType, UINT16 Length`, walked until the
end-of-path node `0x7f`.

**Not by description.** `"UEFI PXEv4"`, `"Network Boot"`, `"IBA GE Slot 0100
v1550"`, and localized variants are all the same thing, and a description
match would be a string test standing in for a fact the bytes already state.
Descriptions are for showing the admin which entry was armed, nothing else.

Only options with `LOAD_OPTION_ACTIVE` (attributes bit 0) are candidates — an
inactive entry is one the firmware will not boot.

Ordering, when there is more than one: walk `BootOrder`, which is the
firmware's own preference, and prefer an IPv4 entry over an IPv6 one. FOG
serves PXE over IPv4, so an IPv6-first pick would arm a boot that reaches no
FOG server.

## 5. Platforms

| platform | read | write |
|---|---|---|
| Linux | `/sys/firmware/efi/efivars/{BootOrder,Boot####}-8be4df61-…` | write `BootNext-…`, 4 attribute bytes then 2 data bytes, in a single `write(2)` |
| Windows | `GetFirmwareEnvironmentVariableW` | `SetFirmwareEnvironmentVariableW` |
| macOS | unsupported | unsupported |

Two mechanics that will otherwise cost an afternoon each:

**efivarfs prepends the 4-byte attribute word on read and requires it on
write, and the whole variable must go in one `write(2)`** — a partial write is
rejected rather than buffered. This is the same layout difference measured for
[0012](0012-secure-boot-reporting.md): the Win32 call has no attribute word at
all, so the two platforms genuinely need different framing, and only Linux
takes the last two bytes rather than the first two.

**Existing efivarfs files carry the immutable flag.** Writing to one fails
with `EPERM` even as root until `FS_IOC_SETFLAGS` clears `FS_IMMUTABLE_FL`.
The flag is the kernel's guard against a careless `rm -rf` bricking a machine
by deleting its firmware variables, so it is cleared for the one write and the
file is left as it was found.

Writing a firmware variable on Windows needs `SE_SYSTEM_ENVIRONMENT_NAME`
*enabled* in the process token, not merely present. The service runs as
SYSTEM, which holds it, but a privilege a token holds is disabled by default
and `AdjustTokenPrivileges` has to turn it on. This is the mirror of the
finding in 0012 section 5: reading these variables needed no privilege at all.

macOS is unsupported rather than best-effort. Apple's platforms do not netboot
into FOS and there is no honest thing to do here.

## 6. Where it hooks in

A new package `internal/netboot`, and about six lines in `coordinate()`
immediately before `reboot.Execute`.

`netboot` is its own package and not part of `reboot` because they answer
different questions. `reboot` decides *whether and when* the machine may be
taken away from its users, which is a policy question about people. `netboot`
decides *what the machine does when it comes back*, which is a question about
firmware. Putting the second inside the first would mean the reboot
coordinator grew a second job and a second set of platform files.

Arming happens as late as possible — after the decision, after the state is
persisted, immediately before `Execute` — and if `Execute` then fails, the
arming is undone. A `BootNext` left set by a reboot that never happened would
send the machine to the network on whatever boot came next, for whatever
reason, which is a surprise nobody asked for.

## 7. What this does not do

It does not touch `BootOrder`, so a site that has already configured
network-first booting keeps working exactly as it does today; arming
`BootNext` to the same entry is a no-op in effect.

It does not make the agent decide *whether* a task should run. FOG already
decides that; this is only about the machine arriving where FOG is waiting.

It does not replace `hostBiosExit`/`hostEfiExit`. Those govern what iPXE does
when it finds no task, which is the other half of the same journey and is
unchanged.

## 8. Open

- Whether "this host has no network boot option" should become a reported
  fact in its own right, so an admin can see before queuing a task that it can
  never run, rather than after. The data is a by-product of section 4 and the
  fact channel from [0006](0006-inventory.md) already exists. It is a
  genuinely useful thing to know about a fleet, and it is out of scope here
  because this document is about not rebooting pointlessly.
- Whether an arming failure should hold the task or cancel it. Holding is
  proposed, because the admin may fix the firmware; cancelling would throw
  away an instruction the agent was not asked to judge.
