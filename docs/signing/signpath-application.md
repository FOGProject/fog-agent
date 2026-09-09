# SignPath Foundation application

**Submitted by Tom Elliott on 2026-09-05**, at <https://signpath.org/apply>,
acknowledged with "Form submitted, thank you, we'll be in touch soon".
**Rejected on 2026-09-09.** Paraphrasing rather than quoting, because the
rejection was private correspondence and this repository is public: the
Foundation programme is for projects that have already established public trust
and visibility, and they weigh external signals -- GitHub stars, forks and
contributors; external articles and independent discussion; institutional
backing; sustained activity. They judged FOG to show too few of those, said
explicitly that this was not a comment on the work's quality, invited a
reapplication once the project had gained broader recognition, and pointed at
the paid SignPath.io subscription as the faster route.

**The application pointed them at the wrong repository.** The Repository URL
field was filled with `FOGProject/fog-agent` -- the thing to be signed -- and
the GitHub API says that repository was **created 2026-09-03**: two days old at
the time of the application, with 0 stars, 0 forks and 1 contributor. Every
signal the rejection lists first is a GitHub repository signal, and the repository he
would have opened has none of them. `FOGProject/fogproject`, read the same day,
has 1,656 stars, 282 forks, 93 contributors and 54 releases going back to its
creation on 2014-04-15 -- and the Reputation free-text box was the only place
in the form that said so.

That is a reading of the evidence, not something SignPath confirmed. But it is
why the answer to this rejection is a reply naming the right repository, not a
quiet wait for "broader recognition" that FOG demonstrably already has.

SignPath publishes no appeals process -- their terms say in as many words that
"there is no independent arbitration mechanism" -- so this is a request to look
again, not a dispute. SignPath explicitly invited a reapplication and offered
the paid SignPath.io subscription as the faster route; that route is priced at
$500/yr minimum and bundles a GlobalSign EV certificate that SignPath's own
terms say can be issued "in the name of a legally registered organization
only", so it is probably not even available to FOG. See
`code-signing-routes.md` for the alternatives if the reply does not land.

**What a Foundation certificate actually is**, which the application never
recorded and which changes what winning is worth: the certificate is issued to
**SignPath Foundation**, and SignPath Foundation is the publisher Windows
displays. Their terms: "we cannot issue certificates to you, your project, or
anybody else [...] What we actually do is get certificates issued to 'SignPath
Foundation' and let OSS projects use them." Confirmed against real signed
binaries -- 0install and Transmission both ship as publisher "SignPath
Foundation". So FOG would never hold the certificate and never appear as the
publisher, which cuts both ways: no personal name is burned into it, and the
SmartScreen reputation being borrowed is one SignPath has already built across
every project it signs, rather than one FOG starts from zero.

This file is kept as the record of what was sent and why. The field list was
read off the live form on the day of submission; every number in it is from a
public API.

**If they come back with questions**, the answers are below. **If they
accept**, the remaining work is in "After acceptance" at the end — the
`SIGNPATH_ORGANIZATION_ID` variable and the `SIGNPATH_API_TOKEN` secret are
Tom's to set, the workflow wiring is not.

The subject of the current signing certificate is deliberately redacted to
`<maintainer>` below: it is a real past maintainer's name, and naming them to
a third party adds nothing the argument needs.

---

## Why SignPath, and why this is not optional

FOG's existing Windows artifacts are signed, but **not by a publicly trusted
certificate.** Both `SmartInstaller.exe` and `FOGService.msi` on a current
server carry an Authenticode signature that chains to:

```
CN=FOG Project - <maintainer>, O=FOG Project, C=DE
  issued by  CN=FOG Project CA, O=FOG Project, C=US
```

That is FOG's own self-signed CA, with a past maintainer as the leaf subject.
It is timestamped by Sectigo, which makes it
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

## Prerequisites

1. **`FOGProject/fog-agent` had no `LICENSE` file.** SignPath Foundation
   requires an OSI-approved open source license. GPL-3.0 was added, matching
   `FOGProject/fogproject` and `FOGProject/fog-client`, which GitHub reports
   as GPL-3.0 for both. Tom confirmed GPL-3.0 for the Go rewrite on
   2026-09-05; settled.

2. **CI now exists.** SignPath Foundation signs from a verifiable CI build,
   not from a developer's machine; that origin check is the point of the
   programme. `.github/workflows/release.yml` triggers on a `v*` tag and
   splits into `build` → `sign` → `publish`: `build` installs `msitools`,
   runs `build/msi.sh` with the tag, and uploads an `unsigned` artifact;
   `sign` is inert until the repository variable `SIGNPATH_ORGANIZATION_ID`
   is set, at which point the SignPath action goes in that job; `publish`
   proceeds when `sign` is skipped, so releases work today and become signed
   releases the moment the certificate exists. `.github/workflows/ci.yml`
   runs test and build on every push. The form's Build System field is a
   dropdown offering only GitHub Actions and GitLab CI/CD, so this was not
   optional.

3. **The project must already be released in the form to be signed.** This is
   an explicit eligibility condition in SignPath's terms, and `fog-agent` had
   no tags and no releases at all — there was nothing for a Download URL to
   point at. A `v0.1.0` release is the unblock: `release.yml` builds the MSI
   and publishes it unsigned, which is the state every project is in before
   it holds a certificate.

4. **The download page must mention SignPath.** The Download URL field's own
   hint: "This page must mention that the project uses the SignPath
   Foundation for code signing." `fogproject.org/download.php` says nothing
   about code signing today. **This is a change to Tom's website, and it is
   his to make** — normally after acceptance, since the statement would
   otherwise be false.

5. **A privacy policy and a published code signing policy are required.**
   Both now exist: [`PRIVACY.md`](../../PRIVACY.md) and
   [`code-signing-policy.md`](code-signing-policy.md). The privacy policy
   matters here because the agent reports logon sessions — account name,
   domain and SID — which is personal data.

6. **Multi-factor authentication is mandatory** for every team member on both
   SignPath and the source repository. Confirmed on for Tom's GitHub account
   (2026-09-05); it will need enabling on the SignPath account when it is
   created.

---

## Application answers

The form at <https://signpath.org/apply> is a HubSpot form; these are its
actual fields, read off the live page on 2026-09-05, in order. `*` marks a
required field. There is no free-text "anything else" box, so anything not
covered by a field below does not get said — which is why the Reputation box
carries the argument.

| Field | Answer |
|---|---|
| Project Name* | `FOG Project` |
| Repository URL* | `https://github.com/FOGProject/fog-agent` |
| Homepage URL* | `https://fogproject.org` |
| Download URL | see blocker 3 |
| Privacy Policy URL* (if data is collected) | `https://github.com/FOGProject/fog-agent/blob/main/PRIVACY.md` |
| Wikipedia URL | *(none)* |
| Tagline* | below |
| Description* | below |
| Reputation* | below |
| Maintainer Type | `Independent community project (no formal organization)` |
| Build System | `GitHub Actions` |
| First Name* / Last Name* | `Tom` / `Elliott` |
| Email* | Tom's — deliberately not written down here |
| Company Name | *(blank — FOG is not an employer's project)* |
| Primary Discovery Channel* | `AI / LLM tools` |
| Code of Conduct checkbox* | must be ticked |
| Data processing checkbox* | must be ticked |

**Project Name** is `FOG Project`, not `fog-agent`. The field's own hint is
"a Google search for this name should clearly identify your project", and
`fog-agent` does not — it is a component name a few weeks old.

**Build System** is a dropdown offering only GitHub Actions and GitLab CI/CD.
FOG qualifies because release artifacts are built by
`.github/workflows/release.yml`; a project building its installer on a
maintainer's laptop cannot answer this field at all.

**Tagline** (one sentence, may be published on signpath.org)
> Free, open source imaging, cloning and management for fleets of computers.

**Description**
> FOG is an open source computer imaging, cloning and management system, in
> continuous use since 2007 by schools, universities, hospitals, municipal IT
> departments and small businesses to image and manage fleets of machines.
> `fog-agent` is the management agent that runs on each managed machine: it
> enrolls with its FOG server using a per-machine certificate, converges the
> machine to the state the server describes, and reports inventory and status
> back over a mutually authenticated channel.

**Reputation**
> FOG has been developed since 2007, with continuous version history from the
> original SourceForge Subversion trunk (the first commit in the current git
> repository is the imported `trunk@1`, dated 2008-02-12) and on GitHub since
> April 2014.
>
> - Community forum: 12,758 registered users, 17,514 topics, 155,276 posts —
>   <https://forums.fogproject.org>
> - `FOGProject/fogproject`: 1,653 stars, 283 forks, 93 contributors, 54
>   releases, most recently 1.5.10.2253 on 2026-08-11
> - GitHub release assets have been downloaded 39,636 times, which
>   substantially understates deployment: FOG is normally installed by
>   cloning the repository and running `installfog.sh`, not by downloading a
>   release asset.
>
> FOG already signs its Windows artifacts, but with its own self-signed CA
> whose root is not in the Microsoft Trusted Root Program, so Windows treats
> them as unsigned. This replaces an untrusted signature with a trusted one
> rather than introducing a new step.

---

## Supporting detail (not form fields)

**Related repositories**
- <https://github.com/FOGProject/fogproject> — the server (GPL-3.0)
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
build, version stamped from the git tag via `-ldflags -X main.Version`. All
release artifacts are built by GitHub Actions from a `v*` tag
(`.github/workflows/release.yml`), never uploaded from a maintainer's
machine; the workflow already carries the signing job as a separate stage
between build and publish.

**Maintainers / who will administer the SignPath account**
Tom Elliott, FOG Project maintainer, GitHub @mastacontrola, working on the
project since 2013. Sole administrator of the SignPath account.

**Project age and community**
FOG dates from 2007. Its version history is continuous from the original
SourceForge Subversion trunk — the first commit in the current git
repository is the imported `svn.code.sf.net/p/freeghost/code/trunk@1`, dated
2008-02-12 — and moved to GitHub in April 2014.

Numbers, all from public sources and read on 2026-09-05:

| | |
|---|---|
| Forum registered users | 12,758 |
| Forum topics / posts | 17,514 / 155,276 |
| Contributors to `fogproject` | 93 |
| Stars / forks / watchers | 1,653 / 283 / 69 |
| Releases published | 54, most recently 1.5.10.2253 on 2026-08-11 |

The forum figures come from the public API at <https://forums.fogproject.org>;
the rest from the GitHub API. GitHub release-asset downloads total 39,636,
but that number badly understates deployment and should not be read as an
install count: FOG is normally installed by cloning the repository and
running `installfog.sh`, not by downloading a release asset.

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
