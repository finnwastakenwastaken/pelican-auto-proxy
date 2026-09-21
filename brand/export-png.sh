#!/usr/bin/env bash
# Re-exports the PNG rasters in png/ from the SVG sources. Anything that needs a
# PNG (app stores, Discord, OG fallbacks) comes from here, never from a manual
# export, so the rasters cannot drift from the vectors.
#
# Uses rsvg-convert if it is on PATH, otherwise a throwaway Alpine container.
# Note: rsvg-convert resolves the <text> font stack against the fonts installed
# where it runs, so wordmark rasters can differ in width between machines. The
# mark-only exports have no text and are byte-stable.
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p png

if command -v rsvg-convert >/dev/null 2>&1; then
  r() { rsvg-convert "$@"; }
else
  command -v docker >/dev/null 2>&1 || { echo "need rsvg-convert or docker" >&2; exit 1; }
  docker build -q -t brand-rsvg:local - <<'DOCKERFILE' >/dev/null
FROM alpine:3.22
RUN apk add --no-cache rsvg-convert font-dejavu ttf-liberation
DOCKERFILE
  r() { docker run --rm -v "$PWD:/w" -w /w brand-rsvg:local rsvg-convert "$@"; }
fi

r -w 256  -h 256  logo-mark.svg      -o png/logo-mark-256.png
r -w 1024 -h 1024 logo-mark.svg      -o png/logo-mark-1024.png
r -w 32   -h 32   favicon.svg        -o png/favicon-32.png
r -w 180  -h 180  favicon.svg        -o png/apple-touch-icon-180.png
r -w 1280 -h 640  social-preview.svg -o png/social-preview.png
r -w 1200 -h 300  readme-banner.svg  -o png/readme-banner.png

echo "exported:"; ls -1 png/*.png
