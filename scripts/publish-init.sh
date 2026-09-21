#!/usr/bin/env bash
# Turns an export into the repository that gets pushed publicly: fresh
# history, one commit, the public identity, and both sweeps run on the result.
#
# Usage: scripts/publish-init.sh <target-dir> [tree-ish]
#   target-dir must not exist or be empty; tree-ish defaults to HEAD.
#
# The private development repository is never pushed anywhere public: its
# commits carry personal identities, and rewriting history after a push is
# not a fix. This script is the only supported way to produce the public
# tree, so that the same steps run every time. It does not push.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
if [ $# -lt 1 ] || [ $# -gt 2 ]; then
  echo "usage: scripts/publish-init.sh <target-dir> [tree-ish]" >&2
  exit 2
fi
dest="$1"
rev="${2:-HEAD}"

public_name="Arrowtje"
public_email="finnwastakenwastaken@users.noreply.github.com"
public_remote="git@github.com:finnwastakenwastaken/pelican-auto-proxy.git"

"$root/scripts/export-public.sh" "$dest" "$rev"
dest="$(cd "$dest" && pwd)"

echo "publish-init: initialising a fresh repository in $dest"
git -C "$dest" init -q -b main
git -C "$dest" -c "user.name=$public_name" -c "user.email=$public_email" \
  add -A
git -C "$dest" -c "user.name=$public_name" -c "user.email=$public_email" \
  commit -q -m "Pelican Auto Proxy: initial public release candidate"
git -C "$dest" config user.name "$public_name"
git -C "$dest" config user.email "$public_email"
git -C "$dest" remote add origin "$public_remote"

echo "publish-init: sweeping the new repository, including author metadata"
"$root/scripts/infra-sweep.sh" --authors "$dest"

echo "publish-init: ready. Review with: git -C $dest log --format='%an <%ae>' && git -C $dest ls-files | wc -l"
echo "publish-init: nothing has been pushed. Push is a separate, deliberate step: git -C $dest push -u origin main"
