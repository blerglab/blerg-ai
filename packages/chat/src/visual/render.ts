import {polishVisual} from './polish'
import {dashboardChart} from './dashboardChart'
import {BoundedCache} from './boundedCache'
import { readVisualTheme, type VisualTheme } from './theme'
import { parseVegaLite, validateMermaid, type VisualKind } from './policy'
import { cacheChartClassification, classifyChart } from './blerg/classify'
import {MAX_CHART_SIDE} from './vegaBudget'
import { chartSize, type VisualFit } from './chartSize'
export type { VisualFit, AvailableSize } from './chartSize'

/** What a renderer hands back: the exact SVG it drew and sanitized once (the same bytes shown on
 *  screen, already bounded by that renderer's own size/area limits), its logical size, and its
 *  teardown. Every renderer (Mermaid, Vega-Lite, and any added later — this chat's own blerg
 *  renderer included) returns this same shape, so export (vegaExport.ts) never branches on which
 *  one produced it. */
export interface RenderedVisual {
  svg: string
  width: number
  height: number
  dispose(): void
  /** Mounted native geometry, committed synchronously before paint. */
  resize?(box: {width: number; height: number}, generation: number): void
  /** Validated model with identical non-data spec; false requests a staged replacement. */
  applyData?(model: Record<string, unknown>, generation: number): boolean
  setTheme?(theme: VisualTheme): void
  snapshot?(): {svg: string; width: number; height: number}
  /** BLG-471: a renderer's own interactive controls (gantt's zoom/fit-to-width) that belong in
   *  the app's existing chrome — VisualBlock's `.visual-actions` row in chat, or the tab's own
   *  `ViewerToolbar` when hosted — never floating over the drawing itself. Absent for every
   *  renderer that needs no controls beyond the shared ones (every kind but gantt, today).
   *  VisualBlock.tsx owns mounting it; this plain DOM node (not JSX — render.ts and its
   *  sub-renderers are framework-free) is built once per render and removed on the next one. */
  toolbar?: HTMLElement
}

// Shared, font-neutral models survive static tab transfer. Only validated source enters the
// bounded cache; each mounted surface owns its own scene and disposal, never another view's.
const chartModels = new BoundedCache<Record<string, unknown>>(8 << 20)
function freezeModel(value: unknown): void {
  if (!value || typeof value !== 'object' || Object.isFrozen(value)) return
  Object.freeze(value)
  for (const child of Object.values(value)) freezeModel(child)
}
function modelBytes(value: unknown): number {
  if (typeof value === 'string') return value.length * 2
  if (!value || typeof value !== 'object') return 8
  return Array.isArray(value) ? 32 + value.reduce((bytes, child) => bytes + 8 + modelBytes(child), 0) : 64 + Object.entries(value).reduce((bytes, [key, child]) => bytes + 24 + key.length * 2 + modelBytes(child), 0)
}
export function getChartModel(source: string): Record<string, unknown> {
  const cached = chartModels.get(source)
  if (cached) return cached
  const model = parseVegaLite(source)
  freezeModel(model)
  cacheChartClassification(model)
  chartModels.set(source, model, modelBytes(model))
  return model
}

function throwIfAborted(signal: AbortSignal | undefined): void {
  if (signal?.aborted) throw new DOMException('Aborted', 'AbortError')
}

/** BLG-467: the size our own renderer draws a classified spec at. parseVegaLite.ts always sets
 *  width/height on a single or layered spec (the author's own number, or its 640x360 default) —
 *  a concat/hconcat/vconcat spec keeps neither, laying itself out from its own panels instead
 *  (vegaPolicy.ts's own comment, unaffected by `fit`: composed panels aren't resized by this). */
export function blergChartSize(spec: Record<string, unknown>, fit: VisualFit): {width: number; height: number} {
  if (fit.stretch && fit.available.width > 0 && fit.available.height > 0) return {width: Math.min(MAX_CHART_SIDE, fit.available.width), height: Math.min(MAX_CHART_SIDE, fit.available.height)}
  const specWidth = Number(spec.width)
  const specHeight = Number(spec.height)
  if (!(specWidth > 0)) return {width: Math.max(1, fit.available.width || 640), height: Math.max(1, specHeight > 0 ? specHeight : 360)}
  return chartSize(specWidth, specHeight > 0 ? specHeight : undefined, fit.available, fit.fill)
}

function defaultFit(container: HTMLElement): VisualFit {
  return {available: {width: container.clientWidth, height: 0}, fill: false}
}

export async function renderVisual(kind: VisualKind, source: string, container: HTMLElement, theme: VisualTheme = readVisualTheme(container), signal?: AbortSignal, fit: VisualFit = defaultFit(container), onChipOpen?: (key: string) => void): Promise<RenderedVisual> {
  if (kind === 'mermaid') {
    const drawn = validateMermaid(source)
    // BLG-468/BLG-471: a flowchart, sequence or gantt diagram in its own declared subset
    // (classify.ts's own pattern — parseFlowchart/parseSequence/parseGantt ARE the classifiers,
    // design doc §2/§5/§7) draws with our own layout and theme tokens instead of Mermaid's;
    // anything outside all three subsets returns null from each attempt and falls through
    // unchanged below. Tried in shipped order only — the three grammars don't overlap (each
    // source starts with its own unique header line, so a mismatched parser already returns null
    // on the first line).
    throwIfAborted(signal)
    try {
      const {tryRenderFlowchart} = await import('./blergFlow/render')
      throwIfAborted(signal)
      const rendered = tryRenderFlowchart(drawn, container, theme, fit, onChipOpen)
      if (rendered) return polishVisual(rendered, source, container, 'diagram')
      const {tryRenderSequence} = await import('./blergSequence/render')
      throwIfAborted(signal)
      const renderedSeq = tryRenderSequence(drawn, container, theme, fit, onChipOpen)
      if (renderedSeq) return polishVisual(renderedSeq, source, container, 'diagram')
      const {tryRenderGantt} = await import('./blergGantt/render')
      throwIfAborted(signal)
      const renderedGantt = tryRenderGantt(drawn, container, theme, fit, onChipOpen)
      if (renderedGantt) return polishVisual(renderedGantt, source, container, 'diagram')
    } catch (error) {
      if (error instanceof DOMException && error.name === 'AbortError') throw error
      // M8: never let a partial attempt's DOM/listeners survive into the fallback draw.
      console.warn('Native diagram drawing failed; using Mermaid', {cause: error instanceof Error ? error.name : 'UnknownError'})
      container.replaceChildren()
    }
    // Review finding: RenderedVisual.svg was always '' for Mermaid (mermaid.ts's own header),
    // contradicting this file's own contract comment above — fixed there; passed through as-is.
    return (await import('./mermaid')).renderMermaid(source, container, theme, fit)
  }
  if (kind === 'dashboard') throw new Error('Dashboards require the card grid renderer')
  const spec = getChartModel(source)
  if (classifyChart(spec) === 'blerg') {
    // Review finding: the Vega path threads `signal` through to its own worker/cancellation
    // logic, but this path never checked it at all — an abort mid-draw (e.g. a fast resize, each
    // one aborting the previous controller) ran renderBlergChart to completion anyway instead of
    // throwing the AbortError callers specifically check for. Checked once before starting (the
    // common case: already aborted by the time this async function got here) and once more after
    // the dynamic import's own await, the only other point this path yields.
    throwIfAborted(signal)
    try {
      const {renderBlergChart} = await import('./blerg/render')
      throwIfAborted(signal)
      const {width, height} = blergChartSize(spec, fit)
      const primary = Array.isArray(spec.layer) ? spec.layer[0] as Record<string, unknown> : spec
      const mark = typeof primary.mark === 'string' ? primary.mark : (primary.mark as {type?: string})?.type ?? 'point'
      return polishVisual(renderBlergChart(spec, theme, container, width, height, !!fit.stretch), source, container, mark, spec)
    } catch (error) {
      if (error instanceof DOMException && error.name === 'AbortError') throw error
      // M8: never let a partial attempt's DOM/listeners survive into the fallback draw.
      console.warn('Native chart drawing failed; using Vega', {cause: error instanceof Error ? error.name : 'UnknownError'})
      container.replaceChildren()
    }
  }
  return (await import('./vega')).renderVegaLite(fit.dashboardGuides ? getChartModel(dashboardChart(spec, (spec.data as {values?:unknown[]})?.values ?? [], true)) : spec, container, theme, signal, fit)
}
