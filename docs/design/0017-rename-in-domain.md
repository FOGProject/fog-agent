# 0017: Renaming a domain-joined machine

Status: §2 BUILT 2026-10-09 (the join flag). §3 PROPOSED, waiting on the
credential decision in §5. Proven in the lab so far: a joined Windows machine
cannot rename its own computer object (§1.3).

The `hostname` capability renames a machine. On a machine that is in a
domain, it renames only the machine, and the computer object in the directory
keeps the old name. The legacy client renamed both. The agent lost that half
when 0001 §7 carried the rename across and 0009 §9 left "a rename of a joined
machine" as "a separate problem". This document is that problem.

---

## 1. What is there today

### 1.1 The agent renames the machine only

`internal/provider/hostname/hostname_windows.go:27-35` calls
`SetComputerNameEx(ComputerNamePhysicalDnsHostname, name)`. That call writes
the new name for the next boot. It does not touch the directory. The provider
reports `pending_reboot`, and the reboot coordinator reboots when the host's
"Enforce Hostname | AD Join Reboots" flag allows it.

After that reboot, the machine is `NEWNAME` and its computer object is still
`OLDNAME$`. *Established practice, not proven here:* the machine's secure
channel then looks for an account that does not exist, and the symptom is
"The trust relationship between this workstation and the primary domain
failed". Domain sign-in, Group Policy and machine certificates stop working
for that machine.

### 1.2 The server sends no credential to a joined machine

`FOG\Agent\DirectoryJoin::blockFor()` returns no block for a host whose last
report says it is joined (`packages/web/src/Agent/DirectoryJoin.php:150`).
This is 0009 §6, and it is correct for a join: a joined estate carries no
join credential. So the agent has no credential with which it could rename
the computer object.

### 1.3 The machine cannot rename itself (proven)

`NetRenameMachineInDomain` with no account uses the caller's own identity.
For a service running as SYSTEM, that is the computer account. Tested on
2026-10-09 on telliottwin11, joined to fogad.lab, as SYSTEM:

```
identity        : NT AUTHORITY\SYSTEM
computer        : TELLIOTTWIN11  part_of_domain=True domain=fogad.lab
secure channel  : True
rename to TELLIOTTWIN11X (no credential): status 5
secure channel  : True
```

Status 5 is `ERROR_ACCESS_DENIED`. Nothing changed, in the directory or on
the machine. The ACL agrees: SELF on the computer object holds the validated
writes for `dNSHostName` and `servicePrincipalName` and the Personal
Information property set, and no write to `sAMAccountName` or to the object's
name. Re-runnable: `background_scripts/probe_rename_in_domain.ps1`.

So a rename of a joined machine needs a credential from outside the machine.
This is the fact the rest of the document rests on.

### 1.4 The legacy client did both halves

`fog-client` `Modules/HostnameChanger/Windows/WindowsHostName.cs:103-178`
(master, 610ad5f04f):

- **In the target domain:** `NetRenameMachineInDomain(null, name, ADUser,
  ADPass, NETSETUP_ACCT_CREATE)`, then `SetComputerNameEx`. One call renames
  the computer object and the machine together. It refused, with a log line,
  when the domain fields were empty.
- **In no domain:** `SetComputerNameEx`, then `NetJoinDomain` with
  `NETSETUP_JOIN_WITH_NEW_NAME` on every join.
- **In another domain:** unjoin, then rejoin. 0009 §1.1 rejects this path,
  and so does this document.

So the agent regressed the legacy behavior twice. §2 fixes the second.

## 2. Joining under the new name — BUILT

The capability order puts `hostname` before `directory`
(`FOG\Agent\State::CAPABILITIES`, `packages/web/src/Agent/State.php:54-69`).
So when an admin renames an unjoined host and sets it to join, one poll does
both: the rename is pending, and then `NetJoinDomain` runs. Without
`NETSETUP_JOIN_WITH_NEW_NAME` (0x400), Windows creates the computer object
under the name the machine runs under NOW, which is the old one. The reboot
then gives the §1.1 mismatch on a machine that was never joined before.

The agent now passes 0x400 on every join
(`internal/provider/directoryjoin/join_windows.go:96`). With no rename
pending, the new name is the current name, so the flag changes nothing. The
legacy client set it on every join for its whole life, which is the evidence
that it is harmless in that case.

**Not proven in the lab:** the lab has no unjoined Windows machine.
telliottwin11 is joined, and unjoining it costs its SID (0009 §1.1). The
proof is the first fresh join through FOG after this ships: rename and join
in one poll, reboot, then `Test-ComputerSecureChannel` and the object's name
in the directory.

Linux needs nothing. `hostnamectl` applies the name at once, so `adcli join`
already runs under the new name.

## 3. Renaming a joined machine — PROPOSED

### 3.1 The options

| Option | What it does | Verdict |
|---|---|---|
| **A. Agent renames through the domain** | The server sends the join credential to a joined host only while a rename is pending. The agent calls `NetRenameMachineInDomain`, which renames the object and the machine together | **Recommended.** It is what Windows itself does (`Rename-Computer -DomainCredential`), and what the legacy client did. It keeps the object's SID, password and group memberships |
| B. Server renames the object over LDAP | The server's directory service account (0009 §8) rewrites `sAMAccountName`, `dNSHostName`, the SPNs and the RDN. The agent renames locally | Rejected. It re-implements `NetRenameMachineInDomain` attribute by attribute. Between the LDAP rename and the reboot, the running machine and its object disagree. It widens the placement account's delegation from "move" to "rewrite". The machine must still reboot |
| C. Refuse | A joined Windows host does not rename. It reports why, and the admin renames by hand | The fallback when A has no credential. As the only behavior it is a regression from the legacy client |
| D. Unjoin, rename, rejoin | — | Rejected by 0009 §1.1: a new SID and two forced reboots |

A and C together: rename through the domain when the host has a credential,
and refuse when it has none. Never rename the machine alone while it is
joined, because that is the one result that is worse than doing nothing.

### 3.2 When the server sends the credential

`DirectoryJoin::blockFor()` gets one more case. It sends the block to a
**joined** host only when all of these hold:

- `useAD` is set, and the host names a domain.
- The last report says the machine is in **that** domain (the same
  `sameDomain` rule the agent uses).
- The reported machine account is not the host's name plus `$`, compared
  without case. `hdMachineAccount` comes from the machine's own report, so
  this is an observation, not an assumption.
- No attempt was made within `RETRY_AFTER`. That cooldown already exists for
  joins and exists for the same reason: a bad password is a failed sign-in
  against somebody's domain controller.

The block is the existing join block plus `"rename_to": "<name>"`. Its
absence means "join", so an older agent that does not know the field refuses
the block (it is already joined to that domain) and does nothing else.

The credential stays under every 0009 §6 rule: over the mutual-TLS channel,
in memory only, never logged, zeroed after the attempt.

### 3.3 What the agent does

- **`hostname` on Windows** reads the join state first. If the machine is in
  a domain, it does not call `SetComputerNameEx`. It reports
  `pending: the rename goes through the directory` and leaves the work to the
  `directory` capability. This is a behavior change: today it renames the
  machine alone. That rename is the defect in §1.1.
- **`directory`** gets a `Rename` action beside `Join`. If the machine is
  already in the block's domain, and `rename_to` differs from its name, it
  calls `NetRenameMachineInDomain(nil, rename_to, user, password,
  NETSETUP_ACCT_CREATE)`. On success, it asks for a reboot under the same
  `reboot` flag a join uses.
- **A rename that is already pending is not repeated.** The agent compares
  `rename_to` with the pending name in
  `HKLM\SYSTEM\CurrentControlSet\Control\ComputerName\ComputerName`, reports
  `renamed` again, and makes no call. This matters because the server cannot
  see the rename until the machine reboots and reports `NEWNAME$`.
- **A block with an empty credential** is reported as `refused`, with the
  missing fields named. This is the C half of §3.1.
- New statuses on the wire: `renamed` (the call succeeded and a reboot is
  pending), plus the existing `failed` and `refused`.

### 3.4 What the join account needs

Renaming an object needs more than creating one: write access to
`sAMAccountName`, `dNSHostName` and `servicePrincipalName`, and the right to
change the object's name. *My judgment, not tested:* the usual "join
computers to this OU" delegation covers the first three only through its
broad property writes, and estates that delegated narrowly will see
`ERROR_ACCESS_DENIED`. The agent reports that error by name, the same way
`joinError` names the join failures.

The lab cannot answer this yet. Its join account holds write to all
properties on the OU (`seed_fogad_lab_objects.sh:78-94`), so a rename there
proves the API path and not the minimum delegation. The proof needs a second
lab account with only the standard join delegation.

### 3.5 Linux

Out of scope for v1. An `adcli`-joined Linux machine keeps working after
`hostnamectl` renames it, because its keytab still holds the old principal.
The directory object keeps the old name. The Directory Membership report
(0009 §7) shows the difference. That is a reporting gap, not a broken
machine.

## 4. Build and proof plan

| Part | Repo | Proof |
|---|---|---|
| `blockFor` rename case, `renamed` status | fogproject, working-1.6 | unit tests on `blockFor` for each gate in §3.2 |
| `Rename` action, Windows backend, pending-name check | fog-agent | unit tests on `Decide`; lab: rename telliottwin11, reboot, `Test-ComputerSecureChannel`, read the object in fogad.lab, rename back |
| `hostname` defers to `directory` when joined | fog-agent | unit test; lab: a joined host with no credential reports `refused` and keeps its name |
| Minimal delegation | lab | a second join account with only the standard delegation; record what the rename needs |

## 5. The decision this needs

§3.2 sends a domain credential to a machine that is already joined. 0009 §6
rules that out today. The exposure is the same one a join already has: one
credential, on one machine, for one attempt, over the authenticated channel.
It happens once per rename instead of once per join. The alternative is §3.1
C alone, and a joined machine then cannot be renamed through FOG at all.
