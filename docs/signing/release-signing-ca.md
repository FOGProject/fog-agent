# The release signing CA

This is the procedure for minting the key that makes agent self-update
possible, and it is written to be run **by a maintainer, on a machine, by
hand** — not by CI. Everything CI needs is derived from it.

Design 0015 §3 and §4.1 are the reasoning; this is the operation.

## What this is, and what it is not

There are **two** signatures on a FOG Agent release, they sign different
things for different audiences, and neither replaces the other.

| | Authenticode (SignPath) | Release manifest (this document) |
|---|---|---|
| Signs | the `.exe` and `.msi` | `manifest.json` |
| Verified by | Windows — SmartScreen, Defender | the agent itself |
| Trust anchor | a public CA, shipped in Windows | `internal/release/roots.pem`, compiled into the agent |
| Needed for | installing without a scary warning | self-update working at all |
| Status | pending SignPath Foundation approval | this procedure |

**The manifest signature stays hand-rolled permanently.** It is not a
placeholder for SignPath. The reason is the trust anchor: the agent accepts
a manifest signed under the root compiled into it, and that root is private
to this project. Sign the manifest with a publicly-trusted code-signing
certificate instead and you would have to embed that CA's public root —
after which *anyone holding a code-signing certificate from that CA* could
author a manifest every FOG agent in the world accepts. A self-signed
project root is not a weaker version of a public one here; it is a stronger
one, because the agent is the verifier and the set of acceptable signers is
exactly one.

So when SignPath is approved, nothing in this document changes. The release
gains a second, unrelated signature over the Windows artifacts, and the
manifest step keeps running afterward — which is the only ordering
constraint between them, because Authenticode rewrites those files and
therefore changes the hashes the manifest records.

## Custody

- **The root key never leaves the machine you generate it on**, is never
  copied to CI, and is backed up offline. Its only job is to issue leaves.
  Losing it means every deployed agent must be replaced by hand, because the
  root is compiled in; leaking it means someone else can author releases
  your fleet installs.
- **The leaf key does live in Actions.** It is short-lived (90 days) and
  reissuable under the same root without touching a single deployed agent,
  which is the whole reason the CA is two levels deep. A leaked leaf costs a
  reissue and a rotation of one secret.

## Procedure

Run on a machine you trust, in a checkout of this repository.

    # 1. Mint the CA. Writes root.key/root.crt and leaf.key/leaf.crt into
    #    ~/.fog-agent-signing (override with --dir).
    build/mint-signing-ca.sh

    # 2. Compile the root into the agent. This rewrites a TRACKED file,
    #    internal/release/roots.pem, which is empty on purpose until now.
    build/mint-signing-ca.sh --install-only

    # 3. Check what you are about to commit is the root and not a lab CA.
    #    Compare this against the fingerprint step 1 printed.
    openssl x509 -in internal/release/roots.pem -noout -fingerprint -sha256
    grep -c 'BEGIN CERTIFICATE' internal/release/roots.pem   # must be 1

    # 4. Commit it. The certificate is public; the key is not and must
    #    never appear in a commit, a paste, or a terminal you screenshot.
    git add internal/release/roots.pem
    git commit -m 'Release: compile in the FOG Agent release signing root'

Then set these two secrets in **`FOGProject/fog-version-check`** — not in
this repository, because that is where the manifest is signed and published:

| Secret | Value |
|---|---|
| `FOG_AGENT_SIGNING_LEAF_KEY` | contents of `~/.fog-agent-signing/leaf.key` |
| `FOG_AGENT_SIGNING_LEAF_CRT` | contents of `~/.fog-agent-signing/leaf.crt` |

Back up `root.key` offline, and **passphrase-encrypt it before the backup
copy leaves the machine**:

    openssl pkey -in ~/.fog-agent-signing/root.key -aes256 -out root.key.enc

`mint-signing-ca.sh` writes it as a plaintext PEM, which is right while it
sits on one trusted disk and wrong the moment a copy is on a VPC or a cloud
drive. There is no revocation path for this key: its certificate is
compiled into every agent, so a leak is fixed by replacing every deployed
agent by hand. Keep the passphrase somewhere other than wherever the backup
lands, and record it before you encrypt — losing it means no leaf can ever
be issued again, which is the same disaster from the other direction.

Delete nothing else: `leaf.key` is needed again at every reissue.

## Publishing the manifest

Signing and publishing both happen in
[`FOGProject/fog-version-check`][vc], the repository behind
`fogproject.org/version/`, which is cloned on the host at
`/var/www/html/website/version`. After a fog-agent release finishes, run its
**Publish the agent release manifest** workflow from the Actions tab
(optionally naming a tag; empty means the latest release), then deploy that
repo the way it is normally deployed:

    cd /var/www/html/website/version && git pull && systemctl reload php-fpm

That workflow downloads the release's artifacts, hashes them, merges the
versions already published, signs, and commits `agent-stable.json` and
`agent-stable.json.sig` together.

[vc]: https://github.com/FOGProject/fog-version-check

**Why not in this repository's release workflow.** It was there first, and
moving it removed a constraint rather than adding one. A manifest built
during the release is built *before* publication, so getting it right means
remembering that Authenticode rewrites the Windows artifacts and that the
manifest step must therefore run after signing — a rule someone has to keep
true forever. Built after the release exists, it hashes the files people
actually download and the ordering cannot be got wrong. It also means the
signing key lives in exactly one repository.

**Serve it as a static file.** The signature is over the manifest's exact
bytes, so it must not pass through anything that re-encodes JSON. Sitting
next to `index.php` as a plain file is exactly right; being generated *by*
`index.php` would not be.

**Both files move together.** The agent fetches the manifest and then
fetches `<url>.sig` as a second request, so anything that updates one
without the other leaves a window in which every polling agent reads a
mismatched pair and reports `signature_invalid`.

`agent-stable.json` names its channel because that URL is **compiled into
every agent** and cannot be changed for one already deployed. The
`/version/` service already answers for three channels (stable, dev-branch,
beta), so a second manifest is a sibling file rather than a rename that
strands the fleet.

## Versions accumulate

The workflow fetches the currently-published manifest and merges the new
release into it, so one file offers every release ever published. That is
not tidiness: `Manifest.Find()` looks a version up by exact key, so a
manifest describing only the newest release means a server naming anything
older answers `no_artifact` — and naming an older version is the *only*
recovery from a build that installs, starts and polls perfectly well and
then behaves badly (§9, §11). Local rollback cannot catch that one, because
by every local measure the agent is healthy.

Growth is about 1.8 KB per release. To withdraw a version so nobody can be
sent to it, edit `agent-stable.json` to remove that key before the next
release merges it forward — deliberately, by a person.

## Reissuing the leaf

Every 90 days, or immediately if the leaf key is ever exposed. It does not
touch a deployed agent and needs no release.

    build/mint-signing-ca.sh --leaf-only

Then update the two secrets. Old manifests keep verifying — they carry the
leaf that signed them, and it chains to the same root.

## After the first release with a manifest

Self-update is available from that release forward and **not before**: an
agent can only be moved by a version of itself that already contains the
update machinery and a root to verify against. Agents built before this
report `no_signing_root` and have to be replaced the way they were
installed.

The manifest must be published (above) before any of this works: without
it the agent fetches the compiled-in URL, gets nothing, and reports
`fetch_failed`.

Set `FOG_AGENT_DESIRED_VERSION` on the FOG server to start using it, or the
per-host **Desired Agent Version** to stage a rollout at a smaller blast
radius. Watch the **Agent Update State** column and the dashboard's Agent
Versions card; `refused` means the version or the manifest is wrong for
everyone, `cannot` means that particular machine or its path to the mirror.

## The traps, collected

- **The signature is over `manifest.json`'s exact bytes.** Reformatting it —
  `jq`, an editor, a CI step that pretty-prints — invalidates it, and the
  agent reports `signature_invalid`, which names the wrong thing.
- **`sequence` must never go backwards.** The agent refuses any manifest
  below the highest it has accepted, and there is no way to lower that floor
  remotely. The release workflow uses seconds since the epoch for this
  reason: a counter tied to a workflow resets if the workflow is renamed,
  and a reset is indistinguishable from a replay.
- **The manifest is built after signing, never before.** Authenticode
  rewrites the Windows artifacts and changes their hashes.
- **The MSI is not a manifest artifact.** A manifest artifact is the file
  the agent renames over its own binary; an installer is not that. It is
  still published as a release asset, because it is how a machine gets the
  agent in the first place.
- **Never commit a lab root.** `--install` rewrites a tracked file, and a
  lab root in `roots.pem` ships a build that trusts a throwaway key.
