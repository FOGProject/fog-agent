# Code signing policy — fog-agent

SignPath Foundation's terms require a published code signing policy naming
the people who can authorize a signature, and the separation of roles between
them. This is that document.

**Nothing here is in force yet.** `fog-agent` applied to SignPath Foundation
on 2026-09-05 and does not hold a certificate; releases today are unsigned,
and the `sign` stage of the release workflow is inert. This document describes
the arrangement that takes effect on acceptance, and exists in advance because
the application requires it.

## Attribution

Free code signing is provided by [SignPath.io](https://signpath.io),
certificate by the [SignPath Foundation](https://signpath.org).

## Team roles

| Role | Who | What it means |
|---|---|---|
| Author | anyone | Opens pull requests against `FOGProject/fog-agent`. Contributions are accepted from anyone; being an Author confers no signing rights. |
| Reviewer | Tom Elliott ([@mastacontrola](https://github.com/mastacontrola)) | Approves pull requests into `main`. |
| Approver | Tom Elliott ([@mastacontrola](https://github.com/mastacontrola)) | Authorizes signing requests in SignPath. |

FOG Project is an independent community project with a single active
maintainer of this component, so Reviewer and Approver are the same person.
That is a real limitation and worth stating plainly rather than dressing up:
the separation SignPath prefers between the person who approves the code and
the person who approves the signature does not exist here. What stands in for
it is that neither role can act on an artifact that did not come from a
tagged, public, CI-produced build — see below.

Additional maintainers will be added to this table, not substituted into it,
if the project gains them.

## Account security

Every person holding a role above uses multi-factor authentication for both
their GitHub account and their SignPath account. Access is removed when
someone stops maintaining the project.

## What gets signed, and from where

Only artifacts built by GitHub Actions in `FOGProject/fog-agent` from a
`v*` tag, by `.github/workflows/release.yml`. Nothing built on a developer's
machine is ever submitted for signing, and there is no path in the workflow
for a locally built file to reach the signing step: the `sign` job consumes
the artifact uploaded by the `build` job of the same workflow run and nothing
else.

Both the installer and the binary inside it are signed. Signing only
`fog-agent.msi` and leaving `fog-agent.exe` unsigned would leave the
long-running service — the file Windows Defender actually watches — without a
signature, which defeats most of the point.

## What is signed on behalf of others

Nothing. `fog-agent` is a single Go binary with no bundled third-party
executables. If that changes, unsigned upstream components will be identified
here rather than signed under FOG Project's certificate.

## Privacy

See [PRIVACY.md](../../PRIVACY.md). In summary: the agent transmits data only
to the FOG server the machine is enrolled with, which is operated by the user's
own organization. It contains no telemetry and sends nothing to FOG Project or
to any third party.

## Revocation and abuse

Certificates are issued in SignPath Foundation's name and may be revoked if
these terms are violated. Report a signed FOG Project artifact you believe to
be malicious, or a signature you believe was issued in error, as a security
issue on <https://github.com/FOGProject/fog-agent/issues> — or, if it should
not be public, through the contact route on <https://fogproject.org>.
