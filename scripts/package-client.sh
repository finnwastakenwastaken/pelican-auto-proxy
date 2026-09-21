#!/usr/bin/env bash
# Builds autoproxy-client.tar.gz (the script + its systemd unit) and a matching
# .sha256 file, for the release assets and for local testing of
# installers/install-client.sh via AUTOPROXY_LOCAL_TARBALL.
#
# Usage: scripts/package-client.sh [output-path]
#   (default output: client/autoproxy-client.tar.gz)
#
# The layout is dictated by installers/install-client.sh, which extracts the
# tarball into a scratch directory and then installs
# "<scratch>/autoproxy-client" and "<scratch>/autoproxy-client.service".
# So: exactly two members, both at the archive ROOT, no directory prefix,
# 0755 for the script and 0644 for the unit. That is asserted below.
#
# The archive is built reproducibly (sorted, no owner names, fixed mtimes), so
# rebuilding the same sources gives the same SHA256 as the published asset.

set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
out="${1:-$root/client/autoproxy-client.tar.gz}"

# The name the release publishes and the name install-client.sh looks for inside
# SHA256SUMS. The checksum line always carries this name, even when the archive
# is written somewhere else for a test, because the installer copies whatever it
# is given to exactly this filename before verifying it.
ASSET_NAME="autoproxy-client.tar.gz"

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

cp "$root/client/autoproxy-client" "$workdir/"
cp "$root/client/autoproxy-client.service" "$workdir/"
chmod 0755 "$workdir/autoproxy-client"
chmod 0644 "$workdir/autoproxy-client.service"

mkdir -p "$(dirname "$out")"
tar --sort=name \
	--owner=0 --group=0 --numeric-owner \
	--mtime="@${SOURCE_DATE_EPOCH:-0}" \
	-czf "$out" -C "$workdir" autoproxy-client autoproxy-client.service

# Read it back. A package nobody has opened is not a package.
listing="$(tar -tzf "$out")"
expected="autoproxy-client
autoproxy-client.service"
if [ "$(printf '%s\n' "$listing" | sort)" != "$expected" ]; then
	echo "unexpected tarball layout (install-client.sh expects exactly two files at the root):" >&2
	printf '%s\n' "$listing" | sed 's/^/  /' >&2
	exit 1
fi
modes="$(tar -tvzf "$out" | awk '{print $1, $NF}' | sort)"
expected_modes="-rw-r--r-- autoproxy-client.service
-rwxr-xr-x autoproxy-client"
if [ "$modes" != "$expected_modes" ]; then
	echo "unexpected modes in the tarball (want 0755 autoproxy-client, 0644 autoproxy-client.service):" >&2
	printf '%s\n' "$modes" | sed 's/^/  /' >&2
	exit 1
fi

# coreutils format: <64 hex><two spaces><bare filename>, which is what
# install-client.sh greps out of SHA256SUMS and feeds to `sha256sum -c`.
printf '%s  %s\n' "$(sha256sum "$out" | cut -d' ' -f1)" "$ASSET_NAME" > "$out.sha256"
grep -qE "^[0-9a-f]{64}  ${ASSET_NAME}\$" "$out.sha256" || {
	echo "checksum sidecar is not in coreutils format: $(cat "$out.sha256")" >&2
	exit 1
}

echo "wrote $out"
echo "wrote $out.sha256"
tar -tvzf "$out" | sed 's/^/  /'
