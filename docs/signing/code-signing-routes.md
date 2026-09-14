# Windows code signing: the routes left after SignPath

SignPath Foundation rejected FOG's application on 2026-09-09
(`signpath-application.md` is the record of what was sent). They publish no
appeals process. This file is what replaces it.

Everything below was read from primary sources on 2026-09-09. Where a fact
could not be confirmed on a vendor's own page it says so; do not promote a
"probably" here into a "yes" without opening the page.

## The finding that changes the decision

**EV certificates no longer bypass SmartScreen.** Microsoft removed that in
2024, and their current guidance says an OV certificate is "functionally
equivalent to Azure Artifact Signing for SmartScreen purposes", and that
paying the EV premium "solely to avoid SmartScreen warnings is no longer
justified".
(<https://learn.microsoft.com/windows/apps/package-and-deploy/smartscreen-reputation>,
updated 2026-05)

Two consequences:

1. The expensive tier buys nothing we want. Anything publicly trusted is
   enough, so pick on cost and on whether it signs unattended from CI.
2. **Signing does not make the warning go away on day one.** SmartScreen
   reputation accrues to a consistent publisher identity over weeks and
   hundreds of clean installs. What signing fixes immediately is the other
   half of the problem, which is the half FOG actually hit: Defender
   heuristics on the firmware-touching code paths, and site administrators
   who will not push an unsigned LocalSystem service to a fleet.

So the publisher identity we choose is a long-lived commitment. Changing it
later restarts reputation from zero.

## The routes

| Route | Cost/yr | Individual, no legal entity? | Unattended from GitHub Actions? |
|---|---|---|---|
| **Azure Artifact Signing** | ~$120 (see note) | Yes -- US/Canada only | Yes, official Microsoft action |
| SSL.com eSigner | ~$280 to ~$2,400 | Yes, explicit IV product | Documented, not Actions-specific |
| Certum SimplySign | ~EUR 49 | Yes | **Unconfirmed** -- may need a phone tap |
| Certum card SKUs | ~EUR 25 to 69 | Yes | No -- physical card in a reader |
| DigiCert KeyLocker | ~$700 (OV) | Not stated | Yes |
| GlobalSign | not published | **No -- organization only** | -- |
| SignPath.io paid (Starter) | $500 | **No -- EV cert is org-only** | Yes |
| Go back to SignPath Foundation | free | n/a | Yes |
| **OSSign** | free | n/a -- signs with its own certificate | Yes, but it builds in its own repo |

Azure Artifact Signing is the former "Azure Trusted Signing", renamed in 2026
(<https://learn.microsoft.com/azure/artifact-signing/>). The $9.99/month Basic
figure is widely repeated but Microsoft's pricing page renders through
JavaScript and the number was **not** read off it -- confirm before budgeting.

Ruled out entirely: **sigstore does not produce Authenticode signatures** --
Fulcio is not in the Microsoft Trusted Root Program, so a sigstore-signed
binary still shows "Unknown Publisher". No Linux Foundation, Google or GitHub
free Authenticode programme was found. The 2026-09-09 research said SignPath
Foundation was the only free programme. That was wrong: OSSign is a second
one (see below).

SignPath's own paid tiers are published at
<https://docs.signpath.io/change-subscription>: Starter $500/yr, Basic Single
$1,000, Basic Team $2,000. Each bundles an EV certificate from GlobalSign, and
SignPath's terms say a GlobalSign certificate "can be issued in the name of a
legally registered organization only". FOG has no legal entity, so the route
SignPath offered in the rejection is four times the price of Azure and probably
not open to us anyway. Not a serious candidate.

## First move: go back to SignPath, because they read the wrong repository

The rejection is built on GitHub repository signals -- "stars, forks,
contributors" is the first thing it lists -- and the Repository URL on the form
was `FOGProject/fog-agent`, created 2026-09-03, which had 0 stars, 0 forks and
1 contributor when they opened it. `FOGProject/fogproject` has 1,656 stars, 282
forks, 93 contributors and 54 releases since 2014. That is a reply, not an
appeal, and SignPath explicitly invited another look.

Winning it is worth more than the $120 it saves, because of what a Foundation
certificate is: **it is issued to SignPath Foundation, and SignPath Foundation
is the publisher Windows shows.** (Their terms; confirmed against 0install and
Transmission binaries, which both display that publisher.) So it puts no
maintainer's legal name on FOG's Windows artifacts, and the SmartScreen
reputation it carries is one SignPath has already accumulated across every
project it signs -- rather than a fresh identity FOG would have to build over
weeks of clean installs. It is free, faster to trusted, and costs nobody's
name.

The cost of trying: one email. Nothing is burning while we wait -- v0.1.x is
pre-release and `release.yml` labels the artifacts unsigned.

**Status 2026-09-14:** the reply went out the week of 2026-09-07 and has had
no answer. Silence is not a second rejection; the reply is a request for a
re-review, and that is slower than a form triage. Send one short follow-up on
the same thread. If there is still no answer by 2026-09-28, stop waiting and
start the Azure route below.

## The second free route: OSSign

Read from primary sources on 2026-09-14: the rendered <https://ossign.org>
page, the `OSSign` GitHub organization, and the signature on a binary it
signed.

**Applications are suspended.** The live page says: "Applications are
currently suspended due to a high workload and large backlog ... Please check
back in a few weeks." The contact form does not take applications.

What it is: a volunteer project in Sweden with one listed maintainer
(`scheibling`), backed by Scheibling Consulting AB and Cloudyne Systems. The
GitHub organization dates from 2025-03.

**The publisher Windows shows is Cloudyne Systems.** The x64 installer of
`vadimgrn/usbip-win2` v.0.9.8.0 carries an EV certificate for
`CN=Cloudyne Systems (Scheibling Consulting AB)`, issued by
`GlobalSign GCC R45 EV CodeSigning CA 2020`, valid until 2027-05-03. Like
SignPath Foundation, it puts no maintainer's name on the artifact. Their FAQ
says projects sign under the shared certificate, and a dedicated one is only
for large, long-standing projects.

Their criteria, checked against fog-agent:

| Criterion | fog-agent |
|---|---|
| OSI-approved license | Yes -- GPL-3.0 |
| "an absolute minimum of 6 months of activity on your account, organization and project" | The account and `FOGProject` (2014) pass. `FOGProject/fog-agent` was created 2026-09-03. If "project" means that repository, it qualifies on 2027-03-03. This is the SignPath trap again: point the application at `FOGProject/fogproject`. |
| Public build pipeline with code quality checks | Yes -- `ci.yml` runs gofmt, go vet on every platform, and go test |
| No political or offensive-security purpose | Yes |

**Their build model differs from SignPath's.** OSSign sets up a companion
repository in its own organization. That repository fetches our source,
builds it, signs the result, and hands it back. Our workflow dispatches the
request and polls with `OSSIGN_USER` / `OSSIGN_TOKEN`. So the binary that gets
signed comes from a build OSSign controls, not from our `release.yml` run. It
is public, but it is a second pipeline to review, and the `build` -> `sign`
split in `release.yml` does not carry over as is.

The risks against SignPath Foundation:

- One listed maintainer. If the project stops, the publisher changes, and
  SmartScreen reputation starts again from zero.
- The publisher is a private consultancy, not a known foundation. An
  administrator sees an unfamiliar Swedish company sign a LocalSystem service
  that writes firmware variables.

Order of preference: SignPath Foundation, then OSSign, then Azure. Both free
routes keep a maintainer's name off the certificate. SignPath wins on its
nonprofit publisher name and because it signs our own CI artifact.

## The fallback if that fails: Azure Artifact Signing

Cheapest of the routes that actually sign unattended, and the only one with a
first-party GitHub Action (`Azure/trusted-signing-action`, Microsoft-owned).
Keys stay in Microsoft's HSM, so the property the SignPath application argued
for survives intact: **no maintainer holds a key, CI holds a revocable
credential.** That still answers design 0001 section 13.

Two things to accept going in:

- **The certificate subject is Tom Elliott's legal name.** Individual
  enrollment has no organization field and no custom CN; identity is proved
  with a government photo ID checked live. Windows will name a person as the
  publisher of FOG's agent, not "FOG Project". That is not a regression from
  what FOG ships today -- the current self-signed leaf is
  `CN=FOG Project - <maintainer>` -- but it is now a publicly trusted claim
  rather than a private one, and per the reputation note above it is
  effectively permanent.
- **The signing job needs a Windows runner.** `release.yml` builds the MSI on
  `ubuntu-latest` with msitools; the `sign` job becomes `windows-latest` and
  takes the artifact across. That is a job boundary we already have.

The escape hatch, if the personal-name problem turns out to matter more than
it looks: incorporating FOG as a legal entity unlocks organization enrollment
on every vendor in the table and widens the country list. That is a much
bigger decision than a signing certificate and should not be made by one.

## What is already in place

`.github/workflows/release.yml` splits `build` -> `sign` -> `publish`
specifically so the signed thing is an artifact of an inspectable CI run.
That shape is what every signing service wants and none of it was
SignPath-specific. Only the contents of the `sign` job and its gating
variable change.

The release **manifest** signature is a separate chain and is unaffected --
see `release-signing-ca.md`. SignPath was never going to replace it.
