#!/usr/bin/env bash
# Run the SAME probe against test/fake-agent.py that scripts/contract-test/run.sh
# runs against the real agent binary.
#
#   scripts/contract-test/run-fake.sh
#
# This is the point of the fake agent: it is only useful while it answers the
# way the Go agent answers. One probe, two servers, same assertions - if the
# double has drifted, this fails and run.sh does not.
#
# Both containers get --network none; the PHP one joins the fake agent's
# network namespace, which is the only way to reach a listener there.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HERE="$REPO_ROOT/scripts/contract-test"
IMAGE="${IMAGE:-autoproxy-vps-test:local}"
PHP_IMAGE="${PHP_IMAGE:-php:8.4-cli}"
VENDOR="${AUTOPROXY_PHP_VENDOR:-/tmp/autoproxy-php/vendor}"

if [ ! -f "$VENDOR/autoload.php" ]; then
	echo "!! no PHP vendor directory at $VENDOR; run scripts/contract-test/vendor.sh" >&2
	exit 2
fi

echo ">> building $IMAGE"
docker build -q -t "$IMAGE" "$REPO_ROOT/scripts/vps-test" >/dev/null

SHARED="$(mktemp -d)"
FAKE_CT="autoproxy-contract-fake-$$"

# shellcheck disable=SC2329,SC2317  # invoked indirectly by the trap below (SC2317 is the same warning in shellcheck 0.9)
cleanup() {
	docker rm -f "$FAKE_CT" >/dev/null 2>&1 || true
	rm -rf "$SHARED"
}
trap cleanup EXIT

echo ">> starting the fake agent (--network none)"
docker run -d --name "$FAKE_CT" --network none \
	-v "$REPO_ROOT/test/fake-agent.py:/fake-agent.py:ro" \
	-v "$HERE/fake-in-container.sh:/run.sh:ro" \
	-v "$SHARED:/shared" \
	"$IMAGE" bash /run.sh >/dev/null

for _ in $(seq 1 120); do
	[ -f "$SHARED/ready" ] && break
	sleep 0.5
done

if [ ! -f "$SHARED/ready" ]; then
	echo "!! the fake agent never came up" >&2
	docker logs "$FAKE_CT" >&2 || true
	exit 1
fi

echo ">> running the plugin's AgentClient against the fake agent"
set +e
docker run --rm --network "container:$FAKE_CT" \
	-v "$REPO_ROOT:/repo:ro" \
	-v "$VENDOR:/vendor:ro" \
	-v "$SHARED:/shared:ro" \
	"$PHP_IMAGE" php /repo/scripts/contract-test/probe.php /shared/vps-code /shared/agent.pem
rc=$?

echo
echo ">> feeding the fake agent's join codes to the real client decoder"
docker run --rm --network none \
	-v "$REPO_ROOT/client:/client:ro" \
	-v "$HERE/client-joincode.sh:/check.sh:ro" \
	-v "$SHARED:/shared:ro" \
	"$IMAGE" bash /check.sh
jc=$?
set -e

[ "$jc" -ne 0 ] && rc="$jc"

exit "$rc"
