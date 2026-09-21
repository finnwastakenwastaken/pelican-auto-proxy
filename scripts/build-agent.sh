#!/usr/bin/env bash
# Build the static autoproxy-agent binary for the VPS (linux/amd64).
#
# Go is deliberately NOT installed on this dev host: the build runs in a
# throwaway container so the host stays untouched and the toolchain version is
# pinned in one place.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
AGENT_DIR="$REPO_ROOT/agent"
OUT_DIR="$AGENT_DIR/bin"
OUT="$OUT_DIR/autoproxy-agent"
GO_IMAGE="${GO_IMAGE:-golang:1.25-alpine}"

# Version stamp: a git description when we have one, otherwise a UTC date.
VERSION="${AUTOPROXY_VERSION:-}"
if [ -z "$VERSION" ]; then
	if VERSION="$(git -C "$REPO_ROOT" describe --tags --always --dirty 2>/dev/null)" && [ -n "$VERSION" ]; then
		:
	else
		VERSION="$(date -u +%Y%m%d-%H%M%S)"
	fi
fi

echo ">> building autoproxy-agent $VERSION (linux/amd64, static) using $GO_IMAGE"
mkdir -p "$OUT_DIR"

# --user keeps the produced binary owned by the invoking user instead of root.
docker run --rm \
	--user "$(id -u):$(id -g)" \
	-v "$AGENT_DIR:/src" \
	-w /src \
	-e GOFLAGS=-mod=mod \
	-e GOCACHE=/tmp/gocache \
	-e GOMODCACHE=/tmp/gomodcache \
	-e HOME=/tmp \
	-e CGO_ENABLED=0 \
	-e GOOS=linux \
	-e GOARCH=amd64 \
	"$GO_IMAGE" \
	go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /src/bin/autoproxy-agent ./cmd/autoproxy-agent

chmod 0755 "$OUT"
echo ">> built $OUT"
ls -l "$OUT"
file "$OUT" 2>/dev/null || true

# Informational only: the binary is linux/amd64 and usually runs here, but a
# failure to execute it must not fail the build.
echo ">> version string: $("$OUT" -version 2>/dev/null || echo '(cannot run here; built for linux/amd64)')"
