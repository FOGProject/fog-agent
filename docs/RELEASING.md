# Releasing the FOG Agent

Cutting a release is **pushing a tag**, then dispatching one workflow, then a
`git pull` on the website host. Three steps, two repositories, no keys on your
machine.

The signing root in `~/.fog-agent-signing` is **not** part of this. It is
touched once a year, by the separate procedure in
[`signing/release-signing-ca.md`](signing/release-signing-ca.md). If you find
yourself reaching for `root.key` to ship a version, stop -- you are in the
wrong procedure.

## What a release consists of

| Artifact | Made by | Lives at |
|---|---|---|
| Cross-compiled binaries and the MSI | `.github/workflows/release.yml`, on the tag push | the GitHub release |
| `agent-stable.json` + `.sig` | `publish-agent-manifest.yml` in `fog-version-check`, dispatched by hand | that repo's root |
| The served manifest | a `*/5` cron on the fogproject.org host, automatically | `/var/www/html/website/version` |

An agent updates itself only once the third row has happened. A GitHub release
on its own moves nothing in the field.

## 1. Tag it

The version is not stored in a file. It is stamped from the tag at build time
(`build/cross.sh` -> `-ldflags -X main.Version=`), so the tag IS the version
and there is nothing to bump first.

    cd /home/telliott/fog-agent
    git switch main && git pull
    go test ./... -count=1          # release.yml runs this too and fails on it
    git tag v0.1.7
    git push origin v0.1.7

`release.yml` triggers on `v*` and does the rest: tests, cross-compiles every
target, builds the MSI, submits the Windows artifacts for signing if SignPath
is configured, regenerates checksums over whatever it actually shipped, and
publishes the GitHub release with `dist/*` attached.

**Watch it finish.** Everything below assumes the release exists with its
artifacts attached.

    gh run watch --repo FOGProject/fog-agent
    gh release view v0.1.7 --repo FOGProject/fog-agent

## 2. Publish the manifest

In **`FOGProject/fog-version-check`** -- not this repository, because that is
where the signing leaf lives -- run the **Publish the agent release manifest**
workflow from the Actions tab. Leave the tag input empty for the latest
release, or name one to republish.

    gh workflow run 'Publish the agent release manifest' \
      --repo FOGProject/fog-version-check
    # or for a specific tag:
    #   -f tag=v0.1.7

It downloads that release's artifacts, hashes them, fetches
`build/sign-manifest.sh` **from that exact tag** (so the manifest is written in
the format that release's agent reads), merges the versions already published,
signs with the leaf, and commits `agent-stable.json` and `agent-stable.json.sig`
together.

Both files must move together: the agent fetches the manifest and its `.sig` as
two requests, so a half-updated pair makes every polling agent report
`signature_invalid`.

## 3. Wait five minutes, then verify it is live

**This step is automatic.** A `*/5` cron on the fogproject.org host runs
`/usr/local/sbin/fog-version-pull.sh`, which pulls the repo and reloads php-fpm
only when the pull actually moved something. Worst-case publish latency is one
cron interval. Nobody has to log in.

`/version/` is served by nginx as a static file with no PHP in the path -- which
it must be, because the signature is over the manifest's exact bytes and
anything that re-encodes the JSON breaks it.

Verify from outside. The pull log is quiet on success, so the log alone never
proves the cron ran:

    curl -fsS https://fogproject.org/version/agent-stable.json \
      | python3 -c 'import json,sys; d=json.load(sys.stdin); print(sorted(d["versions"]), d["signed"])'
    curl -fsSI https://fogproject.org/version/agent-stable.json.sig | head -1

The new version must appear in that list. If it has not after ten minutes, the
cron is the thing to check, and the manual equivalent is:

    cd /var/www/html/website/version && git pull && systemctl reload php-fpm

`/var/www/html/website` is the document root, so a clone at
`/var/www/html/version` is **not** reachable at `fogproject.org/version/`.

## Withdrawing a version

Versions accumulate on purpose -- `Manifest.Find()` looks a version up by exact
key, so a manifest holding only the newest release means a server naming an
older one gets `no_artifact`. Naming an older version is the only recovery from
a build that installs and polls perfectly well and then misbehaves, which local
rollback cannot catch.

To make a version unreachable, edit `agent-stable.json` in `fog-version-check`
to delete that key before the next release merges it forward, commit both files,
and deploy. By hand, deliberately.

## Once a year: the leaf

Not part of a release. See
[`signing/release-signing-ca.md`](signing/release-signing-ca.md#reissuing-the-leaf).

    build/mint-signing-ca.sh --leaf-only

Then update `FOG_AGENT_SIGNING_LEAF_KEY` and `FOG_AGENT_SIGNING_LEAF_CRT` in
`fog-version-check`. No rebuild, no release, no deployed agent affected. Old
manifests keep verifying, because each carries the leaf that signed it and the
agent judges that certificate against the manifest's own `signed` time.

**This step prompts for the root key's passphrase** -- `--leaf-only` signs with
`-CAkey root.key`, and that key is passphrase-protected.
