# Blerg's own chart renderer (BLG-452)

*2026-11-03. Design only — no product code in this round. Phase 1 of BLG-452.*

*Revised 2026-11-03 after design review round 1 (H1–H6, M7–M10, L11) and BLG-451's
renderer-contract addendum, then revised again after design review round 2 (a narrower pass
fixing: the differential gate's build-ability, by moving it into a real browser through a
test-only seam into Vega's `View`; the classifier's allow-list, rewritten as an explicit,
buildable, path-grammar table; reusing `d3-array`'s own tick generation; roving focus per series
rather than per point; making the 300ms/150ms budget a real CI gate; and boxplot's config/sub-mark
coverage). The owner's full scope is kept throughout: nothing here cuts a required mark or
feature. Correctness comes from a conservative, static fallback — anything not fully implemented
and not yet proven against Vega falls back to the existing Vega path, rather than being drawn
approximately or left out of the card.*

## The problem, in one line

Today every Vega-Lite chart an agent writes is drawn by shipping the spec into a Web Worker,
compiling it with the real `vega`/`vega-lite` libraries, rasterizing the result to an inert SVG,
then copying that SVG onto a `<canvas>` with a hand-rolled tooltip hit-test (`vegaRuntime.ts`,
`vega.ts`, `vegaWorker.ts`). It is safe and it works, but it is not ours: the look, the axis and
legend layout, and the interaction model are Vega's defaults filtered through our theme config
(`vegaTheme()` in `theme.ts`), not a renderer designed around Blerg's theme tokens, density and
accessibility needs. BLG-452 replaces the *drawing* with our own code while keeping Vega-Lite as
the format agents write and Vega as insurance for whatever we don't draw ourselves yet.

## 0. The renderer contract (BLG-451)

BLG-451's review changed `renderVisual`'s return shape for every renderer behind it. BLG-451 owns
this contract; this design conforms to it rather than inventing a parallel one:

```ts
export interface RenderedVisual { svg: string; width: number; height: number; dispose: () => void }
export async function renderVisual(kind, source, container, theme, signal): Promise<RenderedVisual>
```

A successful render now **resolves** with a `RenderedVisual` instead of a bare cleanup function.
A terminal failure still **rejects** (throws) exactly as today — only the success shape changed;
the reject path that drives `VisualBlock.tsx`'s `visual-failed` state is untouched. `svg` is the
sanitized SVG string that was actually drawn (already produced today on the Vega path via
`sanitizeSvg(drawing.svg, ...)` before it's rasterized to canvas — returning it is not new work
on that path, just returning a value that already exists). `width`/`height` are the drawn size in
CSS pixels. `dispose` replaces today's bare cleanup callback. Export/download (case 14 in §2)
reads `svg` directly, the same way regardless of which engine drew the chart — there is no
per-renderer export path.

For our renderer, "its live DOM is built from or matches that SVG" (the addendum's wording) is
satisfied literally by construction: our drawing code builds one canonical SVG string first
(§3.2's `svg.ts`), and the live, interactive, accessible DOM is then built **from** that exact
string via `DOMParser` — the same parse step `policy.ts`'s `sanitizeSvg` already performs — with
accessibility affordances (`role`, `tabindex`, `aria-live`, keyboard handlers, §3.5) layered onto
the parsed nodes afterward. There is one drawing pass, not two, so the exported `svg` and the
on-screen DOM can never visually disagree.

Updating `VisualBlock.tsx`, `VisualThumb`, `FullScreen` and the Mermaid path to this contract is
BLG-451's work, out of this card's scope — this design only needs its own renderer, and the
`vega-lite` branch of `render.ts`'s dispatcher, to emit the new shape correctly.

## 1. The failure rule

**What a failure here may cost:** a chart does not render, or renders wrong (wrong marks, wrong
colors, broken layout, a crash in the renderer's code).

**What must never happen:** a chart failure must never break the surrounding surface. Concretely:

- A message in chat still renders every other block; only the chart's own area shows a fallback.
- A file in the file viewer still opens, scrolls, downloads and can be reviewed; only the preview
  pane shows a fallback.
- A tab holding a chart (BLG-451) still opens, closes and retains its position in the tab strip;
  only its content area shows a fallback.
- The chat composer, the session, and every other open tab are completely unaffected — a chart
  failure has no blast radius outside its own block.

**The fallback ladder**, in order:

1. **Our renderer.** Used whenever the spec is *provably* in the subset we draw — provably
   meaning: every key at every path in the spec is on an explicit allow-list (§3.3), and (for
   marks/features that touch time, stacking, binning or geometry) that allow-list entry has
   already passed the differential gate against Vega (§3.10). Anything not on the allow-list, or
   on it but not yet differential-tested, is not "ours" yet — see §3.3/§3.10; this is a scope
   deferral inside the fallback ladder, never a removed feature.
2. **The existing Vega path** (`vega.ts`/`vegaRuntime.ts`/`vegaWorker.ts`), unchanged except for
   also returning `RenderedVisual` (§0), used for any spec outside our subset, and as the runtime
   fallback if our renderer throws after classification said it could draw the spec (an internal
   bug, not a policy refusal — policy refusals happen before either renderer runs and are
   terminal, see §3.3).
3. **The existing "Couldn't draw this chart" failure panel** (`VisualBlock.tsx`'s `visual-failed`
   state), used when *both* renderers fail or policy refuses the spec outright. This already shows
   the closed source (`CodeBlock closed`) so the person can see what was sent, never a blank hole.

A chart can therefore fail twice (ours, then Vega) before falling to the existing closed-source
panel, which was already the terminal failure state and is unchanged by this card. Nothing new is
introduced at the bottom of the ladder; BLG-452 only inserts a new first rung.

**Visible to tests, not to people:** which renderer actually drew a given chart must be
queryable from a DOM attribute (e.g. `data-chart-renderer="blerg"` / `"vega"` on the chart host)
so tests can assert "this known-good spec used ours" and "this unmapped spec fell back", but the
attribute carries no visible label, color or icon — a person sees one chart, drawn well, not a
badge announcing which engine drew it. (Echoes the owner's "0 visible commits · partial" lesson:
internal renderer state is not person-facing.)

## 2. The failure matrix

Layers follow `docs/testing-audit.md`'s doctrine (1 contract, 2 implementation, 3a canned
cross-service, 3b real services, 4 end to end, 5 staging). This is a design doc; the "test"
column names the test to write during implementation, not a test that exists yet.

| # | Failure case | Expected behavior | Layer | Proving test |
|---|---|---|---|---|
| 1 | Spec uses a mark/key/value fully implemented and differential-gated (§3.3, §3.10) | Our renderer draws it; `data-chart-renderer="blerg"` | 2 | `blergChart.render.test.ts`: renders each gated mark kind, asserts the attribute and SVG/DOM shape |
| 2 | Spec uses `transform`, `params`/`selection`/`bind`, or any key `policy.ts`/`vegaPolicy.ts` already refuses | Refused before either renderer runs; closed-source panel; refusal names the key and path | 1/2 (shared with existing policy) | existing `policy.security.test.ts`/`vegaPolicy` suite |
| 2b | **Policy change:** spec uses `mark: "boxplot"`, `config.boxplot`, or a boxplot sub-mark override (`box`/`median`/`outliers`/`rule`/`ticks`) | Today all three are refused (`boxplot` is absent from `vegaPolicy.ts`'s `MARKS` set, so `mark`, `config.boxplot`, and any nested boxplot shape are all unreachable) — refused before classification ever runs. This card changes that: `boxplot` is added to `MARKS`, with the same inline-data-only limits as every other mark (no new data shape, no transform, same `MAX_ARRAY`/`MAX_NODES` bounds). Adding it to `MARKS` also makes `config.boxplot` legal for free, since `checkConfig`'s existing `MARKS.has(key)` branch already generalizes over every mark name; boxplot's own sub-mark override objects (`box`/`median`/`outliers`/`rule`/`ticks`, each `true`/`false`/a nested mark-properties object) are not a new code path either — they already fall through `checkValue`'s existing generic per-key recursion the same way any other mark-property object does | 1 (policy) | new adversarial cases added to `policy.security.test.ts`: (a) `mark: "boxplot"` accepted with the existing inline-data/node/array limits unchanged; (b) `config.boxplot` accepted via the existing generic branch; (c) each sub-mark key accepted as a literal object; (d) an adversarial nested case, e.g. `outliers: {tooltip: {expr: "..."}}`, proving the existing `EXPRESSIONS`/`signal` refusal still fires at that depth, not just at the top level |
| 3 | Spec is policy-valid but *any single key at any path* isn't on §3.3's allow-list, or is on it but not yet past §3.10's differential gate | Classifier returns `fallback` for the whole spec; existing Vega path draws it; `data-chart-renderer="vega"`; person sees a normal chart, no visible difference in chrome | 2 | `classify.test.ts`: one table row per §3.3 key family, each with an accepted case and a fallback case |
| 4 | Our renderer throws while drawing a spec the classifier accepted (an internal bug) | Caught at the render boundary; **container is cleared and any listeners/observers our partial attempt attached are detached** before importing and invoking Vega for that same spec (no stray DOM, no duplicate tooltips/leaked listeners); if Vega also throws, closed-source panel; the internal-bug case is logged (cause and spec shape, no raw agent content beyond what's already shown), per "log every failure" | 2 | `render.test.ts`: inject a throwing mark renderer via test seam; assert the container holds *only* Vega's DOM afterward and that no handler from the aborted attempt still fires |
| 5 | Vega fallback itself throws or times out (existing `HEAVY_CHART` path) | Existing closed-source panel; unchanged | 2 (existing) | existing `vega.worker.test.ts` stuck-worker case |
| 6 | Dense data at the policy ceiling (10,000 points — `MAX_ARRAY`, §3.6) | Draws within the **committed** 300ms/150ms gate (§3.6) — not a soft target — enforced at exactly the 10,000-point cap | 2 | `BlergChartDensity.performance.browser.test.tsx`, in runner/frontend's `browser-performance` Vitest project (real Chromium via `@vitest/browser-playwright`, the same project `CHART_DEADLINE_MS`-style budgets already live in), run on the CI runner with its hardware recorded in the test file |
| 7 | A spec our classifier would otherwise accept exceeds our own (smaller than policy's) budget — e.g. too many marks once panels/layers multiply | Budget check happens *inside classification*, before either renderer draws anything; result is `fallback`, **never a thrown error of our own** — `ChartRefused` stays reserved for the shared policy layer. Vega draws it with its own, already-enforced `CHART_LIMITS`, which may itself refuse (existing case 5) | 2 | `blergBudget.test.ts`: one row per limit, each asserting `classifyChart` returns `'fallback'`, never a throw |
| 8 | Theme changes (light ↔ dark, token edit) while a chart is mounted | Redraws in place using the same `observeVisualTheme` hook already wired in `VisualBlock.tsx`; no flash of unstyled/wrong-theme content held past one animation frame | 2 | extend existing `theme.test.ts` with a "blerg renderer repaints on theme change" case |
| 9 | Keyboard-only person tabs into a chart | Chart root is `role="group"` (not `role="img"`, see §3.5/L11) containing a `role="list"` of **one focusable target per series** (not per point, §3.5/M-round-2); Tab/Shift+Tab moves between series targets; arrow keys inside a focused series move an internal point cursor (no new focusable DOM node per point) whose value is announced via `aria-live="polite"` | 2 | `accessibility.test.ts`: simulated keyboard traversal asserts one focusable node per series (not per point, including on a 10,000-point series), focus order, `role`s and announced text |
| 10 | Screen-reader person, no pointer | Chart root has an accessible name describing the chart (title + mark + series count, not "chart"); the data-table alternative (§3.5/M9) is reachable and shows exactly the drawn (post-aggregation/binning) rows, truthfully captioned — this table, not the series-level roving targets, is how a screen-reader person reaches every individual point | 2 | `accessibility.test.ts`: asserts `aria-label`/`aria-describedby`, the table's row count, and the caption text |
| 11 | Touch-only person (phone, 390px) | Tap shows/moves the tooltip the same way hover does on desktop; tooltip never exceeds the viewport | 3a (real layout) | `runner-browser` case alongside existing responsive chart screenshots |
| 12 | Chart inside a narrow tab pane inside a very wide window (3840px monitor, narrow split) | Renders at the pane's width, not the window's; matches the "check narrow panes inside wide windows" rule in `runner/skills/ux-design/SKILL.md` | 3a | `runner-browser` responsive case, mirrors existing `ResizeObserver` ArtifactViewer coverage |
| 13 | A legitimate dense legend (e.g. 200 categorical series) | Legend overflows gracefully (scroll or "+N more"), never overlaps the plot area or pushes it off-canvas | 2 | `legend.test.ts` |
| 14 | A chart is exported/downloaded (BLG-451's download flow) | Export reads `RenderedVisual.svg` directly (§0), the same code path whichever engine drew it; never a blank/placeholder file | 2/3a | extends existing `ArtifactViewer` download test with a chart artifact case |
| 15 | A spec classified `'blerg'` turns out heavier than the static size threshold predicted (§3.7/M7) | Drawing happens in the terminable worker (not the main thread) whenever the classifier's own point/mark count is at or above the static threshold; the worker's existing hard wall-clock deadline (mirroring `CHART_DEADLINE_MS`) aborts and falls back to Vega, exactly like the existing stuck-worker case | 2 | `render.test.ts` fake-timer case, modeled on the existing `vega.worker.test.ts` stuck-worker test |
| 16 | A temporal field/timeUnit/format outside §3.4b's matched subset (e.g. `week`, `quarter`'s fiscal boundary, `binnedutc*`) | Classifies as `fallback`; Vega draws it with correct, library-backed time semantics | 2 | `classify.test.ts` temporal rows, one per timeUnit family |
| 17 | A temporal field/timeUnit/format **inside** §3.4b's matched subset, under different host timezones | Our renderer's drawn tick values, labels and point positions match Vega's reference output within §3.10's tolerance, in every pinned timezone (`UTC`, `America/Los_Angeles`, `Asia/Kolkata`, `Pacific/Chatham`) | 2 (real-browser differential) | `BlergChartDifferential.browser.test.tsx` (§3.10), run once per pinned `TZ` (a JS process's timezone is fixed at startup, so this is one test file re-run under several `TZ` values, not one test switching zones at runtime) |
| 18 | A shipped mark/feature's layout math regresses (a later change to `scales.ts`/`axes.ts`/`layout.ts` disagrees with Vega beyond §3.10's tolerance) | CI fails before merge; the regression is caught at development time, not seen by a person | 2 (real-browser differential) | `BlergChartDifferential.browser.test.tsx`, in runner/frontend's `browser` Vitest project |

Case 2 and 5 are existing, unmodified behavior included here only so the matrix is read as
complete end to end — BLG-452 does not touch the refusal policy's existing marks or the Vega
fallback's own budget engine. Case 2b is the one deliberate policy change this card makes, called
out as such rather than folded silently into "stays as it is" (§3.1).

## 3. The design

### 3.1 What stays exactly as it is — and the one thing that doesn't

- **The format.** Agents keep writing Vega-Lite. No new chart DSL.
- **The safety boundary, with one named exception.** `policy.ts` (Mermaid) and `vegaPolicy.ts`
  (`parseVegaLite`, `ChartRefused`, `MAX_CHART_SOURCE`, `MAX_ARRAY`, `isCompoundChart`) do not move
  and do not get a second, parallel validator. Both renderers draw the *same* already-validated,
  already-sized spec that `parseVegaLite` returns today (inline data only, no
  transforms/params/selections/bindings, no URLs, bounded node/array/mark/panel counts) — **with
  one explicit change**: `vegaPolicy.ts`'s `MARKS` set gains `'boxplot'` (case 2b above), because
  the card requires boxplot as a mark and today's policy refuses it outright, before either
  renderer or the classifier ever sees a spec that uses it. This is a policy change, stated here
  plainly rather than assumed; it ships with its own adversarial security tests (case 2b) proving
  the existing inline-data-only limits apply to boxplot exactly as they do to every other mark —
  no new data shape, no transform, no widened array/node bound.
- **`render.ts`'s role as the dispatcher**, now returning `RenderedVisual` (§0). It already picks
  Mermaid vs. Vega-Lite by `kind`; it gains one more decision *inside* the `vega-lite` branch (ours
  vs. fallback), not a new top-level branch. Callers (`VisualBlock.tsx`, `ArtifactViewer.tsx`'s
  `TextView`) read the new shape — that update is BLG-451's, not duplicated here.
- **`VisualBlock.tsx` / `VisualThumb` / `FullScreen`.** Chrome (Source/Copy/Open, the full-screen
  dialog, the `visual-failed` panel) is renderer-agnostic today and stays that way: it already
  only cares about `kind` (`'mermaid' | 'vega-lite'`) and the resolved render result, never about
  *which* Vega-Lite renderer drew the content.
- **`theme.ts`'s `readVisualTheme`/`observeVisualTheme`/`VisualTheme`.** Both renderers consume the
  same resolved `VisualTheme` (fg, muted, surface, surface2, border, accent, palette, fontFamily,
  fontSize, dark). `vegaTheme()` stays as Vega's own mapping for the fallback path; our renderer
  gets an equivalent mapping (§3.9) consuming the identical `VisualTheme` object — one resolver,
  two consumers.
- **Tabs and the file viewer.** `visualKind.ts`'s `visualFence`/`visualFile` detection, the
  `artifact` tab kind in `useSessionTabs.ts`, and BLG-451's download flow all key off `VisualKind`
  and a source string; none of them know or need to know which engine draws a `'vega-lite'`
  block. BLG-452 needs zero changes there.

### 3.2 What's new

A sibling module next to `vega.ts`, under `packages/chat/src/visual/blerg/` (name chosen to read
as "our chart renderer" without colliding with `vega*`):

```
visual/blerg/
  classify.ts      // spec -> 'blerg' | 'fallback'; the full key-family allow-list walk (§3.3),
                    // also computing point/mark/panel counts for the budget check (§3.6/M10)
                    // and the worker-threshold decision (§3.7/M7) in the same pass
  render.ts        // draws a classified spec; owns the worker handoff; returns RenderedVisual (§0)
  marks/           // one file per mark family: bar, line+area, point, rule, text, arc, rect, boxplot
                    // (tick and trail are a firm decision, not an open question: both stay on the
                    // Vega fallback path for v1 — neither is in the card's required mark list)
  scales.ts        // linear/log/time/band/ordinal scale construction from encoding + domain
  axes.ts          // axis layout: ticks, labels, gridlines, title placement
  legend.ts        // legend layout: categorical swatches, continuous ramps, overflow ("+N more")
  layout.ts        // composition: layer (overlay), concat/hconcat/vconcat (side by side), facet, repeat
  svg.ts           // builds the one canonical SVG string from layout output (§0); shared by marks/axes/legend
  tooltip.ts       // hover/touch tooltip, shared hit-testing model
  accessibility.ts // builds the live DOM from the SVG string + point descriptors (§0); role=group/list,
                    // roving-tabindex keyboard model, aria-live, the data-table alternative (§3.5/M9)
  theme.ts         // VisualTheme -> our renderer's internal style tokens
  budget.ts        // the limits from §3.6, consulted by classify.ts; never thrown as an error (M10)
  worker.ts        // the terminable worker entry point (mirrors vegaWorker.ts); returns
                    // {svg, width, height, points} — points are per-point descriptors (value,
                    // label, SVG coordinates) that tooltip.ts, the per-series roving cursor and
                    // the data table read (§3.5) — not one focusable DOM node per point, and
                    // not a second drawing pass
  format.ts        // thin wrapper around the shared d3-format / d3-time-format imports (§3.4b/3.8)
```

This mirrors the existing `vega*` file split (`vegaPolicy` / `vegaRuntime` / `vegaWorker` /
`vegaBudget` / `vegaResources`) deliberately, so a reviewer already familiar with the Vega path
can map each concern 1:1.

`render.ts` (the dispatcher) becomes:

```ts
export interface RenderedVisual { svg: string; width: number; height: number; dispose: () => void }

export async function renderVisual(kind, source, container, theme, signal): Promise<RenderedVisual> {
  if (kind === 'mermaid') { ... unchanged (its own conformance to this contract is BLG-451's) ... }
  const spec = parseVegaLite(source)                 // unchanged: the one safety gate
  const classified = classifyChart(spec)              // new, static, no rendering
  if (classified === 'blerg') {
    try { return await renderBlergChart(spec, container, theme, signal) }
    catch (error) {
      if (error instanceof AbortError) throw error     // real cancellation, not a fallback case
      container.replaceChildren()                      // M8: clear any partial DOM/listeners
      // (any observers/handlers our partial attempt attached are torn down here too)
      // internal renderer bug on an accepted spec: fall back rather than show nothing (case 4)
    }
  }
  return (await import('./vega')).renderVegaLite(spec, container, theme, signal)  // also returns RenderedVisual
}
```

Case 4's `catch` is the only place our renderer and Vega's can both run for one spec; it is a
safety net for a bug, not the ordinary path, so it's unobserved by the classifier, and it always
clears the container before Vega draws (M8) — the two renderers' DOM and event listeners must
never coexist.

### 3.3 Classification: a buildable allow-list, static and deterministic, never by trial rendering

`classifyChart(spec: Record<string, unknown>): 'blerg' | 'fallback'` runs on the object
`parseVegaLite` already returns (post-validation, with its defaulted `width`/`height`/`autosize`),
does no rendering, allocates no scene graph, and is pure: same spec in, same answer out, every
time, on every machine. **Any key, value shape, or combination not on the allow-list below routes
the whole spec to `'fallback'`.** There is no implicit "probably fine" case.

#### Path grammar

The table uses a JSON-pointer-style path, so a row names exactly where it applies rather than just
a bare key name (several keys, e.g. `size`/`tooltip`, mean different things at different paths —
see rows 4 and 9):

- `/a/b` — the literal key `b` nested in `a`.
- `*` — any single key or array index at that position (e.g. `/encoding/*/scale` = `scale` under
  any encoding channel; `/layer/*` = any element of a `layer` array).
- The grammar is written **per spec node**, not as one absolute tree from the document root. Every
  node that has its own `mark` (a "unit spec") is checked against the **unit-spec rows** (1–18
  below), wherever it sits in the tree. Every node that composes other specs (`layer`, `concat`,
  `hconcat`, `vconcat`, `facet`, `repeat`) is checked against the **composition rows** (20), which
  say how to recurse into named child nodes (`/layer/*`, `/facet/spec`, `/repeat/spec`, …) — each
  child is then itself a unit spec or a further composition, checked from its own root. Rows 19, 21
  and 22 apply only at the whole document's own root (`top`, the same boolean `vegaPolicy.ts`'s
  `checkSpec(spec, path, depth, top, ...)` already threads through its walk — reused, not
  re-derived).

#### Inheritance (how a child node's context is resolved before its own rows are checked)

- **`layer` children inherit the parent's `encoding`, merged under their own** (child keys
  override parent keys key-for-key) — this mirrors `vegaPolicy.ts`'s `checkSpec`'s existing
  `{...parentEncoding, ...childEncoding}` merge, for `layer` specifically.
- **`concat`/`hconcat`/`vconcat` children do not inherit the parent's `encoding`** (they start from
  `{}`) — `vegaPolicy.ts` already treats concat-family children this way; classify.ts must not
  treat them like layer children.
- **Every composition kind inherits the parent's `data`** when a child sets none of its own
  (`data.values`/`data.name`) — unlike encoding, this rule is the same for all six composition
  keys, mirroring `vegaPolicy.ts`'s `rows = ... : parentRows`.
- **`repeat` substitution:** a `{"repeat": "row"|"column"|"layer"}` field placeholder (row 18) is
  allowed as an opaque literal; which concrete field name it becomes is resolved at draw time, not
  at classify time.
- **`facet`/`repeat`'s inner `spec`** is an ordinary unit/composition node, checked from its own
  root like any other.
- **No second implementation of this merge.** This exact logic already exists once, inside
  `vegaPolicy.ts`'s `checkSpec`. `classify.ts` must not re-derive a parallel copy that can drift
  from it — either `vegaPolicy.ts` exports a small `resolvedChildContext(...)` helper both
  `checkSpec` and `classify.ts` call, or the walk is written as one shared tree-walking utility
  both import. A shared test, fed one corpus of composed specs, asserts both call sites resolve
  identical encoding/data for every node.

#### The allow-list

| # | Path | v1 handles | v1 falls back |
|---|---|---|---|
| 1 | `/mark` (string form) | `bar`, `line`, `area`, `point`/`circle`/`square`, `rule`, `text`, `arc`, `rect`, `boxplot` (after §3.1's policy change) | nothing else is reachable — `vegaPolicy.ts`'s `MARKS` set is the outer bound; this row re-checks independently, defense in depth |
| 2 | `/mark/type` (object form) | same enum as row 1 | same as row 1 |
| 3 | `/mark/<property>` | a fixed, per-mark enumerated list in that mark's own `marks/*.ts` (e.g. `opacity`, `cornerRadius`, `strokeWidth`, `filled`, `point` overlay on line/area, `align`, `baseline`, `innerRadius`/`outerRadius`/`radius` on arc) | any property key not in that mark's own list (e.g. an `interpolate` curve beyond `linear`/`monotone`/`step*`) |
| 3b | `/mark/box`, `/mark/median`, `/mark/outliers`, `/mark/rule`, `/mark/ticks` (boxplot sub-mark overrides) | `true`/`false`, or an object checked recursively against row 3's own allow-list (so `outliers.color` is handled the same as a point mark's `color`) | anything nested there that row 3 wouldn't otherwise allow — already structurally unlikely given policy's generic literal walk, re-checked here as defense in depth (§3.1/case 2b) |
| 4 | `/mark/size`, `/mark/tooltip` (mark-property forms) | `/mark/size`: a literal number. `/mark/tooltip`: a boolean enabling the default hover tooltip | — (distinct from the encoding-channel forms in row 5/9, by path, not by key name alone) |
| 5 | `/encoding/<channel>` for `<channel>` ∈ `x`, `y`, `x2`, `y2`, `theta`, `radius`, `color`, `fill`, `stroke`, `opacity`, `size`, `detail`, `tooltip`, `text`, `order` | a field-def (`field`+`type`, row 6), a `datum`/`value` literal (row 7), or (`tooltip` only) an array of field-defs (row 9) | — |
| 6 | `/encoding/*/type` | **present:** `quantitative`/`ordinal`/`nominal`/`temporal`. | **omitted:** replicating Vega-Lite's type-inference heuristics is deferred — never blocks a required mark, only an authoring shorthand |
| 7 | `/encoding/*/field`, `/encoding/*/datum`, `/encoding/*/value` | `field`: a plain field-name string (already policy-bounded against prototype paths). `datum`: a literal or a DateTime object (§3.4b). `value`: a literal | — |
| 8 | `/encoding/*/aggregate` | `count`, `sum`, `mean`, `median`, `min`, `max`, `distinct` | `stdev`/`stdevp`/`variance`/`variancep`/`ci0`/`ci1`, `argmin`/`argmax`, `q1`/`q3` outside boxplot's own internal expansion (boxplot computes its own five-number summary in `marks/boxplot.ts`, not via general encoding aggregation) |
| 9 | `/encoding/tooltip` **as an array** | each array entry checked against rows 5–7 as if it were `/encoding/tooltip` itself | — |
| 10 | `/encoding/*/bin` | `true`, `"binned"` (pre-binned data), default bin-count math differential-tested (§3.10) against Vega's own binning | object-form custom bin params (`maxbins`, `extent`, `step`, …) |
| 11 | `/encoding/*/timeUnit` | see §3.4b | see §3.4b |
| 12 | `/encoding/*/sort` | `"ascending"`/`"descending"`, an explicit literal array, a simple sort-by-another-channel form | compound multi-field sort objects |
| 13 | `/encoding/*/stack` | `"zero"`/`"center"`/`"normalize"`/boolean/null, offsets differential-tested (§3.10) against Vega's stack transform | any case where our own stack math can't independently prove the stacked channel (mirrors `vegaPolicy.ts`'s own `provenStackedChannel` caution — if *that* code can't prove the orientation, ours doesn't either) |
| 14 | `/encoding/*/scale` | **`null`:** identity/pixel-pass-through mapping, no guide. **object form:** `type`, `domain`, `range` (explicit array or `scheme`), `zero`, `nice`, `clamp`, `reverse`, padding, `round` | `domainMid`, `threshold`/`quantile`/`quantize` scale types |
| 15 | `/encoding/*/axis` | **`null`:** axis hidden entirely. **object form:** `title`, label/tick/grid settings, `tickCount`, orientation, `format`/`formatType` | — (`labelExpr` is an `expr`-suffixed key and never reaches here, already policy-refused) |
| 16 | `/encoding/*/legend` | **`null`:** legend hidden entirely. **object form:** `title`, `orient`, `labelLimit`, `symbolSize`, `gradientLength`, `columns`, a bounded explicit `values` list | — |
| 17 | `/encoding/*/condition` | one level deep, a single predicate (no `param`/`selection` — already policy-refused), literal true/false branches | a list of multiple conditions, or a condition nested inside a condition |
| 18 | `/encoding/row`, `/encoding/column`, `/encoding/facet` (channel-shaped facet forms); `/encoding/*/field` when its value is `{"repeat": "row"\|"column"\|"layer"}` | the `repeat` placeholder form only (already a narrow, literal-only policy allowance) | the channel-shaped facet forms — the required "facet" composition is covered by row 20's top-level `/facet` keyword, which these are an older/alternate syntax for |
| 19 | `/config`, `/config/*` (root only) | identical to `vegaPolicy.ts`'s own already-literal-constrained config allow-list: `background`, `font`, `padding`, `range`, `mark`/`<any MARKS entry>` (including `boxplot` and its sub-mark overrides, free once row 1 adds `boxplot` to `MARKS` — `checkConfig`'s `MARKS.has(key)` branch already generalizes, §3.1/case 2b), every `AXIS_CONFIG`-matching key, `legend`/`header*`/`title`/`view`, `style/*`. `classify.ts` imports and checks against these **same exported constants** (`MARKS`, `AXIS_CONFIG`, `LITERAL_CONFIG`, `RANGE_NAMES`) rather than a hand-copied second list | anything `vegaPolicy.ts`'s own `checkConfig` wouldn't already accept — impossible by construction, since policy runs first |
| 20 | `/layer/*`, `/concat/*`, `/hconcat/*`, `/vconcat/*`, `/facet/spec`, `/facet/row`, `/facet/column`, `/repeat/spec`, `/repeat/row`, `/repeat/column`, `/repeat/layer` (composition) | all four required composition shapes; each child checked from its own root per the inheritance rules above | — |
| 21 | `/title` (root or any node) | a string, or the literal object form (`text`, `subtitle`, `anchor`, `orient`, …, already policy-literal-constrained) | — |
| 22 | `/autosize` (root only) | **moot.** `parseVegaLite` always overwrites this key before `classify.ts` runs regardless of what the agent set (see its final return object) — nothing to allow-list | — |
| 23 | `/resolve/scale/*`, `/resolve/axis/*`, `/resolve/legend/*` | `"shared"`/`"independent"` | `"global"`/`"intersect"`/`"union"` |
| 24 | `/datasets`, `/datasets/*` (root only) | already fully inline/bounded by policy; classify confirms a `data.name` reference resolves, same as policy already requires | — |
| 25 | Global posture: `/config/invalid` **absent** | Vega-Lite's own default (drop invalid/null from aggregation and from path-mark continuity) | `/config/invalid` **present**, any value |

Composition depth and panel count stay governed by `vegaPolicy.ts`'s own existing bounds
(`MAX_DEPTH`, `MAX_TOTAL_PANELS`); row 20 doesn't re-derive those, it confirms `layout.ts`
implements whichever composition shape is present.

#### This table is the classifier's data, not prose to transcribe

`classify.ts` implements the table above as **one exported, structured allow-list** (an array or
map of `{path pattern, handler}` entries matching these rows), not a sprawl of ad hoc `if`
statements written from memory of this document. A dedicated coverage test walks every key
`vegaPolicy.ts`'s own exported tables enumerate (`MARKS`, `NUMBER_ONLY`, `STRING_ENUMS`,
`AXIS_CONFIG`, `LITERAL_CONFIG`) and asserts each one is accounted for by some row above — handled
or explicitly listed as a fallback case — so a key that's new to Vega-Lite's schema, or newly
allowed by a future policy change, can never silently reach either renderer unconsidered, and this
document and the code it describes cannot drift apart without a failing test saying so.

### 3.4 Scales, axes, legends, tooltips

- **Scales** (`scales.ts`): continuous (linear/log/symlog/pow), temporal (time/utc, §3.4b), and
  discrete (ordinal/band/point) domain→range mapping, built from the encoding's resolved domain
  (explicit `scale.domain`, or computed from the already-validated inline data) and the panel's
  pixel extent. The domain→range mapping itself (the arithmetic of where a value lands in pixels)
  is reimplemented directly against our own data model rather than through `vega-scale`. **Tick
  *generation* for continuous scales is not reimplemented at all: `axes.ts` calls `d3-array`'s own
  `ticks()`/`tickStep()` directly** — the same function `d3-scale` (and therefore Vega) calls
  internally for linear/log/pow domains — so continuous-scale tick *values* are identical to
  Vega's by construction, not merely close within §3.10's tolerance. `d3-array` is already present
  in this repository's dependency graph (resolved at `3.2.x` via `vega-scale` and others today,
  confirmed in the lockfile), so this is reuse, not a new dependency (§3.8/H6). Band/ordinal tick
  placement (one tick per category, no generated sequence) and time-unit-based tick placement
  (§3.4b) don't go through `d3-array`'s numeric `ticks()` and stay differential-tested against
  Vega's reference output in §3.10, since their correctness depends on calendar/locale semantics
  `d3-array` doesn't cover.
- **Axes** (`axes.ts`): tick marks, tick labels (number formatting via `d3-format`, time formatting
  via `d3-time-format`, both shared with Vega — §3.4b/H6; width/precision caps are already enforced
  on the spec by `vegaPolicy.ts`'s `checkFormat`, so both renderers inherit the same bound),
  gridlines, axis titles, orientation (top/bottom/left/right). Collision avoidance (rotate or thin
  labels when they'd overlap) is part of this module, not left to the browser.
- **Legends** (`legend.ts`): categorical swatch lists, continuous gradient ramps, with the overflow
  behavior from failure-matrix case 13 (scroll region or "+N more" summary — never silently
  dropped entries, never pushing the plot area off-canvas).
- **Tooltips** (`tooltip.ts`): one hit-testing model shared by hover (`pointermove`) and touch
  (`pointerdown`/`pointerup` with a tap-to-toggle, since touch has no hover) and by keyboard focus
  (§3.5) — a single "which data point/region is active" state drives all three input modes.
  Regions are derived directly from the structured point descriptors `worker.ts` returns (§0,
  §3.2) — the same data `accessibility.ts` uses to build the keyboard list and the data table —
  rather than being inferred from a rendered scenegraph after the fact, the way the existing Vega
  path's `TooltipRegion` reads Vega's scenegraph bounds.

#### 3.4b Time, matched exactly or deferred

Vega-Lite's temporal semantics have real gotchas that must be matched exactly, not approximated:
UTC vs. local display, how a bare ISO date-only string is parsed, and calendar math for
`timeUnit`. Getting any of these subtly wrong produces a chart that looks right in one timezone
and is silently off by hours or a day in another.

- **Libraries, not reimplementation (H6, §3.8).** All parsing/formatting of temporal values goes
  through `d3-time-format` (for `format`/`formatType: 'time'|'utc'` and axis/legend label
  generation) and the underlying `d3-time` calendar-interval math (for `timeUnit`) — the exact
  packages `vega-scale`/`vega-time` already depend on as part of `vega`/`vega-lite`
  (`d3-time-format@4.1.0`, `d3-format@3.1.2`, confirmed present in this repository's dependency
  graph today). We do not write our own month/week/day arithmetic.
- **What we match now (v1):**
  - `utc`-prefixed `timeUnit`s (`utcyear`, `utcyearmonth`, …): computed and displayed in UTC via
    `d3-time`'s UTC intervals / `d3-time-format`'s `utcFormat`.
  - Non-`utc` `timeUnit`s: computed and displayed in the browser's local timezone via `d3-time`'s
    local intervals / `d3-time-format`'s `timeFormat` — the same "local unless prefixed utc" rule
    Vega-Lite itself follows.
  - Raw ISO date-only strings (`"2026-01-05"`) in a `temporal`-typed field without a `timeUnit`:
    parsed as UTC midnight, matching native `Date.parse`'s ISO-8601 behavior, which both Vega and
    any correct implementation inherit from the same JS date-parsing rule — not a Vega-specific
    choice to reverse-engineer.
  - `time`/`utc` format strings: passed straight to `d3-time-format`'s `timeFormat`/`utcFormat`.
- **What falls back at launch, until proven (§3.10):** locale-dependent calendar constructs —
  `week`/`utcweek`, `quarter`/`utcquarter`'s fiscal-boundary semantics, and `binnedutc*`/`binned*`
  pre-binned temporal time units — because their exact boundary conventions need their own
  differential-tested corpus before being trusted. This is a scope deferral inside the fallback
  ladder: these time units ship on our renderer as soon as their corpus passes, same as any other
  row in §3.3's table, not a cut feature.
- **Timezone-pinned tests.** A JS process's timezone is fixed for its lifetime (`Intl`/`Date` read
  `TZ` once at startup, not per call), so the differential suite for temporal cases runs the same
  spec corpus under several `TZ` values set before the process starts: at minimum `UTC`,
  `America/Los_Angeles` (DST-observing, whole-hour offset), `Asia/Kolkata` (fixed UTC+5:30, no
  DST), and `Pacific/Chatham` (UTC+12:45/+13:45, an unusual DST step) — so an off-by-one-hour or
  off-by-half-hour bug can't hide behind one lucky local timezone. This is one more axis of the
  differential harness (§3.10), not a separate mechanism.

### 3.5 Accessibility

- **Keyboard and the chart's ARIA role (L11), at series granularity, not per point.** The chart
  root uses `role="group"` with an accessible name — **not** `role="img"`. A static image role
  doesn't support the roving-focus interaction ARIA authoring practices call for, and our chart is
  real interactive content, not a picture (the existing Vega fallback path's canvas host keeps its
  current `role="img"` label unchanged — that role is correct *there*, since that output really is
  a static raster). Inside the group is a `role="list"` of `role="listitem"`s — **one item per
  series** (one per distinct color/detail-grouped line, bar group, etc.; an ungrouped chart has
  exactly one item), never one per data point. Giving every point its own focusable DOM node would
  mean up to 10,000 focusable nodes on a dense chart (§3.6) — a real cost and a worse experience,
  not an accessibility win. Tab/Shift+Tab moves between series-level items; while a series item is
  focused, arrow keys move an **internal point cursor** tracked only in component state (no DOM
  node per point) along that series in encoding order, and the cursor's position is drawn as a
  small highlight ring positioned at its computed SVG coordinates (not a browser focus ring, since
  there's no element to focus). The cursor's current value is written to an `aria-live="polite"`
  region on every move, so it's announced without needing the visible tooltip or the highlight
  ring. Home/End jump the cursor to the series' first/last point. The data table (§M9 below) is
  where every individual point is actually enumerable — the keyboard model's job is "orient within
  a series and hear values as you move," not "reach all 10,000 points one Tab at a time."
- **Accessible names.** The chart root's `aria-label` is built from the spec's own `title` (if
  any) plus a plain description — "Bar chart, Revenue by quarter, 4 series" — not a generic
  "chart".
- **Data table alternative — one rule (M9).** Every chart drawn by our renderer exposes a toggle
  (reusing `VisualBlock.tsx`'s existing `Source`/`Copy`/`Open` action-row pattern — one more icon
  action, same bar) that renders **the values actually drawn**: after aggregation, binning and
  stacking are applied — exactly the rows our layout iterated over to place marks, never the
  agent's raw pre-aggregation input — as an ordinary HTML `<table>`, one column per encoded channel
  with a field, one row per drawn data point. The table is capped at a stated row count (the exact
  number is an implementation-time UX call — an open question below, not fixed here) with a
  literal, truthful caption above it: "Showing `<shown>` of `<total>` rows", where `<total>` is the
  *actual* post-aggregation row count, never the raw input count, and truncation is deterministic
  (the first `<shown>` rows in draw order) rather than sampled, so the same chart always shows the
  same slice. A table is never silently truncated with no caption, and it is never built from data
  the chart didn't draw.
- **Touch.** Hit targets for tap use the same regions as hover but with a minimum 24px effective
  touch target (expanding small marks' hit area without changing their drawn size).

### 3.6 Dense data and the performance budget — within policy's existing caps

`vegaPolicy.ts` already caps any single inline array at **10,000 entries** (`MAX_ARRAY`) and the
whole spec source at **131,072 characters** (`MAX_CHART_SOURCE`). This design does not raise either
cap. "Dense data" therefore means, concretely: **a single series (one `data.values` array) of up
to 10,000 points** — the policy ceiling, not an arbitrary round number.

**300ms/150ms is a gate, not a soft target.** A single-series line or area at the 10,000-point
ceiling must draw its first frame in **under 300ms**, and re-render (a resize or theme change —
v1 adds no new interaction params, since `params`/`selection` stay policy-refused) in **under
150ms**, enforced in CI, not merely reported after the fact. Concretely:

- **Where it runs:** `BlergChartDensity.performance.browser.test.tsx`, in runner/frontend's
  existing `browser-performance` Vitest project (`vitest.config.ts`'s `browserProject(true)`,
  which already runs real Chromium via `@vitest/browser-playwright`, serially, after every other
  project — the same project any future `CHART_DEADLINE_MS`-style strict timing budget belongs
  in). This is a real browser, not jsdom: jsdom estimates text width rather than measuring it,
  which would make a jsdom-only timing number meaningless for a layout that depends on real label
  measurement.
- **What it measures:** the actual bundled `visual/blerg` renderer drawing a fixed, checked-in
  10,000-point corpus spec (one series, at `MAX_ARRAY`'s cap), timed with
  `performance.mark`/`performance.measure` around the draw call, repeated several times (e.g. 5
  runs) in one test run, asserting the **median** against the 300ms/150ms thresholds — a single
  slow outlier run doesn't fail the gate, a consistently slow implementation does.
- **On what hardware:** the CI runner the gate actually executes on, with that runner's spec
  (vCPU count, memory, headless Chromium) recorded in the test file's header comment, so the
  300ms/150ms numbers are reproducible and attributable rather than a number nobody can re-derive
  (open question below: the exact runner class to record).
- This is the same discipline `runner-performance`'s existing suites already apply to
  layout/rendering costs, pointed at this new code rather than invented fresh.

**How the budget is kept, implementation-side:**
- Layout/scale/axis code is plain array math (map a value through a scale function) — O(n) in
  point count with a small constant, no dataflow graph, no reactive operator network like Vega's
  (`vegaRuntime.ts`'s `guardDataflow` exists specifically because Vega's dataflow can blow up
  memory on dense input; our renderer has no dataflow to guard).
- Marks draw as a single `<path>` per series for line/area-shaped marks (one `d` attribute
  built from all points) rather than one DOM node per point.
- Point-based marks (bar, point, rect, arc) at high cardinality still cost one shape per datum, so
  they're bounded well below the 10,000-point line/area case by `budget.ts`'s own, smaller limits.
- Output is SVG (real DOM nodes, free keyboard focus and screen-reader support, §3.5) rather than
  canvas or WebGL. The current Vega path rasterizes to canvas *because it must* — Vega's
  dataflow/scenegraph can't safely run on the main thread at all, hence the worker. Our renderer's
  simpler, bounded math is what lets it skip that rasterization step and keep real interactive DOM.
- **Exceeding a limit is a classification outcome, not a thrown error (M10).** `budget.ts`'s limits
  (point count per mark, total marks, legend entries; panel count inherits `vegaPolicy.ts`'s own
  cap) are consulted **inside `classify.ts`**, in the same pass that already counts marks/points
  for §3.3's allow-list walk. Exceeding one simply makes `classifyChart` return `'fallback'` — it
  never throws `ChartRefused` or any error of its own. `ChartRefused` stays the shared policy
  layer's error type, used only by `vegaPolicy.ts`/`policy.ts`, which both renderers inherit
  unchanged; our renderer's budget check is a classification signal, not a second error channel,
  so there's exactly one place a chart-drawing error can come from per renderer (ours: never, by
  this design; Vega's: its own existing, unmodified `ChartRefused`/`HEAVY_CHART` path).

### 3.7 Where it runs: main thread vs. worker

Classification (`classify.ts`) runs synchronously on the main thread — it's a bounded tree walk
over an already-bounded, already-validated object; it cannot be heavy by construction, the same
bound that makes `parseVegaLite` itself cheap enough to run on the main thread today.

**Drawing is different: a synchronous call cannot be timed out on the thread it runs on (M7).** A
`setTimeout`-based abort, like the existing Vega path's `CHART_DEADLINE_MS`, only works because
that work happens in a worker — a tight synchronous loop on the main thread blocks the event loop
entirely, so no timer fires until it returns on its own. This design therefore does not attempt a
main-thread timeout at all. Instead, `classify.ts` computes a static size metric (point count ×
mark count — numbers it already has from §3.3's allow-list walk and §3.6's budget check) and
compares it to a fixed threshold decided at implementation time, well below the 10,000-point
budget ceiling (likely in the low hundreds of points): **below the threshold, drawing runs
synchronously on the main thread, justified by that same classification math proving it's small
— not by any runtime timeout; at or above the threshold, drawing happens in the terminable worker**
(`worker.ts`), mirroring the existing Vega path's `compileInWorker`/`ChartWorker` pattern exactly:
one worker per chart, a hard wall-clock deadline that terminates a stuck worker (failure-matrix
case 15), and the existing job queue (`vega.ts`'s `queued` promise chain) extended to also
serialize our renderer's worker jobs, so a transcript with many charts of *either* kind doesn't
spawn unbounded concurrent workers.

The worker, when used, computes the SVG string (§0) and the structured list of per-point
descriptors `accessibility.ts`/`tooltip.ts` need (value, label, bounds in local SVG coordinates) —
not one focusable DOM node per point (§3.5), and not a second, independent drawing pass. The main
thread parses that SVG into real DOM via
`DOMParser` (reusing `sanitizeSvg`'s own parsing/sanitizing step, defense in depth even though the
SVG is our own output) and attaches the accessibility layer and tooltip/keyboard handlers on top.

### 3.8 Bundle size impact

The Vega fallback path keeps its existing dependencies (`vega` 6.4.0, `vega-lite` 6.5.0,
`vega-interpreter` 2.3.2) exactly as they are today, dynamically imported only when a spec falls
back (`await import('./vega')` in `render.ts`, unchanged) — a transcript with zero charts, or only
charts our renderer draws, pays zero bytes for Vega.

Our own module (`visual/blerg/*`) is dynamically imported the same way `./vega` is (`render.ts`'s
classify-then-import split), so it too costs nothing for a transcript with no visual blocks.
**For number/time formatting and continuous-scale tick generation specifically (H6), we add
`d3-format`, `d3-time-format` and `d3-array` as explicit dependencies of `packages/chat`, pinned to
the versions already resolved today by `vega-scale`/`vega-time` (`3.1.2` / `4.1.0` / `3.2.x`) so
the package manager dedupes to the exact same module instances rather than installing second
copies.** This is reuse, not a new piece of the dependency graph: all three packages are already
present, transitively, wherever `vega`/`vega-lite` are installed — they're simply not currently
importable on their own from outside that chain. We do not write a parallel number/time formatter
or a parallel tick-generation algorithm: `vegaPolicy.ts`'s `checkFormat` already parses d3's
format-specifier grammar for validation, and both renderers now share the *same* formatting and
tick-generation libraries for actually rendering, which is also a correctness argument (H4/H6/§3.4):
any future d3 bug-fix or edge case lands identically on both renderers, and our output can never
drift from Vega's by using a different implementation of the same math.

The cost this does add, for a `'blerg'`-only session that never imports `./vega`, is
`d3-format`'s, `d3-time-format`'s and `d3-array`'s own (small) weight — a few KB each gzipped; the
exact figure is an implementation-time bundle-analysis measurement, not asserted here. This is the
one new runtime cost in this design, and it's justified by H4/H6's correctness argument above
rather than by convenience. No other new runtime dependency is introduced: scales (domain→range
mapping), axes, layout math and SVG string construction are plain TypeScript.

### 3.9 Theme token mapping

Both renderers consume the one `VisualTheme` resolved by `readVisualTheme()` — no new token
reading, no new CSS variable names. The mapping differs only in how each renderer *applies* the
theme, matching what each drawing model needs:

| `VisualTheme` field | Vega fallback (`vegaTheme()`) | Our renderer |
|---|---|---|
| `fg` | axis/legend/title text color | axis/legend/title text, data-table text |
| `muted` | axis label color | axis label color, "+N more" legend overflow text |
| `surface`/`surface2` | `background: 'transparent'` (host supplies the surface) | same — chart SVG has `background: transparent`; the host element/card supplies surface color, exactly like today, so a chart never paints its own background box |
| `border` | axis domain/tick color, grid color (0.35 opacity) | axis line/tick color, gridlines (same opacity convention, so a chart doesn't look different in density depending on which engine drew it) |
| `accent` | single-series mark color | single-series mark color, active/focused point ring in keyboard mode (§3.5) |
| `palette` | `range.category` (series colors) | categorical scale range; continuous scales interpolate between `accent` and `surface2` for a sequential ramp, mirroring the existing `mermaidTheme()` mix-based approach for Mermaid's active/secondary colors rather than inventing a new color math |
| `fontFamily`/`fontSize` | all text | all text, including the data-table alternative (so it reads as the same chart, not a different UI) |
| `dark` | (implicit via the resolved colors) | used to pick gridline/legend-overflow contrast tweaks the same way `readable()` in `theme.ts` already guards contrast for Mermaid |

No new `--chart-*` CSS variables are introduced; the existing resolution path (`theme.ts`'s
`color()` helper, with its unsafe-CSS guard and the CSSOM-probe fallback for named/`currentColor`
values) is reused as-is, since it already produces a fully-resolved `VisualTheme` object before
either renderer sees it.

### 3.10 The differential testing gate (H5), in a real browser

Every mark and feature in §3.3's allow-list is gated by one harness, not by individual judgment
calls about whether our math "looks right." **A feature isn't "ours" until it passes this.**

**This gate cannot be built the way the first draft of this design described it, and round 2 of
review caught exactly why:** `vegaRuntime.ts`'s `drawChart` — the function the first draft pointed
at as "the reference" — runs inside the Vega worker and returns only `{svg, width, height,
regions}`; it never exposes the `View` object, so there is no public API to read a tick value or a
scale domain off its result. Tick values specifically have no public accessor at all — they only
exist as scenegraph items. Stack-transform output lands in a Vega-Lite-generated dataset whose
name isn't fixed in source (it's derived from the spec's own structure), so "the stack output
field" has to be discovered, not assumed. And critically: **jsdom estimates text metrics instead
of measuring them**, so any comparison that runs the reference in jsdom and the candidate in a
browser (or vice versa) will disagree on axis/legend padding and therefore on every downstream
position, for reasons that have nothing to do with whether our layout math is correct. The gate
only means anything if both sides measure text the same way.

**The fix: run entirely inside the real browser, through a test-only seam, comparing positions
relative to the plot area rather than the page.**

- **The seam.** A new, deliberately test-only export — not reused by any production path and not
  exported from the package's public surface — exposes the live `View` instead of collapsing it to
  `{svg, width, height}`. It reuses the exact same `compile()`/`parse()`/`View` construction
  `vegaRuntime.ts`'s `drawChart` already performs (so the reference is provably the same
  compilation the production fallback runs, not a second, divergently-maintained copy), but skips
  `guardDataflow` and the heavy/budget checks — the corpus is small, hand-written, trusted test
  fixtures, not adversarial agent input:

  ```ts
  // packages/chat/src/visual/vegaRuntime.ts — test-only, never imported by production code.
  // Exposes the live View and the compiled spec's dataset names for the BLG-452 differential
  // gate (§3.10); drawChart stays the only production entry point and keeps returning the
  // reduced {svg, width, height, regions} shape.
  export async function compileVegaViewForDifferentialTesting(spec: Record<string, unknown>, theme: VisualTheme) {
    const checked = parseVegaLite(JSON.stringify(spec))
    const compiled = compile(checked as unknown as TopLevelSpec, {config: vegaTheme(theme) as Config, logger: logger(0)}).spec
    const view = new View(parse(compiled, undefined, {ast: true}), {renderer: 'none', loader: denyVisualResources, expr: expressionInterpreter})
    await view.runAsync()
    return {view, datasetNames: (compiled.data ?? []).map((d: {name: string}) => d.name)}
  }
  ```

  This follows a convention the codebase already uses: `ChartWorker.browser.test.tsx` already
  imports `vega.ts`/`vegaBudget.ts` via a deep relative path that bypasses `@blerglab/chat`'s
  public export surface precisely to reach test-only seams. This is the same pattern, one level
  deeper into `vegaRuntime.ts`.

- **Reading ticks.** Vega's axis scenegraph groups carry role `'axis'` (confirmed in
  `vega-parser`'s `AxisRole`/`AxisTickRole`/`AxisLabelRole` constants); their tick/label child
  items each carry `datum.value` and `datum.label` (confirmed in `vega-encode`'s `AxisTicks`
  transform, which produces exactly `{value, label}` per generated tick before handing it to the
  scenegraph). The harness walks `view.scenegraph().root` for groups with `role === 'axis'`, reads
  `datum.value`/`datum.label` off their `'axis-tick'`/`'axis-label'` children, and takes each
  item's `x`/`y` relative to its own axis group's origin.
- **Reading data.** `compileVegaViewForDifferentialTesting`'s `datasetNames` (read straight off
  the **compiled spec's** own `data` array, not guessed) tells the harness which names to pass to
  `view.data(name)` — a public, documented Vega API that returns the named dataset's materialized
  rows after every transform (filter, aggregate, bin, stack) has run. This is the ground truth for
  "what did Vega actually draw," including stack offsets, with no need to know the generated
  name's naming scheme in advance.
- **The plot-origin frame — comparing inside the plot, not on the page.** Every position compared
  (tick positions, mark geometry) is first translated into one shared frame: the top-left corner
  of the bounding box of every scenegraph item whose role is the plain mark role (`'mark'`,
  confirmed in `vega-parser`'s `MarkRole` constant) — i.e. the plot rectangle itself, excluding
  axis/legend/title groups. Our own `layout.ts` computes and exposes the equivalent rectangle for
  its own output (it already has to know this to place marks). Subtracting each side's own plot
  origin before comparing cancels out any difference in how the two renderers measured
  axis/legend/title text and therefore padded around the plot — which is real and expected to
  differ slightly, and is not what this gate is checking — leaving only the geometry that actually
  matters: where things sit *relative to the data area*.
- **Comparison and tolerance.** Scale domains and `view.data(name)` values (including stack
  offsets) match within floating-point epsilon — the same arithmetic on the same numbers. Tick
  *values* for continuous scales match exactly, by construction, since both sides call the same
  `d3-array` `ticks()` (§3.4) — this is an equality check, not a tolerance. Band/ordinal/time-unit
  tick values (not backed by the shared `d3-array` call) and all plot-relative positions (tick
  placement, mark geometry — positions, widths, radii) match within a stated pixel tolerance (e.g.
  0.5px), to allow for different but equally valid rounding in intermediate math, not for actual
  disagreement.
- **The gate.** A mark, channel, or feature is added to `classify.ts`'s allow-list (§3.3) only
  once its corpus cases pass this comparison, across every pinned timezone where relevant (§3.4b).
  Until then it simply isn't on the allow-list, so matching specs classify as `'fallback'`
  automatically — the same mechanism as every other not-yet-implemented case in §3.3, not a
  special case. Shipping a feature and passing its differential corpus are the same event.
- **Where it runs, and the test file.** This needs real text measurement on both sides, so it runs
  in `runner/frontend`'s existing **`browser`** Vitest project (real Chromium via
  `@vitest/browser-playwright` — the same project `ChartWorker.browser.test.tsx` already lives in,
  which already deep-imports `packages/chat/src/visual/*` the same way this harness does), as
  `runner/frontend/src/components/BlergChartDifferential.browser.test.tsx`. Per
  `docs/testing-audit.md`'s doctrine this is **layer 2**: a real browser is part of the
  implementation under test (the same classification `runner-browser`'s other suites already
  carry), not a layer 1 contract that can run headless in `chat-tests`/jsdom — the first draft's
  "runs in `chat-tests`" claim was wrong for exactly the text-metrics reason above.
- **Corpus.** A directory of representative specs, one subdirectory per mark family and per
  feature row in §3.3's table (e.g. `corpus/marks/boxplot/*.json`, `corpus/time/utcweek/*.json`,
  `corpus/stack/normalize/*.json`), plus the timezone-pinned temporal cases from §3.4b.
- **Ongoing regression protection (failure-matrix case 18).** Once a mark/feature ships, its
  corpus stays in CI. A later change to `scales.ts`/`axes.ts`/`layout.ts` that disagrees with Vega
  beyond tolerance fails `BlergChartDifferential.browser.test.tsx` before merge — a mechanical,
  re-runnable check rather than a visual bug report filed later.

## 4. Surfaces and UX

Per `runner/skills/ux-design/SKILL.md`: one look per action, icons with tooltips, say a thing once.

- **Chat block** (`VisualBlock.tsx`): no new chrome beyond one new icon action. The existing action
  row (`Source` / `Copy` / `Open`) gains exactly one new icon action, **Table** (toggles the data
  table alternative inline, §3.5/M9, same pattern as `Source`'s toggle) — not a new menu, not a
  second row. Which engine drew the chart is never shown; a person sees one consistent "this is a
  chart" look regardless of whether it's ours or the fallback.
- **File viewer** (`ArtifactViewer.tsx`): no new chrome at all — `TextView` already dispatches to
  `VisualBlock` via `visualFile()`; the `Table` action above is inherited automatically, and
  `ViewerToolbar`'s existing Download action is unchanged (and now reads `RenderedVisual.svg`
  directly per §0, whichever engine drew the chart).
- **Tabs** (BLG-451): no new chrome — a chart tab is an `artifact` tab like any other; BLG-452
  only changes what draws inside the content area that tab already has.
- **Full-screen view** (`VisualBlock.tsx`'s `FullScreen`): the existing zoom controls (`−` / reset
  / `+`) and `Close` stay exactly as they are; our renderer's SVG output scales under the existing
  `zoom` CSS the same way the current canvas output does.
- **Failure state** (`visual-failed`): unchanged — one message, one closed source block, same as
  today. Never a different-looking failure depending on which engine refused.
- **Nothing is said twice:** the renderer choice, the classification outcome, and the fallback
  rung used are deliberately *not* surfaced as chrome anywhere a person looks — only as the
  `data-chart-renderer` attribute for tests (§1) and, per "log every failure," as a console/log
  line when the rare case-4 internal-bug fallback fires, so it can be found and fixed without ever
  being shown to the person as a status message.

## 5. Open questions

1. **Exact data-table row cap (§3.5/M9).** The rule itself — post-aggregation rows, a truthful
   "Showing N of M" caption, deterministic truncation — is settled by this revision. The specific
   number isn't. **Recommendation:** 500 rows as a starting point, revisited if real agent charts
   commonly exceed it with a data shape the table still needs to represent well.
2. **The static worker-threshold number (§3.7/M7).** Decided by implementation-time measurement
   against the stated budget (§3.6), not fixed here. **Recommendation:** start low (low hundreds of
   points) and raise it only if main-thread measurements on representative hardware show headroom
   — erring toward the worker, where real cancellation is possible, costs little and is safer.
3. **Launch sequencing across §3.3's table.** Every mark is required by the card, but §3.10's gate
   means marks/features ship as their corpus passes, not all at once. **Recommendation:** sequence
   implementation by expected agent frequency (bar/line/point/area first, then rule/text/arc, then
   rect/heatmap, then boxplot last given its own statistical surface, §3.3), with the full required
   set targeted before this phase is considered complete — "ignore duration, don't trim scope for
   time" argues against calling the card done with marks still permanently on fallback, but says
   nothing against an incremental, gated rollout order.
4. **CI runner hardware for the §3.6 performance gate.** The 300ms/150ms gate is committed
   (§3.6), but the exact CI runner spec to record alongside it is an implementation-time detail.
   **Recommendation:** record vCPU count, memory and whether the runner is the same class used for
   `runner-performance`'s other budgets today, so the number is comparable to existing gates rather
   than a new, differently-sourced baseline.
