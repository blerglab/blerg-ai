# Blerg's own diagram renderer (BLG-468)

*2026-11-03. Design only — no product code in this round. Revised after design review round 1
(H1–H2, M3–M8, L9): dropped `@mermaid-js/parser` reuse, switched to our own small parser for the
declared subset, added a security section, fixed the layout/subgraph/license questions, widened
hover/focus/chips/differential/matrix scope, and clarified gantt's interactive surface. Revised
again after round 2 (precision fixes, same direction): every diagram kind returns
`{component, RenderedVisual}`, not gantt alone (§5); an explicit token list per grammar (§2); the
dagre size now includes `lodash-es` and names the exact deep import, plus a maintenance note
(§3); chips use both `splitRefs` and `splitUnqualifiedRefs` (§4); and the §7/§8 cross-references
are fixed.*

## The problem, in one line

Diagrams draw through Mermaid 12.2.0 today (`packages/chat/src/visual/mermaid.ts`): a sandboxed
iframe (`mermaidFrame.ts`) injects `mermaid/dist/mermaid.min.js?raw` and runs Mermaid's real
parser *and* real layout/drawing code inside it, returns an SVG string, which is sanitized and
shown as an inert `<img>`. Safe, but not interactive (no hover, no focus, no chips) and not ours
(Mermaid's look, not our tokens) — the gap BLG-452 closed for charts. Same shape here: Mermaid
syntax stays the format agents write; Mermaid stays the fallback engine; we replace only the
*drawing*, for a classifier-declared subset, never a partial drawing of our own.

## The failure rule

Unknown or unsupported syntax returns `'mermaid'` from the classifier and the diagram falls back
to the existing iframe path — same frame, same `<img>`, same sanitizer — never a partial or
best-effort drawing. `SHIPPED_DIAGRAMS` (BLG-452's `classify.ts` pattern) only grows in the commit
that adds the differential corpus proving a type (§8); until then every instance of it is a
correct `'mermaid'` verdict, not a gap. Every refusal logs its cause once (shared diagnostics, no
raw source) before falling back.

## 1. Supported subset, first slice

**Flowchart** (`flowchart`/`graph`): directions `TD`/`TB`, `LR`, `RL`, `BT`; six node shapes
(rect, round, stadium, diamond, circle, subroutine); one level of subgraphs; labeled and unlabeled
edges; arrows `-->`/`---`/`-.->`/`==>`.

**Sequence**: participants (incl. `actor`), sync/async messages, activation bars, notes
(`left/right/over`), one level of `alt`/`opt`/`loop`. No `par`, no nested blocks, no
`autonumber` (v1).

The exact token list for each, and what happens to everything not on it, is §2's grammar table —
not implied here.

`runner/frontend/src/test/mermaid/cases.json` already has 39 `flowchart-v2` and 7 `sequence`
fixtures; nested/`par`/unsupported-shape cases become *fallback* fixtures (proving they stay on
Mermaid, not that we draw them).

## 2. The parser: our own, for the declared subset only

Review finding: `@mermaid-js/parser` 2.0.1's `parse()` covers `info`, `packet`, `pie`,
`treeView`, `architecture`, `gitGraph`, `eventmodeling`, `radar`, the three `railroad*` dialects,
`treemap`, `wardley`, `cynefin` — checked against its own `dist/src/index.d.ts` overloads.
Flowchart, sequence and gantt are **not** in that list: in the installed `mermaid` 12.2.0 they are
still jison/langium grammars compiled straight into `mermaid.min.js`, never published standalone.
And nothing is "already paid": that script runs as a raw string inside `mermaidFrame.ts`'s
sandboxed iframe (`mermaid/dist/mermaid.min.js?raw`), an isolated script context with its own
origin and no shared module graph with the chat's own bundle — any parser we add to *our* bundle,
in or out of that iframe, is new cost and new surface, not reuse of what the iframe already pays.

Direction: write our own small, hand-rolled parser for exactly the declared subset (flowchart and
sequence now, gantt next per BLG-471), the same way `classify.ts` declares exactly what it
accepts rather than wrapping a general grammar. No grammar library, no generated lexer table.
Unknown syntax — a construct our parser doesn't recognize, not just one it rejects by policy —
returns "not supported" and the diagram falls back to Mermaid in its existing frame, same as any
other classifier miss. The parser never produces HTML; its output is a plain AST (node list, edge
list with `{from, to, label}`, subgraph membership, direction) consumed only by our layout and
draw steps. The rule that makes "unknown → fallback" actually hold: every line must be fully
consumed by one of the productions below, or the whole diagram falls back — a line that's
half-recognized (e.g. a label form we parse but a trailing modifier we don't) is exactly the
silent-misparse case this is written to rule out, so it falls back too, not "parses what it can."

**Flowchart grammar, token by token:**

| Construct | Decision |
|---|---|
| `A-->B`, labeled/unlabeled, 4 arrow styles (§1) | supported |
| Chained edges `A-->B-->C` | supported — sugar for `A-->B` + `B-->C` |
| Node-list fan-out `A & B --> C` | **unsupported** — falls back |
| Edge label `A -- text --> B` | supported |
| Edge label `A -->|text| B` | supported |
| Quoted node/edge labels (`A["a, b"]`) | supported — needed for labels with reserved chars |
| `:::class` shorthand | **unsupported** — falls back (theme tokens own color, §4) |
| `style nodeId ...` | **unsupported** — falls back (same reason) |
| `linkStyle ...` | **unsupported** — falls back (same reason) |
| `classDef ...` | **unsupported** — falls back (same reason) |
| `%% comment` (not `%%{init}%%`) | supported — stripped, no effect on drawing |
| `%%{init: ...}%%`, anywhere in the source | **unsupported — always falls back.** Review finding: this doesn't set direction (`flowchart TD`/`graph LR` does, on its own line) so there is no "beyond direction" carve-out; any `init` directive present means fallback, full stop |
| A node literally named `end` | **unsupported** — `end` is reserved for closing a subgraph; falls back rather than resolve the ambiguity |

**Sequence grammar, token by token:**

| Construct | Decision |
|---|---|
| `participant X` / `actor X`, with `as Alias` | supported |
| `A->>B`, `A-->>B` (sync/async), with or without a `+`/`-` activation suffix | supported |
| Explicit `activate X` / `deactivate X` | supported |
| `A-xB`, `A--xB`, `A-)B`, `A--)B` (lost / fire-and-forget) | **unsupported** — falls back (not "sync/async messages", §1) |
| `note left of/right of/over X[,Y]` | supported |
| `alt`/`else`/`opt`/`loop` … `end`, one level | supported |
| `par`/`and`, or any nesting of the above | **unsupported** — falls back |
| `box ... end` participant grouping | **unsupported** — falls back |
| `autonumber` | **unsupported** — falls back (v1) |
| `%% comment` / `%%{init}%%` | same rule as flowchart: comments supported, any `init` falls back |

Label text in both grammars follows one rule, below.

**Label text**: HTML entities are decoded (`&amp;` → `&`); a literal `<br>`/`<br/>` becomes a line
break; any other `<...>` markup in a label is unsupported syntax — the whole diagram falls back,
it is never stripped or passed through. This matches the failure rule: a label we can't represent
exactly as plain text is a reason to fall back, not a reason to approximate.

**Size**: no library dependency, so cost is only what we write. Two real proxies already in this
codebase at comparable scope: `classify.ts` (a static allow-list walk over a parsed structure,
BLG-452) is 22.4 KB source / 7.5 KB gzip; `policy.ts`'s existing hand-rolled `validateMermaid` (a
text scan with no grammar library) is 169 lines. A hand-written recursive-descent parser for two
line-oriented grammars (flowchart, sequence) at this scope should land in the low single digits of
KB gzip — the real number gets measured and pinned here once the parser exists, not asserted
ahead of it.

**Parity proof against Mermaid's own understanding, not just our corpus's expected output**: add
a test-only message to `mermaidFrame.ts`'s existing port protocol that, for a corpus fixture,
calls Mermaid's own `mermaidAPI.getDiagramFromText(source)` inside the frame, reads the returned
`Diagram`'s `.db` (the same object Mermaid's own renderer draws from — nodes, edges, subgraphs,
participants, in Mermaid's own shape), and posts a plain, serializable AST back over the same
`MessageChannel` used for SVG today. This path is compiled into test builds only, behind the same
kind of seam `corpus.ts` already uses to reach `renderInFrame` directly — it never ships in the
production bundle or runs outside a test. §8 diffs our parser's AST against this, not against
Mermaid's drawn pixels.

## 3. Layout

`dagre-d3-es` 7.0.14 (MIT) is Mermaid's own flowchart layout today, so it is what our differential
corpus is already diffing against. It is a compound-graph layout engine (`setParent`,
`nesting-graph.js` in its source): a subgraph maps directly to a dagre parent node — every
contained node gets `g.setParent(nodeId, subgraphId)`, and dagre lays out the subgraph as a nested
cluster with its own bounding box, the same mechanism Mermaid's own flowchart renderer uses for
`subgraph`/`end`. No custom nesting logic needed for one level of subgraphs.

`elkjs` 0.9.3 (EPL-2.0) is already an installed transitive dependency (Mermaid's `flowchart-elk`
layout option) — a real alternative, not a hypothetical one. EPL-2.0 is weak-copyleft at the
*file* level (modifications to EPL-covered files must stay EPL/open), which is not a blocker for
depending on it unmodified via npm, but it is a real difference from `dagre-d3-es`'s MIT that
needs an explicit sign-off before it ships, not an assumption in this doc.

Measured, layout-only (no D3/no UI code in either number), importing the deep path
`dagre-d3-es/src/dagre/index.js` — not the package root (`src/index.js`), which pulls in the D3
drawing half via `dagre-d3-es`'s own `d3` dependency:
- `dagre-d3-es/src/dagre/` + `src/graphlib/` concatenated: 72 KB source / **17 KB gzip**. Review
  finding: that number was incomplete — every file in both directories does
  `import * as _ from 'lodash-es'` (`graph.js`, `layout.js`, `util.js`, …), a namespace import of
  `dagre-d3-es`'s own declared `lodash-es` dependency. A bundler that tree-shakes namespace
  imports of an ESM re-export barrel (Vite/Rollup usually can, for a side-effect-free package like
  `lodash-es`) keeps this close to the 17 KB figure; if it can't, the worst case is the entire
  package, measured at **123 KB gzip** (unminified `lodash-es/*.js` concatenated). Real number
  gets measured against our actual bundler output once wired, not assumed either way.
- `elkjs`'s bundled layout engine (`elk-worker.min.js` — the algorithm is monolithic; there is no
  smaller "layout-only" entry point, `elk-api.js` alone is just a 12 KB wrapper that still needs
  the worker bundle to do anything): 1.6 MB minified / **458 KB gzip**.

**Maintenance**: `dagre-d3-es` (npm `dagre-d3-es`, repo `tbo47/dagre-es`) is a single-maintainer
ESM port, last published 2025-12-03 — not abandoned, but one person. Mitigation if it goes stale:
pin the exact version (no `^` range) so a broken or unpublished release can't move under us; the
layout-only slice we'd actually use is small enough (72 KB source, §above) to vendor directly into
`packages/chat/src/visual/blerg/` if pinning ever isn't enough, with its MIT license header kept
alongside, the same way `LICENSE.mermaid` already travels with the Mermaid corpus.

One alternative worth a line: `@dagrejs/dagre` 3.1.3 (MIT, actively maintained by the `dagrejs`
GitHub org — not a single maintainer, last published 2026-10-24) depends only on its own
`@dagrejs/graphlib`, no `lodash-es`, no `d3`. Measured directly from its published tarball:
`dagre.min.js` is **17.3 KB gzip** (bundles its own graphlib) — materially the same cost as
`dagre-d3-es`'s layout slice, without the `lodash-es` question or the single-maintainer risk. Not
switching the recommendation in this round (this is a precision pass, not a new direction, and
`dagre-d3-es` is what our differential corpus is proven against, §8) — flagged here so the
build round can confirm the pick with real numbers in hand rather than re-discovering this.

Recommendation stands for this slice: `dagre-d3-es`'s layout core directly (`dagre.layout(graph)`,
graph in, positions out), our own SVG emission from the result (`svg.ts`-style). Sequence needs no
graph solver — participants in a row, messages as a vertical timeline — a small hand-rolled layout
pass.

## 4. Theme, hover, focus, chips, reflow

Every color comes from `VisualTheme` tokens (`theme.ts`), light and dark — no literal hex in
node/edge/text drawing, `make lint-theme` stays at 0 violations.

**Hover and keyboard focus** apply to both nodes *and* edges (not nodes alone): one roving focus
target per node and one per edge, an `aria-live` region announcing whichever is focused, a
collapsible text alternative listing nodes and edges in source order — `accessibility.ts`'s shape
from BLG-452, widened to cover the edge case BLG-452 itself never had (no edges in a chart).

**Reflow on resize** reruns layout at the new width — `dagre.layout(graph)` again with the new
rank-separation/width constraint (or a direction change), producing new node positions, not a CSS
`transform: scale()` on the old SVG. A diagram at 390px and one at 3840px are two different layout
solutions, same as a flowchart's lines wrap differently at each width; only the *drawing* (colors,
fonts, stroke widths) stays constant across a resize, never the geometry.

**Chips**: a node, subgraph or sequence participant whose label matches a card reference becomes a
chip through the existing lending contract — `ChatRefs.cards`/`openCard` for a card, the host's
file-open affordance for a path that resolves under the session's workspace. Card matching reuses
`refs.ts` exactly as written, both of its matchers, not one: `splitRefs`'s qualified form for a
full key (`BLG-468`) or `board:ID#N`, and `splitUnqualifiedRefs`'s form for a bare `#147`. A label
that resolves to neither stays plain text, exactly like `RefLinks.tsx`'s unconfirmed-text
behavior — no chip, no lookup attempted.

## 5. RenderedVisual, the live component, and exports

Review finding: hover, keyboard focus, the `aria-live` region and chips (§4) are all live DOM in
Blerg's own React tree — a `role`/`tabindex`/click handler cannot sit on a detached SVG string.
So every diagram kind, not gantt alone, returns **both** a `RenderedVisual` (`{svg, width, height,
dispose()}`, the same contract every renderer already returns, used for export and for any host —
a tab, full screen — that only wants the static contract) **and** a React component: the
interactive surface actually mounted in the chat. This widens, rather than contradicts, BLG-451's
contract — `visualExport.ts` and every existing `RenderedVisual` consumer are untouched; our own
`render.ts` call site is the only one that also receives a component.

The component's own SVG markup is **not** hand-assembled HTML with a chip bolted on — it is built
the same way BLG-452 already builds its live DOM: our drawing code produces one canonical SVG
string first, that exact string is the one returned as `RenderedVisual.svg`, and the *live* DOM is
then parsed from it with `DOMParser` after it has gone through `sanitizeSvg` — the identical
sequence `policy.ts`'s sanitizer already performs, so there is one drawing pass, not two, and the
on-screen DOM and the exported SVG can never visually disagree. Hover/focus targets, the
`aria-live` region, and chip click targets are layered onto those parsed nodes afterward as DOM
attributes and sibling elements (`accessibility.ts`'s existing pattern: appended *after* the SVG,
never serialized into it) — a chip is a real, keyboard-reachable element in that tree, not a
`<foreignObject>` or an `<a>` inside the SVG (§6).

Classification is **asynchronous**: our parser (and, on a `'blerg'` verdict, the layout module)
loads lazily behind `await import(...)`, so `render.ts`'s dispatch awaits a verdict before it can
choose a drawing path — unlike `classifyChart`'s synchronous walk over an already-parsed Vega-Lite
object, there is no parsed AST to classify until that import resolves. `render.ts` gains a diagram
branch next to its existing `vega-lite`/`mermaid` dispatch: parse, classify; on `'blerg'`, lay out
and draw with our own code, returning `{component, visual}`; on `'mermaid'`, fall through to
today's `renderMermaid` unchanged (an `<img>`, no component — Mermaid's own path is untouched).
Non-fill and fill sizing reuse `chartSize.ts`'s `VisualFit` exactly as BLG-467 wired it for
charts, bounded by the same drawing-budget discipline as BLG-452 (§9's oversized-diagram row).

## 6. Security

- **Labels**: decoded entities and `<br>` only (§2) — any other markup is "not supported" and the
  whole diagram falls back; our parser's output is plain text, never HTML, by construction.
- **Output**: our drawn SVG always goes through the same `sanitizeSvg` every renderer already uses
  before it reaches the DOM — defense in depth, not a second opinion on our own draw step.
- Our SVG never contains `href`, `<image>`, or `<foreignObject>` — nothing it draws ever
  references or embeds an external resource, and nothing loads over the network; a chip's card/
  file link stays inside Blerg's own React tree (§4), never an `<a href>` inside the SVG itself.

## 7. Gantt (BLG-471)

A third diagram kind on the same classify → parse → layout → draw → fallback skeleton, with its
own small parser (§2) and its own timeline layout (closer to sequence's hand-rolled pass than
flowchart's dagre pass) — `4` `gantt` corpus fixtures already exist, same "enough to start, not
enough to call done" caveat as §1.

Gantt's component (§5's `{component, RenderedVisual}` pair, not a special case of it) is the one
with extra structure inside: a sticky-left lane-name column over a horizontally-scrolling SVG, to
meet BLG-471's pinned-lanes acceptance. Zoom (buttons + ctrl/pinch, fit-to-width) is a layout
rerun at a new time-to-pixel ratio, same discipline as §4's resize reflow, not a CSS transform —
so a zoomed-in gantt is a different layout solution, not a scaled image of the same one. The
exported `RenderedVisual` is the diagram's natural, un-scrolled, un-zoomed extent, for export and
for any host that only wants the static contract.

## 8. Differential plan

For every corpus fixture of a shipped type, compare **identity, not counts**, against the
test-only Mermaid AST read (§2):
- the node set, each with its resolved label text;
- edge triples — `(from, to, label)`, not just a count;
- subgraph membership, per node;
- rank and order per direction (flowchart) — which layer and position dagre assigned versus
  Mermaid's own, within the "layout engines are not bit-identical, meaning is" tolerance already
  used for coordinates;
- sequence participant order and message order, both source-order-sensitive.

A structural mismatch on any of these fails CI — a node/edge count that happens to match while the
actual set or an edge's endpoints differ is exactly the false-pass this replaces. Alongside the
assertions, the corpus run also produces side-by-side screenshots (ours next to Mermaid's `<img>`
output, same fixture) as the card asks — a human-reviewable artifact, not just a pass/fail.

## 9. Failure matrix (real-browser proof)

| Scenario | Expected | Proof |
|---|---|---|
| Subset flowchart/sequence | drawn by us, themed, interactive | differential corpus (§8), real browser |
| Nested subgraph / `par` / unsupported shape / unsupported label markup | falls back to Mermaid, unchanged look | corpus fixture asserting Mermaid's `<img>` output, not ours |
| Dagre layout throws or returns a degenerate graph (cycle, 0 nodes) | falls back to Mermaid, cause logged | fault-injection unit test on the layout call, browser-level fallback assertion |
| Our parser hits unsupported syntax | falls back to Mermaid's own parse path (unchanged) | parser unit tests plus existing `MermaidCorpus*.refusal` fixtures |
| A node label resolves to neither card nor file | plain text node, no chip, no lookup attempted | unit test on the chip-resolution guard |
| Resize at 390 px / 1024 px pane / 3840 px, with and without `fill` | layout reruns, stays readable, no overflow, chips still hit-testable | real-app screenshots through the existing harness pattern (`PopOutMessage.browser.test.tsx`) |
| Keyboard navigation across nodes and edges | roving focus order matches source order, `aria-live` announces each | accessibility unit test, mirroring `accessibility.test.ts` |
| SVG and PNG export | exported bytes match the drawn diagram, sanitized | `visualExport.test.ts`-style round trip on our `RenderedVisual` |
| Hostile labels (`<script>`, `<img onerror>`, long entity runs) | falls back, nothing executes, nothing partially drawn | security unit test asserting fallback, not just sanitizer survival |
| Oversized diagram against the drawing budget | falls back before a slow layout/draw, cause logged | fault-injection test on the budget check, same discipline as BLG-452's point budget |
| Lazy import of the parser/layout module fails (network, build) | falls back to Mermaid, cause logged | fault-injection test mocking the dynamic `import()` rejection |
| Dark/light theme switch mid-session | every color re-reads tokens, no stale hex | theme fixture pass, mirroring `corpus.ts`'s `fixture.theme` cases |

## Open questions for review

- Chip resolution for sequence participants (an actor box, not a free node) — same lending
  contract; confirming the label-matching rule is identical to flowchart's.
- Whether the file-open chip needs `ChatRefs` to grow a `files` lookup (mirroring `cards`) or can
  reuse `openFiles`'s existing single callback with a path argument.
- EPL-2.0 sign-off is moot unless `dagre-d3-es`'s compound-graph layout turns out insufficient in
  practice (§3) — flagged, not expected to be needed for this slice.
