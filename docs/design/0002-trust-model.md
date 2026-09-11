# fog-agent: enrollment, authentication and trust

Status: enrollment, the certificate channel, renewal and token minting are
built (fogproject PR #1707, 2026-09-03); enrollment and the channel are proven
on the lab, renewal and tokens await a lab run. The eight capabilities named in
`supportedCapabilities` — hostname, taskreboot, power, software, printers,
directory, wake, snapin — are built and each has been exercised on the lab; see
the status line of the design that owns it. Windows service install ships in
the MSI (0005) and runs on `telliottwin11`.

## The one idea

An agent is a **key**, and the server's only question is **which host row does
this key belong to**. Everything below exists to answer that question once, in
front of an admin, and then answer it for free on every later request.

There are no shared secrets. The agent's private key is generated on the
machine and never leaves it; the server only ever sees the public half, first
in a certificate request and then in a certificate it issued. Compare
fog-client, where the server holds a per-host security token that both sides
must keep.

## Trust anchors

| Who trusts what | Anchor | Where it comes from |
|---|---|---|
| Agent trusts the server | settled once, at install, and kept in its state dir: a pinned CA bundle, or, for a web UI on a public or corporate certificate only, the system trust store and the server's name | a file handed over with `--ca`; the CA the server publishes, fetched and then confirmed by fingerprint; or the machine's own trust, checked by the probe (below) |
| Web server trusts agents | **FOG Agent CA**, a new intermediate under the FOG root, published with the root as `management/other/agent-ca-bundle.pem` | installer `createAgentIntermediateCA`, key root-only under `/etc/fog/pki/agent/ca/` |
| PHP trusts agents | the same bundle, re-verified in PHP for client-auth purpose, independently of the web server | `FOG\Agent\Principal::verify()` |
| Server trusts a host binding | `hostAgentFingerprint` on the host row: sha256 of the key's SubjectPublicKeyInfo | written at approval, checked on every request |

### Where the server anchor comes from

Three ways. With no file, the agent tries the second and then the third, so
almost nobody installing an agent needs a PEM file:

- **`--ca FILE`** (`CA=` to the MSI): a bundle carried to the machine by
  whoever is installing. It is for a CA this machine does not trust and the
  server does not publish, such as an offline corporate CA.
- **fetch, then confirm the fingerprint.** The agent reads
  `management/other/ca.cert.pem` from the server with TLS verification off
  — its whole purpose is to find out what the anchor would be, so there is
  nothing yet to verify against — and then refuses to use a byte of it
  until something from outside that connection agrees. That is either
  `--ca-fingerprint` (`CAFINGERPRINT=`), which a deployment script already
  knows, or a person comparing the SHA-256 the installer displays against
  the one the server's own web UI prints under FOG Configuration →
  Certificates. The probe also checks that the server's own certificate is
  actually issued by what it published. If it is not, the published CA is
  the wrong anchor, and the probe moves to the third way.
- **the machine's trust store, for a web UI on a public or corporate
  certificate.** The probe connects again with full verification against
  the system trust store: the chain, the clock and the server name. If that
  passes, the agent keeps no bundle and verifies every later connection the
  same way. Nobody confirms a fingerprint, because the public or corporate
  CA already checked who holds the name. The web UI could not show one
  anyway: its Certificates tab shows FOG's own CA.

The order matters. A FOG CA that is also in the machine's store, which the
legacy client put there, still goes through the fingerprint. The choice is
made once, at install. An agent that pinned a CA never falls back to the
system store when that CA stops verifying. A fingerprint given for a server
on a machine-trusted certificate is refused, not dropped, because whoever
wrote it expected a pinned CA.

Why not pin the public CA's root instead (decided 2026-09-11, after a report
from a server on Let's Encrypt). Pinning Let's Encrypt's root adds almost
nothing: anyone who can get a Let's Encrypt certificate for the name still
passes. It also breaks every agent at once when Let's Encrypt changes its
chain, and it is changing it: fogproject.org already serves a certificate
from `YR2`, one of the new intermediates. The system store gets root changes
from the operating system.

The distinction that matters: **fetching is not trusting.** The old
fog-client downloaded `ca.cert.der` and trusted it, which is trust on first
use — anyone on the path at install time picks the anchor. Here the bytes
arrive over an unverified connection and the *decision* arrives over a
different channel: an admin's eyes, or a fingerprint pinned in a script. The
agent stores the bundle only once the two agree.

Under the MSI the confirmation happens unelevated, in the wizard, and only
the fingerprint crosses into the elevated half; `setup` fetches the
certificate again as SYSTEM and checks it against that fingerprint itself,
so a wizard that was lied to cannot hand a certificate to the install. For a
machine-trusted certificate nothing crosses: `setup` runs the probe again as
SYSTEM and settles on what it finds.

The Agent CA issues **client certificates only** (extended key usage clientAuth,
CA:FALSE, one year). Nothing it signs can pose as a server, so a compromise of
that key is contained to minting agent identities, and every one of those still
has to match a host row's fingerprint to be accepted.

## Identity

Machine identity is SMBIOS: system UUID, system and board serials, chassis
asset tag, plus the MAC list. The agent generates an ECDSA P-256 key **per
identity** and regenerates it if the identity changes underneath it (a
motherboard swap is a new machine). The fingerprint of that key is the same
value whether computed from the CSR or from the issued certificate, which is
what lets one number bind CSR, certificate and host row together.

## Enrollment: `POST /agent/v1/enroll`, unauthenticated

The agent sends protocol version, its version, OS and arch, hostname, the
identity block, a CSR, and optionally a token. The server:

- fingerprints the CSR and looks for an enrollment row by that key **first**;
  a key it has seen before is answered from that row without re-deciding;
- otherwise matches the identity against existing hosts (UUID, serials, MACs);
- answers `issued` (certificate in the body, handed over once), `pending`
  (with a reason and a retry interval), `denied`, or 426 if the protocol
  version is unsupported.

Automatic approval happens in exactly two cases: a valid **token** an admin
minted (Hosts > Agent Tokens, or `POST /agent/token`: shown once, only its
hash stored, expiry required, single or counted or unlimited uses until
expiry, revocable), or an **active deploy task** for the matched host inside
`FOG_AGENT_ENROLL_DEPLOY_WINDOW` (the image just put it there, so the server
already vouches for it). Everything else pends for an admin, with the reason
visible on the Pending Agents page:

| Reason | Meaning |
|---|---|
| `unknown-host` | nothing matched; a pending host row is created so approval also registers it |
| `known-host-no-agent` | the host exists and has never had an agent |
| `rebind` | the host already has a different key bound; someone is claiming an enrolled machine |
| `identity-conflict` | the identity matches more than one host |
| `reissue` | same key, certificate already collected once; the agent lost its cert but kept its key |
| `no-mac` | the request carried no usable MAC |

Approval signs the stored CSR through the same root-only helper the installer
uses for node certificates (`fog-sign-node-cert agent`): the web user stages the
request and a host id, the helper validates both against fixed patterns, signs
with the Agent CA, and hands back a leaf whose subject is `fog-agent host N`
and nothing else. The fingerprint and expiry land on the host row; the agent
collects the certificate on its next enroll call. Denial pins the key as
refused so its repeats cost nothing.

## Authenticated channel: everything else under `/agent/v1/`

Mutual TLS. The agent presents leaf plus intermediate; the vhost verifies with
`optional` client verification so the enroll route and the web UI are
untouched; PHP re-verifies the certificate against the bundle (a misconfigured
vhost cannot let a certificate through) and binds it to a host by fingerprint,
requiring the host to be non-pending. No principal means 401 with a JSON body
and the router never dispatches. The certificate carries **no names**: it
identifies a row, so renames, re-addressing and DHCP changes do not touch it.

Revocation needs no CRL. The binding lives in the database and is checked on
every request, so clearing `hostAgentFingerprint`, deleting the host, or
approving a different key for it all revoke the old certificate immediately.
The agent's side of that is a 401: it drops its certificate, re-enrolls with
the same key, and lands in front of an admin as `reissue` or `rebind`.

**Renewal** rides the same session: inside 120 days of expiry, after a
successful poll, the agent sends a request for its existing key to
`POST /agent/v1/renew` and stores the answer as it stored the first
certificate. Same key only; the binding an admin approved never changes
without an admin. The one-year life bounds what a copied key is worth.

## Planned, not built

- **Capabilities** in the poll answer, driving the providers in 0001.
- **Windows service** install.
- The Apache vhost path is written and untested; the lab is nginx.
