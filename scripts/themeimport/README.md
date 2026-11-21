# themeimport

Maps a [Base16 or Base24](https://github.com/tinted-theming) scheme onto Blerg's brand
tokens (`brand/tokens.css`), applying WCAG contrast fixes where a slot would fail. Built
for BLG-474: bundling curated open-source themes into the theme picker.

## Usage

```
GOWORK=off go run . list                # id, name, license, source for every bundled theme
GOWORK=off go run . print <id>           # one theme's rendered :root[data-theme="<id>"] block
GOWORK=off go run . print-all            # every bundled theme's main block, in picker order
GOWORK=off go run . print-sessions       # every bundled theme's session-color override block
GOWORK=off go run . apply <repo-root>    # regenerate brand/tokens.css and its two copies in place
```

`apply` is the only supported way to add, remove or change a bundled theme:
`brand/tokens.css` is a generated file between the `/* BLG-474:THEMES:START */` /
`...:END */` and `SESSIONS` marker pairs, and `TestBrandTokensCSSHasNoDrift` fails if the
committed file doesn't match what regenerating from `BundledThemes` (`manifest.go`)
produces. See `docs/themes.md` for the bundled schemes' recorded, vendored licenses.

## Mapping

Base16/24's own role convention is fixed regardless of a scheme's dark/light variant:
`base00` is always the default background, `base05` always the default foreground,
`base08`-`base0F` nominally the red/orange/yellow/green/cyan/blue/purple/brown accent
slots — but not always honestly assigned by the port (see "Hue search" below).

| Blerg token(s) | Base16/24 slot |
| --- | --- |
| `--basalt` / `--scree` / `--scree-2` | `base00` / `base01` / `base02` |
| `--stone` | `base03`, lightly contrast-fixed to 3:1 (a border, not text) |
| `--fog-dim` | `base04`, contrast-fixed to 4.5:1 |
| `--chalk` (and `--fog`, blended toward the surface) | `base05` |
| `--danger` / `--blaze` / `--amber` | hue-matched red/orange/yellow among `base08`-`base0F` |
| `--lichen` / `--batch` | `base0B` / `base0D` |
| `--button-fg` | whichever of black/white wins against the (post-fix) `--blaze`; mirrored onto `--primary-white-fg`/`--runner-primary-fg`/`--chat-primary-fg`/`--knob-fg` |
| `--code-keyword` / `-string` / `-number` / `-comment` / `-title` / `-type` / `-variable` / `-builtin` | the Base16 UI-styling convention: purple / green / orange / the muted slot / blue / cyan / orange / cyan |
| ANSI terminal 0-15 | `base01` for black, `base08`-`base0F` for the 8 colors, a contrast-fixed muted ink for bright-black, `base07`/lightened-`--chalk` for white/bright-white, each color's own slot lightened ~12% for its bright variant |
| 12 session-identity swatches | the scheme's own blue/orange/purple/teal accents plus 8 hue-rotations of blue, not the shared defaults (tuned against one specific palette) |

### Hue search

A port's base08 ("red") slot is sometimes a pale gray reused for that port's own editor
syntax highlighting rather than a true red — Tokyo Night's base16 port (by Michaël Ball)
has no true red or orange in base08/base09 at all. Rather than trust slot position,
`pickByHue` searches every accent slot (`base08`-`base0F`) for whichever hue is actually
closest to the target (red for `--danger`, orange for `--blaze`, yellow for `--amber`)
and uses that one instead. For Tokyo Night this finds `#f7768e` — the port's real red,
misassigned to `base0F` — instead of the pale lavender `base08` a naive mapping would use.

## Contrast fixes

`ensureContrast` nudges a candidate color's HSL lightness (toward black or white,
whichever side of the surface it starts closer to) in fixed steps until it clears a
target ratio against the *worst* of the surfaces it can be shown on, preserving hue and
saturation. A bundled scheme is a theme someone picks for everyday use, not a lesser
tier exempt from AA, so almost every role gets the same 4.5:1 bar the three
accessibility presets hold themselves to:

- **4.5:1**: `--chalk`, `--fog`, `--fog-dim`, `--blaze`, `--lichen`, `--amber`,
  `--danger`, `--batch`, `--selected-text`, `--diff-add`/`-del`, every `--code-*` token,
  every non-black/white ANSI color (including bright-black), every `--chart-*` series
  (including idle) and the four named session-identity swatches — each against
  `--basalt`/`--scree`/`--scree-2`. `--chalk` and `--selected-text` additionally clear
  4.5:1 against the actual alpha composite of `--selected-bg`/`--blaze-soft` over every
  surface, since both inks are shown over those translucent fills too. `--button-fg`
  clears 4.5:1 against `--blaze` itself (nudging `--blaze` if neither black nor white
  reaches it alone); `--series-fg` clears 4.5:1 against the three named chart series.
- **3:1**: `--stone` (a border, not text) against every surface.

A color that already clears its target is returned unchanged — the importer never
re-colors a scheme's own choice that was already fine.

## Licensing

Every bundled scheme's actual LICENSE text is fetched directly from its repository (not
a search-engine summary) and vendored next to the scheme file
(`schemes/LICENSE-*.txt`) — MIT requires keeping the notice, and `manifest.go` names
both the palette's original designer and, where different, whoever ported it to base16.
`tinted-theming/schemes`' own LICENSE (the aggregator every scheme YAML came from) is
vendored too. A scheme with no vendoring-grade LICENSE reachable (Gruvbox, Solarized) is
left out — `manifest.go`'s own doc comment says why.

## What this importer does not do

- It does not fetch anything at runtime; scheme and license files are embedded at build
  time (`go:embed`) from `schemes/*`, checked into the repo.
- It does not guarantee pairwise chart-series distinctness (ΔE76) the way the three
  accessibility presets do — that is a different, more elaborate bar than contrast
  alone, and is not required here.
