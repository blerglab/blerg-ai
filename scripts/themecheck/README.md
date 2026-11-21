# themecheck (BLG-473)

Every color in Blerg comes from the central theme tokens (`brand/tokens.css`,
`packages/chat/src/theme/*.css`, and `packages/settings/theme.ts`'s theme list), so a theme the
owner adds later — bundled or uploaded — applies everywhere with no code change. This checker
audits `runner/frontend/src`, `packages/chat/src`, `packages/board`, `board/web/src` and
`core/web/src` for anything that bypasses that: a hex color, an `rgb()`/`hsl()` literal, a CSS
named color, or a theme ID (`dark`, `light`, `colorblind-dark`, `colorblind-light`,
`high-contrast`) named directly instead of through a token.

Run it with:

```
make lint-theme
```

or directly:

```
cd scripts/themecheck && go run . <repo-root>
```

Test files (`*.test.*`, `*.spec.*`, anything under a `test/`, `tests/` or `__tests__/`
directory) are excluded entirely. The theme-definition files themselves
(`tokenFiles`/`exemptFiles` in `main.go`) are exempt — they're the source of the tokens, not a
consumer of them.

## `var(--token, fallback)`

A fallback whose token is declared by every theme (in a base `:root { }` block, which every theme
inherits, or explicitly in every `:root[data-theme="..."] { }` block) is allowed even if the
fallback text looks like a color literal: it is unreachable once the token resolves everywhere. A
fallback naming a token that isn't defined by every theme is flagged — remove the fallback instead.

## Excusing a flagged line

Add a trailing comment with a reason:

```css
color: #ff00aa; /* themecheck:allow BLG-466, owned by the Insights rework, in flight */
```

The reason is required — an empty `themecheck:allow` marker is itself a violation.

## Excusing a whole file

Some files only *look* like they have live color literals — a multi-line template literal of
sample diagram source, say — where a trailing comment on the flagged line would land inside the
string and corrupt it. For those, put a marker anywhere in the file instead:

```ts
// themecheck:allow-file BLG-472/BLG-452, sample diagram fixtures owned by the chart renderer rework, in flight
```

Same rule: the reason is required.
