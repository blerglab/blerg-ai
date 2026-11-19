// BLG-468 §5: ties parse → layout → draw → attach together, and is the one module render.ts's
// shared dispatcher lazily imports on the 'mermaid' kind — never loaded for a diagram that falls
// back (parseFlowchart itself decides that, before anything here runs). `tryRenderFlowchart`
// returns `null` on that fallback verdict; the caller (render.ts) is responsible for then calling
// the existing `renderMermaid`, exactly as today.
import { parseFlowchart } from './parse'
import { layoutFlowchart, type FlowLayout } from './layout'
import { drawFlowchart } from './draw'
import { wireFlowchartInteractivity } from './accessibility'
import { sanitizeSvg } from '../policy'
import type { VisualTheme } from '../theme'
import type { RenderedVisual } from '../render'
import type { VisualFit } from '../chartSize'
import type { FlowGraph } from './ast'

// Review (post-slice-2, applied back to flowchart): in chat, a diagram never draws larger than
// its own natural size — text stays at most 1x. It lays out within the message's own width and
// *shrinks* when that width is narrower than natural, same as a chart with no author-given width
// (chartSize.ts), but it never grows past natural the way an earlier round of this file did
// (round 2's own "chat's own fill is unconditional" decision, now reverted: a small diagram — a
// two-node chain, a two-participant sequence — in a very wide chat message blew its text up many
// times over and ran off the bottom of the message, measured on the sequence renderer's own
// chat-3840 screenshot). Growing past natural size (capped at 2x text, bounded by the available
// height) is a tab/full-screen-only affordance now — `fit.fill` below — never chat's.

const MAX_FILL_SCALE = 2

/** Lays out in the authored direction first; switches a left-right/right-left flow to top-to-
 *  bottom only when the authored layout's own natural width doesn't fit the available width —
 *  never a flat width threshold. BT/TB diagrams are never overridden (already compact
 *  horizontally). `availableWidth <= 0` means "unmeasured" — keep the authored direction. Used
 *  for both chat and fill: a diagram that doesn't fit the pane's own proportions at its natural
 *  size usually still doesn't *look* right stacked the wrong way even after scaling up to it. */
function layoutForWidth(graph: FlowGraph, fontSize: number, availableWidth: number): { graph: FlowGraph; layout: FlowLayout } {
  const natural = layoutFlowchart(graph, fontSize)
  const wraps = graph.direction === 'LR' || graph.direction === 'RL'
  if (!wraps || availableWidth <= 0 || natural.width <= availableWidth) return { graph, layout: natural }
  const stacked: FlowGraph = { ...graph, direction: 'TB' }
  return { graph: stacked, layout: layoutFlowchart(stacked, fontSize) }
}

/** Chat (not filling): the explicit width/height the `<svg>` draws at (its viewBox stays the
 *  natural, dagre-computed coordinates `drawFlowchart` produced) — never larger than that natural
 *  size (this file's own header), only shrunk to fit a message narrower than it. Not a CSS
 *  percentage, which a shrink-wrapped ancestor could resolve against its own (circular) collapsed
 *  size instead of the real available width (round 2 review finding) — set directly as attributes
 *  below. */
function chatSize(natural: { width: number; height: number }, availableWidth: number): { width: number; height: number } {
  const width = Math.max(1, Math.min(natural.width, availableWidth || natural.width))
  return { width, height: Math.max(1, Math.round(natural.height * width / natural.width)) }
}

/** `null`: not our declared subset — the caller falls back to Mermaid. `onChipOpen` is called
 *  with the exact `RefKey` shape refs.ts uses (chips.ts) when a card-reference chip is activated;
 *  absent, chips still draw (role="button", a real focus target) but do nothing on activation —
 *  the same "lending contract absent → no-op" shape refs.ts itself already has. */
export function tryRenderFlowchart(source: string, container: HTMLElement, theme: VisualTheme, fit: VisualFit, onChipOpen?: (key: string) => void): RenderedVisual | null {
  const parsed = parseFlowchart(source)
  if (!parsed) return null
  const { graph, layout: natural } = layoutForWidth(parsed, theme.fontSize, fit.available.width)

  const graphToDraw = graph
  let layoutToDraw = natural
  let themeToDraw = theme
  if (fit.fill) {
    // Review round 3 (§1): fills by growing the *geometry* (a real second dagre run at a bigger
    // font, same as a bigger message gets a bigger node in a chart), not by leaving the drawing
    // at its natural size and only changing the CSS box around it. Bounded by both available
    // dimensions (chartSize.ts's own fill-branch discipline) AND this file's own header: capped at
    // MAX_FILL_SCALE (2x text) — reinstated now that chat itself never grows past 1x (above), so a
    // tab is no longer required to out-grow an uncapped chat just to stay "never smaller than it."
    // `scale` can be below 1 too (a small pane), matching chartSize.ts's own "scaled up, not just
    // down" wording for the *fill* case specifically, not a floor at 1.
    //
    // A single multiply-by-scale estimate undershoots at large factors: sizing.ts's own per-node
    // padding (PAD_X/PAD_Y) is a few pixels regardless of font size, so a node's own width grows
    // *sub-linearly* with a big font scale (the padding is a shrinking fraction of it) — a label
    // measured at 2x font is not simply 2x as wide. Refined by a few bounded iterations instead of
    // assumed correct on the first pass: each one re-measures the actual relaid-out size against
    // the target and nudges the font scale again, converging quickly since the ratio changes less
    // each round. The hard clamp below (after drawing) is still the real guarantee, not this loop.
    const availableWidth = Math.max(1, fit.available.width || natural.width)
    const availableHeight = fit.available.height > 0 ? fit.available.height : Infinity
    let scale = Math.min(availableWidth / natural.width, availableHeight / natural.height, MAX_FILL_SCALE)
    for (let i = 0; i < 4 && Number.isFinite(scale) && scale > 0 && Math.abs(scale - 1) > 0.02; i++) {
      themeToDraw = { ...theme, fontSize: theme.fontSize * scale }
      layoutToDraw = layoutFlowchart(graph, themeToDraw.fontSize)
      const next = Math.min(availableWidth / layoutToDraw.width, availableHeight / layoutToDraw.height)
      if (Math.abs(next - 1) < 0.02) break
      scale = Math.min(scale * next, MAX_FILL_SCALE)
    }
  }

  const drawn = drawFlowchart(graphToDraw, layoutToDraw, themeToDraw)
  const svg = sanitizeSvg(drawn.svg)
  const root = new DOMParser().parseFromString(svg, 'image/svg+xml').documentElement as unknown as SVGSVGElement
  root.setAttribute('data-diagram-renderer', 'blerg-flow')
  root.setAttribute('role', 'group')
  root.setAttribute('aria-label', 'Flowchart diagram')
  if (fit.fill) {
    // Mostly already drawn at the fill-scaled size (the relayout above): a final clamp, never a
    // further grow, for the gap sizing.ts's own MIN_WIDTH/MIN_HEIGHT floors can leave between the
    // single-pass font-scale estimate and the actual relaid-out size (a node can refuse to shrink
    // past its floor, so a short chain of them can still end up a little taller than the estimate
    // alone predicted) — a real hard bound, not just "usually close enough". viewBox stays the
    // relaid-out natural coordinates; only this display box shrinks to fit, same "one drawing, a
    // scaled box around it" mechanism the non-fill branch below also uses.
    const availableWidth = Math.max(1, fit.available.width || drawn.width)
    const availableHeight = fit.available.height > 0 ? fit.available.height : Infinity
    const clamp = Math.min(1, availableWidth / drawn.width, availableHeight / drawn.height)
    root.setAttribute('width', String(Math.max(1, Math.round(drawn.width * clamp))))
    root.setAttribute('height', String(Math.max(1, Math.round(drawn.height * clamp))))
    // Centered in whatever room is left after the height bound (or the clamp above, in the floor-
    // rounding case) stopped it short of the pane's own width (review round 3's own "center it in
    // the space").
    // Absolute positioning, not margin:auto: an SVG's default `display: inline` can't be centered
    // that way, and this must not change `.visual-surface`'s own intrinsic size the way flex/grid
    // centering on a shared, multi-purpose container would risk for every other kind drawn there.
    root.style.position = 'absolute'
    root.style.top = '50%'
    root.style.left = '50%'
    root.style.transform = 'translate(-50%, -50%)'
  } else {
    const size = chatSize({ width: drawn.width, height: drawn.height }, fit.available.width)
    root.setAttribute('width', String(size.width))
    root.setAttribute('height', String(size.height))
  }
  const live = wireFlowchartInteractivity(root, onChipOpen)
  container.replaceChildren(root, live)
  return { svg, width: drawn.width, height: drawn.height, dispose: () => container.replaceChildren() }
}
