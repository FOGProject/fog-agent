# 0013: A task reboot has to land in the network

Status: BUILT and PROVEN END TO END 2026-09-05. Agent `internal/netboot` (the
firmware half) and `planReboot` in `cmd/fog-agent/main.go` (the withholding
policy). Fixes the hole in the task reboot of [0001](0001-architecture.md)
section 6: the agent reboots a machine for a queued FOG task and the firmware
boots the local disk, so the task is still queued and nothing happened.

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

The last step — a machine actually going to the network **because**
`BootNext` was set — is proven too, and it took building a firmware to prove
it. See section 8.

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

> When a task reason is pending and the firmware **has a boot manager that
> holds no network entry**, the agent does not reboot for that task. It
> reports why, and the task stays queued.

This turns the worst failure mode FOG has here — "I queued an image and
nothing ever happened" — into a sentence on the host: *this machine has no
network boot entry in its firmware*. That is a thing an admin can act on. A
reboot loop is not.

**Only that one failure withholds, and the distinction is the whole rule.**
`ErrNoOption` is a fact about the machine: there is a boot manager, and it
lists no network entry, so a reboot provably lands on the local disk.
`ErrUnsupported` is not a fact about where the machine will boot — only that
there is no boot manager to ask. That is a BIOS/CSM machine, which is how
most of FOG's fleet has always reached FOS: by a firmware boot order set to
network-first that the agent cannot see and never could. Withholding there
would stop every one of those machines imaging, in order to prevent a reboot
that in all likelihood works — a worse failure than the one this document
set out to fix, and exactly the behavior change section 7 promises not to
make. Any other error is a failed measurement rather than a finding, and
gets the same benefit of the doubt: "I could not look" is not "there is
nothing there".

An unarmed reboot still says so in the log, because a task reboot that went
ahead without arming is otherwise indistinguishable from one that was armed.

This was very nearly shipped the other way round. The first implementation
withheld on any `Find()` failure, which reads as cautious and is not.

Measured, and the reason this case is not hypothetical: VirtualBox's EDK2
firmware offers `UEFI PXEv4 (MAC:...)` in its interactive Boot Manager menu
but persists **no `Boot####` entry for it**. A VirtualBox guest is therefore a
machine where `BootNext` has nothing to point at, and it is what the lab runs.
Real firmware from major vendors does persist these entries, but "PXE is
disabled in firmware setup" produces the same absence on real hardware.

Confirmed on `telliottwin11` on 2026-09-05, with its firmware in exactly that
state and a Hardware Inventory task queued:

```
10:49:04  taskreboot: task 201 (Hardware Inventory) waiting
10:49:08  netboot: the firmware lists no network boot entry to arm;
          enable PXE or network boot in firmware setup
```

No `reboot: applied` line, `LastBootUpTime` unmoved at 10:38:18, and the task
still queued — the machine was not taken away from its user for a reboot that
could not have served it. The same sentence reached the server's own audit log
(`agent.result`, "reboot failed"), so it is visible to an admin who never sees
the client log.

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

**BIOS/CSM is unsupported in the same sense, and that is not the same as
blocked.** There is no `BootNext` on a BIOS machine, so the agent arms
nothing — and then reboots anyway, exactly as it does today, letting the
firmware's own boot order carry the machine to the network. See section 3:
unsupported is the absence of a measurement, not a finding that the task
cannot be reached.

Proven on `fog-agent-test` (Debian 13, VirtualBox BIOS firmware, no
`/sys/firmware/efi` at all) on 2026-09-05:

```
16:11:02  taskreboot: task 202 (Hardware Inventory) waiting
16:11:02  netboot: no UEFI boot manager here, so there is nothing to arm;
          the task reboot relies on this machine's firmware boot order
          reaching the network, as it always has
16:11:02  reboot: applied (60s warning (task: task 202 ...), mode reboot)
16:12:09  (new boot)
16:12:14  agent back up -- and no second reboot
```

The note, the reboot going ahead, and then `RebootedForTask` suppressing a
repeat when the machine came back on its own disk. That last line matters as
much as the first: withholding is not the only thing standing between this
design and a reboot loop.

Verified on VirtualBox 7.2 (`telliottwin11`, 2026-09-05), which does accept
a runtime variable write from inside the guest as `NT AUTHORITY\SYSTEM` —
`SetFirmwareEnvironmentVariableW("BootNext", …)` succeeds and reads back —
so the write half of this design works on the lab's own VMs even though
their firmware persists no network entry to point it at.

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

## 8. Proving it, and what it cost

The claim that needed proving is the one this package does not implement:
**the firmware boots the armed entry, once, ahead of its own boot order.**
Everything else — parsing `Boot####`, writing `BootNext` — is the agent's code
and has unit tests over real firmware bytes.

**It is provable on a VM, but not on any VM.** The first answer this
investigation reached was that no virtual firmware persists a network
`Boot####`, which was wrong, and wrong for two compounding reasons:

- QEMU ships an iPXE **option ROM** that shadows OVMF's own UEFI network
  drivers, so what looked like a PXE boot was a legacy one and no UEFI boot
  option was ever created. `romfile=` (empty) disables it.
- Every distro OVMF package checked — Fedora, Debian, openSUSE — is built
  **without NetworkPkg**. There is no UEFI network stack in them to enumerate.

So the rig is an OVMF built from edk2 at `edk2-stable202608` with
`NETWORK_PXE_BOOT_ENABLE`. One more trap there: `DxeNetLib` carries a
`[Depex]` on `gEfiRngProtocolGuid`, and `RngDxe` fails on QEMU's default CPU,
so the network stack compiles in and never starts. It needs
`-device virtio-rng-pci`.

**The result**, on one QEMU run with the disk first in `BootOrder`:

```
BdsDxe: loading Boot0002 "UEFI QEMU HARDDISK QM00001"   <- boot 1, boot order
ARMPROOF-FIRST-BOOT-LANDED-ON-DISK-AS-THE-BOOT-ORDER-SAYS
  (set BootNext = 0003, reset)
BdsDxe: loading Boot0003 "UEFI PXEv4 (MAC:525400123456)" <- boot 2, BootNext
iPXE 2.0.0 -- Open Source Network Boot Firmware
https://10.255.20.1/fog/service/ipxe/boot.php... ok      <- the real FOG server
  (FOG menu runs, machine resets)
ARMPROOF-BACK-IN-THE-SHELL-BOOTNEXT-DID-NOT-REDIRECT     <- boot 3, disk again
```

Three boots: the boot order, then the armed entry, then the boot order again
without anything being cleaned up. That is section 2's one-shot property
observed rather than quoted, and the middle boot reached FOG's own iPXE menu.

**VirtualBox proves the other half.** `telliottwin11` (VirtualBox 7.2, Windows
11) is where the agent's real Windows write path was exercised as
`NT AUTHORITY\SYSTEM`, on a queued task, against firmware that persists no
network entry. Given a hand-written one to point at, its firmware honored
`BootNext` and said so on screen:

```
BdsDxe: failed to load Boot0009 "UEFI PXEv4 (MAC:080027E9FF13)"
        from PciRoot(0x0)/Pci(0x3,0x0)/MAC(080027E9FF13,0x1): Not Found
```

It went to the armed entry ahead of Windows and could not resolve it —
VirtualBox binds a UEFI network driver only inside its interactive Boot
Manager, never at BDS. That is why it persists no network `Boot####`: an
option it cannot resolve is one its BDS deletes, which it did to the first
entry written for it. A **MAC-only** device path
(`PciRoot/Pci/MAC`, no IPv4 node) survives; the IPv4-node form does not.

So VirtualBox is a good rig for the refusal path and for the Windows write,
and cannot netboot from NVRAM at all. Nothing in the agent needs changing for
it: `Find()` returns `ErrNoOption` there, which is exactly true.

Two smaller things that will cost an afternoon again otherwise: **VirtualBox
and KVM contend for VMX** on this workstation, so a QEMU guest launched while
a VirtualBox VM is running dies at the first VM entry with `KVM: entry failed,
hardware error 0x0` and no output at all — `accel=tcg` sidesteps it. And
QEMU's user-mode networking is enough for the whole test: slirp answers DHCP
and TFTP locally and still routes the guest to the real FOG server on the host,
so nothing about the lab network has to be touched.

## 9. Open

- Whether "this host has no network boot option" should become a reported
  fact in its own right, so an admin can see before queuing a task that it can
  never run, rather than after. The data is a by-product of section 4 and the
  fact channel from [0006](0006-inventory.md) already exists. It is a
  genuinely useful thing to know about a fleet, and it is out of scope here
  because this document is about not rebooting pointlessly.
- Whether an arming failure should hold the task or cancel it. Holding is
  proposed, because the admin may fix the firmware; cancelling would throw
  away an instruction the agent was not asked to judge.
