#!/usr/bin/env bash
# Downloads and installs the Pelican Auto Proxy tunnel client.
#
# Usage:
#   curl -fsSL <release-url>/install-client.sh | sudo bash -s -- <join-code> [install options]
#
# Env:
#   AUTOPROXY_VERSION=v1.2.3     pin a release instead of resolving latest
#   AUTOPROXY_LOCAL_TARBALL=/path/to/autoproxy-client.tar.gz
#                                skip the download entirely (for tests);
#                                a "<tarball>.sha256" next to it is used to
#                                verify the checksum if present
#
# This script only downloads, verifies and extracts two files, then hands
# off to the real installer (`autoproxy-client install`, see client/). Keep
# it short: OS gate, fetch, verify, extract, exec.

set -euo pipefail

REPO="finnwastakenwastaken/pelican-auto-proxy"
VERSION="${AUTOPROXY_VERSION:-latest}"
BASE_URL="https://github.com/${REPO}/releases/download"

log() { printf '[install-client] %s\n' "$*"; }
err() { printf '[install-client] ERROR: %s\n' "$*" >&2; }

# Verify "$1" (a bare filename) inside directory "$2" against "$2/SHA256SUMS".
# SHA256SUMS is coreutils format and nothing else: <64 hex><two spaces><bare
# filename>. A line carrying a directory prefix is treated as no entry at all,
# because `sha256sum -c` would then look for a path that does not exist here and
# fail for a reason that has nothing to do with the bytes we downloaded.
# Returns 2 when there is nothing to check against, 1 on a mismatch, 0 on a match.
verify_against_sha256sums() {
    local asset="$1" dir="$2" line
    [[ -f "$dir/SHA256SUMS" ]] || return 2
    line="$(grep -E "^[0-9a-f]{64}[[:space:]]+[*]?${asset}\$" "$dir/SHA256SUMS")" || return 2
    [[ "$(printf '%s\n' "$line" | wc -l)" -eq 1 ]] || return 2
    ( cd "$dir" && printf '%s\n' "$line" | sha256sum -c --strict - >/dev/null )
}

# Lets scripts/test-checksum-parse.sh load the verification above and exercise it
# without running an install. Nothing below this line runs when it is set.
if [[ "${AUTOPROXY_INSTALLER_SOURCE_ONLY:-}" == "1" ]]; then
    # shellcheck disable=SC2317  # reached only when this file is sourced, which is the point
    return 0 2>/dev/null || exit 0
fi

if [[ "$(id -u)" -ne 0 ]]; then
    err "must run as root (sudo)"
    exit 1
fi

[[ -r /etc/os-release ]] || { err "cannot read /etc/os-release; unsupported system"; exit 1; }
# shellcheck disable=SC1091
source /etc/os-release
# Adding a platform later is one more case arm here (and in install-vps.sh
# and client/autoproxy-client).
case "${ID:-}:${VERSION_ID:-}" in
    debian:12|debian:13)
        : ;;
    ubuntu:*)
        err "Ubuntu is not supported in this release; Debian 12 or 13 only. Ubuntu support is tracked for a later release."
        exit 1
        ;;
    *)
        err "unsupported OS: ${PRETTY_NAME:-${ID:-} ${VERSION_ID:-}}. Supported: Debian 12/13."
        exit 1
        ;;
esac

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

tarball="$workdir/autoproxy-client.tar.gz"
sums="$workdir/SHA256SUMS"

if [[ -n "${AUTOPROXY_LOCAL_TARBALL:-}" ]]; then
    log "using local tarball ${AUTOPROXY_LOCAL_TARBALL} (AUTOPROXY_LOCAL_TARBALL set, no download)"
    [[ -f "$AUTOPROXY_LOCAL_TARBALL" ]] || { err "AUTOPROXY_LOCAL_TARBALL does not exist: ${AUTOPROXY_LOCAL_TARBALL}"; exit 1; }
    cp "$AUTOPROXY_LOCAL_TARBALL" "$tarball"
    if [[ -f "${AUTOPROXY_LOCAL_TARBALL}.sha256" ]]; then
        cp "${AUTOPROXY_LOCAL_TARBALL}.sha256" "$sums"
    fi
else
    if [[ "$VERSION" == "latest" ]]; then
        log "resolving latest release"
        VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
            | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')"
        [[ -n "$VERSION" ]] || { err "could not resolve the latest release from the GitHub API"; exit 1; }
        log "latest release is ${VERSION}"
    fi
    url="${BASE_URL}/${VERSION}"
    log "downloading autoproxy-client.tar.gz and SHA256SUMS from ${url}"
    curl -fsSL -o "$tarball" "${url}/autoproxy-client.tar.gz"
    curl -fsSL -o "$sums" "${url}/SHA256SUMS"
fi

log "verifying checksum"
verify_rc=0
verify_against_sha256sums autoproxy-client.tar.gz "$workdir" || verify_rc=$?
if [[ "$verify_rc" -eq 2 ]]; then
    err "no usable SHA256SUMS entry for autoproxy-client.tar.gz; refusing to install an unverified tarball"
    exit 1
elif [[ "$verify_rc" -ne 0 ]]; then
    rm -f "$tarball"
    err "CHECKSUM MISMATCH: the tarball does not match SHA256SUMS and has been deleted."
    err "Nothing was installed. Do not retry blindly -- report this at https://github.com/${REPO}/issues"
    exit 1
fi
log "checksum OK"

log "extracting to /usr/local/bin and /etc/systemd/system"
extracted="$workdir/extracted"
mkdir -p "$extracted" /usr/local/bin /etc/systemd/system
tar -xzf "$tarball" -C "$extracted"

install -m 0755 "$extracted/autoproxy-client" /usr/local/bin/autoproxy-client
install -m 0644 "$extracted/autoproxy-client.service" /etc/systemd/system/autoproxy-client.service
command -v systemctl >/dev/null 2>&1 && systemctl daemon-reload

exec /usr/local/bin/autoproxy-client install "$@"
