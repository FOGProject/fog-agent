# 0015: Self-update

Status: PROPOSED, 2026-09-06. No code. Supersedes the **"Applying"** and
**"Pinning"** rows of design 0001 section 9, and nothing else there: the
central signed manifest, the anonymous check, the "unreachable central is a
log line" rule and decision 1's "the admin decides, not the agent" all stand
and are what this design is built on.

Amended 2026-09-11: update modes and rings (sections 2.2 and 7), and
server-served updates (section 5).

The change to 0001 §9 is one row: **applying is a desired-state block, not a
task an admin queues.** Everything else in the agent converges from desired
state; making the one dangerous operation the exception would have been the
odd one out, and a task cannot express "this host should be on 1.4.0 and
stay there".

## 0. Why now

The agent is 0.1.1 and unreleased. Whatever version first carries this code
becomes a permanent floor: an agent below it can only be updated by a
package, a snapin, or hands. Every machine enrolled before that floor is a
manual machine forever. This is the cheapest this decision will ever be.

## 1. Two version numbers, and they must diverge

| Number | Lives | On the wire | Stored | Moves when |
|---|---|---|---|---|
| **Protocol version** | `enroll.Protocol = 1` (`internal/enroll/client.go:41`), `\FOG\Agent\Enrollment::PROTOCOL` | `protocol` in the enroll request, `protocol` in the poll answer, the `v1` in `/agent/v1/` | not stored per host | the wire contract breaks compatibly-unreadably: a field changes meaning, a required field appears, a status disappears |
| **Agent version** | `main.Version`, stamped `-X main.Version=` by `build/cross.sh` from the tag | `agent_version` in enroll and poll | `hostAgentVersion`, `aeAgentVersion`, `varchar(50)` | every release, including one that changes no wire byte |

Agent 1.4.0 speaking `/agent/v1/` is the normal case and must stay normal.
The rule, to be written into `protocol-v1.md`:

> **Neither number is derived from the other, and the server never gates
> behavior on `agent_version`.** `agent_version` exists to be reported, and
> to be compared against a desired version. Anything else the server needs
> to know about what an agent can do comes from `protocol` or from the
> agent's declared capability list, never from parsing a version string.
> A protocol bump is a deliberate act with a migration story; an agent
> release is not.

### 1.1 The agent must declare what it supports

Today the server infers capabilities from the host's *module* rows and has
no idea what the binary in front of it can actually do. That is fine while
every capability is server-gated, and it breaks the moment the server needs
to know "will this agent act on an `update` block". Version sniffing is the
wrong answer to that and is how the floor problem becomes invisible.

**Add `supported` to the poll request**: the agent's own capability list,
the same string it already keeps as `supportedCapabilities`
(`cmd/fog-agent/main.go:356` compares it to `Config.AppliedWith`). A new
value in an existing route. The server stores it beside `hostAgentVersion`
and can then answer "which hosts cannot hear a desired version" as a fact
rather than a guess.

This is a small addition that pays for itself far beyond update, and it is
the honest fix for section 10's floor problem.

## 2. The mechanism

### 2.1 The block

A new capability `update`, and a block in the desired state, per the route
rule. No new route.

```json
{
  "revision": "3f1c9a0b2d4e5f60",
  "capabilities": ["hostname", "update"],
  "update": {
    "desired": "0.4.2",
    "manifest_url": "https://releases.fogproject.org/agent/stable.json"
  }
}
```

| Field | Meaning |
|---|---|
| `desired` | An exact version. Never `latest`: the server resolves `latest` to a version before it answers. See 2.2 |
| `manifest_url` | Optional. Where the signed release manifest lives. Absent means the URL compiled into the binary. Present is how a site points at an internal mirror. On its own it does not serve a site with no internet access; see section 5 |
| `manifest`, `signature` | Optional, a pair, added 2026-09-11. Base64 of the exact manifest and envelope bytes the server synced. The agent verifies them instead of fetching `manifest_url`. See section 5 |
| `artifact` | Optional, added 2026-09-11. The payload id of the server's cached file for this host's platform. See section 5 |

The capability is listed only when the resolved desired version is
non-empty, so a server that has never set one describes no block and the
agent has nothing to do. **The feature is off until an admin turns it on**,
which is the rule 0010 and 0011 already hold themselves to: nothing begins
managing a machine because somebody upgraded their server.

### 2.2 The agent refuses `latest`; the server resolves it

Amended 2026-09-11. `desired` is still an exact version, and the agent
still rejects anything else. What changed is who picks the number.

The first draft refused `latest` everywhere. Its reason was sound: `latest`
hands the decision to whatever central published that morning, which is the
fog-client defect design 0001 §1 names. The cost showed up in the field. A
site that set 0.1.7 stayed on 0.1.7 until an admin found the setting again,
and every release needed every admin to act.

The server now holds a mode, and an admin chooses it:

| `FOG_AGENT_UPDATE_MODE` | Meaning |
|---|---|
| `off` | The default. No host updates. Nothing starts managing agent versions because a server was upgraded |
| `pinned` | Every host follows `FOG_AGENT_DESIRED_VERSION`, an exact version |
| `latest` | Each host follows the newest published version whose ring delay has passed (section 7) |

A human still makes the decision. In `latest` mode the decision is the
policy and the ring delays, not each version number.

**The server resolves `latest`, not the agent.** This reverses the
2026-09-03 decision that the server does no manifest handling. The reason
is the installed base. An agent that resolved `latest` itself would need a
build that understands it, and agents 0.1.2 to 0.1.7 refuse `latest` as
`bad_desired_version`. Every site already on 0.1.7 would have to pin once by
hand. A server that resolves it sends those agents an exact version, which
they already accept.

The server gains no power from this. It already chose which published
version a host runs. It still cannot publish one, because the agent checks
every manifest against the root compiled into it.

### 2.3 The agent's state machine

Ordered, and every step refuses forward on failure:

| # | Step | Refusal |
|---|---|---|
| 1 | Compare `desired` with `main.Version`, semver. Equal → done, report nothing | a `desired` that is not a version: `failed`, detail `bad_desired_version` |
| 2 | Refuse if anything is in flight (section 8) | not a failure; defer to the next poll, report nothing |
| 3 | Fetch the manifest and its envelope. Verify the signature over the manifest bytes, then the chain against the root compiled into this binary — judged at the manifest's `signed` time, not the agent's clock (section 3.7) | `failed`, `signature_invalid` |
| 4 | Check the manifest's `sequence` against the highest this agent has seen (stored in `config.json`), its `signed` time against `maxSignatureAge`, and its `expires` against now | `failed`, `stale_manifest` |
| 5 | Find the entry for (`desired`, GOOS, GOARCH). Absent → refuse | `failed`, `no_artifact` |
| 6 | Download the artifact to `<statedir>/update/`, hashing as it streams, exactly as a snapin payload is hashed (`protocol-v1.md`, Snapins, "verify") | `failed`, `hash_mismatch` |
| 7 | **Windows only:** verify Authenticode on the downloaded file and that the signer chains to a trusted root with the pinned subject | `failed`, `signature_invalid` |
| 8 | Arm the probation record and the supervisor's recovery action (section 6) | `failed`, `cannot_arm_rollback` — and it does not proceed |
| 9 | Swap the binary, exit non-zero, let the supervisor restart us | |
| 10 | New binary: on first start under probation, poll. Success clears probation and reports `applied`. Deadline without a success reverts | `reverted` |

Steps 3 through 7 are the whole of section 3. Step 8 refusing to proceed is
the design's one non-obvious insistence: **if the rollback cannot be armed,
the update does not happen.** An update that cannot be undone is worse than
no update.

## 3. Verification

This is a channel that reaches every machine automatically. TLS proves
which socket the bytes came out of and nothing about who wrote them — the
same argument the plugin tarball's sha256 loses when the hash arrives over
the channel it is supposed to protect.

### 3.1 The primary check, on every platform: a manifest signed under a project CA

**One mechanism, every OS, every architecture: the release manifest is
signed by a leaf certificate issued under a FOG-held signing CA, and the
CA's root certificate is compiled into the agent binary.** The manifest
carries the sha256 of every artifact, so a verified manifest plus a
matching hash is a verified artifact.

Verification is `crypto/x509` and `crypto/ecdsa`, both stdlib: build a
`CertPool` from the compiled-in root, `Verify()` the chain in the envelope,
then `CheckSignature` over the manifest bytes. The agent already generates
and handles ECDSA P-256 keys for its own identity, so this is the key type
it already knows.

Envelope, beside the manifest:

```json
{"chain": ["-----BEGIN CERTIFICATE-----…leaf…", "…intermediate…"],
 "alg": "ecdsa-p256-sha256",
 "sig": "base64…"}
```

A custom envelope rather than PKCS#7/CMS deliberately: CMS is not in the
Go standard library, and the whole point of this shape is that verification
costs the agent no dependency. `go.mod` stays at one module.

**Why a self-signed project CA is the right anchor, not a compromise.**
Public trust is irrelevant here. A publicly trusted certificate exists so
that *somebody else's* verifier — Windows, a browser — will accept a
signature from a party it has never met. **The verifier here is the agent,
and it trusts exactly what was compiled into it.** A self-signed FOG root
is therefore not a weaker anchor than a purchased one; for this job it is
the same strength, and it is under FOG's control rather than a third
party's.

**The project CA is not a stopgap, and SignPath does not replace it.** The
two sign different objects, for different verifiers, and they do not
interact:

| | Signs | Verified by | Covers |
|---|---|---|---|
| FOG project signing CA | the release **manifest** | the agent, against a root compiled into it | every platform, permanently |
| SignPath / Authenticode | the **artifacts** (`fog-agent.exe`, the MSI) | Windows — Defender, SmartScreen, and the agent's second check (3.4) | Windows only |

Getting a publicly trusted certificate later **adds** a signature; it
removes nothing and retires nothing. Dropping the manifest chain once
Authenticode existed would leave Linux and ARM with no verification at all,
which is the platform half of the fleet this feature reaches
automatically. So the order of arrival does not matter: build the manifest
chain now, add Authenticode to the Windows path if and when the
certificate lands, and no agent code written today is thrown away either
way.

The consequence matters for scheduling: **update verification never
depended on SignPath and does not wait for it.** SignPath buys one thing,
and it is a different thing — Defender and SmartScreen leaving an unsigned
Windows binary alone at install time (3.2). If the application falls
through entirely, self-update still ships, still verifies, and is still
safe against a compromised FOG server or mirror. That is the answer to
"what do people do in the interim": they use it.

This supersedes the "minisign key" wording in design 0001 §9. The property
0001 was buying — a signature the agent checks against something compiled
into it, so that neither the FOG server nor the CDN is trusted — is
unchanged and is the point. The change is the format, and it buys rotation
(3.3).

Properties that matter:

- **The transport is untrusted, by construction.** It does not matter
  whether the bytes came from GitHub, from `manifest_url` pointing at an
  internal mirror, or from a compromised CDN. This is why it is safe for
  the FOG server to name the URL: a hostile server can serve nothing, serve
  an old validly-signed manifest (blocked by step 4), or serve the real
  thing. It cannot serve a fourth option.
- **The FOG server is not a trust root for binaries.** A compromised or
  merely mistaken FOG server can choose *which* published version a fleet
  runs. It cannot publish one. That is the boundary design 0001 §3 draws,
  and self-update is the feature most likely to erase it if nobody holds
  the line here.
- **Stdlib only.** The agent's entire dependency set is
  `golang.org/x/sys` (`go.mod`) and this design adds nothing to it.

### 3.2 It must be a PROJECT CA, not the customer's FOG server CA

The one correction to make, because the two are easy to say in the same
breath and only one of them works.

| CA | Who holds the key | Scope |
|---|---|---|
| **The FOG server's root CA** (`/etc/fog/pki/`, the one the agent already pins for mTLS, and the one `createAgentIntermediateCA` builds the Agent CA under) | **every FOG server, separately** — each install generates its own | that one server's own machines |
| **A FOG project signing CA** — a new one, held by the project, conceptually the same thing as the existing `CN=FOG Project CA` that already signs `SmartInstaller.exe` and `FOGService.msi` | the project | every FOG installation everywhere |

Signing releases with the *server's* CA cannot work and should not be made
to:

- It is per-installation. A binary signed by school A's server is
  unverifiable by an agent enrolled with school B, and there is no build of
  the agent that could contain every server's root.
- It inverts the entire trust model. That key lives on the FOG server. A
  manifest signed by it proves "this server said so", which is precisely
  what section 3 exists to stop being sufficient — and a FOG server is a
  box on a school network, not a hardened signing appliance.

What *is* worth taking from the server side is the **tooling and the
shape**. FOG already knows how to mint a root, mint an intermediate under
it with a root-only helper, and sign with it — `createAgentIntermediateCA`
in the installer, `fog-sign-node-cert`, `fog-pki-admin`. A project signing
CA is the same construction operated in a different place, so there is
nothing new to invent.

**Note:** the existing `CN=FOG Project CA` has a *past* maintainer's name
on its leaf (`docs/signing/signpath-application.md`), so whether its key is
available is an open question. A new CA is the likely and unremarkable
answer.

### 3.3 Rotation, and why this retires the two-keys workaround

An earlier draft of this design used a bare Ed25519 public key compiled in,
and had to bolt on "compile in two keys" because losing the one key would
kill the update channel permanently for the entire installed base.

A CA does not need that, because **the compiled-in thing and the signing
thing are different objects**:

| | Held | Life | If compromised |
|---|---|---|---|
| Root key | offline, backed up the way a CA key is backed up | 20 years | the cliff. See below |
| Leaf key | wherever signing happens, including CI | short — months | let it expire, issue a new leaf. **No agent build changes** |

That split is what makes the custody question from earlier rounds much less
sharp than I made it: a **leaf** key in GitHub Actions has a bounded blast
radius and a recovery that does not touch a single deployed agent, which a
bare signing key never had. The root stays offline and is the only thing
that has to be kept safe by hand.

The residual cliff is **root expiry or root loss** — every agent stops
accepting updates and only a re-install fixes it. Mitigations, both cheap:
a long root life, and compiling in a second root generated at the same time
and stored separately, accepted as an alternative chain. The second root
costs one more entry in the `CertPool`.

**Revocation is not checked.** No CRL, no OCSP: a managed machine may be
offline for weeks and an update path that fails closed on an unreachable
CRL is an update path that stops working. Containment is short leaf lives
plus the manifest `sequence` floor, which is what actually stops a replayed
or stale signed manifest.

### 3.4 Windows: Authenticode as the second, independent check

Once the SignPath certificate exists — and **only then; nothing waits for
it** — the agent additionally calls `WinVerifyTrust` on the downloaded file
and requires a valid signature chaining to a trusted root whose leaf
subject matches the pinned publisher (subject CN and issuer, not a
thumbprint: the leaf rotates yearly and pinning a thumbprint turns a
certificate renewal into a fleet-wide refusal).

**Which check is load-bearing:** the manifest chain, on every platform
including Windows. Authenticode is a second check in a *different
compromise domain* — SignPath's HSM, reachable by whoever holds the CI
token, with a human authorizing each request per
`docs/signing/code-signing-policy.md`. That separation is the only reason
the second check is worth writing.

`code-signing-policy.md` already commits to signing `fog-agent.exe` and not
just the MSI. That commitment is what makes this check possible on the
artifact self-update actually downloads, and it was made for a different
reason — Defender watches the service binary. Convenient.

### 3.5 Linux and ARM

The manifest chain is the entire verification story there and it is a good
one: an offline root compiled into the binary being replaced. Authenticode
has no equivalent and none is worth inventing.

Rejected for a second check on Linux:

| Alternative | Why not |
|---|---|
| Distro package signatures (deb/rpm signed by a FOG repo key) | Real, and worth having *for the package path*. Not usable here: it means shelling out to `dpkg`/`rpm` to verify, it needs a repo, and it gives ARM, musl and everything else nothing. A packaging decision (0005), not a self-update one |
| sigstore / cosign keyless with GitHub OIDC | Verification needs Fulcio and Rekor reachable from every managed machine, plus a large dependency tree in a binary whose dependency list is one module. Wrong shape for an agent that must work on an isolated VLAN |
| TUF | Correct, and enormous. What it addresses beyond "signed manifest, sequence floor, offline root" is a multi-role repository compromise, which a single-maintainer project does not have the roles to express |
| A hash pinned in the FOG server's database by the admin | Moves the trust to the server — the thing being defended against — and makes every admin transcribe hashes |

### 3.6 When verification fails

**Refuse, keep running the current version, and be loud.** Specifically:

- The agent does **not** apply, does not retry on a timer, and does not
  quarantine itself. It carries on doing everything else it was doing.
- It reports a result on capability `update` with `status: failed` and a
  detail naming the check: `signature_invalid`, `hash_mismatch`,
  `stale_manifest`, `no_artifact`, `cannot_arm_rollback`. That lands in
  `auditLog` through the path `\FOG\Agent\State` already uses for
  `agent.result`.
- **A verification failure is retried at most once per changed input** —
  once per new `desired`, or per manifest whose `sequence` moved. It is not
  a transient network error and treating it as one lets a hostile mirror
  turn every agent into a download loop.
- A network failure *is* transient and does retry, on the poll cadence,
  with no cap.

How the admin finds out, in descending order of how likely they are to see
it:

| Surface | What it shows |
|---|---|
| Host list "Agent version" column (section 11) | the version did not move. This is the passive signal and it is the one that works even if nobody built anything else |
| A new `hostAgentUpdateState` on the host row (`ok` / `pending` / `refused`) plus `hostAgentUpdateError` | a filterable "show me every host that refused an update" |
| The Host Agent Activity tab (`HostManagement.php:4237`) and `auditLog` | the detail, per transition |

A signature failure specifically means either a broken mirror or somebody
trying something. It deserves more than a row in a log nobody reads, and
whether FOG has an existing admin-notification surface worth hanging it on
is an open question in section 14.


### 3.7 The chain is judged at signing time, not at use time

The signing leaf lives in CI and is short-lived on purpose: expiry is the
only revocation this PKI has. But verifying the chain against the *agent's*
clock made that rotation destructive. On the day the leaf expired, every
manifest it had ever signed stopped verifying at once — and stopped with
`signature_invalid`, which says "not signed by a key this build trusts" and
sends whoever is debugging it hunting a compromised key rather than a date.

The manifest therefore carries `signed`, and the certificate chain is
verified against that. A manifest signed while the leaf was good keeps
verifying for its whole stated life, so rotating the leaf touches nothing
that is already published. This is what timestamping does for Authenticode,
and it is why a binary signed in 2010 still validates.

Three things make it safe to let the manifest name the time it is judged at:

- **The signature is checked before `signed` is read.** Verification now
  runs the ECDSA check over the raw bytes first, using the leaf's public
  key without yet trusting it, and only then parses and walks the chain. By
  the time `signed` is used it is bound to the key, so nobody without the
  key can choose it.
- **A claim in the future is clamped to now.** Otherwise a manifest could
  reach forward into a certificate's validity window — and clock skew,
  which is real, gets the conservative reading for free.
- **A claim further back than `maxSignatureAge` is refused as stale.** This
  is the one that matters. Honouring the signing time would otherwise let
  whoever holds an *expired* leaf backdate into the window where it was
  valid and sign forever. With the bound, a leaf dead longer than
  `maxSignatureAge` has no usable claim left: every moment it was valid is
  by now too old to honour. The exposure from a leaked leaf is its
  remaining validity plus that bound, and nothing else.

`expires` is unaffected and is always judged against the real clock: it is
the publisher's statement about freshness, and reading it at `signed` would
let a manifest declare itself eternally fresh.

A manifest with no `signed` field is judged against now, exactly as before —
which is what the manifests published before this existed need.

## 4. What this asks of the build pipeline

**Nothing that blocks building or testing.** Everything below can be stood
up today, on the lab box, by the project, without a decision from anyone
outside it.

### 4.1 A project signing CA, and a release step that uses it

The one genuinely new piece:

- **Mint a "FOG Agent Signing CA"**: a self-signed root (long life, key
  offline and backed up) and an intermediate, using the same construction
  the FOG installer already uses for its own PKI. The root certificate —
  public, not secret — is committed into the agent source and compiled in.
- **`release.yml` gains a manifest step** emitting `manifest-<version>.json`
  with every artifact's sha256, plus the signature envelope of 3.1. It runs
  after signing, since the Windows hashes change if SignPath ever runs.
- **The leaf key signs it.** Short-lived and reissuable (3.3), so it can
  live in Actions with a bounded blast radius, or be turned by hand — that
  is a release-process choice, not a design one, and it no longer changes
  what a compromise costs at the scale it used to.

The lab proof uses the same code with a CA generated on the lab box: same
verifier, same envelope, same failure branches, different bytes. That is a
real test of the shipping path rather than a rig that proves a different
one.

### 4.2 A Windows certificate, eventually, for a different problem

`.github/workflows/release.yml` guards the `sign` job on
`vars.SIGNPATH_ORGANIZATION_ID` and it is unset; the SignPath Foundation
application went in 2026-09-05 with no answer and no date.

**Nothing in this design waits for it, including on Windows.** Update
verification is 3.1 and it is self-contained. What the certificate buys is
one thing: **Defender and SmartScreen leaving an unsigned Windows binary
alone.** One was quarantined from `C:\Windows\Temp` on 2026-09-05
(`docs/signing/signpath-application.md`), and writing an unsigned exe into
`C:\Program Files\FOG\` and restarting a service onto it is a real
deployment hazard.

That hazard is worth measuring rather than assuming: **find out what
Defender actually does to a self-replaced unsigned binary on
`telliottwin11`.** It may be a non-event on a machine where the file is
already installed and running, and it may not. Either answer is a result.

If SignPath falls through entirely, the position is: self-update works,
verifies, and is safe on every platform, and Windows users see the same
Defender friction they already see installing FOG's existing unsigned-in-
practice client. That is a shipping product, not a blocked one.

### 4.3 Worth asking SignPath if the application progresses

Not a blocker and not a dependency: whether SignPath can also sign the
manifest envelope. It would put the signing key in an HSM behind an
approval, which is nicer than a leaf key in CI. The likely blocker is
format — the agent verifies with `crypto/x509` and a custom envelope, and
an Authenticode or CMS signature is not that. UNKNOWN; I have not read
their documentation on supported artifact types.

## 5. Where the artifacts come from

**The FOG server first, then the origin the manifest names.** Amended
2026-09-11.

The first draft said the FOG server is not a binary distribution point. It
rejected `GET /agent/v1/payload/update/{id}` and answered the bandwidth and
air-gap cases with `manifest_url`: copy the release assets and the signed
manifest to a web server, and point the block at it.

**That answer was wrong for an air gap.** The manifest carries each
artifact's URL inside its signed bytes, and the agent downloaded from that
URL (`download()` in `internal/provider/update/update.go` used `art.URL`).
So a mirror moved the manifest fetch and nothing else. Every host still went
to GitHub for the file, and a mirror cannot rewrite those URLs without
breaking the signature.

### 5.1 Server-served updates

- **A daemon syncs releases.** `FOGAgentReleaseSync` fetches the signed
  manifest and its signature, and keeps their exact bytes. It records when
  this server first saw each version. Ring delays count from that time
  (section 7). The sync never runs inside an agent poll.
- **Where it keeps them.** Each file goes under
  `/opt/fog/agent/versions/<version>/`, with the file name from its artifact
  URL, owned by the web server's user. The exact manifest and signature
  bytes are kept under `/opt/fog/agent/versions/` as well.
- **What it keeps.** Every version a host runs or is told to run, plus the
  newest `FOG_AGENT_KEEP_VERSIONS` versions still in the manifest (default
  3), so a rollback works when the origin is unreachable. A version
  withdrawn from the manifest is pruned unless a host still needs it. Files
  are kept only for the OS and architecture pairs its enrolled hosts report.
- **Agents fetch over mTLS.** The `update` block carries the manifest and
  signature inline, and `artifact` names the cached file. The agent fetches
  that file from `GET /agent/v1/payload/update/{id}` over its client
  certificate.
- **The origin is the fallback.** Any failure of the server's copy falls
  back once to the URL in the manifest entry. A host with no internet access
  never needs the origin while its server holds a good copy.
- **Trust is unchanged.** The agent verifies the inline manifest against its
  compiled root and hashes the file against the manifest entry, exactly as
  for a download. A compromised server can serve wrong bytes, and the agent
  refuses them.

The first draft's three objections, and why they no longer hold:

| Objection | Now |
|---|---|
| The `{id}` names nothing; a row invented to have an id is the route rule followed backwards | The server has to know which files it holds and which to delete. The id names one of those files |
| It makes every FOG server a software distribution point again, the first row of 0001 §1 | The fog-client defect was an unverified in-place update. The server now hands out files the agent verifies against a root the server does not hold, and it already chose the version |
| 10 MB per platform per version, streamed from a storage node over FTP (`Agent/Snapins.php`) | The server stores only the platforms its hosts report. The FTP plumbing is the snapin implementation; the `update` payload streams from the server's own cache |

Agents older than these fields ignore them and download from the origin, as
before.

**TLS for the manifest and artifact fetch** uses the system roots plus the
FOG CA, which is the exception design 0003 already made for the Chocolatey
bootstrap fetch for exactly this reason ("so the FOG server can host it").
It is a deliberate, narrow exception to 0002's "the system trust store is
never consulted", and it costs nothing because TLS is not what is trusted
here.

## 6. Rollback

The hard part. An update that breaks the agent breaks the thing that would
undo it.

### 6.1 Three layers

**Layer 1 — the old binary survives on disk.** Exactly one: the swap is
`rename(fog-agent → fog-agent.prev)` then `rename(new → fog-agent)`. On
Unix a running binary can be renamed and replaced; on Windows a running exe
can be renamed but not overwritten, which is the same sequence. Cost is one
binary, ~10 MB. `config.json` is copied to `config.json.prev` in the same
step, because a new agent that writes a state file an old one cannot read
would make the revert useless.

**Layer 2 — the supervisor restarts a crashing binary and, after N
failures, runs the revert.** Set at install, by the old binary, before the
swap:

| OS | Mechanism |
|---|---|
| Windows | service failure actions: `restart / restart / run <exe> update-revert`, reset daily. `service install` does not set these today; it must |
| Linux | `Restart=always` in the unit, plus `OnFailure=fog-agent-revert.service` (a `Type=oneshot` unit running `fog-agent update-revert`), and `StartLimitBurst` so it fires |

This also gives the restart mechanism for free: **the agent replaces its
binary, then exits non-zero, and the supervisor starts it again.** One
mechanism, both platforms, no detached helper process, no PID race.

**Layer 3 — probation.** Before the swap the old binary writes to
`config.json`:

```json
"update_probation": {"from": "0.4.1", "to": "0.4.2",
                     "deadline": "2026-09-06T14:31:00Z"}
```

Deadline is `3 × poll_interval`, floor 15 minutes. The new binary must
complete one successful authenticated poll before it. Success clears the
record and reports `applied`. The deadline passing reverts: restore
`.prev`, restore `config.json.prev`, exit non-zero, and on the next start
report `reverted` with the reason.

### 6.2 What each layer actually catches — and what nothing catches

| Failure | Caught by | Automatic? |
|---|---|---|
| New binary crashes on start | Layer 2 | Yes |
| New binary starts but cannot poll (a cert-handling or protocol regression) | Layer 3, timer inside the new process | Yes, if it is healthy enough to run its own timer |
| New binary hangs before reaching its timer | Layer 2, only if the supervisor sees a failure. A hung, non-exiting process looks healthy to systemd and the SCM | **No** |
| New binary polls fine but a provider misbehaves (reboot loop, printers wiped) | Nothing local. Recovery is the server setting an older `desired` — which works, because the agent is still polling | Yes, via section 9 |
| New binary corrupts its own state | Layer 1's `config.json.prev` | Partly |
| A bad version was given to the whole fleet at once | Nothing. This is what section 7 exists to prevent | No |

**The honest answer to the case nothing catches:** a binary that starts,
hangs, and never polls needs hands on the machine, or the MSI/package, or a
re-image. There is no clever way out of it — a watchdog inside the process
cannot fire, and a watchdog outside it is a second thing to install and
keep working, which is a second thing that can be the broken one. The
mitigation is not a fourth layer, it is section 7: **do not give a build to
the whole fleet at once.** A hang that survives a canary group is the
scenario this design does not defend against, and saying so is better than
implying it does.

### 6.3 What an update must never touch

The state directory — key, certificate, `config.json`'s enrollment fields.
Already true today (the MSI upgrade path leaves `C:\ProgramData\FOG\agent`
alone, proven 2026-09-04) and it must stay true: an update that can break
enrollment converts a bad build into a fleet that needs re-approving by
hand.

## 7. Staged rollout

Amended 2026-09-11. The first draft staged a rollout by mass-editing host
overrides, and it said no wave machinery was needed. `pinned` mode still
works that way. `latest` mode needs rings, because a version that reaches
every host at once is the one failure nothing recovers from (section 6.2).

**A group is the tag concept** (fogproject ADR 0038 decision 16), so the
obvious move is a `groupAgentDesiredVersion` column resolved across a host's
groups. **ADR 0038 rules that out**, and it is worth quoting the test rather
than working around it: the line between what a group owns and what it does
not is "whether two sources granting the same thing can be combined without
one of them losing". A host holds exactly one desired version. Two groups
naming different ones means one loses. That puts it in the ADR's
**imperative half**, alongside `hostKernelArgs` and `hostImage` — and the
imperative half is *leaving* the group page for mass edit driven from the
host list (ADR 0038 decision 8). Adding a group scalar now would be adding a
column to the pile that ADR is removing.

The same test decides where a ring lives: a host is in exactly one ring.
So the shape is settings on the server and columns on the host, with no
group column and no group resolver:

| Source | Setting or column | Rule |
|---|---|---|
| Global | `FOG_AGENT_UPDATE_MODE` | `off`, `pinned` or `latest` (section 2.2). `off` is the default |
| Global | `FOG_AGENT_DESIRED_VERSION` | the fleet's version in `pinned` mode |
| Global | `FOG_AGENT_UPDATE_RINGS` | delays in days, one per ring, in ring order. The default is `0,3,7` |
| Global | `FOG_AGENT_MIN_VERSION` | a floor in every mode, empty by default. See below |
| Global | `FOG_AGENT_KEEP_VERSIONS` | how many of the newest versions in the manifest the server keeps on disk, default 3 (section 5.1) |
| The host | `hostAgentUpdateRing` | the host's ring. Empty means the last ring, so a new host is never a canary by accident |
| The host | `hostAgentDesiredVersion` | an **override**. Non-empty wins outright over every mode, including when it is lower |

**How the server resolves `latest` for one host:**

- It records when it first saw each version in the manifest.
- A version is eligible once that time plus the host's ring delay has
  passed. The target is the newest eligible version.
- If no version is eligible yet, the host stays where it is.
- The target is never below the version the host runs, unless that version
  has left the manifest. Withdrawing a release deletes its key from the
  manifest (`docs/RELEASING.md`), so hosts on it drop back with nobody
  acting.
- A version marked `security: true` waits for its ring like any other. A fix
  that must go out now is a pin.

**`FOG_AGENT_MIN_VERSION` is a floor in every mode.** It is empty by
default. No host is told a version below it: a pin, a host override, or a
ring whose delay would leave a host below it is raised to it. A host that
runs a release below it is raised to it even in `off` mode. A lab build that
is not a release version is left alone. Saving a pin or a host override
below it is refused, and the floor must name a version in the server's
manifest. This server setting is separate from the downgrade floor compiled
into the agent (section 10), which refuses with `below_floor`.

An override wins outright rather than being a floor because **fleet-wide
rollback has to work**: if the resolved value were `max(global, host)`, a
canary host set to 0.4.2 would ignore the global being pulled back to 0.4.1,
and section 9's whole recovery story dies on the machines most likely to
need it.

**Groups are still the lever, exactly as Tom said — they are the
*selection*, not the storage.**

In `latest` mode a rollout is set up once:

1. Filter the host list by the canary tag. Select all. Mass edit
   `hostAgentUpdateRing` to 0.
2. Repeat with the next tag and ring 1. Every other host stays in the last
   ring.
3. Each new release then moves by itself: ring 0 once the server first sees
   it, and each later ring after its delay. Watch the version column and the
   update-state column while it moves.

To stop a bad release in `latest` mode, switch to `pinned` at the good
version, or set overrides on the hosts that already have it.

In `pinned` mode a rollout is the first draft's:

1. Filter the host list by the tag. Select all. Mass edit
   `hostAgentDesiredVersion` to 0.4.2. Those hosts move; nothing else does.
2. Watch the version column and the update-state column.
3. Widen by repeating on more tags, or finish by setting the global to
   0.4.2 and clearing the overrides in one more mass edit.

Rollback of a canary is *clearing the override*, which drops those hosts
back to the global — the fastest recovery in this design, and it needs no
manifest fetch and no new version number. Rollback of a fleet is setting
the global lower and clearing any overrides that would shadow it.

The cost of an override is that it is sticky, and a stale one silently
holds a machine back. That is the copy problem ADR 0038 exists to complain
about, and the mitigation is visibility, not cleverness: **the host list's
update-state column says `override` when `hostAgentDesiredVersion` is set**,
so "show me every host not following the fleet" is a filter, and clearing
them is one mass edit. `hostAgentUpdateRing` has the same cost: a host added
to a canary tag later needs one more mass edit.

**No percentage machinery.** Rings are a list of delays, and a host's ring
is one column, set with the mass edit and tag filter that already exist.

One agent-side addition: **jitter the artifact fetch by up to one poll
interval.** 500 machines learning about a new version in the same 5-minute
window and each pulling 10 MB is a thundering herd against whatever is
serving the mirror. Random jitter costs three lines and no server change.

## 8. Restart semantics

The update provider **runs last in the reconcile, and only when nothing is
in flight.** Concretely, it defers (reports nothing, tries next poll) when
any of these hold:

| In flight | Why it matters |
|---|---|
| A snapin payload is downloaded or running | The child process is in its own process group and survives the agent dying. The task row stays "in progress" server-side and its result is never reported, so the job hangs until an admin cancels it. This is the one that would actually hurt |
| A software entry is mid-install | Same shape. `choco` finishing after the agent is gone leaves `softwareStatus` stale |
| The reboot coordinator is about to act | Doing both means two restarts and a window where a half-applied update meets a reboot. **The update yields to a pending reboot** — the reboot restarts the agent anyway, and the update happens after the machine is back |
| A directory join is underway | It carries a credential and it is not idempotent midway |

What is safe:

- **A queued task** (imaging, waiting for the host to boot into FOS) is
  server-side state. The agent re-reads it on the next poll. `config.json`
  carries `RebootedForTask` across the swap, so the "reboot once per task
  id" rule survives.
- **Pending reboot reasons** persist in `config.json` and survive.
- **Facts hashes** persist; a restart does not cause a full resend.

The Windows service already drains cleanly on stop — `handler.Execute`
cancels the loop's context and waits, "so a converge in progress finishes
its report before the process goes" (`cmd/fog-agent/service_windows.go`).
The update path exits deliberately rather than being stopped, so it must do
the same waiting itself.

## 9. Downgrade

**Supported, with three guards.**

The argument for supporting it is section 6.2's fourth row: the most likely
shape of a bad build is one that runs and polls perfectly well and does
something wrong. Local rollback cannot catch that — the binary is healthy
by every local measure. The only fleet-wide recovery is the server naming
an older version, and refusing downgrade means every instance of that
becomes hands-on across a school.

The hazards are real:

| Hazard | Handling |
|---|---|
| A newer agent wrote state fields an older one does not know | Go's `encoding/json` ignores unknown fields by default and the agent nowhere calls `DisallowUnknownFields`, so an old agent *reads* a new `config.json` fine. It will **drop** those fields when it next writes — a lost pending-reboot reason of a new kind, say. Accepted: that cost is far below the cost of having no fleet-wide recovery |
| A downgrade below the format the state file requires | Guard 1: a compiled-in `MinDowngrade` floor. An agent refuses to become a version below it, reporting `failed`/`below_floor`. Set to the first self-update-capable version initially and raised only when a state change genuinely cannot be read backwards |
| A downgrade to something unpublished or tampered with | Guard 2: a downgrade verifies through exactly the same manifest chain as an upgrade. No special case, no shortcut |
| A downgrade loop with an upgrade | Guard 3: the agent never chooses a downgrade on its own. Only the server's `desired` drives one. The probation revert (6.1 layer 3) is a local `.prev` restore, not a manifest fetch, and it does not re-enter this path |

## 10. The version floor

An agent at 0.1.1 has no update code. Told to become 0.4.2 it will ignore
the block entirely — Go's JSON decoder discards the unknown `update` key
without error. It will never update itself, ever, by this mechanism.

That is not fixable and should not be dressed up. What *is* fixable is
making it visible and finite:

- **The server knows which hosts can hear it**, from `supported` in the
  poll request (section 1.1) — not from parsing `hostAgentVersion`, which
  would be the same version-sniffing mistake in a different place.
- **The fleet view distinguishes three states, not two**: `pending` (told,
  hasn't moved yet), `refused` (told, verification failed — see it), and
  **`cannot` (this agent does not support self-update; a package, a snapin
  or hands is the only path)**. A `cannot` host must never sit in `pending`
  forever looking like it is about to work.
- An agent that predates `supported` and predates `update` reports neither.
  Absence of `supported` **is** the floor signal for exactly the versions
  that matter, which is a happy accident worth relying on: 0.1.1 sends no
  `supported` key, and every agent that does send one is new enough to have
  been built after this design.

The `cannot` list is a finite worklist an admin can work through with the
MSI or a snapin. Without it, it is an invisible permanent tail.

## 11. Policy: automatic, or approved per version?

**Recommendation: the admin approves a version, once, and every host
resolved to it applies automatically.** The approval unit is the *version*,
not the host and not each transition.

| Option | Case for | Case against | Verdict |
|---|---|---|---|
| Fully automatic (agent takes the newest central publishes) | Security patches land without anyone doing anything, on the machines least likely to have anyone watching | Recreates the fog-client defect named in 0001 §1 verbatim. A bad build reaches every school simultaneously and no local admin has a lever. Also hands the project the ability to change what a customer's machines run without the customer agreeing, which 0001 §3 says it must not have | Rejected |
| **Admin sets a version; hosts apply it** | One decision per release, made by someone who can read the notes. The rollout lever is groups. A bad build is stopped by not setting it, and recovered by setting an older one | A security patch waits for an admin who is on holiday | **Recommended** |
| Approve per host, or per transition | Maximum control | 500 approvals is not a workflow anyone finishes. It degrades to nobody ever updating, which is strictly worse for security than the option above | Rejected |

Two things make the middle option carry its weakness:

- `security: true` in a manifest version entry, surfaced prominently by the
  server ("6 hosts are on a version with a published security issue") but
  **never auto-applied**. It makes urgency visible without taking the
  decision. Small, and it is the whole answer to "waits for an admin".
- The default is empty: a server that never sets a desired version never
  updates anything. Nobody gets this behavior by upgrading into it.

Amended 2026-09-11: `latest` mode (section 2.2) is not the rejected first
row. The agent does not take what central publishes. The server resolves a
version after an admin chose the mode and the ring delays, and a release
reaches the fleet ring by ring, not all at once. The default is still `off`.

## 12. Surfacing

| Where | What | Cost |
|---|---|---|
| Host list column + filter | Agent version. `hostAgentVersion` is already on the row and `Host.php:142` already maps `agentVersion`; `HostManagement.php:128–192` declares the columns and does not include it | A column definition and a search field. This is most of the feature |
| Host list column | Update state (`ok`/`pending`/`refused`/`cannot`) from a new `hostAgentUpdateState` | Filterable, which is what turns "something is wrong somewhere" into a list |
| Dashboard | Agents by version — a count, grouped. This is what makes a staged rollout observable while it is happening | Small, and the thing an admin actually watches during a rollout |
| Host Agent Activity tab (`HostManagement.php:4237`) | The per-host transition history. It already exists and already shows agent checkin and version; transitions belong here, not on a new page | Rows, no new page |

## 13. The audit trail (ADR 0021)

Two kinds of row, and the first is free.

**Who set the desired version.** Put it on real objects — a global setting
and a column on `hosts` — and every write goes through the normal
authorized save path. ADR 0021 writes the header at
`Authorization::require*Permission()` and the `auditChange` old→new rows at
the model, with a correlation id joining them. **A bespoke table or a
side-channel write would have to reimplement all of that**, which is on its
own a sufficient argument for the shape in section 7. The mass edit path
is the one wrinkle: `FOGManagerController::update()` writes bulk SQL
directly and is one of the 40 call sites ADR 0021 decision 2 names as
blind spots for change rows. A mass edit of 40 hosts must still produce
one header with `affectedCount = 40`, which is what `correlationID` and
`affectedCount` are for; confirm the mass edit path already does this
before relying on it.

**Each agent's transition.** Machine-originated, so ADR 0021 decision 4
applies: `createdBy = 'fog'`, `authSource = 'agent'` — which is what
`\FOG\Agent\State` already records for `agent.result` rows — and
`permission = ''`, saying truthfully that no authorization was consulted.
New types:

| Type | When | Subject / text |
|---|---|---|
| `agent.update.applied` | probation cleared | host; `0.4.1 → 0.4.2` |
| `agent.update.refused` | any verification failure | host; the check that failed and the version it was asked for |
| `agent.update.reverted` | probation deadline passed | host; `0.4.2 → 0.4.1` and why |

**Not audited:** "checked, already at the desired version". That is every
poll on every host forever, and ADR 0021 decision 4's scope limit — writes
that change state a person would care about, "not every checkin" — excludes
it explicitly.

## 14. Open questions

- **Does FOG have an admin-notification surface** worth raising a
  signature failure on, beyond a filterable column? A refused signature is
  the one event here that means somebody may be attacking a fleet, and a
  column nobody sorts by is a weak place for it.
- **Linux packaging conflict.** deb/rpm do not exist yet (0005: "Linux and
  macOS packages are later slices"). If the package owns
  `/usr/sbin/fog-agent`, a self-updated binary makes the package's file
  hash wrong and the next distro upgrade silently reverts it. This design
  constrains 0005 and 0005 must record the choice; the cheapest answer is
  that both paths are legitimate, the newest wins, and the agent's
  reported version makes the divergence visible.
- **The MSI's ProductVersion goes stale** when the exe is swapped
  underneath it. Add/Remove Programs will show the installed MSI version
  while `fog-agent version` and the server show the truth. Accepted
  deliberately (see the rejected alternative below), but it should be
  written into 0005 rather than discovered.

## 15. Alternatives rejected

| Alternative | Why not |
|---|---|
| **Self-update by running the MSI on Windows** (`msiexec /i /qn`, which `build/upgrade-lab.ps1` proves works) | Keeps Add/Remove Programs honest and lets Windows Installer handle the service stop/start and its own transactional rollback. Rejected because **rollback is remove-then-install**: MajorUpgrade will not go backwards, so reverting means `/x` the new product then `/i` the old one, with a window where no agent is installed at all. That is the wrong tradeoff on the one path that exists to recover from a bad build. A binary rename is atomic and takes microseconds |
| **A queued task, per 0001 §9's "Applying" row** | A task is a one-shot instruction; desired version is a *state* a host should hold. A task cannot express "and stay there", so a re-imaged machine coming back on an old version would need the task re-queued. Every other capability in this agent converges; this one should too |
| **A new `/agent/v1/update` route** | Fails the route rule on its own terms: no new transport shape, no new trust boundary, no new verb. It is a new value in the existing poll answer |
| **`GET /agent/v1/payload/update/{id}`** for the bytes | Rejected in the first draft, adopted 2026-09-11. Section 5 |
| **Waiting for SignPath before shipping self-update** | Section 3.1. The agent is the verifier and trusts what was compiled into it, so a self-signed project root is a full-strength anchor for this job. SignPath solves Defender, which is a different problem on a different schedule |
| **A bare Ed25519 public key compiled in, per 0001 §9's "minisign key"** | What this design said in its first draft, and what it was corrected from. It cannot rotate: the compiled-in thing and the signing thing are the same object, so a leaked key or a lost key is a new build for the entire installed base, and it forces a "compile in two keys" workaround for the loss case. A CA separates the two (3.3) for the same stdlib cost |
| **Signing the manifest with the customer's FOG server CA** | Section 3.2. Per-installation, so it cannot verify across servers, and it is the key a compromised server holds |
| **Trusting the FOG server's TLS as the verification** | The premise of the whole section 3. A compromised server is the case this defends against, and it is not an exotic one: FOG servers are on school networks and are not hardened appliances |
| **No self-update at all; MSI and packages forever** | Defensible today, and impossible to adopt later without the fleet already carrying the code. Section 0 |

## 16. Claims

**VERIFIED** — read in the code named:

| Claim | Command |
|---|---|
| `agent_version` is sanitised at Route.php:3012 and written as `agentVersion` at :3018 | `sed -n '3007,3025p' /var/www/html/fog-1.6/src/Router/Route.php` |
| `hostAgentVersion` and `aeAgentVersion` are `varchar(50)`; `Host.php:142` and `AgentEnrollment.php:56` map `agentVersion` | `grep -rn 'hostAgentVersion\|aeAgentVersion' /var/www/html/fog-1.6 --include='*.php'` |
| The poll answer's top-level keys are status, protocol, host, revision, poll_interval, server_time, state, facts, sessions | `sed -n '3026,3064p' /var/www/html/fog-1.6/src/Router/Route.php` |
| The payload route validates against `State::PAYLOADS`, which today contains `snapin` only | `sed -n '110,125p' /var/www/html/fog-1.6/src/Agent/State.php` |
| `update` is not in `Route::$validClasses`, so the noun test would not fire on it | `sed -n '657,725p' /var/www/html/fog-1.6/src/Router/Route.php` |
| The host list columns (`HostManagement.php:128–192`) do not include agent version; it appears only in the Host Agent Activity tab at :4237 | `sed -n '128,192p' /var/www/html/fog-1.6/src/Pages/HostManagement.php` |
| A group is the tag concept (ADR 0038 decision 16) | `grep -n 'tag concept' /home/telliott/fogproject/docs/adr/0038-a-group-grants-it-does-not-copy.md` |
| ADR 0038 splits group-owned from host-held on "can two sources be combined without one losing", and sends the imperative half to mass edit | `sed -n '190,215p' /home/telliott/fogproject/docs/adr/0038-a-group-grants-it-does-not-copy.md` |
| `groups` does carry scalar columns today (`groupKernel`, `groupKernelArgs`, `groupInit`, `groupPrimaryDisk`) and ADR 0038 decision 8 is removing them | `sed -n '338,400p' /var/www/html/fog-1.6/commons/schema-expected.php \| grep -o "'group[A-Za-z]*'" \| sort -u` |
| ADR 0021 writes the header at the authorization seam and gives machine writes `createdBy='fog'` with an empty permission | `sed -n '218,300p' /home/telliott/fogproject/docs/adr/0021-the-audit-trail.md` |
| The agent's only non-stdlib dependency is `golang.org/x/sys` | `cat go.mod` |
| The agent never calls `DisallowUnknownFields`, so an old build ignores an `update` block | `grep -rn DisallowUnknownFields --include='*.go' .` |
| `main.Version` is ldflags-stamped; `enroll.Protocol = 1` | `grep -n 'main.Version\|const Protocol' cmd/fog-agent/main.go internal/enroll/client.go` |
| The `sign` job is inert on an unset `SIGNPATH_ORGANIZATION_ID` | `sed -n '76,82p' .github/workflows/release.yml` |
| The code signing policy already commits to signing the exe as well as the MSI | `grep -n 'Both the installer' docs/signing/code-signing-policy.md` |
| The Windows service drains its context on stop | `sed -n '64,90p' cmd/fog-agent/service_windows.go` |
| 0005 decision 6 says there is no self-upgrade and calls it its own slice | `grep -n 'No self-upgrade' docs/design/0005-packaging.md` |
| 0001 §9 already decided a signed central manifest with the public key compiled in | `sed -n '258,275p' docs/design/0001-architecture.md` |
| Defender quarantined an unsigned Go binary on 2026-09-05 | `grep -n 'quarantined' docs/signing/signpath-application.md` |
| FOG's existing Windows artifacts are signed by a self-signed `CN=FOG Project CA` whose leaf names a past maintainer, and Windows treats them as unsigned | `sed -n '28,45p' docs/signing/signpath-application.md` |
| The FOG server's CA is per-installation, pinned in the agent's state dir, and the Agent CA is an intermediate under it | `sed -n '20,32p' docs/design/0002-trust-model.md` |
| The agent already generates and handles ECDSA P-256 keys | `grep -n 'ecdsa' internal/enroll/state.go` |
| 0003 already makes the system-roots-plus-FOG-CA exception for the choco bootstrap fetch | `grep -n 'system roots' docs/design/0003-software.md` |

**PROVEN ON A RIG** — `background_scripts/prove_self_update.sh`, run 2026-09-06 on
10.255.20.1 against a real signed manifest, a real HTTP mirror and a real systemd
service. Fourteen assertions, all passing. These were inference when this document
was written; they are now observed behavior of the shipped code:

| Claim | What the run showed |
|---|---|
| A running binary can be renamed and replaced under itself on Unix, and the process keeps executing the old inode | `update: applied (0.2.0 -> 0.3.0, restarting)`; the service kept running until restarted, then reported 0.3.0 |
| Exiting non-zero is a working restart signal to a service manager | `Restart=always` + exit 1; `Started fog-agent-lab.service` on the new binary |
| A tampered manifest is refused before anything is downloaded | `signature_invalid: the manifest is not signed by a key this build trusts`, binary byte-identical to 0.2.0 |
| A correctly signed manifest whose mirror serves different bytes is refused on the artifact hash | `hash_mismatch: the bytes served are not the bytes the manifest describes` — and NOT on the signature, which the test asserts separately |
| The sequence floor survives a restart and refuses a replayed manifest | `stale_manifest: sequence 1, already accepted 2` |
| Probation is armed before the swap, and the record survives the restart | `{"from":"0.2.0","to":"0.3.0","sequence":2,"deadline":...}` read back after the service came up |
| **A deadline that passes with no successful poll reverts the fleet member unattended** | The running service noticed on its own loop 300s later, restored 0.2.0, exited, and came back up on it. Nobody intervened |

Two of those checks passed for the wrong reason on the first run, which is worth
recording because both are the shape where a security test goes green while testing
nothing. Building the wrong-bytes case by editing a signed manifest breaks the
signature, so the refusal came one step earlier and the hash was never reached; and
asking a 0.3.0 agent to become 0.3.0 short-circuits on the version comparison before
the manifest is ever fetched, so the floor was never consulted. Both now assert
*which* check refused, not merely that something did.

**PROVEN AGAINST THE REAL SERVER** — `background_scripts/prove_self_update_server.sh`,
run 2026-09-06 once schema step 434 reached the lab database. A real agent enrolled
against the lab FOG server over mTLS, was approved, and was then driven entirely by
the server:

| Claim | What the run showed |
|---|---|
| The server naming a version is enough on its own to move a host | `hostAgentDesiredVersion=0.3.0` set on host 237; the agent updated 290s later with no other input |
| The `update` capability and block reach the agent through the existing poll | no new route; `State::desired()` emitted it beside every other capability |
| **Probation clears on a successful authenticated poll** | `update: 0.3.0 polled successfully; 0.2.0 -> 0.3.0 is now the installed version`, 10s after the restart |
| A good update is therefore NOT reverted when its deadline passes | the record was gone before the deadline; the following poll reported `unchanged (already 0.3.0)` |
| The new version is visible to the server, so a staged rollout is observable | `hostAgentVersion` became `0.3.0` on the host row |

That closes the last gap. Every claim in this document that could be tested has now
been tested against real signed artifacts, a real service manager and a real server.

**INFERRED** — reasoning, not a read:

- A leaf certificate plus chain in a JSON envelope, verified with `x509.CertPool` / `Certificate.Verify` / `CheckSignature` against a compiled-in root, needs no dependency beyond the standard library. Each of those APIs is stdlib and I am confident of the shape; I have not written it.
- A running exe on Windows can be renamed but not overwritten, so rename-then-place works. Standard Windows behavior; not tested here.
- Windows service failure actions can run an arbitrary program on the Nth failure, and can be set from `service install`. Documented behavior; `service install` does not set them today, which **is** verified.
- A snapin's child process survives the agent being killed (they run in their own process group, which the timeout path kills deliberately). The consequence — a task row stranded "in progress" — follows but has not been observed.
- Go's `encoding/json` discarding unknown fields means a 0.1.1 agent ignores an `update` block harmlessly. The decoder behavior is certain; that nothing else in the poll path chokes is inference.

**UNKNOWN** — needs a look before this is built:

- Whether the existing `CN=FOG Project CA` key is available to the project at all, or whether the signing CA is a new one (3.2). Almost certainly the latter, and it changes nothing.
- What Defender actually does to a self-replaced unsigned binary already installed and running on `telliottwin11` (4.2). Measurable this week.
- Whether FOG 1.6 has an admin-notification surface for a security-relevant event (section 14).
- Whether `msiexec /f` repair of a self-updated install cleanly restores the packaged exe, and what it does to a running service.
- Whether the storage-node/FTP assumptions anywhere in the poll path care about an artifact that never came from a node. Probably not, since section 5 keeps artifacts off the server entirely.
- Whether ARM Windows is a target at all. It changes the artifact matrix and nothing else.
- Whether FOG 1.6's mass edit path writes one ADR 0021 header with `affectedCount` for a bulk change, or one per row (section 13).

## 17. The claim that would hurt most if false

**"A self-signed project root compiled into the agent is a full-strength
anchor for update verification."**

Everything in section 3 rests on it, and it is what makes the whole feature
independent of SignPath. The reasoning is that public trust exists so a
verifier you do not control will accept a signature, and here the verifier
*is* the agent — so the compiled-in root is exactly as trustworthy as the
build that contains it. I believe that is straightforwardly correct, and
the failure mode if it is not is not cryptographic but procedural: if
anything downstream (a distro, an enterprise policy, an auditor) demands
that a binary's provenance chain to a public root before it may be
installed, then the manifest chain does not satisfy it and Windows becomes
gated on SignPath after all. That is a policy question, not a maths one,
and it does not apply to Linux at all.

Runner-up, and the one I would actually watch while building: **this design
has the agent writing into its own state directory during the swap**
(`config.json.prev`, the probation record) — a new writer on the one file
that must survive. A bug there converts "the new build is bad" into "the
fleet needs re-approving by hand", which is the worst outcome in this
document. The rollback paths should be the most tested code in the slice,
and the probation record is a candidate for its own file rather than a
field in `config.json` for exactly that reason.

Third: **root key custody and root expiry.** Lose or expire the root and
every deployed agent stops accepting updates with no path back except a
re-install. Cheap to mitigate (long life, a second root compiled in, backed
up the way any CA key is), and expensive to fix afterwards, so it should be
done when the CA is minted rather than when it bites.
