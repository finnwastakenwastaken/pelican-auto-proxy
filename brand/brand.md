# Pelican Auto Proxy — brand kit

Everything here is hand-written SVG on a `viewBox`. No rasters in the sources, no
external fonts, no filters, no gradients, no `<style>` blocks — so the files survive
GitHub's SVG sanitiser, render the same in Chromium and librsvg, and stay editable
in any text editor years from now.

## The idea

A geometric pelican head leaves an open ring. The ring is the tunnel mouth; the beak
is the only thing that crosses it, which is the product in one picture: one way in,
and the host stays inside. The pouch is the second colour because that is the part of
a pelican everybody recognises, and because it is what carries the traffic.

Pelican Panel's restraint and blue give the primary; WireGuard's maroon gives the
accent and the hard geometry. Both palettes are honoured without copying either mark —
every curve here is original.

## Palette

| Token | Hex | Role |
|---|---|---|
| Pelican Blue | `#3b82f6` | Primary. Head and upper mandible, links, primary buttons. |
| Tunnel Red | `#c8262c` | Accent. Ring and pouch, rules, warnings. |
| Maroon Deep | `#88171a` | Print and light-only surfaces. The WireGuard-family anchor. |
| Ink | `#0b0f19` | Deepest background. Social preview, banner, favicon badge. |
| Panel | `#111827` | Raised card on Ink. |
| Hairline | `#1f2937` | 1 px borders. |
| Muted | `#94a3b8` | Secondary text on dark. |
| Snow | `#f8fafc` | Primary text on dark. |

### Contrast (WCAG 2.1 ratios, computed, not estimated)

| Pair | Ratio | Verdict |
|---|---|---|
| Pelican Blue on Ink | 5.21 : 1 | Passes AA for text and for graphics. |
| Pelican Blue on white | 3.68 : 1 | Graphics and large text only. Not body text. |
| Tunnel Red on Ink | 3.44 : 1 | Graphics only (≥3:1). Not text. |
| Tunnel Red on white | 5.57 : 1 | Passes AA for text. |
| Tunnel Red on Panel `#111827` | 3.18 : 1 | Graphics only, and with little margin. |
| Maroon Deep on white | 9.62 : 1 | Excellent. |
| **Maroon Deep on Ink** | **1.99 : 1** | **Fails everything. Never put `#88171a` on a dark background.** |
| Snow on Ink | 18.5 : 1 | Excellent. |
| Muted on Ink | 7.47 : 1 | Passes AA. |
| Muted on Panel | 6.92 : 1 | Passes AA. |

That last-but-four row is the reason the mark uses `#c8262c` rather than WireGuard's
own `#88171a`: `#c8262c` is the single red that clears 3:1 on both Ink and white, so
one coloured mark works on a dark panel and on a white README alike.

## Construction

The mark is built on a 256 × 256 grid. Every number below is in grid units; multiply
by `size / 256` to work at any scale.

- **Ring.** Centre (128, 128), radius 96, stroke 18 with round caps — outer edge 105,
  inner edge 87. It is an arc, not a circle: open from −14° to 54° (a 68° gap,
  clockwise, 0° = due right, y down). Only the beak and the front of the pouch
  cross that band, and they only cross it inside the gap.
- **Head.** Circle centre (100, 104), radius 34.
- **Eye.** Radius 6.5 at (114, 94), knocked out with `fill-rule="evenodd"` — it is a
  hole, not a painted dot, so the mark works on any background colour.
- **Beak.** Gape leaves the head circle at 25°, top edge at −47°; the tip is (240, 148),
  which sits 114 units from centre, clear of the ring's 105 outer edge.
- **Pouch.** A closed shape held roughly 9 units clear of the gape. That gap is real
  negative space, which is what lets the one-colour versions read — without it the
  head, beak and pouch collapse into one blob.
- **Art bounding box.** x 23–240, y 23–233. Optical centre is a touch right of the
  geometric centre, which is correct: the beak wants the room.

## Clear space and minimum size

- **Clear space:** at least 36 grid units (14% of the mark's height) on all four
  sides, measured from the art bounding box, not from the `viewBox`. Nothing —
  text, rules, other logos, an image edge — inside that.
- **Minimum sizes**, measured and confirmed by rendering, not guessed:
  - `favicon.svg` — 16 px. At 16 px the beak and pouch merge into one blue-and-red
    diagonal; that is expected and it still reads as a distinct badge.
  - `logo-mark.svg` — 24 px.
  - Mono marks — 20 px (they have less colour separation to lose).
  - `logo-horizontal*.svg` — 160 px wide. Below that the wordmark closes up; use the
    mark alone instead.

## Do / don't

**Do**
- Use `favicon.svg` (the dark badge) for browser tabs and the Pelican plugin icon.
- Use the mono marks whenever the background is busy, photographic, or a single flat
  brand colour that is not ours.
- Keep the mark's proportions — scale the whole `<svg>`, never one path.
- Recolour by editing the hex values; there are only three in the mark.

**Don't**
- Don't put `#88171a` on a dark background (1.99 : 1 — see the table).
- Don't close the ring. The gap is the idea.
- Don't rotate, skew, outline, emboss or add a drop shadow to the mark.
- Don't set the wordmark in the mark's blue or red — the wordmark is always Ink or
  Snow so the mark stays the only coloured thing.
- Don't rebuild the lockup by hand; use `logo-horizontal*.svg` so the spacing is fixed.
- Don't stretch a raster from `png/` — re-export at the size you need.

## Type

The wordmark is live `<text>`, not paths, with the stack
`Inter, Segoe UI, Helvetica, Arial, sans-serif`. This is deliberate: the files stay
tiny and editable, and the wordmark can be corrected by typing. The trade-off is real
and you should know it — **the wordmark's width depends on which font the renderer
resolves.** The lockups are laid out with enough right-hand room for Arial (the widest
fallback), so with Inter installed the right margin looks generous. If you ever need a
pixel-exact lockup independent of installed fonts, export a PNG from `png/` or convert
that one file's text to paths at that point.

## Which file goes where

| Use | File |
|---|---|
| Browser tab / favicon | `favicon.svg` (plus `png/favicon-32.png` for old browsers) |
| Pelican Panel plugin icon | `favicon.svg`, or `png/logo-mark-256.png` if a raster is required |
| README header | `readme-banner.svg` |
| GitHub social preview (Settings → Social preview) | `png/social-preview.png` — GitHub needs a raster here; `social-preview.svg` is the source |
| Docs header, light background | `logo-horizontal.svg` |
| Docs header, dark background | `logo-horizontal-dark-bg.svg` |
| App icon / iOS home screen | `png/apple-touch-icon-180.png` |
| Stickers, laser etching, single-colour print | `logo-mark-mono-dark.svg` (light stock) or `logo-mark-mono-light.svg` (dark stock) |

## Re-exporting the PNGs

Run the committed script — it picks a local `rsvg-convert` if there is one and falls
back to a throwaway Alpine container otherwise:

```sh
./brand/export-png.sh
```

Doing it by hand instead:

```sh
# librsvg
rsvg-convert -w 256  -h 256  logo-mark.svg      -o png/logo-mark-256.png
rsvg-convert -w 32   -h 32   favicon.svg        -o png/favicon-32.png
rsvg-convert -w 1280 -h 640  social-preview.svg -o png/social-preview.png

# Inkscape 1.x
inkscape logo-mark.svg      -w 256  -h 256 -o png/logo-mark-256.png
inkscape favicon.svg        -w 32   -h 32  -o png/favicon-32.png
inkscape social-preview.svg -w 1280 -h 640 -o png/social-preview.png

# Neither installed
docker run --rm -v "$PWD:/w" -w /w minidocks/inkscape \
  inkscape logo-mark.svg -w 256 -h 256 -o png/logo-mark-256.png
```

## Checking a change

Any edit to an SVG should be followed by:

```sh
for f in brand/*.svg; do
  python3 -c "import xml.dom.minidom,sys; xml.dom.minidom.parse(sys.argv[1])" "$f" || echo "BROKEN: $f"
done
./brand/export-png.sh          # and look at the result
scripts/infra-sweep.sh brand   # must print "infra-sweep: clean"
```

The parse loop was confirmed to fail on a deliberately corrupted file, so a silent
pass here means the file really did parse.
