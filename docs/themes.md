# Themes

The Theme menu in Core, Runner and Board offers Follow system, Default dark, Default
light, Color-blind dark, Color-blind light and High contrast. Signed-in choices are
saved on the Core account, shared across apps and devices, and refreshed when an app
regains focus or becomes visible. Before the first account choice, the device's color
scheme applies. An unowned local pin seeds the first unset account on upgrade or sign-
in, including a choice made while signed out. Account-owned pins are cleared before
another account can adopt them. Follow system removes the pin from the document and
follows live OS changes; this preference also syncs across devices. Signed-out pages can
keep a local appearance. Keyboard arrows preview a choice; Enter or Space saves it, and
Escape cancels. Pointer selections save immediately. Writes, including first-account
seeding, run in order; rapid selections are retained. Failed saves show an error and
retain the previous saved choice.

The color-blind presets use blue/orange status and diff inks, accompanied by words,
symbols and patterns. They avoid red/green opposition for deuteranopia and protanopia;
redundant cues also carry the meaning for tritanopia and monochrome vision. They are not
a clinical simulation or a guarantee that every pair of chart series can be
distinguished by hue alone. High contrast uses black surfaces, white text and bright
borders.

## Shared tokens

Workspace colors use twelve stable named keys and None. The current swatch lives
in the card actions and Workspace panel; it opens a compact palette on the shared
popover surface. Arrow keys, Home and End save a choice; Escape closes the palette
and returns focus. Changes are authoritative after the server broadcasts them.
The command palette exposes the same names. There is no arbitrary color input.

Identity bars paint above the left card border. Selection occupies the other three
sides; state glyphs, waiting tints and unread marks remain visible. Browser tests
cover every card state and combinations, plus collapsed tiles, in every preset.
Every ink meets 3:1 against the rendered card backgrounds. The four-column palette
orders neighboring inks for at least 10 ΔE76 under full-severity Machado protanopia,
deuteranopia and tritanopia simulation, and ordinary vision. ΔE76 measures lightness
as well as hue; the measured minimum across all presets and simulations is 10.33.
Contrast also covers the brightest point in the animated waiting sweep. This is
a neighboring-swatch distinction, not a claim that all pairs
of twelve inks remain distinguishable. Names and selected rings provide extra cues.
The original four colors also retain their stronger pairwise floor of 15 ΔE76
in the accessible presets (5 in the defaults), including every simulated mode.

`brand/tokens.css` owns app, status, selection, diff, syntax and terminal colors. Runner
and Board import it directly; Core embeds a byte-identical copy at
`core/internal/landing/static/tokens.css`. Chat ships another byte-identical copy as
`src/theme/brand.css`, imported by its Blerg adapter and copied into its package.
`TestThemeCopiesAndConsumers` guards these copies and imports.

Chat's Blerg syntax scheme resolves the shared `--code-*` tokens. Existing explicit
syntax schemes and per-token overrides remain personal choices. Charts and Mermaid read
the inherited chat palette; terminal colors resolve the same root tokens. Saved terminal
history retains its legacy palette in the two defaults and follows the shared ANSI
palette in the accessibility presets. Activity chart series also use shared named
tokens. Standalone chat consumers opt into these presets by loading
`@blerglab/chat/blerg.css` and setting `data-theme` on their document root. They own
account sync themselves.

## Contrast and redundant cues

`core/internal/landing/theme_test.go` calculates WCAG relative luminance and checks text
contrast on all three app surfaces. All five check primary and secondary text. The three
accessibility presets additionally check muted, accent, status, selection, diff and
syntax text. Their button foregrounds are chosen for the accent fill; meaningful borders
use contrasting ink.

Default palettes retain their previous values, guarded by frozen token and resolved
component fixtures captured from main (ab133ab3). `node scripts/test-default-theme-
colors.mjs` checks explicit and OS defaults for primary buttons, the settings knob, chat
accent text, Session Insights segments, Activity Insights series and Board diffs. The
focused Runner theme browser test freezes Mermaid and Vega palettes. Default dark Board
diff text retains its historical #b9d49a/#e2a3a3; Default light deliberately keeps the
corrected theme-aware diff inks. Default button foregrounds, chart series and segment
text are exempt from the new 4.5:1 checks. Their legacy muted/status/accent colors do
**not** all meet 4.5:1 on every surface (for example, Default light's muted text on its
app background is about 4:1). These tests do not certify every rendered control or
arbitrary user-authored chart as AA. Changing those default inks would conflict with
preserving their appearance.

The red/green audit covers:

| Surface | Second cue |
| --- | --- |
| Diff lines and counts | Literal `+`/`−`, line placement, added/removed counts |
| Git badges | `A`, `M`, `D`, `R`, `?` codes with status labels |
| CI/test status and state chips | Named states and pass/fail icons or counts |
| Usage meters | Percentages, spoken warning/high/near-limit labels; accessibility presets add diagonal stripes at 80%, vertical stripes at 90% and a persistent `!` at 95% |

Focused browser checks exercise all five presets at 390 and 3840 px in all three apps.
The Runner check also verifies inherited syntax, terminal, chart and Mermaid palette
resolution and the usage patterns.

## Account API

`GET /api/me/theme` returns `{"theme":""}` before a choice, otherwise one of `system`,
`dark`, `light`, `colorblind-dark`, `colorblind-light`, `high-contrast`. `PUT
/api/me/theme` accepts exactly `{"theme":"<preset>"}`. Core migration 110 stores the
value on the account; the browser cannot name another account.

Runner and Board relay this fixed path to Core with the original bearer and their fixed
audience. This Core endpoint alone accepts the three first-party audiences; it validates
the token, human kind, normal account capability and live human session. There is no
CORS grant, general audience override or agent write path. Redirects, oversized
bodies/responses and unavailable Core fail closed. Responses are not cached. The pre-
paint local cache is only an appearance hint; a successful account read replaces it,
while an unset account adopts a valid local pin. Each origin caches its own first-paint
hint; a new origin follows the OS until its authenticated account read completes.

The regression checks also cover distinct activity series, chart text, session identity
colors, terminal colored inks and selection/accent tints. ANSI black and white are
checked against each other because both can serve as backgrounds. The default terminal
bright-black ink retains the historical muted-ink exception for the owner. Color-blind
warning/error, blue/purple and diff pairs must have CIE Lab ΔE76 of at least 15 after
full-severity Machado protanopia, deuteranopia and tritanopia transforms. This is a
regression threshold, alongside the labels and patterns, rather than a clinical
guarantee.

Chart series require pairwise CIE Lab ΔE76 ≥ 5 in the frozen defaults and ≥ 15 in each
accessibility preset, in plain vision for all presets, and also under all three full-
severity Machado transforms for the accessibility presets. Stacked Activity and Session
Insights segments repeat distinct textures in their named legends in accessibility
presets. The thresholds detect regressions; textures, labels and values remain
necessary. Default Mermaid and Vega palettes retain the historical accent-plus-syntax
sequence.

## Bundled open-source schemes (BLG-474)

The theme picker also offers five curated Base16 color schemes, each ported onto every
token above by `scripts/themeimport` (`go run . print-all` / `print-sessions`; see that
package's own tests and `README.md` for the token mapping). They hold the exact same
WCAG bar the three accessibility presets do — `core/internal/landing/theme_test.go`'s
`bundledThemes` list runs the same extended battery colorblind-dark/light and
high-contrast get (every status, accent, chart, terminal and syntax ink at 4.5:1, chart
marks and session colors at 4.5:1 against every surface, the primary-button foreground
contrast-fixed against the actual accent fill) — not a lighter "aesthetic" tier. Only
Default dark/light, frozen legacy palettes that predate BLG-472, keep the lighter check.

| Theme | id | Palette design | Base16 port | License (confirmed) |
| --- | --- | --- | --- | --- |
| Catppuccin Mocha | `catppuccin-mocha` | [catppuccin/catppuccin](https://github.com/catppuccin/catppuccin) | self-ported | MIT, Copyright (c) 2021 Catppuccin — fetched and vendored directly: `scripts/themeimport/schemes/LICENSE-catppuccin.txt` |
| Catppuccin Latte | `catppuccin-latte` | [catppuccin/catppuccin](https://github.com/catppuccin/catppuccin) | self-ported | MIT, Copyright (c) 2021 Catppuccin — same vendored file |
| Dracula | `dracula` | [dracula/dracula-theme](https://github.com/dracula/dracula-theme) | clach04, contributed to tinted-theming/schemes | MIT, Copyright (c) 2023 Dracula Theme — fetched and vendored directly: `LICENSE-dracula-theme.txt` |
| Nord | `nord` | [nordtheme/nord](https://github.com/nordtheme/nord) (Sven Greb, formerly published as Arctic Ice Studio) | self-ported | MIT, Copyright (c) 2016-present Sven Greb — fetched and vendored directly: `LICENSE-nord.txt` |
| Tokyo Night | `tokyo-night` | [enkia/tokyo-night-vscode-theme](https://github.com/enkia/tokyo-night-vscode-theme) | **Michaël Ball** (the YAML's own `author` field), contributed to tinted-theming/schemes — an earlier round of this card wrongly credited enkia as the porter | MIT, Copyright (c) 2018-present Enkia (original palette) — fetched and vendored directly: `LICENSE-tokyo-night-vscode-theme.txt`; the base16 port itself is covered by tinted-theming/schemes' own MIT license (below) |

Every vendored LICENSE file above was fetched directly from the named repository (not a
search-engine summary) and contains the real copyright notice MIT requires keeping;
`scripts/themeimport`'s `TestBundledSchemesVendorARealLicenseNotice` proves each one is
present and is the actual full license text, not just a claim in `manifest.go`.

Palette values themselves come from the [tinted-theming/schemes](https://github.com/tinted-theming/schemes)
repository, which republishes each upstream scheme as a standard Base16 YAML file; its
own LICENSE (MIT, Copyright (c) 2022 Tinted Theming) is vendored too:
`scripts/themeimport/schemes/LICENSE-tinted-theming-schemes.txt`.

Two themes were considered and left out, both for licensing, not contrast:

- **Gruvbox** (morhetz/gruvbox): no vendoring-grade LICENSE text was reachable. The repo
  has no LICENSE file, only a terse "MIT/X11" mention in the README with no copyright
  notice to vendor. A different, unrelated fork's LICENSE.md (gruvbox-community/gruvbox,
  copyright Pavel Pertsev) does not cover the original colors this import would have
  used, so it wasn't substituted in.
- **Solarized** (Ethan Schoonover): only indirect, inconsistent secondary evidence for
  its license was reachable, no direct LICENSE file.

Either can be bundled later if a confirmable LICENSE file turns up.

The importer maps Base16's fixed role convention (`base00` is always the background,
`base05` always the default foreground, `base08`-`base0F` the accent slots, regardless
of the scheme's own dark/light variant) onto every surface, text, accent, diff, syntax
and terminal token the existing dark/light blocks declare. A port's own slot assignment
isn't always trustworthy, though: Tokyo Night's base16 port has no true red or orange in
base08/base09 (they're pale lavender-grays, reused for that port's own syntax
highlighting), so the importer searches every accent slot (`base08`-`base0F`) for
whichever hue is actually closest to red/orange/yellow and uses that instead — Tokyo
Night's `--danger`/`--blaze` resolve to `#f7768e`, the port's real red (misassigned to
`base0F`), not the pale `base08` slot a naive port-literal mapping would have used.

Every role that stands in for text, a status label, a chart mark, a terminal color, a
session-identity swatch or a button-on-accent foreground is contrast-fixed to at least
4.5:1 against every surface it can be shown on — chalk and selected-text specifically
also clear 4.5:1 against the actual translucent-fill composite of `--selected-bg`/
`--blaze-soft`, since both inks are shown over those fills too; `--button-fg` (and the
`--primary-white-fg`/`--runner-primary-fg`/`--chat-primary-fg`/`--knob-fg` tokens that
mirror it, the same redirection the accessibility presets use instead of a fixed `#fff`)
is picked as whichever of black or white wins against the theme's own `--blaze`, nudging
`--blaze` itself if neither extreme reaches 4.5:1 on its own. A scheme's own color is
used unchanged wherever it already clears the bar; `scripts/themeimport`'s own tests
assert this for all five bundled schemes, and the Go landing tests re-check the
generated blocks the same way they check every other theme.

`brand/tokens.css` is a **generated file** for the two sections BLG-474 owns (marked
`/* BLG-474:THEMES:START */.../* BLG-474:THEMES:END */` and the matching `SESSIONS`
pair): hand-editing inside those markers is pointless — `scripts/themeimport`'s
`TestBrandTokensCSSHasNoDrift` regenerates from the schemes and fails the build if the
committed file doesn't match. To add, remove or re-tune a bundled theme:

```
cd scripts/themeimport
# edit map.go / session.go / manifest.go / schemes/*.yaml as needed, then:
GOWORK=off go run . apply ../..
```

`apply` regenerates both marked sections in `brand/tokens.css` and copies the result
over `packages/chat/src/theme/brand.css` and `core/internal/landing/static/tokens.css`
in place — the only supported way those three files change.
