#!/usr/bin/env bash
# Brings an existing checkout of the public repository up to date from an
# export, and commits the difference under the public identity.
#
# Usage: scripts/publish-sync.sh <public-checkout> [tree-ish]
#   public-checkout: a clone of the public repository, on the branch to update
#   tree-ish:        defaults to HEAD of this repository
#
# Everything tracked in the checkout is replaced by the export; files that are
# no longer in the export are deleted. .git is untouched. Nothing is pushed and
# nothing is tagged: both commands are printed at the end for a human to run.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
if [ $# -lt 1 ] || [ $# -gt 2 ]; then
  echo "usage: scripts/publish-sync.sh <public-checkout> [tree-ish]" >&2
  exit 2
fi
pub="$1"
rev="${2:-HEAD}"

public_name="Arrowtje"
public_email="finnwastakenwastaken@users.noreply.github.com"

[ -d "$pub/.git" ] || { echo "publish-sync: $pub is not a git checkout" >&2; exit 2; }
if [ -n "$(git -C "$pub" status --porcelain)" ]; then
  echo "publish-sync: $pub has uncommitted changes; refusing to overwrite them" >&2
  exit 2
fi
pub="$(cd "$pub" && pwd)"

export_dir="$(mktemp -d)"
trap 'rm -rf "$export_dir"' EXIT
rmdir "$export_dir"
"$root/scripts/export-public.sh" "$export_dir" "$rev"

echo "publish-sync: replacing the tracked tree of $pub with the export"
rsync -a --delete --exclude='/.git' "$export_dir/" "$pub/"

git -C "$pub" add -A
if git -C "$pub" diff --cached --quiet; then
  echo "publish-sync: nothing changed; the public checkout already matches $rev"
else
  src="$(git -C "$root" rev-parse --short "$rev")"
  git -C "$pub" -c "user.name=$public_name" -c "user.email=$public_email" \
    commit -q -m "Sync from development tree ($src)"
  echo "publish-sync: committed $(git -C "$pub" rev-parse --short HEAD)"
fi

echo "publish-sync: sweeping the checkout, including author metadata"
"$root/scripts/infra-sweep.sh" --authors "$pub"

echo "publish-sync: done. Nothing pushed. When ready:"
echo "  git -C $pub push"
echo "  git -C $pub tag v<version> && git -C $pub push origin v<version>"
