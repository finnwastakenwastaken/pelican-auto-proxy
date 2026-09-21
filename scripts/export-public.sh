#!/usr/bin/env bash
# Produces the tree that is safe to publish, and refuses if it is not.
#
# Usage: scripts/export-public.sh <target-dir> [tree-ish]
#   tree-ish defaults to HEAD. Pass another commit or tree to export exactly
#   that, for example to check work that is not committed yet.
#
# The export is a `git archive` of HEAD, so uncommitted work and everything
# .gitignore'd is left out by construction rather than by a copy rule someone
# has to remember. On top of that, paths that are committed but must never be
# published are removed, and the whole result is swept with both denylists.
# Nothing is copied in from the working tree: if a file is not in HEAD, it does
# not reach the public repository.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"

if [ $# -lt 1 ] || [ $# -gt 2 ]; then
  echo "usage: scripts/export-public.sh <target-dir> [tree-ish]" >&2
  exit 2
fi
dest="$1"
rev="${2:-HEAD}"

if [ -e "$dest" ] && [ -n "$(ls -A "$dest" 2>/dev/null)" ]; then
  echo "export-public: $dest exists and is not empty. Remove it yourself first;" >&2
  echo "               this script will not delete a directory you named." >&2
  exit 2
fi
mkdir -p "$dest"
dest="$(cd "$dest" && pwd)"

if [ $# -eq 1 ] && ! git -C "$root" diff --quiet "$rev" -- 2>/dev/null; then
  echo "export-public: warning: the working tree differs from $rev."
  echo "               The export is built from $rev, so those changes are not in it."
fi

echo "export-public: archiving $rev of $root into $dest"
git -C "$root" archive "$rev" | tar -x -C "$dest"

# Committed paths that never go public. Reference material, build output and
# any internal notes. Each is removed by exact name, and only under $dest.
excluded=(
  "legacy"
  "dist"
  "docs/dev/handover.md"
)
for rel in "${excluded[@]}"; do
  if [ -e "$dest/$rel" ]; then
    echo "export-public: removing $rel"
    rm -rf -- "${dest:?}/$rel"
  fi
done
# Packaged client tarballs and their checksums, whatever the exact suffix.
while IFS= read -r f; do
  echo "export-public: removing ${f#"$dest"/}"
  rm -f -- "$f"
done < <(find "$dest/client" -maxdepth 1 -name '*.tar.gz' -o -maxdepth 1 -name '*.tar.gz.*' 2>/dev/null)

echo "export-public: sweeping the export"
# The sweep is run from this repository, not from the export, so it reads the
# local denylist as well; the export never contains it (it is gitignored).
if ! "$root/scripts/infra-sweep.sh" "$dest"; then
  echo "export-public: FAILED — the export is not safe to publish." >&2
  exit 1
fi

echo "export-public: $dest is clean ($(find "$dest" -type f | wc -l) files)"
