#!/usr/bin/env bash
# Run the VPS agent's end-to-end gate in a throwaway container.
#
#   scripts/vps-test/run.sh
#
# The container gets NET_ADMIN so the agent can drive real nftables, WireGuard
# and routes, and --network none so it can reach nothing. Never run the inner
# script on a real host: it installs, starts and then removes the agent.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HERE="$REPO_ROOT/scripts/vps-test"
BIN="$REPO_ROOT/agent/bin/autoproxy-agent"
IMAGE="${IMAGE:-autoproxy-vps-test:local}"

if [ ! -x "$BIN" ]; then
	echo ">> building the agent first"
	"$REPO_ROOT/scripts/build-agent.sh"
fi

# Built with network access; the test itself then runs with --network none.
echo ">> building $IMAGE"
docker build -q -t "$IMAGE" "$HERE" >/dev/null

echo ">> running the gate (--network none, NET_ADMIN)"
exec docker run --rm --network none --cap-add NET_ADMIN \
	-v "$BIN:/agent:ro" \
	-v "$REPO_ROOT/installers/install-vps.sh:/install-vps.sh:ro" \
	-v "$HERE/run-in-container.sh:/run.sh:ro" \
	"$IMAGE" bash /run.sh
