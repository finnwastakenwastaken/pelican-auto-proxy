#!/usr/bin/env bash
# Both installers, run exactly as the docs print them, must resolve the latest
# public release, download the asset and SHA256SUMS, and verify the checksum,
# before anything privileged happens. Runs in a throwaway Debian container with
# network but without NET_ADMIN, so each installer is expected to stop at its
# first privileged step -- after the download and verification succeeded.
#
# Why this exists: the client installer once built its download URL from the
# OS version, because sourcing /etc/os-release overwrote a variable named
# VERSION. Nothing caught it: the live tests used AUTOPROXY_LOCAL_TARBALL and
# skipped the download path entirely.
#
# Usage: client/tests/test-install-resolve.sh [path-to-installers-dir]
#   Default: the installers in this checkout. Pass a directory to test other
#   copies (for example a release's).
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
dir="${1:-$here/installers}"
image="${IMAGE:-debian:trixie-slim}"
fail=0

run_one() { # name, expected-download-marker
	local name="$1" marker="$2" out
	echo ">> $name"
	out="$(docker run --rm -v "$dir/$name:/inst.sh:ro" "$image" bash -c \
		'apt-get update -qq >/dev/null && apt-get install -y -qq curl ca-certificates >/dev/null 2>&1; bash /inst.sh' 2>&1 || true)"
	printf '%s\n' "$out" | sed 's/^/   /' | head -20
	if ! printf '%s\n' "$out" | grep -qE "$marker"; then
		echo "   FAIL: $name did not download and verify a release asset"; fail=1
	fi
	if printf '%s\n' "$out" | grep -qiE "malformed|releases/download/[0-9]+ \("; then
		echo "   FAIL: $name built a download URL from the wrong version"; fail=1
	fi
}

run_one install-vps.sh    'Checksum OK'
run_one install-client.sh 'checksum OK'

if [ $fail -ne 0 ]; then echo "test-install-resolve: FAILED"; exit 1; fi
echo "test-install-resolve: all green"
