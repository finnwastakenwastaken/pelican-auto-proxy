#!/usr/bin/env bash
# Run the plugin's own AgentClient against the REAL autoproxy-agent binary.
#
#   scripts/contract-test/run.sh
#
# Why: test/fake-agent.py is a hand-written double. The plugin passing against
# it proves the plugin and the double agree, not that either agrees with the Go
# code that ships. This runs the class the panel actually uses against the agent
# compiled from agent/.
#
# How: one container runs the agent with NET_ADMIN and --network none, so it can
# drive nftables and WireGuard but reach nothing. A second container joins that
# container's network namespace (--network container:<id>), which is the only way
# to reach a --network none listener, and runs PHP there. Neither container can
# reach the internet or this host's network.
#
# Needs a PHP vendor directory with illuminate/http. Build it once with:
#   scripts/contract-test/vendor.sh
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HERE="$REPO_ROOT/scripts/contract-test"
BIN="$REPO_ROOT/agent/bin/autoproxy-agent"
IMAGE="${IMAGE:-autoproxy-vps-test:local}"
PHP_IMAGE="${PHP_IMAGE:-php:8.4-cli}"
VENDOR="${AUTOPROXY_PHP_VENDOR:-/tmp/autoproxy-php/vendor}"

if [ ! -x "$BIN" ]; then
	echo ">> building the agent first"
	"$REPO_ROOT/scripts/build-agent.sh"
fi

if [ ! -f "$VENDOR/autoload.php" ]; then
	echo "!! no PHP vendor directory at $VENDOR" >&2
	echo "!! run scripts/contract-test/vendor.sh first" >&2
	exit 2
fi

echo ">> building $IMAGE"
docker build -q -t "$IMAGE" "$REPO_ROOT/scripts/vps-test" >/dev/null

SHARED="$(mktemp -d)"
AGENT_CT="autoproxy-contract-agent-$$"

# shellcheck disable=SC2329,SC2317  # invoked indirectly by the trap below (SC2317 is the same warning in shellcheck 0.9)
cleanup() {
	docker rm -f "$AGENT_CT" >/dev/null 2>&1 || true
	rm -rf "$SHARED"
}
trap cleanup EXIT

echo ">> starting the agent (--network none, NET_ADMIN)"
docker run -d --name "$AGENT_CT" --network none --cap-add NET_ADMIN \
	-v "$BIN:/agent:ro" \
	-v "$HERE/agent-in-container.sh:/run.sh:ro" \
	-v "$REPO_ROOT/client:/client:ro" \
	-v "$SHARED:/shared" \
	"$IMAGE" bash /run.sh >/dev/null

for _ in $(seq 1 120); do
	[ -f "$SHARED/ready" ] && break
	sleep 0.5
done

if [ ! -f "$SHARED/ready" ]; then
	echo "!! the agent never came up" >&2
	docker logs "$AGENT_CT" >&2 || true
	exit 1
fi

echo ">> the agent is listening; running the plugin's AgentClient in its netns"
set +e
docker run --rm --network "container:$AGENT_CT" \
	-v "$REPO_ROOT:/repo:ro" \
	-v "$VENDOR:/vendor:ro" \
	-v "$SHARED:/shared:ro" \
	"$PHP_IMAGE" php /repo/scripts/contract-test/probe.php /shared/vps-code /shared/agent.pem
rc=$?

echo
echo ">> feeding the agent's own join codes to the real client decoder"
docker run --rm --network none \
	-v "$REPO_ROOT/client:/client:ro" \
	-v "$HERE/client-joincode.sh:/check.sh:ro" \
	-v "$SHARED:/shared:ro" \
	"$IMAGE" bash /check.sh
jc=$?
set -e

if [ "$jc" -ne 0 ]; then
	rc="$jc"
fi

echo
echo ">> tunnel check-in (the real client's code against the real agent)"
cat "$SHARED/tunnel-checks"
if grep -q '^FAIL' "$SHARED/tunnel-checks" || ! grep -q '^PASS' "$SHARED/tunnel-checks"; then
	rc=1
fi

touch "$SHARED/done"
sleep 1

echo
echo ">> what the agent logged"
docker logs "$AGENT_CT" 2>&1 | sed -n '/agent log/,$p' | head -60

exit "$rc"
