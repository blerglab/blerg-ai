// Imported only by the disposable worker. No compiler, parser or dataflow runs on the page.
import { wrapMeasured } from './textLayout'
import * as Vega from 'vega'
import { View, Operator, Transform, parse, logger } from 'vega'
import { compile, type Config, type TopLevelSpec } from 'vega-lite'
import { expressionInterpreter } from 'vega-interpreter'
import { vegaTheme, type VisualTheme } from './theme'
import { ChartRefused, parseVegaLite } from './vegaPolicy'
import { denyVisualResources } from './vegaResources'
import { CHART_LIMITS, MAX_CHART_AREA, MAX_CHART_SIDE, heavyChart, tooltipText, type ChartDrawing, type ChartLimits, type ChartSize, type TooltipRegion } from './vegaBudget'

// A worker has no portable JS heap-limit API. Bound runtime tuple storage, operator count,
// scene items, panel cardinality, serialized output and pixels in addition to the page's timer.
// Inspect every operator's pulses, including operators created dynamically by facets.
function guardDataflow(limits: ChartLimits) {
  const originals = [Operator.prototype.run, Transform.prototype.run]
  const arrays = new WeakMap<object, number>()
  const operators = new WeakSet<object>()
  let slots = 0, count = 0, sceneSlots = 0, panelSlots = 0
  const marks = new WeakMap<object, number>()
  const scene = (value: unknown) => {
    if (!value || typeof value !== 'object') return
    const mark = value as SceneNode
    if (!mark.marktype || !Array.isArray(mark.items)) return
    const previous = marks.get(mark)
    const growth = Math.max(0, mark.items.length - (previous ?? 0))
    sceneSlots += growth + (previous === undefined ? 1 : 0)
    if (sceneSlots > limits.sceneItems) heavyChart()
    if (mark.marktype === 'group' && mark.role === 'scope' && (panelSlots += growth) > limits.panels) heavyChart()
    marks.set(mark, mark.items.length)
  }
  const rows = (value: unknown) => {
    if (!Array.isArray(value)) return
    const growth = Math.max(0, value.length - (arrays.get(value) ?? 0))
    if (value.length > limits.rows || (slots += growth) > limits.rowSlots) heavyChart()
    arrays.set(value, value.length)
  }
  const pulse = (value: unknown) => {
    if (!value || typeof value !== 'object') return
    const p = value as {source?: unknown; add?: unknown; mod?: unknown; rem?: unknown}
    rows(p.source); rows(p.add); rows(p.mod); rows(p.rem)
  }
  for (const [index, prototype] of [Operator.prototype, Transform.prototype].entries()) {
    prototype.run = function (input: unknown): unknown {
      if (!operators.has(this)) {
        operators.add(this)
        if (++count > limits.operators) heavyChart()
      }
      pulse(input)
      const result: unknown = originals[index].call(this, input)
      pulse(result)
      const value = (this as unknown as {value: unknown}).value
      rows(value)
      scene(value)
      return result
    }
  }
  return () => { Operator.prototype.run = originals[0]; Transform.prototype.run = originals[1] }
}

interface SceneNode {
  items?: SceneNode[]
  marktype?: string
  role?: string
  x?: number
  y?: number
  bounds?: {x1: number; y1: number; x2: number; y2: number; width(): number; height(): number}
  strokeDash?: number[]
  mark?: {marktype?: string}
  width?: number
  height?: number
  tooltip?: unknown
}
function inspectScene(root: SceneNode, limits: ChartLimits, offset: number[]): TooltipRegion[] {
  const regions: TooltipRegion[] = []
  let count = 0, panels = 0, textBytes = 0, dashBytes = 0
  function visit(node: SceneNode, x: number, y: number) {
    if (++count > limits.sceneItems) heavyChart()
    // Vega generates one scope group item per realized facet cell, including nested facets.
    if (node.marktype === 'group' && node.role === 'scope') {
      panels += node.items?.length ?? 0
      if (panels > limits.panels) heavyChart()
    }
    // SVGStringRenderer stringifies strokeDash separately for every item. Count
    // commas and numeric characters without joining the array or rendering SVG.
    // Shared arrays still cost bytes on every occurrence in the output.
    if (Array.isArray(node.strokeDash)) {
      for (const value of node.strokeDash) {
        dashBytes += String(value).length + 1
        if (dashBytes > limits.svgBytes) heavyChart()
      }
    }
    if (node.tooltip != null && node.bounds) {
      const text = tooltipText(node.tooltip)
      textBytes += text.length
      if (textBytes > limits.svgBytes) heavyChart()
      const b = node.bounds
      // Path item bounds cover the whole series, not this datum. Use a local point
      // target so the last line datum cannot steal every bar's tooltip.
      const pathPoint = node.mark?.marktype === 'line' || node.mark?.marktype === 'area' || node.mark?.marktype === 'trail'
      if (pathPoint && typeof node.x === 'number' && typeof node.y === 'number') {
        regions.push({x1: node.x + x - 6, y1: node.y + y - 6, x2: node.x + x + 6, y2: node.y + y + 6, text})
      } else if (node.mark?.marktype === 'rect' && typeof node.x === 'number' && typeof node.y === 'number' && typeof node.width === 'number' && typeof node.height === 'number') {
        regions.push({x1: node.x + x, y1: node.y + y, x2: node.x + x + node.width, y2: node.y + y + node.height, text, bar: true})
      } else {
        regions.push({x1: b.x1 + x, y1: b.y1 + y, x2: b.x2 + x, y2: b.y2 + y, text, bar: node.mark?.marktype === 'rect'})
      }
    }
    for (const item of node.items ?? []) {
      // Mark bounds are relative to their containing group; only group *items* translate.
      visit(item, x + (node.marktype ? 0 : node.x ?? 0), y + (node.marktype ? 0 : node.y ?? 0))
    }
  }
  visit(root, offset[0], offset[1])
  return regions
}

// BLG-452 §3.10: test-only. Exposes the live View and the compiled spec's own dataset names for
// the real-browser differential gate, instead of collapsing to drawChart's {svg, width, height,
// regions}. Reuses the exact same compile()/parse()/View construction drawChart already performs
// — so the reference is provably the same compilation the production fallback runs — but skips
// guardDataflow and the heavy/budget checks: the differential corpus is small, hand-written,
// trusted fixtures, not adversarial agent input. Never imported by production code.
export async function compileVegaViewForDifferentialTesting(spec: Record<string, unknown>, theme: VisualTheme): Promise<{view: View; datasetNames: string[]}> {
  const checked = parseVegaLite(JSON.stringify(spec))
  const compiled = compile(checked as unknown as TopLevelSpec, {config: vegaTheme(theme) as Config, logger: logger(0)}).spec
  const view = new View(parse(compiled, undefined, {ast: true}), {renderer: 'none', loader: denyVisualResources, expr: expressionInterpreter})
  await view.runAsync()
  const datasetNames = (compiled.data ?? []).map(entry => (entry as {name: string}).name).filter((name): name is string => typeof name === 'string')
  return {view, datasetNames}
}

export async function drawChart(spec: Record<string, unknown>, theme: VisualTheme, limits: ChartLimits = CHART_LIMITS, size?: ChartSize): Promise<ChartDrawing> {
  // The policy checks the author's spec as written; a fit/fill size Blerg chose is applied after it,
  // bounded by the drawing budget's own side limit rather than the author-facing 1024.
  const authored = parseVegaLite(JSON.stringify(spec))
  const fits = (v: unknown) => typeof v === 'number' && Number.isFinite(v) && v >= 1 && v <= MAX_CHART_SIDE
  const checked = size && fits(size.width) && fits(size.height) ? {...authored, width: Math.round(size.width), height: Math.round(size.height)} : authored
  // Worker canvas metrics use the same resolved typography as the drawing, rather than
  // estimating columns from character counts (wide and narrow glyphs differ substantially).
  const title = checked.title
  const titleObject = title && typeof title === 'object' && !Array.isArray(title) ? title as Record<string, unknown> : {}
  const titleText = typeof title === 'string' ? title : titleObject.text
  let titleLines: ((width: number) => string[]) | undefined
  if (typeof titleText === 'string') {
    const canvas = typeof OffscreenCanvas !== 'undefined' ? new OffscreenCanvas(1, 1) : typeof document !== 'undefined' ? document.createElement('canvas') : null
    const context = canvas?.getContext('2d') as CanvasRenderingContext2D | OffscreenCanvasRenderingContext2D | null
    if (!context) throw Object.assign(new Error('Chart text metrics are unavailable in this browser.'), {name: 'TextMetricsUnavailable'})
    context.font = `bold ${theme.fontSize + 1}px ${theme.fontFamily}`
    const limit = Math.max(1, Number(checked.width || 640) - 64)
    titleLines = width => wrapMeasured(titleText, width, text => context.measureText(text).width)
    checked.title = {...titleObject, text: titleLines(limit), anchor: 'start', limit}
  }
  // Vega's worker has no document-backed canvas. Supply the real OffscreenCanvas metrics
  // to its own layout and truncation code too, so wrapped headings and axes use one font.
  const metrics = (Vega as unknown as {textMetrics: {width: (item: {font?: string; fontSize?: number; fontWeight?: string; fontStyle?: string; text?: string}, text?: string) => number}}).textMetrics
  const previousMetrics = metrics.width
  const metricCanvas = typeof OffscreenCanvas !== 'undefined' ? new OffscreenCanvas(1, 1) : typeof document !== 'undefined' ? document.createElement('canvas') : null
  const metricContext = metricCanvas?.getContext('2d')
  if (metricContext) metrics.width = (item, text) => {
    const size = item.fontSize ?? 11
    metricContext.font = `${item.fontStyle ?? 'normal'} ${item.fontWeight ?? 'normal'} ${size}px ${item.font ?? theme.fontFamily}`
    return size <= 0 ? 0 : metricContext.measureText(String(text ?? item.text ?? '').trim()).width
  }
  const restore = guardDataflow(limits)
  let view: View | undefined
  try {
    const compiled = compile(checked as unknown as TopLevelSpec, {config: vegaTheme(theme) as Config, logger: logger(0)}).spec
    if (titleLines && compiled.title && !Array.isArray(compiled.title)) {
      // These renderer-owned signals are introduced only after the untrusted spec has passed
      // policy. They let the second layout use measured plot width without recompilation.
      const title = compiled.title as unknown as Record<string, unknown>
      compiled.signals = [...(compiled.signals ?? []), {name: 'blergMeasuredTitle', value: (checked.title as {text: string[]}).text}, {name: 'blergTitleLimit', value: Number(checked.width) - 64}]
      title.text = {signal: 'blergMeasuredTitle'}
      title.limit = {signal: 'blergTitleLimit'}
    }
    // Vega otherwise logs and swallows dataflow exceptions, which could return a partial chart.
    const log = logger(0)
    log.error = (..._args: unknown[]) => heavyChart()
    view = new View(parse(compiled, undefined, {ast: true}), {
      renderer: 'none', loader: denyVisualResources, logger: log, expr: expressionInterpreter,
    })
    await view.runAsync()
    // A bounded second layout uses the measured plot width and chrome reservation. Root
    // headings wrap in the actual available space, including independent axes and legends.
    let secondPass = false
    let addedTitleHeight = 0
    if (titleLines && compiled.title && !Array.isArray(compiled.title)) {
      const limit = Math.max(1, view.width())
      const lines = titleLines(limit)
      addedTitleHeight = Math.max(0, lines.length - (checked.title as {text: string[]}).text.length) * (theme.fontSize + 3)
      view.signal('blergMeasuredTitle', lines)
      view.signal('blergTitleLimit', limit)
      secondPass = true
    }
    if (size && Array.isArray(checked.layer)) {
      const chrome = Number(checked.height) - view.height()
      const desired = Math.min(Number(authored.height), size.maxHeight && fits(size.maxHeight) ? size.maxHeight : Infinity, Math.ceil(chrome + addedTitleHeight + theme.fontSize * 16))
      if (desired > Number(checked.height)) { view.height(desired); secondPass = true }
    }
    if (secondPass) await view.runAsync()
    const root = (view.scenegraph() as unknown as {root: SceneNode}).root
    const bounds = root.bounds!
    const padding = view.padding() as {left: number; right: number; top: number; bottom: number}
    const origin = view.origin()
    const width = Math.ceil(Math.max(bounds.width(), view.width()) + padding.left + padding.right)
    const height = Math.ceil(Math.max(bounds.height(), view.height()) + padding.top + padding.bottom)
    if (!(width > 0 && height > 0 && width <= MAX_CHART_SIDE && height <= MAX_CHART_SIDE && width * height <= MAX_CHART_AREA)) throw new ChartRefused(`Chart refused: it is too large to draw (${width} × ${height} pixels); use fewer or smaller panels`)
    const regions = inspectScene(root, limits, [origin[0] + padding.left, origin[1] + padding.top])
    const svg = await view.toSVG()
    let bytes = 0
    for (const char of svg) {
      const code = char.codePointAt(0)!
      bytes += code <= 0x7f ? 1 : code <= 0x7ff ? 2 : code <= 0xffff ? 3 : 4
      if (bytes > limits.svgBytes) heavyChart()
    }
    // Bound the SVG's actual intrinsic size before the page's image decoder sees it, too.
    const intrinsicSize = /^<svg\b[^>]*\bwidth="([\d.]+)"\s+height="([\d.]+)"/.exec(svg)
    if (!intrinsicSize) heavyChart()
    const imageWidth = Math.ceil(Number(intrinsicSize[1])), imageHeight = Math.ceil(Number(intrinsicSize[2]))
    if (!(imageWidth > 0 && imageHeight > 0 && imageWidth <= MAX_CHART_SIDE && imageHeight <= MAX_CHART_SIDE && imageWidth * imageHeight <= MAX_CHART_AREA)) heavyChart()
    let labels = 0, legendLabels = 0
    const countLabels = (node: SceneNode) => {
      if (node.marktype === 'text') {
        labels += node.items?.length ?? 0
        if (node.role === 'legend-label') legendLabels += node.items?.length ?? 0
      }
      for (const child of node.items ?? []) countLabels(child)
    }
    countLabels(root)
    return {svg, width: imageWidth, height: imageHeight, regions, layout: {plot: [origin[0] + padding.left, origin[1] + padding.top, view.width(), view.height()], labels, legendLabels}}
  } finally {
    try { view?.finalize() } finally { metrics.width = previousMetrics; restore() }
  }
}
