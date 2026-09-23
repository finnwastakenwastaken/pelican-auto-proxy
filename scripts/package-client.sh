#!/usr/bin/env bash
# Builds autoproxy-client.tar.gz (the script + its systemd unit) and a matching
# .sha256 file, for the release assets and for local testing of
# installers/install-client.sh via AUTOPROXY_LOCAL_TARBALL.
#
# Usage: scripts/package-client.sh [output-path] [version]
#   (default output: client/autoproxy-client.tar.gz)
#   version: what the packaged script reports as its own version, e.g. 0.3.0
#            (default: plugin/autoproxy/plugin.json's version with "-dev"
#            appended, so a locally built tarball never passes for a release).
#            The release workflow passes the tag's version.
#
# The script in the source tree says AUTOPROXY_VERSION="dev". The packaged copy
# carries the real number, which is what "autoproxy-client status" shows, what
# the client reports to the agent, and what "autoproxy-client update" compares
# against before it will install anything.
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
version="${2:-}"
if [ -z "$version" ]; then
	version="$(sed -n 's/^[[:space:]]*"version":[[:space:]]*"\([^"]*\)".*/\1/p' "$root/plugin/autoproxy/plugin.json" | head -n1)-dev"
fi
version="${version#v}"
if ! printf '%s' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'; then
	echo "version '$version' is not X.Y.Z or X.Y.Z-suffix" >&2
	exit 1
fi

# The name the release publishes and the name install-client.sh looks for inside
# SHA256SUMS. The checksum line always carries this name, even when the archive
# is written somewhere else for a test, because the installer copies whatever it
# is given to exactly this filename before verifying it.
ASSET_NAME="autoproxy-client.tar.gz"

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

# Stamp exactly the one line the script reserves for this, and refuse to build
# when it is not there: an unstamped release would call itself "dev" forever
# and could never be compared with anything.
stamp_line='readonly AUTOPROXY_VERSION="dev"'
if [ "$(grep -cxF "$stamp_line" "$root/client/autoproxy-client")" -ne 1 ]; then
	echo "client/autoproxy-client must contain exactly one line: $stamp_line" >&2
	exit 1
fi
sed "s/^readonly AUTOPROXY_VERSION=\"dev\"\$/readonly AUTOPROXY_VERSION=\"${version}\"/" \
	"$root/client/autoproxy-client" > "$workdir/autoproxy-client"
if [ "$(grep -cxF "readonly AUTOPROXY_VERSION=\"${version}\"" "$workdir/autoproxy-client")" -ne 1 ]; then
	echo "stamping the version into autoproxy-client failed" >&2
	exit 1
fi
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

# Read the stamp back out of the archive, the way "autoproxy-client version"
# will see it on a node.
packed="$(tar -xzOf "$out" autoproxy-client | sed -n 's/^readonly AUTOPROXY_VERSION="\(.*\)"$/\1/p')"
if [ "$packed" != "$version" ]; then
	echo "the packaged script reports version '$packed', expected '$version'" >&2
	exit 1
fi

echo "wrote $out (client version $version)"
echo "wrote $out.sha256"
tar -tvzf "$out" | sed 's/^/  /'
