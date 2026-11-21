# BLG-451: charts and diagrams as tabs

Design only — no product code in this change. Reviewer: check before anyone builds.

## 0. What already exists

Most of the plumbing this card needs is already in the tree, under different names:

- A published `.vl.json` or `.mmd` file **already** opens as a real tab and **already**
  follows new versions. `ArtifactViewer` → `FollowingArtifact`
  (`packages/chat/src/components/ArtifactViewer.tsx:109`) polls the file list, swaps in a newer
  version when "Follow latest" is on, and its body (`TextView`,
  `packages/chat/src/components/ArtifactViewer.tsx:436`) already calls
  `visualFile(name, view, text)` (`packages/chat/src/model/visualKind.ts`) and renders a
  `VisualBlock` when the file is a chart or diagram. The tab itself is a `HostedView` of kind
  `'artifact'` (`packages/chat/src/components/ViewerHost.tsx:11`), which `useSessionTabs`
  (`runner/frontend/src/hooks/useSessionTabs.ts:11`) already carries as a first-class
  `SessionView` in the tab strip, with its own entry in the Window/Ctrl+Tab list, drag-to-split,
  etc. **Nothing new is needed for the "published file, opens as a tab, follows versions" half of
  this card.**
- Mermaid is **already** themed through `themeVariables`: `mermaidTheme()`
  (`packages/chat/src/visual/theme.ts:117`) maps Blerg's resolved `VisualTheme` (read live off
  the DOM, with a safe-CSS parser and WCAG-contrast repair, `readVisualTheme`,
  `packages/chat/src/visual/theme.ts:72`) onto Mermaid's full `themeVariables` surface —
  background, text, node fill/border, actor/state/gantt colors, grid, today-line, everything
  Mermaid exposes a variable for. It redraws on `MutationObserver` + `prefers-color-scheme`
  (`observeVisualTheme`, `theme.ts:162`), so theme and light/dark switches already propagate live.
  What's missing is the second half the card asks for — a pass for what `themeVariables`
  cannot reach (§3.5) — plus verification at 390/3840px, which has not been done end to end.
- A drawn chart or diagram is sanitized before it's on screen, but **the sanitized SVG is thrown
  away, not kept — there is nothing for an export to download yet.** `renderVegaLite`
  (`packages/chat/src/visual/vega.ts:37-94`) computes `sanitizeSvg(drawing.svg, {stripAriaLabels:
  true})` into a local `image.src`, draws that decoded `<img>` into an on-screen `<canvas>` (for
  tooltip hit-testing, at a scale capped at `min(devicePixelRatio, 2, …budget)`), and returns only
  a `() => void` cleanup — the sanitized string, the decoded bitmap, and the chosen scale all go
  out of scope when the function returns. `drawMermaid` (`packages/chat/src/visual/mermaid.ts:32-73`)
  runs `sanitizeSvg(svg)` *first*, then resizes the result (bounding it to 4096px, setting
  `width`/`height` on the sanitized element) and serializes *that* into `image.src` — likewise
  discarded; the function returns `() => image.remove()`. Neither renderer hands its SVG string,
  its resolved pixel size, or its decoded image back to its caller today. §3.3 fixes this by
  changing what `renderVisual` returns — there is no second render pass for export, but there is
  currently no first one to export *from* either.

What's actually new, scoped precisely:

1. A renderer contract (`RenderedVisual`) so Mermaid, Vega-Lite and any future renderer hand back
   what they already compute internally, instead of discarding it (§3.3).
2. An inline (unpublished) chart/diagram block gets an "open in a tab" action (§3.1–3.2).
3. The tab — for both an inline snapshot and a published file — gets Download SVG / Download PNG
   / Download source actions, built on that contract (§3.3).
4. Inline snapshot sources get a storage home that isn't the shared tab-layout `localStorage`
   blob — reusing BLG-449's generic bounded snapshot store rather than a chart-specific one (§3.1).
5. A Mermaid theming pass beyond `themeVariables`, done inside the existing sandboxed frame
   (§3.5).
6. Verification: real-timeline and real-tab tests, screenshots at 390 and 3840px (§2, §6).

## 1. Failure rule

**A chart or diagram that cannot be drawn, exported, or kept up to date must never take anything
else down with it.** Concretely, ranked by cost:

- **Never breaks the message it's in.** `VisualBlock` already isolates a draw failure to its own
  block (`visual-failed`, `VisualBlock.tsx:130`) and always shows the raw source instead — the
  rest of the message, and the rest of the chat timeline, keeps rendering and scrolling. The tab
  work must preserve this: a block that fails to draw must still offer `Tab` (opening the
  *source*, not a blank pane) — never disable or hide the action because drawing failed.
- **Never breaks the tab, or the tab strip.** A tab that fails to draw, fails to export, or loses
  its follow (the file went missing, a later version doesn't parse) shows its own in-tab status
  line and source fallback — the same pattern `ArtifactViewer` already uses for a gone file
  (`FILE_GONE_MESSAGE`, `ArtifactViewer.tsx:349`) or a load error (`artifact-error`,
  `ArtifactViewer.tsx:376`). It must never throw past its own render boundary into the tab strip,
  the session shell, or another tab.
- **Never breaks Download.** If SVG/PNG/source export fails (e.g., the sanitizer rejects what the
  renderer drew, or `canvas.toBlob` is refused by the browser), that one download button reports
  its own failure inline (the existing `downloadError` pattern, `ArtifactViewer.tsx:212`) — it does
  not touch the other two downloads or the on-screen drawing.
- **Never re-opens a closed safety gate to make any of the above work.** No export path, no
  "render once more to get crisper pixels" step, and no theming pass may load a URL, run
  `eval`/`Function`, enable Mermaid's `securityLevel: 'loose'`, or accept raw HTML/CSS from the
  diagram source. Every export downloads or rasterizes the SVG the renderer already drew and
  sanitized once (§3.3's `RenderedVisual`), and re-runs `sanitizeSvg()` on it a second time at
  export — defense in depth, not a second opinion on untrusted input: it never re-renders from the
  original diagram source with relaxed rules to get a "nicer" export.
- **Rendering stays in the terminable worker/frame it already uses.** Vega-Lite compiles in a
  Worker (`vegaWorker.ts`, killed on timeout/abort, `vega.ts:9-33`); Mermaid lays out in a
  sandboxed, no-network iframe (`mermaidFrame.ts`). A tab's redraw-on-resize and a tab's export
  reuse these same bounded, killable surfaces — a tab must not add a second, unbounded rendering
  path "for export quality."

## 2. Failure matrix

Layers per `docs/testing-audit.md` §"Layer doctrine": **1** contract, **2** implementation
(real browser/kernel where that's the thing under test), **3a** canned cross-service, **3b** real
services, **4** end to end, **5** staging. All cases below are layer 1–2 (pure functions, or a real
browser against the chat/tab/viewer components with no server) except where noted.

| # | Case | Expected behavior | Proof | Layer |
|---|------|--------------------|-------|-------|
| 1 | Inline Mermaid/Vega-Lite draws fine, person clicks `Tab` | A `visual` tab opens; the tab draws the same picture from the same source | New `.browser.test.tsx` opening a real chat message with a fence, clicking `Tab`, asserting the tab's surface matches the inline one | 2 |
| 2 | Inline block fails to draw (refused source, or a renderer throw) | The inline block shows source + reason (already true); `Tab` is still present and still offers the source in a tab | Extend `VisualBlock.test.tsx`: a refused `DiagramRefused`/`ChartRefused` source still renders the action row | 1/2 |
| 3 | A snapshot tab's source itself is corrupt/refused at open time (race: source changed between the `Tab` click and tab mount) | Tab shows the same `visual-failed` pattern as the inline block — never a blank or crashed tab | `.browser.test.tsx`: mutate source between click and mount, assert tab renders fallback, not a React error boundary | 2 |
| 4 | Published `.vl.json`/`.mmd` opened as a file, draws fine | Tab shows the drawing (already true via `TextView` → `visualFile` → `VisualBlock`) | Existing `ArtifactViewer` coverage; add one assertion that `visualFile` detection still fires inside the tab path specifically | 1 |
| 5a | Published chart file gets a new version while its tab is open, "Follow latest" on, new version draws fine | Tab redraws the new version in place | Extend `FollowingArtifact`'s existing success path, parametrized with a visual file | 2 |
| 5b | Same, but the new version fails to pre-render (§3.6) | The still-good previous drawing stays on screen with the existing "previous version is still shown" banner — never blanked, never remounted | New case on `validateAndAccept` (§3.6): inject a `renderVisual` throw for the new version, assert `shown`/the mounted `VisualBlock` are untouched | 2 |
| 5c | Same scenario, but the person clicks the version-available dot (`onVersionUpdate`, the manual path) instead of waiting for auto-follow | Same outcome as 5b — manual accept goes through the same `validateAndAccept` check, not a bare `setShown` | New case: click the version dot on a version that fails to draw; assert the session does not remount and the old drawing survives | 2 |
| 6 | Published chart file's newest version is deleted/gone | Tab shows the existing `FILE_GONE_MESSAGE` fallback, not a draw attempt | Existing `artifact-gone` test path, parametrized with a visual file | 1 |
| 7 | Download SVG | Re-sanitized output is a valid, policy-passing SVG whose drawn content matches `rendered.svg`; this holds for a Mermaid-sourced, a Vega-Lite-sourced, and (once it lands) a BLG-452-sourced `RenderedVisual` through the one shared export function (§3.3's renderer-agnostic test) | New export unit test, parametrized by renderer | 1 |
| 8 | Download PNG | PNG is rasterized at the shared `exportScale(width, height)` (§3.4) — the same clamp the on-screen canvas already applies — with the resolved theme surface color painted as background first (no transparency "leaking" through on paste); holds for every renderer via the same parametrized test as case 7 | `.browser.test.tsx`: stub `devicePixelRatio=2`, assert output canvas matches `exportScale`'s result and a corner pixel equals `theme.surface`, for each renderer | 2 |
| 9 | Download source | For a snapshot, the exact text read back from the snapshot store (§3.1.1) by `id`; for a published file, the exact file bytes at the shown version — not the rendered picture | Unit test per path | 1 |
| 10 | Any export attempted on a diagram the policy already refused | `rendered` is `null` (nothing drew), so SVG/PNG are unavailable — but **Download source is still offered**: a refused spec is still just text, and the person should still be able to get it back out | `policy.security.test.ts`-style case: refused source → SVG/PNG actions absent, source action present and functional | 1 |
| 11 | Mermaid's `themeCSS` pass (§3.5) changes a themeable property (stroke width, corner radius, label padding) | Dark and light render with Blerg's token colors/shapes, generated entirely inside the sandboxed frame; a generated `themeCSS` string never fails `sanitizeSvg`'s `safeStyle()` check | New `mermaid.theme.browser.test.tsx` (pattern: existing `MermaidTextLabels.browser.test.tsx`) rendering one diagram of each Mermaid grammar in both themes, screenshot diff | 2 |
| 12 | Diagram readable at 390px and at 3840px | No horizontal scroll forced on the message/tab shell at 390; no oversized/blurry raster at 3840 (vector stays vector until the PNG export step) | Screenshots at both widths through the real timeline and the real tab, attached to the PR per §6 | 2/4 |
| 13 | Safety: refused Mermaid feature (click, link, `%%{…}%%`, `url()`, HTML) still refused inside a tab/export path | `policy.security.test.ts`/`theme.security.test.ts` continue to pass unmodified; no new code path (including export's re-`sanitizeSvg`) bypasses `validateMermaid`/`sanitizeSvg` | Existing security suites, re-run unmodified; a new case added only if a new entry point (export) skips validation — it must not | 1 |
| 14 | Chart compiling in the Worker hits its deadline/heavy-chart budget while a tab (not just the inline block) is open | Tab shows `HEAVY_CHART`'s message, same as inline; Worker still terminates on timeout (existing `CHART_DEADLINE_MS`/`finish` path, `vega.ts:9-33`) | Extend existing Worker-timeout test to assert the tab-hosted surface also receives the same rejection | 2 |
| 15 | Two tabs of the same inline chart are opened (duplicate `Tab` click, or a split-pane duplicate) | Each tab renders and fails independently; one tab's abort/teardown never cancels the other's render (each render owns its own `AbortController`, `VisualBlock.tsx:22-25`) | `.browser.test.tsx`: open two tabs of the same source, fail one via injected error, assert the other is unaffected | 2 |
| 16 | A popped-out source exceeds BLG-449's per-snapshot (1 MB) or per-account (8 MB) cap (§3.1.1) | The tab still opens and still draws (source came from the already-rendered message); BLG-449's own over-cap signal fires for this snapshot exactly as for one of its own; the shared tab-layout `localStorage` write (`sessionTabsKey`) is unaffected | Unit test on BLG-449's store, parametrized with a chart/diagram snapshot: fill it past either cap, assert the over-cap path (never a throw), assert `sessionTabsKey`'s own `setItem` is never called by the snapshot path | 1 |
| 17 | A `visual` tab is restored after a reload | If its snapshot-store entry is still there, the tab is restored and draws as before; if not (over cap, or a new browser session), the tab is dropped from the restored layout — the same outcome a 'message' tab already has in this situation, not a third, chart-specific behavior | `.browser.test.tsx`/hook test: reload with a saved `visual` tab and (a) its snapshot entry present, (b) absent; assert the tab is present and drawn in (a), absent in (b) | 2 |
| 18 | `localStorage` write for the shared tab-layout state throws (quota) while one or more `visual` tabs are open | BLG-449's store writes under its own separate keys and is unaffected (different storage, different quota bucket from `sessionTabsKey`); the existing `flushSave` catch still swallows only the layout write's own failure, not a `visual` tab's content | Unit test: stub `localStorage.setItem` to throw for `sessionTabsKey` only, assert a concurrent write to BLG-449's store still succeeds | 1 |
| 19 | `canvas.toBlob` rejects with `SecurityError` during PNG export | The PNG download reports failure through the existing `downloadError` pattern; SVG/source downloads and the on-screen drawing are unaffected | Unit test: stub `toBlob` to reject, assert only the PNG action surfaces an error | 1 |
| 20 | The theme changes between a successful draw and a click on Download | `onRendered` already nulled `rendered` for the redraw's duration (§3.4); the download actions are disabled for exactly that span, so a click can't export a `RenderedVisual` that no longer matches what's on screen | `.browser.test.tsx`: trigger a theme change mid-render, assert download actions are disabled until the new `RenderedVisual` arrives | 2 |
| 21 | End-to-end: a real session publishes a `.vl.json`, the person edits it, republishes, watches the tab update, downloads SVG/PNG/source | One full user path through a real local install | Layer 4 case in the pre-merge e2e inventory, added once this ships — not required for this design doc's review | 4 |

## 3. Design

### 3.1 Identifying an inline chart for a tab (the "snapshot")

**Status: built.** BLG-449 merged with its snapshot store at `runner/frontend/src/lib/
messageSnapshots.ts`, already written kind-agnostic (`saveSnapshot`/`loadSnapshot` operate on a
body + id, not a message) — renamed to `snapshots.ts` ahead of this card reusing it unchanged, as
the file's own comment already anticipated. Everything below (§3.1.1's storage reuse, §3.2's `Tab`
button, `useSessionTabs.ts`'s `validView`/`read`/`open`/`duplicate` wiring) matches what actually
shipped, not merely what was planned here before BLG-449 landed. The "won't be kept after reload"
wording is `MessageViewer`'s own established copy ("This message was too large to keep — it won't
be here after a reload."), reused verbatim for a diagram/chart ("This diagram/chart was too large
to keep…") rather than a second, differently-worded message.


An inline chart/diagram has no `ArtifactInfo` — it's text inside a chat message, detected the same
way it always has been: a fenced code block whose info string is `mermaid` or `vega-lite`
(`visualFence`, `packages/chat/src/model/visualKind.ts:4`), rendered by `VisualBlock`.

A tab needs something to hold onto once the message scrolls away or the source line is edited
mid-stream, so the tab is explicitly a **snapshot**: the `source` string and `kind` captured at the
moment `Tab` is clicked, carried by value into the tab's view, never a live reference
back into the message DOM or the streaming buffer. This matches the card's instruction ("an inline
one opens as a snapshot") and the existing precedent in `FollowingArtifact` of a React `key` tied
to a content identity, not a position.

New `HostedView` member (`packages/chat/src/components/ViewerHost.tsx:11`) — note what it does
**not** carry:

```ts
| { kind: 'visual'; id: string; visualKind: VisualKind; label?: string }
```

`id` is a content hash of `kind + source` (same pattern as `VisualBlock`'s existing remount key,
`` `${kind}:${source}` ``, `VisualBlock.tsx:106`) so that popping out the *same* unchanged block
twice reuses one tab (`useSessionTabs.open`, which looks tabs up by `viewId`,
`useSessionTabs.ts:27-33`) instead of stacking duplicates, while a block whose source has since
changed pops out as a distinct tab (case 15 in §2 is about two *independent* pop-outs of two
different moments, which is allowed and must not interfere with each other — a hash key only
dedupes an identical, unchanged source).

`tabLabel`/`baseViewId` (`useSessionTabs.ts:32,40`) grow one more arm: label is `label ?? (kind ===
'mermaid' ? 'Diagram' : 'Chart')`, matching the existing block headers ("Mermaid" / "Vega-Lite",
`VisualBlock.tsx:136`).

#### 3.1.1 Where the source text actually lives

`useSessionTabs` persists its whole tab/layout state — every tab's `SessionView`, for every group,
for every open shell — as one JSON blob in one `localStorage` key per account+session
(`sessionTabsKey`, `useSessionTabs.ts:26`; written by `flushSave`, `useSessionTabs.ts:160-166`,
inside a bare `try { localStorage.setItem(...) } catch { /* Storage can be unavailable. */ }`). A
chart or diagram's source text is already capped per-chart by policy — `MERMAID_MAX = 16_384`
(`policy.ts:7`), `MAX_CHART_SOURCE = 131_072` (`vegaPolicy.ts:13`, 128KB) — but that cap is *per
chart*, not per session: several popped-out tabs, each near its own cap, stacked inside the one
shared blob that also carries every shell and every other tab's state, can push the combined write
over the browser's per-origin quota. When that `setItem` throws, the existing `catch` swallows it
silently — **and because the write is one blob, not an incremental append, that one failure stops
saving *every* tab, shell position and layout for the rest of the session, not just the oversized
one.** A `source` field would also sit in `localStorage` indefinitely — it is never cleared at
sign-out — which is worse the more a diagram's content happens to matter.

So a `visual` `HostedView` carries no `source` at all; the field above is deliberately only `id` +
`visualKind` + `label`. **This card does not design its own store for the text: BLG-449 is already
building a generic bounded snapshot store — separate keys per account, session and snapshot id,
cleared on sign-out, capped at 1 MB per snapshot and 8 MB per account — shared with its own
`Duplicate` action.** A chart/diagram snapshot's source is written there under the same `id` the
`visual` tab is opened with, exactly as BLG-449's own snapshots are. This is a second reason the
`HostedView` above carries no `source`: there would otherwise be two stores answering "where does
a popped-out tab's content live," which is the kind of duplicated mechanism this codebase's
conventions already argue against.

Two things fall out of reusing BLG-449's store rather than inventing a second one:

- **The caps are BLG-449's, not a new number for this card to pick.** A single chart/diagram
  source is already policy-capped well under the 1 MB per-snapshot limit (128 KB for Vega-Lite,
  16 KB for Mermaid) — this card's snapshots essentially never hit that ceiling on their own. The
  8 MB per-account ceiling is shared across *every* BLG-449 snapshot in the account, chart/diagram
  ones included — a person with many popped-out charts and many popped-out messages draws against
  the same budget, which is the right shape: one stated cap, not a chart-specific one layered on
  top of a message-specific one.
- **Over-cap behavior is BLG-449's to define once, not this card's to redefine.** Whatever BLG-449
  surfaces when a snapshot doesn't fit (open but unsaved, a status line, or however it signals
  this) is what a chart/diagram snapshot gets too — this doc does not invent a second "won't
  survive a reload" message distinct from BLG-449's own. If BLG-449 hasn't settled that wording by
  the time this card is implemented, it's a one-line addition to BLG-449's existing mechanism, not
  a parallel one here.

`useSessionTabs`'s `validView` (`useSessionTabs.ts:61-75`) needs the matching arm for the `visual`
kind specifically — without it, a restored `visual` tab falls through to the final `return
view.kind === 'artifact' && …` check, which is `false` for kind `'visual'`, and the entire tab is
silently dropped from the restored layout (not shown as a fallback — gone, as if never opened):

```ts
if (view.kind === 'visual') return typeof view.id === 'string' && (view.visualKind === 'mermaid' || view.visualKind === 'vega-lite') && typeof view.source === 'string' && typeof view.label === 'string'
```

Per code review: this doc's earlier draft proposed a *third* outcome — the tab stays, showing a
source-missing fallback — when BLG-449's actual, shipped behavior for a 'message' tab in this
exact situation is simpler and already established: **the tab is dropped**, the same as any other
saved tab whose view no longer resolves. `read()` (`useSessionTabs.ts:88-98`) already has this
shape for 'message' — the flatMap step that swaps in the snapshot-store body returns `[]` (not the
tab) when `loadSnapshot` comes back `undefined` — and `'visual'` reuses that same step, same
behavior: `validView` accepts the *saved shape* (the serialized tab always carries `source: ''`, a
placeholder stripped before persisting, §3.1.1), but `read()`'s own flatMap is where a missing
snapshot body actually decides whether the tab survives. Keeping this consistent with 'message'
matters more than the alternative this doc once proposed: one dropped-tab behavior for every
snapshot kind, not two different ones a person would have to learn separately.

### 3.2 The pop-out action

`VisualContent`'s action row (`VisualBlock.tsx:135-142`) is `<span>{kind label}</span>` followed by
three **plain, icon-less text buttons**: `Source`, `Copy`, `Open`. That row's existing convention
is text, not icons — unlike `ViewerToolbar`'s utility bar (§3.4), which is all icons. These are two
different, already-established rows with two different existing looks; the new control follows the
one it's joining, not the toolbar it happens to open. So the new control is a **fourth text
button** in this same row, not an icon: `Tab`. (Not "Open" — that name is already taken by the
zoom overlay below, and reusing it on a neighboring button would be exactly the kind of same-word-
different-thing confusion the UX rules call out.)

BLG-449 separately adds a quiet pop-out *icon* to a message's own action row (a different
component, a different row, already icon-based there) and a `'message'` `HostedView` kind. This
card does not reuse that icon inside `VisualBlock`'s row — the row it's landing in is text, and
mixing one icon into three text buttons is the same "mixing words and icons" failure the UX rules
name. What this card does reuse from BLG-449 is the *mechanism* (a `HostedView` kind opening a
tab through `useViewerHost()`) and BLG-449's choice of wording for the action, so "open as a tab"
reads the same way wherever a person meets it.

"Open" today launches the in-place `FullScreen` zoom overlay (`VisualBlock.tsx:49-93`) — that
stays (it's the quick full-bleed zoom-and-pan view people already use, with its own zoom controls;
pulling every click into a tab would be a regression, not a fix). `Tab` is the new, additional
control, calling the surrounding `ViewerHost` and handing the snapshot store (§3.1.1) the source
it needs to keep, by the same `id` the tab itself is opened with:

```tsx
const host = useViewerHost()
{host && <button type="button" onClick={() => {
  const id = hash(kind, source)
  snapshotStore.put(id, source) // §3.1.1: BLG-449's generic snapshot store, bounded, id-keyed
  host.open({ kind: 'visual', id, visualKind: kind })
}}>Tab</button>}
```

`host` is `undefined` wherever `VisualBlock` renders outside a tab-hosting surface (e.g. a
markup/review pane with no `ViewerHostContext.Provider` above it) — the button simply doesn't
render there, the same guard `ArtifactViewer` already uses (`host &&`, `ArtifactViewer.tsx:103`).

`VisualThumb` (the hovercard-size preview, `VisualBlock.tsx:151`) gets no `Tab` button — it's
already a miniature of a block that has one; the acceptance criterion is "every inline chart in
chat," meaning the block, not every thumbnail of it.

### 3.3 The renderer contract: `RenderedVisual`

Per review: this is a decision, not an option. `renderVisual` (`render.ts:4`) currently returns
`Promise<() => void>` — a cleanup function and nothing else, matching what `renderMermaid`/
`renderVegaLite` return today (§0). That contract changes to:

```ts
export interface RenderedVisual {
  /** The exact SVG string the renderer drew and sanitized once — the same bytes shown on
   *  screen, already bounded by that renderer's own size/area limits. */
  svg: string
  width: number
  height: number
  dispose(): void
}
export async function renderVisual(kind, source, container, theme?, signal?): Promise<RenderedVisual>
```

`renderMermaid` and `renderVegaLite` each change to return this instead of discarding their
sanitized string and decoded image (§0):

- **Vega-Lite** (`vega.ts:37-94`): `svg` is the `sanitizeSvg(drawing.svg, {stripAriaLabels:
  true})` string it already computes before drawing it into the on-screen canvas; `width`/
  `height` are `drawing.width`/`drawing.height` (the logical size, already clamped to
  `MAX_CHART_SIDE`/`MAX_CHART_AREA` — not the device-scaled canvas pixel size, which is a display
  concern). `dispose` is the existing teardown.
- **Mermaid** (`mermaid.ts:32-73`): `svg` is the sanitized, *already-resized* SVG string — the
  serialization of `safe` after the `width`/`height` bounding (`mermaid.ts:54-62`), i.e. exactly
  the bytes that end up in `image.src`. `width`/`height` normally come off the resolved
  `viewBox`×`scale` the resize step already computes. Per review: when a diagram's SVG has no
  `viewBox` (the resize step's existing fallback, `mermaid.ts:54-55`'s `bounds?.length === 4`
  check failing), there is nothing on the *main* thread to measure it from — `safe` is a detached,
  unlaid-out DOM, so `getBoundingClientRect()`/`getBBox()` on it answers `0`. The only place a
  real bounding box exists is inside the frame, where Mermaid just drew the diagram into a live,
  attached document (`mermaidFrame.ts`'s `host`). So `renderInFrame`'s response grows an optional
  `measured: {width, height}`, computed in the frame (via a detached-from-response probe element,
  `host.append`ed only long enough to read `getBBox()`, then removed) exactly when the frame's own
  root lacks a `viewBox`; `drawMermaid` uses `measured` in place of `bounds` when `bounds` is
  absent, through the same 4096px-side scale clamp. `dispose` is the existing `() => image.remove()`.
- **BLG-452's renderer** conforms to the same interface. This is the one contract every renderer
  implements — BLG-452's worker has been told to return `RenderedVisual` rather than `()=>void`;
  this card does not otherwise depend on or describe what BLG-452 renders.

This makes export **renderer-agnostic**: everything in §3.4 operates purely on a
`RenderedVisual` — it has no branch on `kind`, no renderer-specific code, and would work unchanged
for a fourth kind added after this card ships.

Per review: every current caller treats `renderVisual`'s result as a bare function, so every one
of them changes to call `.dispose()` instead of invoking the return value directly. This is the
full list, so none is missed:

- `VisualBlock.tsx:31-33` (`Surface`'s render effect) — `cleanup => { ...; dispose = cleanup }`
  becomes `visual => { ...; dispose = visual.dispose }` (and, once built, feeds `onRendered`, §3.4).
- `ArtifactViewer.tsx:150-151` (`FollowingArtifact`'s off-screen pre-validate render) —
  `const dispose = await renderVisual(...); dispose()` becomes `const visual = await
  renderVisual(...); visual.dispose()`.
- `WorkspaceFileViewer.tsx:155` — same shape, same fix: `const dispose = await renderVisual(...);
  dispose()` becomes `const visual = await renderVisual(...); visual.dispose()`.
- Mocks that currently return a bare function from a mocked `renderVisual`/`renderMermaid`/
  `renderVegaLite` and so must return a `RenderedVisual` instead:
  `packages/chat/src/components/ArtifactCard.diagram.test.tsx`,
  `packages/chat/src/visual/VisualBlock.test.tsx`, `packages/chat/src/visual/mermaid.test.ts`,
  `packages/chat/src/visual/mermaid.dimensions.security.test.ts`,
  `packages/chat/src/visual/vega.worker.test.ts`,
  `runner/frontend/src/components/MermaidTextLabels.browser.test.tsx`,
  `runner/frontend/src/components/ChartWorker.browser.test.tsx`, and
  `runner/frontend/src/test/mermaid/corpus.ts`.

They simply also now have `.svg`/`.width`/`.height` available, which `VisualViewer` (§3.4) and the
download-validation path (§3.6) are the first to use.

A dedicated test (new, alongside `vega.ts`/`mermaid.ts`'s existing unit suites) runs the export
path from §3.4 against **each** renderer's `RenderedVisual` in turn — one Mermaid diagram, one
Vega-Lite chart, and (once it lands) BLG-452's kind — asserting each produces a valid, re-sanitized
SVG string and a non-empty PNG blob through the exact same export function, with no renderer-
specific code path inside that function. This is the test that proves "renderer-agnostic" rather
than merely asserting it in prose.

### 3.4 The tab body and its downloads

`HostedViewers` (`ViewerHost.tsx:48-56`) grows one more arm alongside `workspace`/`commit`/
`artifact`:

```tsx
else if (view.kind === 'visual') body = <VisualViewer visualKind={view.visualKind} source={view.source} label={view.label} kept={view.kept} onClose={() => host.close(id)} />
```

Per code review: there is no separate "source missing" state for `VisualViewer` to render. The
snapshot-store lookup by `id` happens once, earlier, in `useSessionTabs`'s own `read()` (§3.1.1) —
the same place 'message' already resolves its body — and a tab whose snapshot didn't survive never
reaches `HostedViewers` at all (dropped from the restored layout, matrix case 17). By the time
`view.source` reaches `VisualViewer`, it is always real source text, the same guarantee
`MessageViewer`'s `text` prop already has. `VisualViewer` draws it unconditionally through the
exact same `renderVisual`/`readVisualTheme`/`observeVisualTheme` pipeline `VisualBlock` already
uses (no second renderer, no second sanitizer — now returning `RenderedVisual`, §3.3), keeps the
latest `RenderedVisual` in its own state, and puts `ViewerToolbar`
(`packages/chat/src/components/ViewerToolbar.tsx:7`) above it — the same toolbar shell
`ArtifactViewer` uses, so a `visual` tab and an `artifact` tab that happens to be a `.vl.json` look
identical in chrome. It carries no version-follow machinery itself (a snapshot is frozen by
definition).

**The channel from a drawn visual to its download actions.** `headerActions` is the wrong place
for these: it's a `ReactNode` that `FollowingArtifact` computes in its own component (for the
Follow/Pin button, `ArtifactViewer.tsx:176-182`) and hands down to `ArtifactViewerSession` as a
prop — but the `RenderedVisual` only exists where the body actually draws, two components further
down (`TextView` → `VisualBlock` → `Surface`), and only intermittently (absent while drawing, null
on failure). Reusing `headerActions` for this would mean threading render state *up* through a
prop whose owner is a sibling branch, not a parent, of where the drawing happens. Instead:

- `VisualBlock` takes an optional `onRendered?: (visual: RenderedVisual | null) => void` and calls
  it from `Surface`'s existing render effect (`VisualBlock.tsx:23-45`): with the `RenderedVisual`
  once a render resolves, with `null` the moment a redraw starts (source, theme, or container-width
  change) or a render fails — so a consumer never holds a stale `RenderedVisual` across a draw in
  flight.
- `VisualViewer` and `ArtifactViewerSession` (for a published visual file) each hold
  `const [rendered, setRendered] = useState<RenderedVisual | null>(null)` fed by that callback, and
  render the three download `ViewerAction`s **themselves**, directly in their own `ViewerToolbar`
  usage — not through `headerActions` — disabled whenever `rendered` is `null` (nothing drawn yet,
  or the last draw failed: §3.6 covers the published-file version of this).
- For a published file this is the *only* toolbar change needed beyond what `FollowingArtifact`
  already does: `ArtifactViewerSession`'s existing `TextView` call already detects `visualFile`
  (`ArtifactViewer.tsx:451-452`); it grows the same `onRendered` wiring, scoped to when
  `visualFile` is non-null.

Three new `ViewerAction`s (`ViewerToolbar.tsx:34-53` gains `downloadSvg`, `downloadPng`, and
reuses the existing `download` icon for source — see open question 1 on whether three distinct
icons or one icon + a small menu reads better), each operating only on the current `rendered`:

- **Download SVG** — re-runs `sanitizeSvg(rendered.svg)` (defense in depth, per the failure rule —
  this is the renderer-agnostic re-sanitization the review asked for, not a second opinion on the
  original diagram source) and serializes the result as a `.svg` file. No re-render, no network —
  a `Blob` download of a string already in memory (failure matrix case 7).
- **Download PNG** — decodes `rendered.svg` (after that same re-sanitization) into a fresh
  `Image`, awaits `image.decode()`, and draws it onto an off-screen `<canvas>` sized to
  `rendered.width`/`rendered.height` × a new, export-only `exportScale(width, height)`:
  `Math.min(devicePixelRatio || 1, 2, MAX_CHART_SIDE / width, MAX_CHART_SIDE / height,
  Math.sqrt(MAX_CHART_AREA / (width * height)))`. Per review: this is a **sibling** to
  `vega.ts:58-60`'s existing display-path `scale`, not a refactor of it — Vega-Lite's own
  `Math.max(1, Math.min(...))` floor at `vega.ts:62` stays exactly as it is (its chart is already
  policy-clamped to `MAX_CHART_AREA` *before* that scale applies, so the floor never pushes the
  on-screen canvas over budget there). `exportScale` has no such floor, because it also has to
  serve Mermaid, whose `width`/`height` are only clamped per-side to 4096px (§3.3), not by area —
  a large, roughly-square diagram can already exceed `MAX_CHART_AREA` at `scale === 1`, something
  Vega-Lite's own pre-clamped chart never does. So `exportScale` can legitimately return less than
  1; the PNG is then smaller than `rendered.width × devicePixelRatio` would suggest, and the tab
  says so (§3.7) — it never silently fails or truncates. Mermaid's on-screen *display* has no
  canvas at all (it's a plain, CSS-scaled `<img>`, §0) and stays that way; `exportScale` only ever
  runs at export time, which is the first time Mermaid's output needs a pixel-budgeted canvas.
  Export then fills the canvas with `theme.surface` first (so a PNG pasted into something with its
  own white/dark background doesn't show a transparency checkerboard or clash), `drawImage`s the
  decoded image at that scale, then `canvas.toBlob('image/png')`. This is a fresh decode of the
  already-sanitized string, not a reuse of the display's live `<img>` — that element may already
  be disposed by the time Download is clicked, and a fresh decode keeps the export path fully
  self-contained and renderer-agnostic (§3.3's test exercises exactly this path).
- **Download source** — for a snapshot: the source text read from the snapshot store (§3.1.1) by
  `id`, as `.mmd` or `.vl.json` (client-side `Blob` download, no server round-trip — there is no
  artifact to ask for). For a published file: the existing `files.download(sessionId, artifact.id)`
  call `ArtifactViewer` already makes (`ArtifactViewer.tsx:271-279`) — unchanged, and available
  even when `rendered` is `null` (case 10 in §2: a refused spec has no drawing, but its source is
  still just text).

All three live in the **same utility bar** as every other per-object action (`ViewerToolbar`'s
`children`), per `runner/skills/ux-design`'s "keep one object's actions in one utility bar" —
not a new menu, not a second row.

### 3.5 Mermaid theming: `themeVariables` plus `themeCSS`, both inside the frame

Per review: the previous draft of this section contradicted itself — it described a pass over
"the already-sanitized SVG string," run "before it's handed to `sanitizeSvg()`." Fixed design:
**there is no SVG rewrite outside the sandboxed frame, and no second rendering path.** Everything
below happens inside the one existing frame (`mermaidFrame.ts`), as part of the one existing
`mermaid.render()` call, before the one existing `sanitizeSvg()` call that already runs right
after `renderInFrame` resolves (`mermaid.ts:38,47`).

`themeVariables` already carries every color Mermaid itself names a variable for (§0) — and,
per review, it must stay the *only* place font family and font size go: Mermaid measures text
against those values *before* it lays a diagram out (node sizes, edge routing all depend on how
wide a label measures), so anything font-related has to be in `themeVariables`, resolved before
layout, not applied to the SVG after. `fontFamily`/`fontSize` are already there
(`mermaidTheme`, `theme.ts:122`) — nothing new is needed for font metrics; this section is scoped
down to **paint only**: the handful of fixed `stroke-width`/`rx` values Mermaid hard-codes on some
diagram types' composite borders and cardinality markers, which have no `themeVariables` entry at
all. Those are reached the same way Mermaid itself already supports reaching paint it doesn't
variabilize: `themeCSS`, a plain string of CSS rules Mermaid applies to its own generated classes
*during* `mermaid.render()`, inside the frame — after layout has already happened, which is
exactly why font rules don't belong there and stroke/radius rules are safe there (they don't
change any element's measured size, only how its already-fixed outline is painted).

`themeCSS` is one of the keys `CONFIG.secure` names (`mermaid.ts:17`) — not an allow-list of keys
the frame may receive (the frame already receives whatever config we hand it; `secure` is Mermaid's
own list of keys a diagram's `%%{init: …}%%` directive is not allowed to override, a second,
Mermaid-internal layer behind the policy validator's own blanket refusal of any `%%{…}%%` directive
at all, `policy.ts:63-64`). `mermaidTheme()` (`theme.ts:117`) simply hasn't set `themeCSS` yet. This
card adds it: a small, fixed, diagram-source-free string of paint rules (stroke width, corner
radius) matching the small fixed values the rest of the chat chrome already uses (e.g.
`visual.css`'s `1px solid` borders and `4px`–`8px` radii) — not read from a `VisualTheme` token,
since none of width/radius is themeable today; only the *colors* `themeCSS` might reference (if
any) come from `theme`, exactly as `themeVariables` already does — passed through
`{...CONFIG, ...mermaidTheme(theme), fontFamily: theme.fontFamily}` into `renderInFrame`
(`mermaid.ts:38`) exactly where `themeVariables` already goes today.

Mermaid applies that CSS to its own generated classes as part of `mermaid.render()`, inside the
sandboxed, no-network frame, under the same `strict` security level as everything else it draws.
The SVG that comes back already has the corrected strokes/radii baked in by Mermaid's own
renderer — `sanitizeSvg()` then runs exactly once, exactly where it already does
(`mermaid.ts:47`), on that output. A generated `themeCSS` string built only from color/length
values (no `url()`, no `@import`, no selectors touching anything but Mermaid's own classes) passes
`sanitizeSvg`'s existing `safeStyle()` check (`policy.ts:141-146`) unchanged — no loosening of that
check is needed or made.

**What can't be themed, and is left alone:** Mermaid's layout algorithm itself (node positions,
edge routing) is unthemed and stays unthemed — this card is a paint job, not a layout rewrite.
Diagram types Mermaid renders through `<foreignObject>` HTML labels are already excluded by
`htmlLabels: false` (`mermaid.ts:11-13`) for security; `themeCSS` reaches Mermaid's own SVG
classes, not arbitrary HTML, so this stays within what was already excluded, not a new hole in it.

Vega-Lite needs no equivalent pass: `vegaTheme()` (`theme.ts:150`) already drives every themeable
Vega-Lite config surface (axis, legend, mark, title, category range) with no un-variabilized
leftovers — this is a Mermaid-specific gap, not a general visual-theming gap.

### 3.6 Keeping the last good drawing when a followed version fails

`FollowingArtifact`'s background effect (`ArtifactViewer.tsx:124-166`) already does the right
thing for its *automatic* path: it only calls `setShown({ artifact: latest, text })` after a new
version has been fetched and (for a visual file) successfully pre-rendered off-screen
(`ArtifactViewer.tsx:150`); on any failure it sets `failed` and leaves `shown` — and the drawing on
screen — untouched. The gap is the *manual* "a newer version is available" path: the version-dot
button's `onVersionUpdate` (`ArtifactViewer.tsx:185`) calls `setShown({ artifact: latest })`
directly, with no pre-render check. Because `ArtifactViewerSession` is keyed on
`artifactIdentity(shown.artifact)` (`ArtifactViewer.tsx:106`), that swap **remounts** the session —
the old `VisualBlock`, and whatever it had drawn, is gone before the new version's draw is even
attempted. If the new version fails to draw, what's left on screen is that version's own failure
state, with no memory that an earlier good drawing ever existed.

Fix: route both paths through the one pre-render check `FollowingArtifact` already has. Pull the
existing "fetch the new version, and if it's a visual file render it off-screen before accepting
it" logic (`ArtifactViewer.tsx:134-162`) into one `validateAndAccept(next: ArtifactInfo)` helper
that both the automatic effect and `onVersionUpdate` call. `onVersionUpdate` becomes `() =>
latest && validateAndAccept(latest)`; on success it swaps `shown` exactly as it does now; on
failure it does what the automatic path already does — sets `failed`, leaves `shown` (and the
still-mounted, still-drawn old version) exactly as is, and the existing banner ("Could not update
preview. The previous version is still shown.", `ArtifactViewer.tsx:184`) explains why nothing
changed. No new user-facing message is invented; the one that already exists for the automatic
case now also covers the manual one.

### 3.7 Export edge cases

Four cases the export path (§3.4) must handle explicitly, each with its own matrix row (§2):

- **The DPR/area clamp matters for Mermaid's PNG export specifically — it has no equivalent
  today, because Mermaid's display path has no canvas to clamp.** `exportScale` (§3.4) is a new,
  export-only function precisely because Mermaid needs it: Vega-Lite's chart is already
  area-clamped to `MAX_CHART_AREA` before its own display scale ever applies, but Mermaid's
  `width`/`height` are only clamped per side (4096px, §3.3) — a large, roughly-square diagram
  (common at a 3840px tab width) can already exceed `MAX_CHART_AREA` at `scale === 1`, before
  `devicePixelRatio` is even applied. `exportScale` has no `Math.max(1, …)` floor (unlike Vega-
  Lite's own display-path scale, which keeps its, §3.4) precisely so it can drop below 1 here.
  When it does, the PNG download includes one line noting it: "Exported at a lower resolution to
  stay within size limits" — this must never happen silently, and the export must never simply
  fail or truncate instead of clamping.
- **`canvas.toBlob` can reject with a `SecurityError`** if the browser ever considers the canvas
  tainted. Nothing in this design should taint it — the decoded image is always a `data:` URI
  (opaque-origin-safe, same as every other image this codebase already draws from one), never a
  cross-origin `http(s):` URL — but the download action still wraps the call and reports failure
  through the same `downloadError` pattern as every other download (§1's "never breaks Download"),
  rather than assuming the browser can never surprise it.
- **Fonts not installed on the viewer's machine.** PNG export never needs to embed a font: it
  decodes the SVG into an `<img>` and lets the browser's own image rasterizer resolve
  `font-family` the same way it already does for the on-screen drawing — whatever substitution
  the browser makes there is what the PNG shows, consistently with what the person is already
  looking at. For the **SVG** download, opened later on a different machine, there is no embedding
  either — `safeFont()` (`theme.ts:65-70`) already guarantees every `font-family` value it
  resolves ends in a generic fallback (`system-ui, sans-serif` when the configured stack doesn't
  parse as safe CSS), so an opened SVG always has a safe stack to fall back to. No new embedding
  or outlining is added; this is the existing safe-stack guarantee, stated as the answer rather
  than left implicit.
- **The theme changes between a draw and a click on Download.** `observeVisualTheme` (`theme.ts:162`)
  already triggers a redraw on a theme change, which (via `onRendered`, §3.4) sets `rendered` to
  `null` the instant the redraw starts and back to the new `RenderedVisual` once it resolves. The
  download actions, already disabled whenever `rendered` is `null` (§3.4), are therefore disabled
  for exactly the span of a theme-triggered redraw — a click landing in that window simply can't
  happen, rather than exporting a `RenderedVisual` that no longer matches what's on screen.

### 3.8 Readability at 390px and 3840px

Both the inline block and the tab already size by container width (`VisualBlock`'s
`ResizeObserver`-driven redraw for Vega-Lite, `VisualBlock.tsx:37-43`; Mermaid's natural-aspect SVG
with a `4096px`-bounded intrinsic size, `mermaid.ts:54-62`) — a tab is simply a wider or
narrower container than a chat column, so no new sizing logic is needed, only verification:
- At 390px, the tab (like the inline block) must never force the message/tab shell to scroll
  horizontally — this is the exact regression named in the UX rules' "a graph needing horizontal
  scroll" failure. Mermaid's SVG already scales down via CSS (`mermaid.ts:58`, "CSS still scales it
  down to fit"); the tab shell must apply the same `max-width: 100%` the inline block's container
  already gets.
- At 3840px, a tab gets much more room than a chat column ever would; verify the diagram grows to
  fill it without the `themeCSS` pass's stroke/radius corrections (§3.5) looking sparse, and that
  Mermaid's own font metrics (now themed by the same pass, §3.5) still produce legible,
  non-overlapping labels at that scale. Also verify the PNG export at this width hits §3.7's
  stated clamp-and-say-so behavior rather than silently producing a lower-resolution PNG than the
  on-screen diagram's own detail would suggest.

## 4. Surfaces and UX

Per `runner/skills/ux-design`: one look per action, icons with tooltips, say a thing once.

- **`Tab`** matches the row it's joining (§3.2): `VisualBlock`'s action row is plain text buttons
  (`Source` / `Copy` / `Open`), so the new control is a fourth text button, not an icon borrowed
  from a different row's convention — mixing the two in one row is exactly the "icons
  consistently, rather than mixing words and icons" failure the UX rules name. Named `Tab`, not
  `Open`, so it can't be confused with the existing zoom-overlay button beside it.
- **The tab's title** is "Diagram" / "Chart" for a snapshot (matching the inline block's own
  header text, `VisualBlock.tsx:136`) or the file's name for a published one (existing
  `ArtifactViewer` title logic, unchanged) — never both a generic word and the kind repeated (the
  UX rules' flagged failure: "a heading repeating its tab").
- **The three download actions** sit in the existing `ViewerToolbar`'s single utility bar, as
  icons with tooltips ("Download SVG", "Download PNG", "Download source") — consistent with every
  other `ViewerAction` already there (Download, Copy, Maximize, Close). No labeled buttons mixed
  with icons (the UX rules' "icons consistently, rather than mixing words and icons").
- **A draw failure in a tab** uses the same sentence pattern the inline block already uses
  ("Couldn't draw this chart/diagram: …") plus the source — never a bare "Error" or an internal
  exception string, and never a second, differently-worded failure message invented for the tab.
- **Nothing here is a new top-level surface.** A `visual` tab reuses the session's existing tab
  strip, the existing `ViewerHost`/`HostedViewers` plumbing, and the existing viewer toolbar shell
  — it earns its place exactly because it's the same object (a chart) getting the same treatment
  (a tab) every other artifact already gets, per the UX rules' "give a new top-level element a
  reason to exist."
- **State, not internals.** A snapshot tab that can no longer draw (source changed underneath it —
  it can't, since it's a snapshot, but the failure-matrix equivalent for a *published* file going
  stale) says "Couldn't update preview. The previous version is still shown." — the exact wording
  `FollowingArtifact` already uses (`ArtifactViewer.tsx:184`) — never "stale" or "sync failed" or
  any other internal-state word.

## 5. Open questions

1. **Icon set for the three downloads.** Three separate toolbar icons (SVG/PNG/source) match
   "one look per action" but add three new icon glyphs to a toolbar that currently has one generic
   Download icon everywhere else. **Recommendation:** one Download icon (reusing the existing
   glyph) that opens a small, anchored menu with the three choices — closer to "quietest version
   that works" and avoids three new SVG paths for an action most people will use rarely, while
   still reading as one object's one utility-bar action rather than three competing ones.
2. **Does a snapshot tab ever need to re-sync to a *published* version of the same chart?** E.g.
   someone pops out an inline chart, then separately publishes the same source as a `.vl.json` —
   should the snapshot tab "upgrade" into a following one? **Recommendation: no.** A snapshot is
   explicitly frozen per the card's own wording; conflating the two would require guessing at
   identity across two different objects (a message and a file) that the system otherwise keeps
   deliberately separate. If someone wants a following tab, they publish and open that file.
3. **Mermaid theming-pass scope creep.** §3.5's stroke/radius/font correction is deliberately narrow
   (declarative attribute snapping, not a layout engine). A reviewer could reasonably ask for more
   (e.g. recoloring specific diagram-type defaults Mermaid doesn't theme at all, like some ER
   diagram markers). **Recommendation:** ship the narrow pass first and file anything it doesn't
   cover as a follow-up once it's visible against real diagrams in both themes — guessing at every
   Mermaid grammar's untheme-able corner up front risks exactly the kind of speculative scope this
   codebase's conventions (no fallback for scenarios that can't happen; narrow passes) argue
   against.
4. **PNG background when the person's own OS/clipboard composites transparency usefully** (e.g.
   pasting into a slide with its own dark background). Always painting `theme.surface` behind the
   PNG (per the card's explicit "theme background" instruction) means a PNG never looks right
   pasted onto a different background. **Recommendation:** follow the card's instruction exactly —
   "theme background" is explicit in the acceptance criteria — and leave a transparent-background
   option as a future export if anyone asks; don't speculate on it now.
5. **Whether BLG-449's snapshot store API (put/get/cap signal) is settled by the time this card is
   implemented.** §3.1.1 reuses it rather than designing a second store, which is only safe if its
   shape (keying, cap signal) is stable by then. **Recommendation:** if BLG-449 lands first, as
   it's listed as a dependency, there's nothing to decide — call its API as-is. If this card's
   implementation starts before BLG-449's store is settled, coordinate the store's shape with that
   work rather than building a chart-specific store "for now" that would need to be torn out once
   BLG-449 lands.
