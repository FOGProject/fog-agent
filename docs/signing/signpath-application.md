# SignPath Foundation application — draft for review

**This is a draft. It has not been submitted.** It goes out under FOG
Project's name to a third party, so it is Tom's to send, not mine. Read it,
change what is wrong, and submit it yourself at
<https://signpath.org/apply> (or whatever the current form URL is).

Everything below marked **[CONFIRM]** is something I could not source from the
repositories and did not want to invent. Fill those in or delete them.

---

## Why SignPath, and why this is not optional

FOG's existing Windows artifacts are signed, but **not by a publicly trusted
certificate.** Both `SmartInstaller.exe` and `FOGService.msi` on a current
server carry an Authenticode signature that chains to:

```
CN=FOG Project - Sebastian Roth, O=FOG Project, C=DE
  issued by  CN=FOG Project CA, O=FOG Project, C=US
```

That is FOG's own self-signed CA. It is timestamped by Sectigo, which makes it
*look* like a commercial signature, but the root is not in the Microsoft
Trusted Root Program, so Windows treats these binaries as unsigned. FOG has
therefore never shipped a publicly trusted signed client, and this is a
first-time decision rather than a renewal.

It matters more for the agent than it did for the old client. `fog-agent`
reads *and writes* UEFI firmware variables (Secure Boot posture reporting, and
one-shot network boot arming for imaging tasks). During lab work on
2026-09-05, Windows Defender quarantined an unsigned Go binary that only
*read* those variables — "the file contains a virus or potentially unwanted
software" — from a plain `C:\Windows\Temp` path. An unsigned agent will be
fought by Defender and SmartScreen on exactly the machines it is meant to
manage.

Since June 2023 the CA/Browser Forum requires code-signing private keys to
live on FIPS 140-2 Level 2 hardware or in a cloud signing service, so "buy a
`.pfx` and keep it on the build box" is no longer available to anyone. A
signing *service* is now the normal answer, and it has a side benefit worth
naming: **no maintainer holds a key.** CI holds a revocable credential. That
answers the question design 0001 §13 left open — "who owns the signing
keys" — without anyone having to own one.

## Two blockers, both mine to fix before you submit

1. **`FOGProject/fog-agent` had no `LICENSE` file.** SignPath Foundation
   requires an OSI-approved open source license. I added GPL-3.0, matching
   `FOGProject/fogproject` and `FOGProject/fog-client`, which are both
   GPL-3.0. **Confirm that is the license you want for the Go rewrite** — it
   is a one-file change if not.

2. **There is no CI.** SignPath Foundation signs from a verifiable CI build,
   not from a developer's machine; that origin check is the point of the
   programme. `fog-agent` today has no `.github/workflows`, no Makefile, and
   no publish step — `build/cross.sh` and `build/msi.sh` write to a local
   `./dist` and stop. A GitHub Actions workflow that builds the MSI has to
   exist before the application can be honest about how signing will work.
   Say the word and I will write it.

---

## Application answers

**Project name**
FOG Project — `fog-agent`

**Project website**
<https://fogproject.org>

**Source repository**
<https://github.com/FOGProject/fog-agent>

**Related repositories**
- <https://github.com/FOGProject/fogproject> — the server (GPL-3.0, 1,653
  stars, 283 forks)
- <https://github.com/FOGProject/fog-client> — the .NET client this replaces
  (GPL-3.0)

**License**
GPL-3.0-only.

**What the project is**
FOG is an open source computer imaging, cloning and management system, in
continuous use since 2007 by schools, universities, hospitals, municipal IT
and small businesses to image and manage fleets of machines. `fog-agent` is
the Go rewrite of the client component that runs on each managed machine: it
enrolls with its FOG server using a per-machine certificate, then converges
the machine to the state the server describes — hostname, domain membership,
printers, software, power schedules, snapin execution — and reports hardware
inventory, installed software, user sessions and Secure Boot posture back.

**What we want to sign**
- `fog-agent.msi` — the Windows installer (x64; arm64 and x86 to follow)
- `fog-agent.exe` — the agent binary inside it

**Why signing is needed**
The agent is installed on managed Windows fleets, usually by an administrator
pushing it to thousands of machines at once, and it runs as a service under
LocalSystem. It reads and writes UEFI firmware variables. Unsigned, it draws
SmartScreen warnings on every manual install and is subject to Defender
heuristics on precisely the firmware-touching code paths that make it useful.
Site administrators are also, reasonably, unwilling to deploy an unsigned
service binary fleet-wide.

**Build system**
Go (`CGO_ENABLED=0`), cross-compiled from Linux; the MSI is produced with
`msitools` (`wixl` + `msibuild`) from `build/msi/fog-agent.wxs`. Builds are
reproducible in the sense that matters here: no cgo, no network access during
build, version stamped from the git tag via `-ldflags -X main.Version`.
**[CONFIRM: this will move to GitHub Actions — see blocker 2.]**

**Maintainers / who will administer the SignPath account**
**[CONFIRM: Tom Elliott, FOG Project maintainer, GitHub @mastacontrola —
plus whoever else should hold administrative access.]**

**Project age and community**
**[CONFIRM: FOG Project has been developed since 2007. Add the numbers you
would want a reviewer to see — forum registrations, download counts, known
institutional users. I deliberately did not estimate any of these.]**

**Anything else**
The project has been signing its own artifacts with a private CA for years, so
the release process already has a signing step in it — this replaces an
untrusted signature with a trusted one rather than introducing a new step.

---

## After acceptance

The signing call belongs in CI, gated on a tag, and should sign the MSI
*and* the `fog-agent.exe` inside it — signing only the installer leaves the
service binary that Defender actually watches unsigned. Design 0005
(packaging) is where that sequencing should be written down once the route is
confirmed.
