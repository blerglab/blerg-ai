import ChartWorker from './vegaWorker?worker&inline'
import { readVisualTheme, type VisualTheme } from './theme'
import { ChartRefused, isCompoundChart, parseVegaLite, sanitizeSvg } from './policy'
import { CHART_DEADLINE_MS, HEAVY_CHART, MAX_CHART_AREA, MAX_CHART_SIDE, CHART_LIMITS, type ChartDrawing, type ChartSize } from './vegaBudget'
import type { RenderedVisual, VisualFit } from './render'
import { chartSize } from './chartSize'
import { fillRefText } from './refText'
export { MAX_CHART_SIDE, MAX_CHART_AREA, tooltipText } from './vegaBudget'
export { denyVisualResources } from './vegaResources'

// Serialize jobs to bound aggregate worker memory when a transcript has many charts.
let queued: Promise<unknown> = Promise.resolve()
export type {ChartSize} from './vegaBudget'
/** A fit/fill size Blerg chose, clamped to the drawing budget's sides (never the author's 1024). */
export function boundedSize(size: ChartSize): ChartSize {
  const side = (v: number) => Math.max(1, Math.min(MAX_CHART_SIDE, Math.round(Number.isFinite(v) ? v : 1)))
  return {width: side(size.width), height: side(size.height)}
}

export function compileInWorker(spec: Record<string, unknown>, theme: VisualTheme, signal?: AbortSignal, size?: ChartSize): Promise<ChartDrawing> {
  const job = queued.catch(() => {}).then(() => new Promise<ChartDrawing>((resolve, reject) => {
    if (signal?.aborted) { reject(new DOMException('Aborted', 'AbortError')); return }
    const worker = new ChartWorker()
    let finished = false
    const finish = (error?: Error, drawing?: ChartDrawing) => {
      if (finished) return
      finished = true
      clearTimeout(timer)
      signal?.removeEventListener('abort', abort)
      worker.terminate()
      if (error) reject(error)
      else resolve(drawing!)
    }
    const abort = () => finish(new DOMException('Aborted', 'AbortError'))
    const timer = setTimeout(() => finish(new ChartRefused(HEAVY_CHART)), CHART_DEADLINE_MS)
    signal?.addEventListener('abort', abort, {once: true})
    worker.onmessage = (event: MessageEvent<{drawing?: ChartDrawing; error?: string}>) => {
      if (event.data.error || !event.data.drawing) finish(new ChartRefused(event.data.error || HEAVY_CHART))
      else finish(undefined, event.data.drawing)
    }
    worker.onerror = () => finish(new ChartRefused(HEAVY_CHART))
    worker.onmessageerror = () => finish(new ChartRefused(HEAVY_CHART))
    try { worker.postMessage({spec, theme, size}) } catch { finish(new ChartRefused(HEAVY_CHART)) }
  }))
  queued = job.then(() => {}, () => {})
  return job
}

export async function renderVegaLite(spec: Record<string, unknown>, container: HTMLElement, theme: VisualTheme = readVisualTheme(container), signal?: AbortSignal, fit?: VisualFit): Promise<RenderedVisual> {
  // Keep the static policy as the first gate, even for direct renderer callers.
  // The author's spec passes the policy as written (its own width/height at most 1024). The size
  // Blerg picks to fit or fill the surface is ours, not the author's: it travels separately and is
  // applied after the policy, bounded by the drawing budget (MAX_CHART_SIDE), never by 1024.
  const checked = parseVegaLite(JSON.stringify(spec))
  let size: ChartSize | undefined
  if (!isCompoundChart(checked)) {
    const available = fit?.available ?? {width: container.clientWidth || container.parentElement?.clientWidth || 640, height: 0}
    const specWidth = Number(checked.width), specHeight = Number(checked.height)
    size = boundedSize(fit?.stretch && available.width > 0 && available.height > 0 ? available : chartSize(specWidth > 0 ? specWidth : undefined, specHeight > 0 ? specHeight : undefined, available, fit?.fill ?? false))
  }
  if (size && fit?.fill && fit.available.height > 0) {
    size.maxHeight = Math.max(1, Math.floor(Math.min(MAX_CHART_SIDE, fit.available.height)))
    size.height = Math.min(size.height, size.maxHeight)
  }
  const drawing = await compileInWorker(checked, theme, signal, size)
  if (signal?.aborted) throw new DOMException('Aborted', 'AbortError')
  if (!(drawing.width > 0 && drawing.height > 0 && drawing.width <= MAX_CHART_SIDE && drawing.height <= MAX_CHART_SIDE && drawing.width * drawing.height <= MAX_CHART_AREA)
    || drawing.svg.length > CHART_LIMITS.svgBytes || drawing.regions.length > CHART_LIMITS.sceneItems) throw new ChartRefused(HEAVY_CHART)
  // SVG is rendered as an inert image and then copied to a bounded canvas. Vega never runs here.
  // Kept (not re-derived) as RenderedVisual.svg: the exact bytes shown on screen, for export.
  const sanitized = sanitizeSvg(drawing.svg, {stripAriaLabels: true})
  const image = new Image()
  image.src = 'data:image/svg+xml;charset=utf-8,' + encodeURIComponent(sanitized)
  await image.decode()
  if (signal?.aborted) throw new DOMException('Aborted', 'AbortError')
  const host = document.createElement('div')
  host.className = 'visual-chart'
  const title = checked.title
  const titleText = typeof title === 'string' ? title : title && typeof title === 'object' ? (title as {text?: unknown}).text : undefined
  if (typeof titleText === 'string') host.title = titleText
  else if (Array.isArray(titleText)) host.title = titleText.join(' ')
  if (drawing.layout) {
    host.dataset.plotBounds = JSON.stringify(drawing.layout.plot)
    host.dataset.labelCount = String(drawing.layout.labels)
    host.dataset.legendCount = String(drawing.layout.legendLabels)
  }
  host.setAttribute('role', 'img')
  host.setAttribute('aria-label', 'Vega-Lite chart')
  const canvas = document.createElement('canvas')
  const scale = Math.max(1, Math.min(devicePixelRatio || 1, 2,
    MAX_CHART_SIDE / drawing.width, MAX_CHART_SIDE / drawing.height,
    Math.sqrt(MAX_CHART_AREA / (drawing.width * drawing.height))))
  canvas.width = Math.floor(drawing.width * scale)
  canvas.height = Math.floor(drawing.height * scale)
  canvas.style.width = `${drawing.width}px`
  canvas.style.height = 'auto'
  const context = canvas.getContext('2d')
  if (!context) throw new Error('Canvas is unavailable')
  context.scale(scale, scale)
  context.drawImage(image, 0, 0)
  host.append(canvas)
  const tooltip = document.createElement('div')
  tooltip.className = 'visual-tooltip'
  tooltip.setAttribute('role', 'tooltip')
  tooltip.hidden = true
  let hoverFrame = 0
  let hovered: typeof drawing.regions[number] | undefined
  const emphasize = (region: typeof hovered) => {
    if (hovered === region) return
    hovered = region
    cancelAnimationFrame(hoverFrame)
    const start = performance.now()
    const reduced = matchMedia('(prefers-reduced-motion: reduce)').matches
    const paint = () => {
      const progress = reduced ? 1 : Math.min(1, (performance.now() - start) / 120)
      context.clearRect(0, 0, drawing.width, drawing.height)
      context.drawImage(image, 0, 0)
      if (region?.bar) {
        context.fillStyle = theme.surface
        context.globalAlpha = .22 * progress
        for (const other of drawing.regions) {
          if (other.bar && other !== region) context.fillRect(other.x1, other.y1, other.x2 - other.x1, other.y2 - other.y1)
        }
        context.globalAlpha = progress
        context.strokeStyle = theme.fg
        context.lineWidth = 1
        context.strokeRect(region.x1, region.y1, region.x2 - region.x1, region.y2 - region.y1)
        context.globalAlpha = 1
      }
      if (progress < 1) hoverFrame = requestAnimationFrame(paint)
    }
    paint()
  }
  canvas.onpointermove = event => {
    const bounds = canvas.getBoundingClientRect()
    const x = (event.clientX - bounds.left) * drawing.width / bounds.width
    const y = (event.clientY - bounds.top) * drawing.height / bounds.height
    const hits = drawing.regions.filter(region => x >= region.x1 && x <= region.x2 && y >= region.y1 && y <= region.y2)
    const bars = hits.filter(region => region.bar)
    const region = (bars.length ? bars : hits).reduce<typeof hits[number] | undefined>((nearest, candidate) =>
      !nearest || Math.abs(x - (candidate.x1 + candidate.x2) / 2) < Math.abs(x - (nearest.x1 + nearest.x2) / 2) ? candidate : nearest, undefined)
    emphasize(region)
    // BLG-485: styled like chat's own chip, not interactive — same reasoning as the Blerg
    // renderer's own tooltip (blerg/tooltip.ts, refText.ts's own header): this one hides just as
    // fast on pointerleave.
    if (region) fillRefText(tooltip, region.text, 'visual-tooltip-ref', false)
    else tooltip.replaceChildren()
    tooltip.hidden = !region
    if (region) {
      const outer = container.getBoundingClientRect()
      tooltip.style.left = `${Math.max(0, Math.min(event.clientX - outer.left + 12, outer.width - 100))}px`
      tooltip.style.top = `${Math.max(0, event.clientY - outer.top + 12)}px`
    }
  }
  canvas.onpointerleave = () => { tooltip.hidden = true; emphasize(undefined) }
  container.replaceChildren(host, tooltip)
  host.setAttribute('data-chart-renderer', 'vega')
  return {
    svg: sanitized, width: drawing.width, height: drawing.height,
    dispose: () => { cancelAnimationFrame(hoverFrame); canvas.onpointermove = canvas.onpointerleave = null; host.remove(); tooltip.remove() },
  }
}
