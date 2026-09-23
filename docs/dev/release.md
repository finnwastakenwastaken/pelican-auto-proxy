# Cutting a release

## Before tagging

1. `scripts/infra-sweep.sh` clean on the tree.
2. `CHANGELOG.md` has an entry for the version being cut (move it out of `[Unreleased]`).
3. `plugin/autoproxy/plugin.json`'s `version` field equals the tag you're about to push, exactly (the release
   workflow checks this and fails the build if it doesn't match — a mismatch here is how a hub listing ends up
   pointing at the wrong version).
4. Every doc page that mentions a version number is consistent with it (mainly `CHANGELOG.md`; most pages
   deliberately don't hardcode a version and just say "the latest release").
5. `docs/img/setup-step1.png` and `docs/img/setup-step2.png` exist (`scripts/release-checklist.sh` checks for
   exactly these two), reflect the version being released rather than an older UI, and the screenshot placeholders
   in `README.md` and `plugin/README.md` have been replaced with them. Until then those pages say "added at first
   release" rather than linking an image that is not there.
6. `scripts/release-checklist.sh` passes — it re-checks everything above that can be checked automatically
   (including that the artefact names agree across the workflow, this page and both installers, and that the
   installers still verify a fixture `SHA256SUMS` built the way the workflow builds it) and fails loudly on
   anything it can't confirm.

## Tag

```bash
git tag v1.2.3
git push origin v1.2.3
```

Tags matching `v*` trigger `.github/workflows/release.yml`. Nothing is published from a branch push or a manual
run without a tag.

## What the workflow builds

| Asset | From |
|---|---|
| `autoproxy-agent_linux_amd64` | `agent/`, `CGO_ENABLED=0`, `-trimpath`, version baked in via `-ldflags`. |
| `autoproxy-client.tar.gz` | `scripts/package-client.sh dist/autoproxy-client.tar.gz <version>`: `client/autoproxy-client` with the tag's version stamped into its `AUTOPROXY_VERSION` line, plus `client/autoproxy-client.service`. |
| `autoproxy-<version>.zip` | `scripts/make-plugin-zip.sh` over `plugin/autoproxy/`, `plugin.json` at the zip root; the workflow verifies its `version` field matches the tag before packaging. |
| `install-vps.sh`, `install-client.sh`, `setup-node.sh` | `installers/`, copied as-is. |
| `update.json` | Generated from `plugin.json`'s version and the tag; this is what the panel's plugin updater reads. |
| `SHA256SUMS` | Checksums of every asset above, so both installers can verify what they downloaded before running it. |

The workflow calls `scripts/package-client.sh` and `scripts/make-plugin-zip.sh` rather than re-implementing them,
so a release cannot produce a different layout than the one a developer tested locally before tagging.

The infra sweep runs again against the built zip specifically (not just the source tree) before the release is
created — a value that never appears in a source file but ends up baked into a generated asset would otherwise slip
through.

### `autoproxy-client.tar.gz` layout

Dictated by `installers/install-client.sh`, which extracts into a scratch directory and then installs
`<scratch>/autoproxy-client` and `<scratch>/autoproxy-client.service`:

- exactly two members, both at the archive **root**, with no directory prefix;
- the script's `readonly AUTOPROXY_VERSION="dev"` line replaced by the release number (the source tree always says
  `dev`; the packaging script refuses to build when that line is missing or duplicated, and reads the stamp back out of the
  finished archive). This is what `autoproxy-client status` shows, what the client reports to the agent, and what
  `autoproxy-client update` compares before installing anything. Without a version argument it stamps
  `<plugin.json version>-dev`, so a local build never passes for a release;
- `autoproxy-client`, mode `0755`;
- `autoproxy-client.service`, mode `0644`.

`scripts/package-client.sh` asserts all of this on the archive it just wrote, and builds reproducibly (sorted
entries, numeric owner 0/0, fixed mtimes), so rebuilding the same sources gives the same SHA256 as the published
asset.

### `SHA256SUMS` format

Plain coreutils output and nothing else, one line per asset:

```
<64 hex characters><two spaces><bare filename>
```

The filename must be bare. Both installers download `SHA256SUMS` next to the asset into a scratch directory and
pipe the matching line to `sha256sum -c`, which resolves the filename relative to that directory — a `dist/` prefix
turns a perfectly good download into a failed verification. The workflow refuses to continue if any line does not
match `^[0-9a-f]{64}  [^/]+$`, and `scripts/test-checksum-parse.sh` builds a fixture the same way the workflow does
and runs both installers' `verify_against_sha256sums()` against it, for an untouched artefact and a corrupted one.

### `update.json`

`plugin/autoproxy/plugin.json` sets `update_url` to this repository's
`releases/latest/download/update.json`, so the panel fetches the manifest attached to the most recent release.
The panel's `app/Models/Plugin.php` reads it like this:

- `getUpdateData()` fetches the URL and JSON-decodes it. If the top-level object has a key equal to the plugin id
  (`autoproxy`), it descends into that key first — that is the "one manifest, several plugins" form. We do not use
  it, so the top level must **not** have an `autoproxy` key.
- `isUpdateAvailable()` and `getDownloadUrlForUpdate()` look up the panel's own version string as a key
  (e.g. `1.0.0-beta38`), fall back to `"*"`, and then read `['version']` and `['download_url']` from that entry.
  Both keys are required; a missing one is a PHP error, not a graceful skip. Any other key is ignored.

So the published file is:

```json
{
  "*": {
    "version": "0.2.0",
    "download_url": "https://github.com/finnwastakenwastaken/pelican-auto-proxy/releases/download/v0.2.0/autoproxy-0.2.0.zip"
  }
}
```

`version` is the tag without its leading `v` (it is compared with `version_compare()` against the installed
`plugin.json` version, so `v0.2.0` would break the comparison). `download_url` is pinned to the tag, never to
`/latest/`, so a panel is offered exactly the version this manifest names. Per-panel-version keys can be added
alongside `"*"` later if a release ever needs to serve different zips to different panel versions.

The GitHub release itself is created with all of the above attached, using the tag's own name and the matching
`CHANGELOG.md` section as the release notes.

## Dry run before the first tag

The release workflow can be started by hand from the Actions tab ("Release", "Run workflow") on any branch. That run
derives the version from `plugin.json` instead of a tag, builds and checks every asset exactly as a tag push would
(binary, client tarball, plugin zip, installers, `update.json`, `SHA256SUMS`, the sweep on the zip) and uploads them
as a workflow artifact instead of creating a release. Two checks that would fail a real tag only warn on a dry run: a
repository whose name differs from the one `plugin.json` publishes to, and a missing CHANGELOG section for the
version. What a dry run does not exercise is the release creation itself; the first real tag is the first time that
step runs.

## Hub submission checklist

Prepared as part of the release, submitted separately (this needs the maintainer's own account, not something a
release workflow can do on its own):

- [ ] Plugin name, id (`autoproxy`) and description match `plugin.json`.
- [ ] Screenshots reflect the version being submitted.
- [ ] The zip URL points at this release's asset, not `latest` (a hub listing should be pinned, so a later
      in-progress release doesn't change what an existing listing serves).
- [ ] `update_url` in `plugin.json` is reachable and returns the version just released.
- [ ] License and repository links are correct and public (a hub submission implies the repository itself is
      public — confirm this has actually happened, not just that the release exists in a private repo).

## Before a public sync

Before pushing anything from this tree to a public repository, produce the export and sweep the export itself, not
the working tree:

```
scripts/export-public.sh /tmp/pap-export
scripts/infra-sweep.sh --authors /tmp/pap-export
```

`scripts/export-public.sh` writes a `git archive` of `HEAD` into the target directory, drops the paths that never go
public (`legacy/`, `dist/`, `client/*.tar.gz*`, `docs/dev/handover.md`), and runs the sweep with both denylists
against what is left. It exits non-zero on any hit.

The `--authors` run is the second half: it checks `git log` author and committer metadata, which must contain only
`users.noreply.github.com` addresses and the name `Arrowtje`. A personal email address in a commit header is just as
public as one in a file, and rewriting history after a push is not a fix. Run this on the repository that is about
to be pushed, once its history exists.

`scripts/publish-sync.sh <public-checkout> [tree-ish]` is the routine path once the public repository exists: it
exports, replaces the checkout's tracked tree with the export (nothing outside `.git` survives), commits under the
public identity if anything changed, and runs the sweep with `--authors` on the checkout. It never pushes or tags;
it prints both commands. The public history is kept from 0.2.0 onward, so a release is one sync commit plus a tag.

`scripts/publish-init.sh <target-dir>` was the first publish and chains the two: it exports, initialises a fresh repository in the target with
a single commit under the public identity (`Arrowtje <…@users.noreply.github.com>`), sets the public remote, and
runs the sweep with `--authors` on the result. It never pushes; the push is a separate, deliberate command printed
at the end. The development repository itself is never pushed publicly, because its history carries personal
identities that a rewrite after the fact cannot un-publish.

Both checks read `scripts/infra-sweep.denylist.local` when it is present. That file is gitignored, so CI cannot see
it — a clean CI sweep only proves the shape patterns passed. The sweep prints which lists it used; if the local list
is missing from that output, the run did not check private values at all.

## Infra sweep, one more time

Run `scripts/infra-sweep.sh` against the checked-out tag (not just against whatever the working tree currently
looks like) as a final check before anything is made public. A clean sweep on an untagged commit does not
guarantee the tagged commit is also clean if anything was committed out of order.
