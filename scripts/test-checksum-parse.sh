#!/usr/bin/env bash
# Proves that what .github/workflows/release.yml writes into SHA256SUMS is what
# installers/install-vps.sh and installers/install-client.sh can actually verify.
#
#   scripts/test-checksum-parse.sh
#
# It builds a fixture release directory the same way the workflow does, then calls
# the installers' own verify_against_sha256sums() (loaded with
# AUTOPROXY_INSTALLER_SOURCE_ONLY=1, so no install runs) against it:
#
#   1. good case          -> must accept
#   2. corrupted artefact -> must refuse        (rule 6: watch the gate fail)
#   3. directory-prefixed SHA256SUMS -> must refuse, because `sha256sum -c` would
#      look for a path that does not exist next to the download
#   4. missing entry      -> must refuse
#
# No network, no root, nothing installed.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
version="$(python3 -c 'import json; print(json.load(open("'"$root"'/plugin/autoproxy/plugin.json"))["version"])')"

pass=0
fail=0
ok() {
	printf '  [ok] %s\n' "$*"
	pass=$((pass + 1))
}
bad() {
	printf '  [FAIL] %s\n' "$*" >&2
	fail=$((fail + 1))
}

# Run one installer's verification function against a fixture directory and echo
# its exit status. Sourced in a subshell so the installer's `set -e` and its
# helpers cannot leak into this script.
verify_with() {
	local installer="$1" asset="$2" dir="$3" rc=0
	(
		set +e
		# shellcheck disable=SC1090  # path is built above and checked by the caller
		AUTOPROXY_INSTALLER_SOURCE_ONLY=1 source "$installer" >/dev/null 2>&1
		verify_against_sha256sums "$asset" "$dir" >/dev/null 2>&1
		exit $?
	) || rc=$?
	echo "$rc"
}

expect() {
	local what="$1" got="$2" want="$3"
	if [ "$got" = "$want" ]; then
		ok "$what (exit $got)"
	else
		bad "$what: exit $got, expected $want"
	fi
}

fixture="$(mktemp -d)"
trap 'rm -rf "$fixture"' EXIT

echo ">> building a fixture release directory in $fixture"
printf 'not a real binary\n' >"$fixture/autoproxy-agent_linux_amd64"
printf 'not a real zip\n' >"$fixture/autoproxy-$version.zip"
printf '{"*":{"version":"%s","download_url":"x"}}\n' "$version" >"$fixture/update.json"
cp "$root/installers/install-vps.sh" "$root/installers/install-client.sh" "$fixture/"
"$root/scripts/package-client.sh" "$fixture/autoproxy-client.tar.gz" >/dev/null
rm -f "$fixture/autoproxy-client.tar.gz.sha256"

# Exactly the command the workflow runs, in the directory the assets live in.
(
	cd "$fixture" && sha256sum -- \
		autoproxy-agent_linux_amd64 \
		autoproxy-client.tar.gz \
		"autoproxy-$version.zip" \
		install-vps.sh \
		install-client.sh \
		update.json >SHA256SUMS
)
echo ">> SHA256SUMS the workflow would publish:"
sed 's/^/     /' "$fixture/SHA256SUMS"

if grep -qvE '^[0-9a-f]{64}  [^/]+$' "$fixture/SHA256SUMS"; then
	bad "SHA256SUMS is not <64 hex><two spaces><bare filename>"
else
	ok "SHA256SUMS is coreutils format with bare filenames"
fi

echo
echo ">> 1. good case: both installers must accept an untouched artefact"
expect "install-vps.sh accepts autoproxy-agent_linux_amd64" \
	"$(verify_with "$root/installers/install-vps.sh" autoproxy-agent_linux_amd64 "$fixture")" 0
expect "install-client.sh accepts autoproxy-client.tar.gz" \
	"$(verify_with "$root/installers/install-client.sh" autoproxy-client.tar.gz "$fixture")" 0

echo
echo ">> 2. corrupted artefact: both installers must refuse (this is the gate failing on purpose)"
broken="$(mktemp -d)"
cp -a "$fixture/." "$broken/"
printf 'tampered\n' >>"$broken/autoproxy-agent_linux_amd64"
printf 'tampered' | gzip >>"$broken/autoproxy-client.tar.gz"
expect "install-vps.sh refuses a tampered binary" \
	"$(verify_with "$root/installers/install-vps.sh" autoproxy-agent_linux_amd64 "$broken")" 1
expect "install-client.sh refuses a tampered tarball" \
	"$(verify_with "$root/installers/install-client.sh" autoproxy-client.tar.gz "$broken")" 1
rm -rf "$broken"

echo
echo ">> 3. directory-prefixed SHA256SUMS (the old 'dist/' shape): must refuse, not half-work"
prefixed="$(mktemp -d)"
cp -a "$fixture/." "$prefixed/"
sed -E 's|^([0-9a-f]{64}  )|\1dist/|' "$fixture/SHA256SUMS" >"$prefixed/SHA256SUMS"
expect "install-vps.sh refuses dist/-prefixed entries" \
	"$(verify_with "$root/installers/install-vps.sh" autoproxy-agent_linux_amd64 "$prefixed")" 2
expect "install-client.sh refuses dist/-prefixed entries" \
	"$(verify_with "$root/installers/install-client.sh" autoproxy-client.tar.gz "$prefixed")" 2
rm -rf "$prefixed"

echo
echo ">> 4. missing entry: must refuse rather than install something unverified"
missing="$(mktemp -d)"
cp -a "$fixture/." "$missing/"
grep -v 'autoproxy-agent_linux_amd64$' "$fixture/SHA256SUMS" >"$missing/SHA256SUMS"
grep -v 'autoproxy-client.tar.gz$' "$missing/SHA256SUMS" >"$missing/SHA256SUMS.tmp"
mv "$missing/SHA256SUMS.tmp" "$missing/SHA256SUMS"
expect "install-vps.sh refuses when its asset is absent from SHA256SUMS" \
	"$(verify_with "$root/installers/install-vps.sh" autoproxy-agent_linux_amd64 "$missing")" 2
expect "install-client.sh refuses when its asset is absent from SHA256SUMS" \
	"$(verify_with "$root/installers/install-client.sh" autoproxy-client.tar.gz "$missing")" 2
rm -rf "$missing"

echo
if [ "$fail" -ne 0 ]; then
	echo "test-checksum-parse: FAILED ($pass passed, $fail failed)" >&2
	exit 1
fi
echo "test-checksum-parse: all $pass checks passed"
