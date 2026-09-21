#!/usr/bin/env bash
# Pelican Auto Proxy -- VPS installer.
#
#   curl -fsSL https://raw.githubusercontent.com/finnwastakenwastaken/pelican-auto-proxy/main/installers/install-vps.sh | bash -s -- --yes
#
# It downloads the released autoproxy-agent binary, verifies it against the
# release SHA256SUMS, installs it to /usr/local/bin, and hands over to
# `autoproxy-agent setup`, which does the actual provisioning. Every flag you
# pass is forwarded to that command unchanged.
#
# Environment:
#   AUTOPROXY_VERSION       release tag to install (default: the latest release)
#   AUTOPROXY_LOCAL_BINARY  install this local file instead of downloading;
#                           skips the download and the checksum check entirely.
#                           This is how the container tests install a binary
#                           that has not been released yet.
set -euo pipefail

REPO="finnwastakenwastaken/pelican-auto-proxy"
ASSET="autoproxy-agent_linux_amd64"
INSTALL_PATH="/usr/local/bin/autoproxy-agent"

log() { printf '>> %s\n' "$*"; }
die() {
	printf 'ERROR: %s\n' "$*" >&2
	exit 1
}

# Verify "$1" (a bare filename) inside directory "$2" against "$2/SHA256SUMS".
# SHA256SUMS is coreutils format and nothing else: <64 hex><two spaces><bare
# filename>. A line carrying a directory prefix is treated as no entry at all,
# because `sha256sum -c` would then look for a path that does not exist here and
# fail for a reason that has nothing to do with the bytes we downloaded.
# Returns non-zero on any mismatch; the caller decides how loudly to die.
verify_against_sha256sums() {
	local asset="$1" dir="$2"
	[ -f "$dir/SHA256SUMS" ] || return 2
	grep -E "^[0-9a-f]{64}[[:space:]]+[*]?${asset}\$" "$dir/SHA256SUMS" >"$dir/SHA256SUMS.asset" || return 2
	(cd "$dir" && sha256sum -c --strict SHA256SUMS.asset >/dev/null 2>&1)
}

# Lets scripts/test-checksum-parse.sh load the verification above and exercise it
# without running an install. Nothing below this line runs when it is set.
if [ "${AUTOPROXY_INSTALLER_SOURCE_ONLY:-}" = "1" ]; then
	# shellcheck disable=SC2317  # reached only when this file is sourced, which is the point
	return 0 2>/dev/null || exit 0
fi

# --- 1. Root ---------------------------------------------------------------
[ "$(id -u)" -eq 0 ] || die "this installer must run as root (try: sudo bash install-vps.sh)"

# --- 2. Supported OS -------------------------------------------------------
# Adding a platform later is one more case arm here (and in install-client.sh
# and client/autoproxy-client).
SUPPORTED="Debian 12 and Debian 13"
[ -r /etc/os-release ] || die "cannot read /etc/os-release, so the OS cannot be checked. Supported: $SUPPORTED"
# shellcheck disable=SC1091  # path is fixed and checked above; not available at lint time
. /etc/os-release
case "${ID:-}:${VERSION_ID:-}" in
debian:12 | debian:13) ;;
ubuntu:*) die "Ubuntu is not supported in this release; Debian 12 or 13 only. Ubuntu support is tracked for a later release." ;;
*) die "unsupported OS: ${PRETTY_NAME:-${ID:-unknown} ${VERSION_ID:-}}. Supported: $SUPPORTED" ;;
esac
log "OS: ${PRETTY_NAME:-$ID $VERSION_ID}"

# curl is only needed to fetch a release, so it is installed inside the
# download branch below -- an offline AUTOPROXY_LOCAL_BINARY install must not
# depend on apt reaching the network.
need_curl() {
	command -v curl >/dev/null 2>&1 && return 0
	log "curl is missing; installing it"
	export DEBIAN_FRONTEND=noninteractive
	apt-get update || die "apt-get update failed; install curl yourself and re-run"
	apt-get install -y curl || die "could not install curl; install it yourself and re-run"
}

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# --- 3. Obtain the binary --------------------------------------------------
if [ -n "${AUTOPROXY_LOCAL_BINARY:-}" ]; then
	# Escape hatch for tests and for air-gapped installs. The checksum gate
	# only protects downloads, so it is skipped here -- say so out loud.
	[ -f "$AUTOPROXY_LOCAL_BINARY" ] || die "AUTOPROXY_LOCAL_BINARY is set but $AUTOPROXY_LOCAL_BINARY is not a file"
	log "AUTOPROXY_LOCAL_BINARY is set: installing $AUTOPROXY_LOCAL_BINARY without downloading or verifying a checksum"
	cp "$AUTOPROXY_LOCAL_BINARY" "$WORK/$ASSET"
else
	need_curl
	VERSION="${AUTOPROXY_VERSION:-latest}"
	if [ "$VERSION" = "latest" ]; then
		log "Resolving the latest release tag"
		API_JSON="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest")" ||
			die "could not reach the GitHub API to find the latest release. Set AUTOPROXY_VERSION to a tag to skip this step."
		VERSION="$(printf '%s\n' "$API_JSON" | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/')"
		[ -n "$VERSION" ] || die "the GitHub API returned no tag_name; set AUTOPROXY_VERSION to a release tag instead"
	fi
	BASE="https://github.com/$REPO/releases/download/$VERSION"
	log "Installing version $VERSION"

	log "Downloading $ASSET"
	curl -fsSL -o "$WORK/$ASSET" "$BASE/$ASSET" || die "download failed: $BASE/$ASSET"
	log "Downloading SHA256SUMS"
	curl -fsSL -o "$WORK/SHA256SUMS" "$BASE/SHA256SUMS" || die "download failed: $BASE/SHA256SUMS"

	# --- 4. Verify. A failure here must be loud and fatal; an installer that
	# prints success after skipping verification is worse than no installer.
	log "Verifying $ASSET against SHA256SUMS"
	verify_rc=0
	verify_against_sha256sums "$ASSET" "$WORK" || verify_rc=$?
	if [ "$verify_rc" -eq 2 ]; then
		die "SHA256SUMS contains no usable entry for $ASSET; refusing to install an unverified binary"
	elif [ "$verify_rc" -ne 0 ]; then
		rm -f "$WORK/$ASSET"
		printf 'ERROR: CHECKSUM MISMATCH for %s (release %s).\n' "$ASSET" "$VERSION" >&2
		printf 'ERROR: The download did not match the published SHA256SUMS and has been deleted.\n' >&2
		printf 'ERROR: Nothing was installed. Do not retry blindly -- report this at https://github.com/%s/issues\n' "$REPO" >&2
		exit 1
	fi
	log "Checksum OK"
fi

# --- 5. Install ------------------------------------------------------------
install -m 0755 "$WORK/$ASSET" "$INSTALL_PATH"
log "Installed $INSTALL_PATH"

# --- 6. Hand over to the agent's own setup, flags and all ------------------
log "Running: autoproxy-agent setup $*"
exec "$INSTALL_PATH" setup "$@"
