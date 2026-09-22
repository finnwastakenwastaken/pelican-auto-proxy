#!/usr/bin/env bash
# Pre-release checklist for Pelican Auto Proxy.
#
#   scripts/release-checklist.sh [version]
#
# Prints every item that must be true before tagging a release. Items this script can check itself are checked and
# the script fails (non-zero exit) if any of them is false. Items it cannot check reliably (a human still has to
# look) are printed as a checklist instead, and do not affect the exit code.
#
# [version] defaults to the version in plugin/autoproxy/plugin.json, so running this with no argument checks
# "would today's tree be ready to tag as its own plugin.json version".
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

log() { printf '>> %s\n' "$*"; }
ok() { printf '  [ok] %s\n' "$*"; }
bad() { printf '  [FAIL] %s\n' "$*" >&2; }
manual() { printf '  [ ] %s\n' "$*"; }

FAIL=0

plugin_json="plugin/autoproxy/plugin.json"
if [ ! -f "$plugin_json" ]; then
	bad "$plugin_json not found"
	exit 1
fi
plugin_version="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["version"])' "$plugin_json")"
version="${1:-$plugin_version}"

log "Checking release readiness for version $version"
echo

log "Automated checks"

if scripts/infra-sweep.sh >/tmp/release-checklist-sweep.log 2>&1; then
	ok "infra sweep is clean"
else
	bad "infra sweep failed:"
	sed 's/^/         /' /tmp/release-checklist-sweep.log >&2
	FAIL=1
fi

if grep -qE "^## \[$version\]" CHANGELOG.md; then
	ok "CHANGELOG.md has an entry for $version"
else
	bad "CHANGELOG.md has no '## [$version]' section — move it out of [Unreleased] first"
	FAIL=1
fi

if [ "$plugin_version" = "$version" ]; then
	ok "plugin.json version ($plugin_version) matches $version"
else
	bad "plugin.json version ($plugin_version) does not match $version"
	FAIL=1
fi

# The panel's plugin updater fetches this URL and reads {"<panel version>"|"*": {version, download_url}}
# out of it, so a wrong URL here is an Update button that never appears.
update_url="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("update_url",""))' "$plugin_json")"
expected_update_url="https://github.com/finnwastakenwastaken/pelican-auto-proxy/releases/latest/download/update.json"
if [ "$update_url" = "$expected_update_url" ]; then
	ok "plugin.json update_url points at this repository's latest release"
else
	bad "plugin.json update_url is '$update_url'; expected '$expected_update_url'"
	FAIL=1
fi

# Artefact names drift between the workflow, the docs and the installers, and the
# first symptom is a 404 in somebody's terminal. Check them against each other.
release_wf=".github/workflows/release.yml"
release_doc="docs/dev/release.md"
for asset in autoproxy-agent_linux_amd64 autoproxy-client.tar.gz install-vps.sh install-client.sh setup-node.sh update.json SHA256SUMS; do
	for f in "$release_wf" "$release_doc"; do
		if grep -qF -- "$asset" "$f"; then
			ok "$f names $asset"
		else
			bad "$f never mentions the release asset $asset"
			FAIL=1
		fi
	done
done
# shellcheck disable=SC2016  # a GitHub Actions expression, matched literally with grep -F
if grep -qF -- 'dist/autoproxy-${{ steps.version.outputs.version }}.zip' "$release_wf"; then
	ok "$release_wf publishes autoproxy-<version>.zip"
else
	bad "$release_wf does not publish dist/autoproxy-\${{ steps.version.outputs.version }}.zip"
	FAIL=1
fi
if grep -qF -- 'autoproxy-<version>.zip' "$release_doc"; then
	ok "$release_doc names autoproxy-<version>.zip"
else
	bad "$release_doc does not name autoproxy-<version>.zip"
	FAIL=1
fi
if grep -qF -- "autoproxy-agent_linux_amd64" installers/install-vps.sh; then
	ok "installers/install-vps.sh downloads autoproxy-agent_linux_amd64"
else
	bad "installers/install-vps.sh no longer names autoproxy-agent_linux_amd64"
	FAIL=1
fi
if grep -qF -- "autoproxy-client.tar.gz" installers/install-client.sh; then
	ok "installers/install-client.sh downloads autoproxy-client.tar.gz"
else
	bad "installers/install-client.sh no longer names autoproxy-client.tar.gz"
	FAIL=1
fi

# Prove the installers can still verify what the workflow writes, good case and bad.
if scripts/test-checksum-parse.sh >/tmp/release-checklist-checksums.log 2>&1; then
	ok "installers verify a fixture SHA256SUMS built the way the workflow builds it, and refuse a corrupted one"
else
	bad "scripts/test-checksum-parse.sh failed:"
	sed 's/^/         /' /tmp/release-checklist-checksums.log >&2
	FAIL=1
fi

screenshots=(docs/img/setup-step1.png docs/img/setup-step2.png)
missing=0
for f in "${screenshots[@]}"; do
	if [ -f "$f" ]; then
		ok "$f present"
	else
		bad "$f is missing"
		missing=1
	fi
done
[ "$missing" -eq 1 ] && FAIL=1

echo
log "Manual checklist (confirm by hand, then re-run with everything above green)"
manual "Every doc page that names a version number matches $version (mainly CHANGELOG.md; most pages intentionally say \"the latest release\" instead of a hard-coded version)."
manual "Screenshots above reflect the UI in this version, not an older one."
manual "The 5-minute run (docs/dev/testing.md) has been done on a freshly re-imaged VM for this version, not assumed unchanged from the last one."
manual "The hub submission checklist in docs/dev/release.md is ready if this release is meant to update the hub listing."

echo
if [ "$FAIL" -ne 0 ]; then
	echo "release-checklist: FAILED — fix the automated items above before tagging" >&2
	exit 1
fi
echo "release-checklist: automated checks passed — confirm the manual checklist before tagging"
