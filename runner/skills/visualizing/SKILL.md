---
name: visualizing
description: Use whenever a chart or diagram would help someone understand what you found or built — before writing Vega-Lite or Mermaid source.
---

# Charts and diagrams

Worked templates: examples.md

## Decide first

A picture earns its place only when it answers a question text can't answer as fast. Pick by the
question, not the data shape:

- A quantity changing over time → **time series** (line/area, x is temporal).
- Parts of one whole → **breakdown** (sorted bar, or arc only for 2-6 slices).
- Several things measured the same way → **comparison** (bar, sorted by value).
- How values spread → **distribution** (boxplot, or a binned bar for a histogram).
- Steps, branches or a system's shape → **flow** (Mermaid flowchart).
- Who calls whom, in order → **sequence** (Mermaid sequence diagram).

If the finding is one number or one sentence, say it in prose instead. Don't chart a count of two.

## Chart conventions (Vega-Lite)

- **Inline data only.** `data.values` is the only data Blerg accepts; no `url`. Keep rows small —
  aggregate before charting, since Blerg refuses any `transform` (not available yet).
- **Title states the finding**, not the mechanism: "Latency doubled after the 03-04 deploy", not
  "Latency by time".
- **Axis titles carry units** ("Latency (ms)", "Requests / min"); never a bare field name.
- **Sort categorical axes** by value (`sort: "-y"` or an explicit order), not insertion order,
  unless the order itself is the point (e.g. pipeline stages).
- **Color encodes meaning**, not decoration: one series, one hue. Set no colors, schemes or
  ranges at all; the person's theme colors every series (including color-blind themes), and a
  highlighted category is shown by encoding, not by a hand-picked color. A chart with two or more
  series (two layers, say bars plus a running-total line) encodes `color` by a series field in
  each layer, so the theme gives each its own color and a legend. Without it, both layers draw in
  the same first theme color.
- **Tooltips on the fields a reader would ask about** (the exact value and its unit, the category,
  the date) — not every field in the row.
- Use one mark per chart type: `bar`, `line`, `area`, `point`, `boxplot`, `arc`, `rect`, `rule`,
  `tick`, `text`, `trail`, `square`, `circle`. Compose with `layer`/`concat`/`facet`/`repeat`, not a
  second chart.
- Blerg refuses, with a named reason: any `transform`, `impute`, `url`/`href`/`$ref`, `expr`/
  `signal`/`calculate`/`param`/`bind`/`selection`, custom `formatType`, and any string where the
  schema takes a number or one fixed word. Keep specs small (well under 128 KB) and shallow.
- Unsized single/layered charts default to 640×360 and fit their pane; a concat/facet/repeat lays
  itself out from its own panels — give those an explicit `width`/`height` per panel if cramped.

## Diagram conventions (Mermaid)

- **Short labels.** A node, participant or message label is read at a glance — a few words, not a
  sentence. Put detail in a note or a second node, not a longer label.
- **Card keys link when the whole label is exactly one**: `BLG-123`, `board:ID#123`, or a bare
  `#123` (matching how card references resolve everywhere else in chat). `Fix BLG-123` does not
  link — only `BLG-123` alone does. Write the full key; it carries the hovercard.
- A file path in a label is useful to a reader as exact, real, repo-relative text (`runner/skills/
  visualizing/SKILL.md`), but it does not become a link today — don't rely on it as navigation.
- Native flowcharts support the six standard node shapes, plain/dotted/thick
  arrows and subgraphs. Native sequences support participants, messages, notes,
  loops/alt/opt and activation. Wider Mermaid syntax uses the fallback renderer.


## Repeatable

For recurring visuals publish `.vl.json`/`.mmd`; reuse the same filename to update
its tab. Use `--live` for frequent updates without version accumulation. Start
from examples.md and keep rows bounded.

## Dashboard documents

Publish a version-1 `.dash.json`: `format: "blerg-dashboard"`, `version: 1`,
`title`, up to 32 cards with unique `id`, `kind` (stat/gauge/text/chart), `title`,
`span` (1–4), `data: {kind: "inline", rows: [...]}`. The grid has four columns
at 1024 px, two at 520 px, one below.
Never set colors, ranges, schemes, or backgrounds in embedded chart specs.
See examples.md.

## Live dashboard scalar cards

Use stable card IDs and `data: {kind: "dataset", name: NAME, parameters: {...}}`.
Stat/gauge cards select a numeric field; unknown is never zero.

- `blerg.workers`, `{}`: select `running` or `idle`, selection `latest`.
- `blerg.engine.usage`, `{engine: "codex"}`: `percent`; gauge min 0, max 100,
  unit "%". Weekly usage is sampled about every ten minutes; resets stay visible.
- `blerg.cards.merged`, `{bucket: "day", timezone: "America/New_York"}`: `count`,
  selection `latest`. Bucket is day/hour. Optional `release` requires `base`
  (revision/tag); multi-repository workspaces also name an authorized `member`.
  Counts first-parent main merge commits; excludes squash/fast-forward commits.
- `blerg.cards.time`, `{board: "BOARD_ID", card: "7"}`: known worker active
  `seconds`, or pending/unknown. Uses the viewer’s work and current Board access.
- `blerg.gates.durations`, `{suite: "total"}`: latest run wall `seconds`; other
  fields are `suite`, `outcome`, `commit`, UTC `start`/`end`. Suite is optional;
  suite durations may overlap. Missing reports are gaps, corrections replace a run.

Gate data is **reported by the agent, not verified**. Run
`blerg-runner gate report gate-report.json` to post a structured run envelope
(`run_id`, positive `revision`, commit SHA, UTC `started_at`/`finished_at`, and
`report` with `seconds`, `complete`, `suites`). `scripts/pre_merge.py` uploads its
sanitized envelope automatically inside a session. Logs/host/path metadata is
excluded; an upload failure leaves a visible coverage gap and does not change
its gate verdict. Caps: 30 requests/minute and 256 runs/session, 30-day retention.
Gate `since` defaults to seven days, clamped to 30 days; newest 77 runs are kept
in a bounded response. Freshness is the stored ingestion observation time.

### For chart cards once chunk 5 admits live charts

These schemas are available from the source service; do not bind them to a chart
until native live-chart admission ships. No fallback is silently called live.

- `blerg.gates.bins`, `{width: "60", suite: "total"}`: half-open numeric
  `start`/`end` bins with `count` (seconds; suite optional), same gate window.
- `blerg.cards.merged.heatmap`: same parameters as merges; local `day`/`hour`,
  `count`, UTC `bucket`/`end`, `timezone`. Repeated DST hours combine.
- `blerg.workers.timeline`, `{}`: opaque `worker`, UTC `start`/`end`, `status`.
  Optional RFC3339 `since` defaults to 24 hours, clamped to seven days; intervals
  clamp to the window, newest first, at most 10,000. Prior coverage is unknown
  unless a recorded status establishes it. No titles or reason prose.
- `blerg.reviews.pairs`, `{board: "BOARD_ID", card: "7"}`: `author_engine`,
  `reviewer_engine`, `reviews`, verdict counts and `high`/`medium`/`low` findings.
  Latest target/reviewer round replaces old findings. Reviews are claims;
  SHA prefixes match; ambiguous/missing delivery evidence and empty engines are unknown.

Specs contain no tokens, paths, URLs, queries or expressions. Visibility comes
from the viewing session and current source authority; sharing a dashboard does
not grant another person's usage, Git or Board authority. Visible cards subscribe;
hidden cards stop sampling. Revocation purges rows; transient failures retain
prior authorized rows as unavailable. Sequence gaps request a keyed snapshot.
Board authority is checked live on collection; immediate delivery may reuse a
successful check for one second. Source stays reachable through the utility bar.
Reduced motion disables transitions. Use clock axes within a day, dates across
days, horizontal day labels and theme colors. Refresh needs no republication.

Ready frames use `cause: "truncated"` when a row/run cap cuts coverage.
