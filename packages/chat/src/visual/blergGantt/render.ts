// BLG-471 §5/§7: ties parse → layout → draw → attach together, mirroring blergFlow/render.ts and
// blergSequence/render.ts's own shape, widened for the ticket's own gantt-only chrome: a
// horizontally scrolling SVG with its own lane-name column pinned over that scroll (draw.ts's
// `data-blerg-gantt-lane-column` group, translated here on scroll rather than CSS
// `position: sticky`, draw.ts's own header), and a time-axis zoom (buttons, ctrl+wheel, pinch,
// fit-to-width) that reruns layout at a new `pxPerDay` — never a CSS `transform: scale()` on the
// old layout, same discipline as blergFlow/render.ts's own fill-scale loop. The exported
// `RenderedVisual` stays the diagram's natural, un-scrolled, un-zoomed extent (design doc §7):
// captured from the very first draw, before any zoom interaction can change the live component's
// own `pxPerDay`.
//
// Review (first round): the zoom toolbar used to be plain unstyled buttons floating over the
// chart, inside this module's own wrapper. It now builds `RenderedVisual.toolbar` instead — the
// app's existing chrome (VisualBlock.tsx's `.visual-actions` row in chat, the tab's own
// `ViewerToolbar` when hosted) mounts it, never this module. The markup below matches
// ViewerToolbar.tsx's `ViewerAction` byte-for-byte (`refresh-control viewer-action` class, the
// same inline SVG shape) so it looks identical to every other icon action in the app — duplicated
// rather than imported because this file (like every renderer under visual/) is framework-free,
// called from a plain DOM effect, not React.
import { parseGantt } from './parse'
import { layoutGantt, clampPxPerDay, DEFAULT_PX_PER_DAY } from './layout'
import { drawGantt } from './draw'
import { wireGanttInteractivity } from './accessibility'
import { sanitizeSvg } from '../policy'
import type { VisualTheme } from '../theme'
import type { RenderedVisual } from '../render'
import type { VisualFit } from '../chartSize'
import type { GanttGraph } from './ast'

const ZOOM_IN_FACTOR = 1.25
const ZOOM_OUT_FACTOR = 0.8
const WHEEL_ZOOM_SENSITIVITY = 0.0015
// Same cap blergFlow/render.ts and blergSequence/render.ts already use for a tab/full screen's
// own text-size growth: never more than 2x natural text, so a two-task plan opened in a huge tab
// doesn't blow its lane names up past readability.
const MAX_FILL_SCALE = 2

// ViewerToolbar.tsx's own `paths` map, the three entries this toolbar needs — kept in sync by
// hand (this file's own header explains why it can't just import the React component).
const ICON_PATHS = {
  zoomIn: 'M10 3a7 7 0 1 0 0 14 7 7 0 0 0 0-14m5 12 6 6M7 10h6m-3-3v6',
  zoomOut: 'M10 3a7 7 0 1 0 0 14 7 7 0 0 0 0-14m5 12 6 6M7 10h6',
  // "Fit to width" reuses the `maximize` icon, same as the app's own ImageView.tsx does for its
  // "Fit" control — there is no separate "fit" glyph in the shared icon set.
  maximize: 'M8 3H3v5m13-5h5v5M3 16v5h5m13-5v5h-5',
}

function viewerActionButton(icon: keyof typeof ICON_PATHS, label: string, title: string, onClick: () => void): HTMLButtonElement {
  const button = document.createElement('button')
  button.type = 'button'
  button.className = 'refresh-control viewer-action'
  button.setAttribute('aria-label', label)
  button.title = title
  button.innerHTML = `<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="${ICON_PATHS[icon]}"/></svg>`
  button.addEventListener('click', onClick)
  return button
}

/** Solves for the `pxPerDay` that makes the chart's own drawn width equal `availableWidth`,
 *  without duplicating layout.ts's own geometry: width is linear in `pxPerDay` for a fixed graph
 *  and font size (every other term is `pxPerDay`-independent), so two probe layouts pin down the
 *  line exactly — no iteration needed, unlike blergFlow/render.ts's font-scale search (that loop
 *  exists because a label's own measured width isn't linear in font size; a day's own pixel
 *  width here is exactly linear in `pxPerDay` by construction). */
function fitPxPerDay(graph: GanttGraph, fontSize: number, now: Date, availableWidth: number): number {
  const w1 = layoutGantt(graph, 1, fontSize, now).width
  const w2 = layoutGantt(graph, 2, fontSize, now).width
  const totalDays = w2 - w1
  if (totalDays <= 0) return DEFAULT_PX_PER_DAY
  const base = 2 * w1 - w2
  return clampPxPerDay((availableWidth - base) / totalDays)
}

function touchDistance(touches: TouchList): number {
  const [a, b] = [touches[0], touches[1]]
  return Math.hypot(a.clientX - b.clientX, a.clientY - b.clientY)
}

/** `null`: not our declared subset — the caller falls back to Mermaid, same contract as
 *  `tryRenderFlowchart`/`tryRenderSequence`. `now` is a seam (test-driven-development skill: fake
 *  time around an OS service) for the today line's own deterministic placement. */
export function tryRenderGantt(source: string, container: HTMLElement, theme: VisualTheme, fit: VisualFit, onChipOpen?: (key: string) => void, now: Date = new Date()): RenderedVisual | null {
  const parsed = parseGantt(source)
  if (!parsed) return null
  const graph: GanttGraph = parsed

  const natural = layoutGantt(graph, DEFAULT_PX_PER_DAY, theme.fontSize, now)

  // Review (first round): a tab/full screen left bars/text at natural (14px) size regardless of
  // how much room it had — the same bug blergFlow/render.ts's own header already explains (a
  // diagram that only shrinks, never grows, past chat's own natural size). Decoupled here from
  // blergFlow's single combined zoom: `fontSize` governs row height/stroke/text (bounded by the
  // *available height* alone — unlike a node-link diagram, a release plan's own vertical extent
  // is set by its task count, independent of how wide its time range is), while `pxPerDay` fills
  // the *width* separately, fitted at whatever `fontSize` this loop settles on (fitPxPerDay.ts
  // layout geometry — lane column width, row height — depends on it). A bounded refinement loop,
  // not a single-shot estimate, for the same reason blergFlow's own loop isn't: `layoutGantt`'s
  // `Math.round(const * scale)` roundings and `measureLabel`'s own padding make the real relaid-
  // out height not perfectly linear in `fontSize`.
  let themeToDraw = theme
  let initialPxPerDay = DEFAULT_PX_PER_DAY
  if (fit.fill) {
    const availableWidth = Math.max(1, fit.available.width || natural.width)
    const availableHeight = fit.available.height > 0 ? fit.available.height : Infinity
    let scale = Math.min(availableHeight / natural.height, MAX_FILL_SCALE)
    for (let i = 0; i < 4 && Number.isFinite(scale) && scale > 0 && Math.abs(scale - 1) > 0.02; i++) {
      themeToDraw = { ...theme, fontSize: theme.fontSize * scale }
      const scaledHeight = layoutGantt(graph, DEFAULT_PX_PER_DAY, themeToDraw.fontSize, now).height
      const next = availableHeight / scaledHeight
      if (Math.abs(next - 1) < 0.02) break
      scale = Math.min(scale * next, MAX_FILL_SCALE)
    }
    initialPxPerDay = fitPxPerDay(graph, themeToDraw.fontSize, now, availableWidth)
  }

  const naturalAtScale = layoutGantt(graph, initialPxPerDay, themeToDraw.fontSize, now)
  const drawnNatural = drawGantt(graph, naturalAtScale, themeToDraw)
  const exportedSvg = sanitizeSvg(drawnNatural.svg)

  const wrapper = document.createElement('div')
  wrapper.className = 'visual-blerg-gantt'
  if (fit.fill) {
    // Vertically centered when shorter than the tab's own available height (the ticket's own
    // "centered" — width already fills it via `initialPxPerDay`, above, so no horizontal
    // centering is wanted here, only "has the room, isn't pinned to the top"). `container` is
    // `.visual-surface`'s own freshly created host div (VisualBlock.tsx's `Surface`), stretched
    // to its full height so this flex centering has a real height to center within.
    container.style.height = '100%'
    wrapper.style.cssText = 'position:relative;height:100%;display:flex;flex-direction:column;align-items:center;justify-content:center'
  } else {
    wrapper.style.cssText = 'position:relative'
  }

  // Not appended into `wrapper` — handed back as `RenderedVisual.toolbar` instead (this file's
  // own header), so the app's existing chrome mounts it, never floating over the drawing.
  const toolbar = document.createElement('span')
  toolbar.className = 'visual-blerg-gantt-toolbar'
  // Same convention as `.viewer-control-slot` (ViewerToolbar.tsx's own CSS): no role/aria-label
  // or layout box of its own (each button below carries its own aria-label already) — its three
  // buttons become direct flex items of whichever row mounts it (`.visual-actions` in chat,
  // `.viewer-toolbar` in a tab) and pick up that row's own gap.
  toolbar.style.display = 'contents'

  const scrollHost = document.createElement('div')
  scrollHost.className = 'visual-blerg-gantt-scroll'
  scrollHost.style.cssText = 'overflow-x:auto;position:relative;max-width:100%'

  let pxPerDay = initialPxPerDay
  let root: SVGSVGElement | null = null
  let live: HTMLElement | null = null

  function attachScrollPin(svgRoot: SVGSVGElement): void {
    const laneColumn = svgRoot.querySelector('[data-blerg-gantt-lane-column]')
    scrollHost.onscroll = () => laneColumn?.setAttribute('transform', `translate(${scrollHost.scrollLeft},0)`)
  }

  function redraw(nextPxPerDay: number): void {
    pxPerDay = clampPxPerDay(nextPxPerDay)
    const layout = layoutGantt(graph, pxPerDay, themeToDraw.fontSize, now)
    const drawn = drawGantt(graph, layout, themeToDraw)
    const svg = sanitizeSvg(drawn.svg)
    const nextRoot = new DOMParser().parseFromString(svg, 'image/svg+xml').documentElement as unknown as SVGSVGElement
    nextRoot.setAttribute('data-diagram-renderer', 'blerg-gantt')
    nextRoot.setAttribute('role', 'group')
    nextRoot.setAttribute('aria-label', 'Gantt chart')
    if (fit.fill) {
      // A safety net for the fontScale convergence loop's own ~2% residual overshoot (above),
      // uniform so the drawing only ever shrinks, never stretches out of proportion — the same
      // clamp blergFlow/render.ts's own fill branch applies after its matching convergence loop.
      // Never grows past `drawn`'s own size: a live zoom-in intentionally exceeds the available
      // width, which is exactly when the chart is meant to scroll (scrollHost's own job), not be
      // clamped back down.
      const availableHeight = fit.available.height > 0 ? fit.available.height : Infinity
      const clamp = Math.min(1, availableHeight / drawn.height)
      if (clamp < 1) {
        nextRoot.setAttribute('width', String(Math.max(1, Math.round(drawn.width * clamp))))
        nextRoot.setAttribute('height', String(Math.max(1, Math.round(drawn.height * clamp))))
      }
    }
    if (root) root.replaceWith(nextRoot)
    else scrollHost.append(nextRoot)
    root = nextRoot
    attachScrollPin(root)
    const nextLive = wireGanttInteractivity(root, onChipOpen)
    if (live) live.replaceWith(nextLive)
    else wrapper.append(nextLive)
    live = nextLive
  }

  redraw(pxPerDay)

  toolbar.append(
    viewerActionButton('zoomOut', 'Zoom out', 'Zoom out', () => redraw(pxPerDay * ZOOM_OUT_FACTOR)),
    viewerActionButton('zoomIn', 'Zoom in', 'Zoom in', () => redraw(pxPerDay * ZOOM_IN_FACTOR)),
    viewerActionButton('maximize', 'Fit to width', 'Fit to width', () => redraw(fitPxPerDay(graph, themeToDraw.fontSize, now, Math.max(1, scrollHost.clientWidth || fit.available.width || natural.width)))),
  )
  scrollHost.addEventListener('wheel', event => {
    if (!event.ctrlKey) return
    event.preventDefault()
    redraw(pxPerDay * Math.exp(-event.deltaY * WHEEL_ZOOM_SENSITIVITY))
  }, { passive: false })

  let pinchStartDistance = 0
  let pinchStartPxPerDay = pxPerDay
  scrollHost.addEventListener('touchstart', event => {
    if (event.touches.length !== 2) return
    pinchStartDistance = touchDistance(event.touches)
    pinchStartPxPerDay = pxPerDay
  }, { passive: true })
  scrollHost.addEventListener('touchmove', event => {
    if (event.touches.length !== 2 || pinchStartDistance <= 0) return
    event.preventDefault()
    redraw(pinchStartPxPerDay * (touchDistance(event.touches) / pinchStartDistance))
  }, { passive: false })

  wrapper.append(scrollHost)
  container.replaceChildren(wrapper)
  return { svg: exportedSvg, width: drawnNatural.width, height: drawnNatural.height, dispose: () => container.replaceChildren(), toolbar }
}
