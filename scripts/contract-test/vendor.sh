#!/usr/bin/env bash
# Build the PHP vendor directory the contract test needs, once.
#
#   scripts/contract-test/vendor.sh
#
# Only the Laravel HTTP client and its dependencies: the plugin's AgentClient
# needs Http, config() and a container, and nothing else. Filament, Eloquent and
# the panel itself are deliberately absent, which is why the contract test can
# only exercise the transport and not anything that touches the database.
#
# This step needs network access (composer downloads packages). The test itself
# then runs with no network at all.
set -euo pipefail

DEST="${AUTOPROXY_PHP_VENDOR:-/tmp/autoproxy-php/vendor}"
WORK="$(dirname "$DEST")"

mkdir -p "$WORK"
cat > "$WORK/composer.json" <<'EOF'
{
    "require": {
        "illuminate/http": "^11.0",
        "illuminate/support": "^11.0",
        "illuminate/config": "^11.0",
        "illuminate/container": "^11.0",
        "guzzlehttp/guzzle": "^7.8"
    }
}
EOF

echo ">> installing into $DEST"
docker run --rm \
	-v "$WORK:/app" -w /app \
	--user "$(id -u):$(id -g)" \
	-e COMPOSER_HOME=/tmp/composer \
	composer:2 update --no-interaction --no-progress

echo ">> done"
ls "$DEST/illuminate"
