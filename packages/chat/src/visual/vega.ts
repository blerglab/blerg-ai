import { View, parse, logger, type Loader } from 'vega'
import { compile, type Config, type TopLevelSpec } from 'vega-lite'
import { expressionInterpreter } from 'vega-interpreter'
import { readVisualTheme, vegaTheme, type VisualTheme } from './theme'

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
  const available = container.clientWidth || container.parentElement?.clientWidth || 640
  const width = Math.max(1, Math.min(Number(spec.width) || 640, available, 1024))
  const height = Math.max(1, Math.min(1024, (Number(spec.height) || 360) * width / (Number(spec.width) || 640)))
  const compiled = compile({...spec, width, height} as unknown as TopLevelSpec,
    {config: vegaTheme(theme) as Config, logger: logger(0)}).spec
  const runtime = parse(compiled, undefined, {ast: true})
  const view = new View(runtime, {
    renderer: 'canvas', container: host, loader: denyVisualResources, logger: logger(0),
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
    return () => { view.finalize(); host.remove(); tooltip.remove() }
  } catch (error) {
    view.finalize()
    host.remove()
    tooltip.remove()
    throw error
  }
}
