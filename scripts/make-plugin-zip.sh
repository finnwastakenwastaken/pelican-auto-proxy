#!/usr/bin/env bash
# Builds the importable Pelican plugin zip.
#
#   scripts/make-plugin-zip.sh   ->   dist/autoproxy-<version>.zip
#
# plugin.json sits at the zip ROOT (Pelican also accepts a single top-level
# folder, but root is one less thing to get wrong). Tests are not shipped.
# Uses python3's zipfile: no `zip` binary is assumed.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC="$ROOT/plugin/autoproxy"
README="$ROOT/plugin/README.md"
DIST="$ROOT/dist"

[ -f "$SRC/plugin.json" ] || { echo "no plugin.json in $SRC" >&2; exit 1; }
mkdir -p "$DIST"

python3 - "$SRC" "$DIST" "$README" <<'PY'
import json, os, sys, zipfile

src, dist, readme = sys.argv[1], sys.argv[2], sys.argv[3]

manifest = json.load(open(os.path.join(src, "plugin.json")))
plugin_id, version = manifest["id"], manifest.get("version", "0.0.0")

if plugin_id != os.path.basename(src):
    raise SystemExit(f"plugin.json id '{plugin_id}' must equal the folder name '{os.path.basename(src)}'")

# The panel reads these and refuses or mislabels the plugin when they are wrong,
# so a bad manifest fails the build instead of the user's install.
for key in ("name", "author", "category", "namespace", "class"):
    if not manifest.get(key):
        raise SystemExit(f"plugin.json is missing '{key}'")

# Not shipped: tests, editor leftovers, VCS metadata, compiled python.
SKIP_DIRS = {".git", "tests", "node_modules", "__pycache__", ".idea", ".vscode"}
SKIP_EXT = {".pyc", ".swp", ".orig", ".rej", ".bak"}
SKIP_FILES = {".DS_Store"}

target = os.path.join(dist, f"{plugin_id}-{version}.zip")
if os.path.exists(target):
    os.remove(target)

written = []
with zipfile.ZipFile(target, "w", zipfile.ZIP_DEFLATED) as zf:
    for root, dirs, files in os.walk(src):
        dirs[:] = sorted(d for d in dirs if d not in SKIP_DIRS)
        for name in sorted(files):
            if name in SKIP_FILES or os.path.splitext(name)[1] in SKIP_EXT:
                continue
            full = os.path.join(root, name)
            arc = os.path.relpath(full, src)
            zf.write(full, arc)
            written.append(arc)

    if os.path.exists(readme) and "README.md" not in written:
        zf.write(readme, "README.md")
        written.append("README.md")

# Read it back: a build nobody has opened is not a build.
with zipfile.ZipFile(target) as zf:
    names = zf.namelist()
    assert "plugin.json" in names, "plugin.json is not at the zip root"
    assert not any(n.startswith("tests/") for n in names), "tests leaked into the zip"
    check = json.loads(zf.read("plugin.json"))
    assert check["id"] == plugin_id, "plugin.json in the zip has the wrong id"
    # The old prototype name must never ship: it would collide with a different
    # plugin id and break every update path.
    for n in names:
        body = zf.read(n)
        assert b"portgate" not in body.lower(), f"the old prototype name survives in {n}"
    bad = zf.testzip()
    assert bad is None, f"corrupt entry: {bad}"

print(f"{target}  ({len(names)} files, {os.path.getsize(target)} bytes)")
for n in sorted(names):
    print(f"  {n}")
PY

# Sweep the artefact we just wrote. scripts/infra-sweep.sh excludes dist/ on a
# whole-tree run (its SHA256SUMS is 64-hex by definition), so without this line
# the one file that actually ships would be the one nothing checks. Two safe
# decisions that had never met.
VERSION="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["version"])' "$SRC/plugin.json")"
ZIP="$DIST/autoproxy-${VERSION}.zip"
echo ">> sweeping $ZIP"
"$ROOT/scripts/infra-sweep.sh" "$ZIP"
