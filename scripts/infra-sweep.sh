#!/usr/bin/env bash
# Fails when any private infrastructure value or secret-shaped string appears in the tree.
#
# Usage: scripts/infra-sweep.sh [--authors] [path ...]
#   default path: repository root; zips are scanned by listing + content
#   --authors:    also check git author/committer metadata of the first path
#                 (or the repository root), which must contain only
#                 users.noreply.github.com addresses and the name Arrowtje.
#                 Run this before any public push.
#
# Two denylists are read: scripts/infra-sweep.denylist (public, shapes only)
# and scripts/infra-sweep.denylist.local (gitignored, the private values of a
# real deployment) when it exists. The script prints which lists it used, so a
# clean run that only had the public list cannot be mistaken for a full one.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"

check_authors=0
targets=()
for arg in "$@"; do
  case "$arg" in
    --authors) check_authors=1 ;;
    -h|--help) sed -n '2,12p' "$0"; exit 0 ;;
    -*) echo "infra-sweep: unknown option: $arg" >&2; exit 2 ;;
    *) targets+=("$arg") ;;
  esac
done
[ ${#targets[@]} -eq 0 ] && targets=("$root")

lists=("$root/scripts/infra-sweep.denylist")
[ -f "$root/scripts/infra-sweep.denylist.local" ] && lists+=("$root/scripts/infra-sweep.denylist.local")
echo "infra-sweep: denylists in use:"
for l in "${lists[@]}"; do echo "  ${l#"$root"/} ($(grep -cvE '^\s*(#|$)' "$l") patterns)"; done
[ ${#lists[@]} -eq 1 ] && echo "  note: no local denylist present, so only shapes were checked, not private values."

fail=0
while IFS= read -r pat; do
  [[ -z "$pat" || "$pat" == \#* ]] && continue
  for t in "${targets[@]}"; do
    if [ -f "$t" ] && [[ "$t" == *.zip ]]; then
      if python3 - "$t" "$pat" <<'PY'; then :; else fail=1; fi
import re,sys,zipfile
z,pat=sys.argv[1],sys.argv[2]; rx=re.compile(pat.encode()); bad=0
with zipfile.ZipFile(z) as f:
    for n in f.namelist():
        if rx.search(n.encode()) or rx.search(f.read(n)): print(f"  {z}:{n}: matches /{pat}/"); bad=1
sys.exit(bad)
PY
    else
      # dist/ is gitignored release output and its SHA256SUMS is 64-hex by
      # definition, so a whole-tree scan trips on it every time a release has
      # been built locally. The same is true of the *.sha256 file that
      # scripts/package-client.sh writes next to the client tarball, which is
      # gitignored build output in client/ rather than in dist/. A checksum
      # file that fails this gate teaches everyone to ignore the gate, which
      # is worse than the false positive. Their contents are not left
      # unchecked: the plugin zip is swept by name from
      # scripts/make-plugin-zip.sh, and CI sweeps a clean checkout.
      # Anything excluded here has to be swept somewhere else.
      # The denylists themselves are excluded: they contain the patterns.
      if hits=$(grep -rEIn --exclude-dir=.git --exclude-dir=legacy --exclude-dir=node_modules --exclude-dir=dist --exclude-dir=vendor --exclude='infra-sweep.denylist' --exclude='infra-sweep.denylist.local' --exclude='*.zip' --exclude='*.sha256' -- "$pat" "$t" 2>/dev/null); [ -n "$hits" ]; then
        echo "  pattern /$pat/:"; echo "$hits" | head -20 | sed 's/^/    /'; fail=1
      fi
    fi
  done
done < <(cat "${lists[@]}")

if [ "$check_authors" -eq 1 ]; then
  adir="${targets[0]}"
  [ -d "$adir" ] || adir="$root"
  if ! git -C "$adir" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    echo "  author check: $adir is not a git repository, so nothing was verified"; fail=1
  else
    echo "infra-sweep: author metadata of $adir"
    bad=$(git -C "$adir" log --format='%ae %an%n%ce %cn' | sort -u |
      grep -vE '^[^[:space:]]+@users\.noreply\.github\.com Arrowtje$' || true)
    if [ -n "$bad" ]; then
      echo "  author check: identities that are not 'Arrowtje <...@users.noreply.github.com>':"
      echo "$bad" | head -20 | sed 's/^/    /'; fail=1
    else
      echo "  author check: clean"
    fi
  fi
fi

if [ $fail -ne 0 ]; then echo "infra-sweep: FAILED (private infrastructure values or secret-shaped strings found)"; exit 1; fi
echo "infra-sweep: clean"
