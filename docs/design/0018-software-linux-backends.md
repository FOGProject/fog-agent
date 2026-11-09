# 0018: Software management on Linux, flatpak and snap

Status: agreed 2026-11-09. Extends [0003](0003-software.md), which built the
`software.Backend` interface and shipped Chocolatey first. This document adds
two backends behind that interface: `flatpak` and `snap`.

## 1. Problem

Linux hosts had no software backend. The only backend was `choco`, so a Linux
host could take a software set but could not run any entry in it. Admins fell
back to snapins, with all the faults 0003 section 1 names.

## 2. The shape of the answer

**The admin picks the backend per entry.** The `backend` field on a software
entry already exists. The agent never picks a backend for the admin. An entry
that says `flatpak` runs through flatpak, whatever else is on the host.

**The agent groups entries by backend.** It runs each group against its own
backend, in the order the backends first appear in the set. Inside a group the
entries keep the set order. An entry for a backend this agent does not have
reports `cannot_run` with "this agent has no <name> backend".

**The agent does not install the manager.** It does not install flatpak or
snapd, and it does not add a remote. A missing binary reports `cannot_run`.
The fog-docs pages give the manual steps and the pieces the host must have.
Snap needs a working snapd and its filesystem layout, and flatpak needs a
remote such as flathub. Those are host decisions, not agent decisions.

**Neither backend can pin a version.** Flatpak pins by commit hash. Snap pins
by revision, and snaps refresh on their own schedule. Neither takes a version
string. The server refuses an exact version for these backends at save. The
agent refuses too, in case old data holds one. `Install` with an exact
version returns `cannot_run` and runs nothing: "<name> cannot hold an exact
version (<version>); set the version policy to any or latest".

**The `source` field has one meaning per backend.**

| Backend | `source` means | Empty means |
|---|---|---|
| `choco` | the feed | the default feed |
| `flatpak` | the remote name | `flathub` |
| `snap` | the channel | the snap's default channel |

## 3. Command lines

The agent runs these exactly. `<args>` is the entry's `args`, split into
words.

| Backend | Action | Command |
|---|---|---|
| flatpak | list | `flatpak list --system --app --columns=application,version` |
| flatpak | install | `flatpak install --system -y --noninteractive <source or flathub> <pkg> <args>` |
| flatpak | upgrade | `flatpak update --system -y --noninteractive <pkg> <args>` |
| flatpak | uninstall | `flatpak uninstall --system -y --noninteractive <pkg>` |
| snap | list | `snap list` |
| snap | install | `snap install <pkg> [--channel=<source>] <args>` |
| snap | upgrade | `snap refresh <pkg> [--channel=<source>] <args>` |
| snap | uninstall | `snap remove <pkg>` |

The flatpak list output has no header when it is not a terminal. Columns are
separated by a tab, and the version can be empty. The agent splits on the
first tab. A line without a tab counts as an application only when it has a
dot and no spaces. The snap list output has a header line and whitespace
columns. The agent skips the header, and takes the name and the version from
the first two columns. Both parsers lowercase the id, as the choco parser
does.

## 4. The missing-backend state

0003 gave the agent a `SoftwareBlocked` flag. It meant that the last run could
not run Chocolatey, so the agent looked for the binary every poll. With more
than one backend, one flag is not enough.

`Config.SoftwareBlocked` is replaced by `Config.SoftwareMissing`, a list of
backend names. A name is on the list when the backend was not installed at
the last run. The agent stats each listed backend every poll. When any of
them appears, the set is due at once.

A failed Chocolatey bootstrap does not add `choco` to the list. The agent
tries it again at the next drift check, as before.

An agent that upgrades from an older release loses the old `software_blocked`
flag, because the field no longer exists. The agent recovers at the next
drift check. This costs at most one drift interval.

This also changes one old behavior. Before, a choco whose `list` command
failed made every report `cannot_run`, set `SoftwareBlocked`, and
re-converged the set on every poll. Now the agent watches per poll only a
backend whose binary is missing. A failing `list` retries at the drift check.

## 5. Compatibility

The agent declares the feature `software-backends-v1` in every poll request.
An agent that declares it runs each entry through the backend the entry
names. The server sends an agent that does not declare it only the entries
whose backend is `choco`.

The reason is that old agents stay in the field. An update ring delays them,
or an admin has turned self-update off. Agents up to v0.1.21 run every entry
through choco. Without the handshake, a snap entry "firefox absent" would run
`choco uninstall firefox` on a Windows host. So the server withholds
non-Chocolatey entries from any agent that does not declare
`software-backends-v1`.

## 6. Why not Homebrew

Homebrew refuses to run as root, and the agent runs as root. There is also no
macOS package yet (0005). A brew backend needs a per-user helper and a macOS
package first. It stays a follow-on.

## 7. Proof plan

1. Unit tests: the two parsers against real output from Fedora 44, the
   command lines, the pin refusal, grouping, and the backend registry.
2. An end-to-end test through `Converge` with a shim script that acts as
   `flatpak` and keeps its installed list in a file.
3. The BIOS lab VM `fog-agent-test`: real flatpak and real snap, one entry
   present, one `latest`, one `absent`, with the agent running as the service.
   Only a real host can prove this.

## 8. Decisions taken here

| # | Decision | Why |
|---|---|---|
| 1 | The admin picks the backend per entry | The agent cannot know which manager the admin wants kept |
| 2 | The agent installs no manager and adds no remote | These are host decisions; docs give the manual steps |
| 3 | No exact versions for flatpak or snap | Flatpak pins by commit, snap by revision, and snaps auto-refresh |
| 4 | Entries run grouped by backend | Each backend lists once; run order across backends can change |
| 5 | `SoftwareMissing` replaces `SoftwareBlocked` | One flag cannot name which of several backends is missing |
| 6 | The agent declares `software-backends-v1` | Old agents run every entry through choco; the server sends them choco entries only |
| 7 | No Homebrew | brew refuses root, and the macOS package does not exist yet |
