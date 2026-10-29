import { View, parse, logger, type Loader, type Scene } from 'vega'
import { compile, type Config, type TopLevelSpec } from 'vega-lite'
import { expressionInterpreter } from 'vega-interpreter'
import { readVisualTheme, vegaTheme, type VisualTheme } from './theme'
import { ChartRefused, isCompoundChart } from './vegaPolicy'

// The largest canvas a chart may lay out to, in CSS pixels. A composition's size comes from its
// panels and, for a facet, from its data, so it is measured before anything is drawn.
export const MAX_CHART_SIDE = 4096
export const MAX_CHART_AREA = 4096 * 2048

export const denyVisualResources: Loader = {
  load: async () => { throw new Error('External chart resources are disabled') },
  sanitize: async () => { throw new Error('Chart links are disabled') },
  http: async () => { throw new Error('External chart resources are disabled') },
  file: async () => { throw new Error('External chart resources are disabled') },
}

export function tooltipText(value: unknown): string {
  if (value === null || value === undefined) return ''
  if (typeof value === 'object') {
    return Object.entries(value).map(([key, item]) => `${key}: ${typeof item === 'object' ? JSON.stringify(item) : String(item)}`).join('\n').slice(0, 4000)
  }
  return String(value).slice(0, 4000)
}

export async function renderVegaLite(spec: Record<string, unknown>, container: HTMLElement, theme: VisualTheme = readVisualTheme(container)): Promise<() => void> {
  const host = document.createElement('div')
  host.className = 'visual-chart'
  host.setAttribute('role', 'img')
  host.setAttribute('aria-label', 'Vega-Lite chart')
  const tooltip = document.createElement('div')
  tooltip.className = 'visual-tooltip'
  tooltip.setAttribute('role', 'tooltip')
  tooltip.hidden = true
  container.replaceChildren(host, tooltip)
  // Preserve the spec's bounded dimensions/aspect, scaling down to the message.
  // Never scale a small authored plot up or allocate a canvas beyond policy limits.
  // A composition keeps its authored panel sizes and is measured below instead.
  let sized = spec
  if (!isCompoundChart(spec)) {
    const available = container.clientWidth || container.parentElement?.clientWidth || 640
    const width = Math.max(1, Math.min(Number(spec.width) || 640, available, 1024))
    const height = Math.max(1, Math.min(1024, (Number(spec.height) || 360) * width / (Number(spec.width) || 640)))
    sized = {...spec, width, height}
  }
  const compiled = compile(sized as unknown as TopLevelSpec,
    {config: vegaTheme(theme) as Config, logger: logger(0)}).spec
  const runtime = parse(compiled, undefined, {ast: true})
  // Lay the chart out without a renderer first, so an oversized one never allocates a canvas.
  const view = new View(runtime, {
    renderer: 'none', loader: denyVisualResources, logger: logger(0),
    expr: expressionInterpreter,
    tooltip: (_handler, event, _item, value: unknown) => {
      tooltip.textContent = tooltipText(value)
      tooltip.hidden = value === null || value === undefined
      if (tooltip.hidden || !event) return
      const bounds = container.getBoundingClientRect()
      tooltip.style.left = `${Math.max(0, Math.min(event.clientX - bounds.left + 12, bounds.width - 100))}px`
      tooltip.style.top = `${Math.max(0, event.clientY - bounds.top + 12)}px`
    },
  })
  try {
    await view.runAsync()
    // At runtime scenegraph() is the Scenegraph whose root mark carries the laid-out bounds.
    const bounds = (view.scenegraph() as unknown as {root: Scene}).root.bounds
    const outerWidth = Math.max(bounds.width(), view.width())
    const outerHeight = Math.max(bounds.height(), view.height())
    if (!(outerWidth <= MAX_CHART_SIDE && outerHeight <= MAX_CHART_SIDE && outerWidth * outerHeight <= MAX_CHART_AREA)) {
      throw new ChartRefused(`Chart refused: it is too large to draw (${Math.round(outerWidth)} × ${Math.round(outerHeight)} pixels); use fewer or smaller panels`)
    }
    view.renderer('canvas').initialize(host)
    await view.runAsync()
    return () => { view.finalize(); host.remove(); tooltip.remove() }
  } catch (error) {
    view.finalize()
    host.remove()
    tooltip.remove()
    throw error
  }
}
