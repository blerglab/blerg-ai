// BLG-452 design doc §3.4: tooltip construction and the shared hover/touch hit-testing model.
// Value shapes and formatting are confirmed empirically against real Vega output (a single
// tooltip field -> the bare formatted value; an array -> an object keyed by each field's title),
// then handed to vegaBudget.ts's existing tooltipText() so both renderers format the final
// "key: value" lines identically — one formatter, not two.
import { utcFormat,timeFormat } from 'd3-time-format'
import {format as numberFormat} from 'd3-format'
import { tooltipText, type TooltipRegion as VegaTooltipRegion } from '../vegaBudget'
import { fillRefText } from '../refText'

export { tooltipText } from '../vegaBudget'
export type TooltipRegion = VegaTooltipRegion & {markKey?: string}

export interface TooltipFieldDef { field?: string; aggregate?: string; bin?: unknown; type?: string; timeUnit?: string; title?:string|null; format?:string }

const defaultTemporalFormat = utcFormat('%Y-%m-%d %H:%M:%S UTC')

/** Matches Vega-Lite's default tooltip title exactly: "Count of Records" for a bare count,
 *  "<Capitalized aggregate> of <field>" for any other aggregate, "<field> (binned)" for a
 *  binned field, else the plain field name — confirmed against real Vega tooltip output. */
export function tooltipFieldTitle(def: TooltipFieldDef): string {
  if(typeof def.title==='string')return def.title
  if (def.aggregate === 'count') return 'Count of Records'
  if (def.aggregate) return def.aggregate[0]!.toUpperCase() + def.aggregate.slice(1) + ' of ' + def.field
  if (def.bin) return `${def.field} (binned)`
  return def.field ?? ''
}

/** Temporal fields use the native timeline's UTC zone, including exact observation time. */
export function tooltipFieldValue(def: TooltipFieldDef, raw: unknown): string {
  if(raw == null)return 'Unknown'
  if (def.type === 'temporal' && !def.timeUnit && (typeof raw === 'string' || typeof raw === 'number')) {
    const date = new Date(raw)
    if (!Number.isNaN(date.getTime())) return def.format ? timeFormat(def.format)(date) : defaultTemporalFormat(date)
  }
  if(def.format && def.type==='quantitative' && typeof raw==='number')return numberFormat(def.format)(raw)
  return String(raw)
}

/** Builds the same value shape Vega-Lite's compiled tooltip signal produces for a field, a
 *  single-field tooltip channel, or an array tooltip channel — then tooltipText() formats it. */
export function tooltipValueFor(tooltipDef: TooltipFieldDef | TooltipFieldDef[] | undefined, row: Record<string, unknown>): unknown {
  if (!tooltipDef) return undefined
  if (Array.isArray(tooltipDef)) {
    const value: Record<string, string> = {}
    for (const def of tooltipDef) if (def.field) value[tooltipFieldTitle(def)] = tooltipFieldValue(def, row[def.field])
    return value
  }
  return tooltipDef.field ? tooltipFieldValue(tooltipDef, row[tooltipDef.field]) : undefined
}

/** The formatted tooltip text for one drawn point's row — what a TooltipRegion's text field
 *  holds (design doc §3.4: regions carry already-formatted text, matching vegaBudget.ts's
 *  existing convention on the Vega path). Empty when the spec has no tooltip channel at all,
 *  matching Vega's own no-tooltip-channel behavior confirmed empirically. */
export function tooltipRegionText(tooltipDef: TooltipFieldDef | TooltipFieldDef[] | undefined, row: Record<string, unknown>): string {
  const value = tooltipValueFor(tooltipDef, row)
  return value === undefined ? '' : tooltipText(value)
}

/** Attaches hover and touch tooltips to a chart's SVG root, reusing the exact same
 *  TooltipRegion shape and .visual-tooltip conventions the existing Vega path already uses
 *  (vega.ts's canvas.onpointermove), so both renderers' tooltips look and behave identically.
 *  pointerdown covers a tap (touch has no hover); pointermove covers hover and touch-drag. */
let tooltipId = 0
export function attachTooltip(root: SVGElement, container: HTMLElement, regions: readonly TooltipRegion[]): {tooltip: HTMLElement; setRegions: (next: readonly TooltipRegion[], refreshMarks?: boolean) => void; dispose: () => void} {
  const tooltip = document.createElement('div')
  tooltip.className = 'visual-tooltip'
  tooltip.id = `chart-tooltip-${++tooltipId}`
  tooltip.setAttribute('role', 'tooltip')
  tooltip.hidden = true

  root.setAttribute('tabindex', '0')
  let focusIndex = 0
  let active: TooltipRegion | undefined
  let keyed = new Map<string, SVGElement[]>()
  const indexMarks = () => {
    keyed = new Map()
    for (const mark of root.querySelectorAll<SVGElement>('[data-datum-key]')) {
      const key = mark.getAttribute('data-datum-key')!
      const list = keyed.get(key) ?? []
      list.push(mark); keyed.set(key, list)
    }
  }
  indexMarks()
  const indicator = document.createElementNS('http://www.w3.org/2000/svg', 'circle')
  indicator.setAttribute('data-chart-indicator', 'true')
  indicator.setAttribute('r', '4')
  indicator.setAttribute('fill', 'currentColor')
  indicator.setAttribute('pointer-events', 'none')
  const emphasize = (region?: TooltipRegion) => {
    if (active === region) return
    root.querySelectorAll('[data-chart-active]').forEach(mark => mark.removeAttribute('data-chart-active'))
    active = region
    indicator.remove()
    let marks = region?.key ? keyed.get(region.markKey ?? region.key) ?? [] : []
    // Composed panels and static scatterplots may repeat a datum key. Resolve only
    // those collisions against display bounds; ordinary hover never measures marks.
    if (region && marks.length > 1) {
      const bounds = root.getBoundingClientRect()
      if (bounds.width > 0 && bounds.height > 0) {
        const x = bounds.x + (region.x1 + region.x2) / 2 * bounds.width / Number(root.getAttribute('width'))
        const y = bounds.y + (region.y1 + region.y2) / 2 * bounds.height / Number(root.getAttribute('height'))
        marks = marks.filter(mark => {const box = mark.getBoundingClientRect(); return x >= box.left && x <= box.right && y >= box.top && y <= box.bottom})
      }
    }
    if (region && marks.some(mark => mark.tagName === 'path')) {
      indicator.setAttribute('cx', String((region.x1 + region.x2) / 2))
      indicator.setAttribute('cy', String((region.y1 + region.y2) / 2))
      root.append(indicator)
    }
    marks.forEach(mark => mark.setAttribute('data-chart-active', 'true'))
    root.toggleAttribute('data-chart-emphasis', !!region)
  }
  const regionAt = (x: number, y: number) => regions.findLast(region => x >= region.x1 && x <= region.x2 && y >= region.y1 && y <= region.y2)

  let pointer: {x:number; y:number} | undefined
  const show = (clientX: number, clientY: number) => {
    pointer = {x:clientX,y:clientY}
    const bounds = root.getBoundingClientRect()
    const x = (clientX - bounds.left) * (Number(root.getAttribute('width')) / bounds.width || 1)
    const y = (clientY - bounds.top) * (Number(root.getAttribute('height')) / bounds.height || 1)
    const region = regionAt(x, y)
    emphasize(region)
    // BLG-485: a card/commit ref in the tooltip text is drawn in the same quiet, underlined
    // style chat text's own chip uses — not a live hovercard/click target (refText.ts's own
    // header): this tooltip hides the moment the pointer leaves the chart, before a hover-intent
    // over something inside it could ever complete.
    if (region) fillRefText(tooltip, region.text, 'visual-tooltip-ref', false)
    else tooltip.replaceChildren()
    tooltip.hidden = !region
    if (region) {
      const outer = container.getBoundingClientRect()
      tooltip.style.left = `${Math.max(0, Math.min(clientX - outer.left + 12, outer.width - 100))}px`
      tooltip.style.top = `${Math.max(0, clientY - outer.top + 12)}px`
    }
  }
  const onMove = (event: PointerEvent) => show(event.clientX, event.clientY)
  const onDown = (event: PointerEvent) => show(event.clientX, event.clientY)
  const onLeave = () => { pointer = undefined; tooltip.hidden = true; root.removeAttribute('aria-describedby'); emphasize() }
  const onFocus = () => {
    const region = regions[focusIndex]
    if (!region) return
    emphasize(region)
    root.setAttribute('aria-describedby', tooltip.id)
    fillRefText(tooltip, region.text, 'visual-tooltip-ref', false)
    tooltip.hidden = !region.text
    tooltip.style.left = '12px'; tooltip.style.top = '12px'
  }
  const onKey = (event: KeyboardEvent) => {
    if (event.key === 'Escape') {onLeave(); event.stopPropagation(); return}
    if (!['ArrowRight','ArrowDown','ArrowLeft','ArrowUp','Home','End'].includes(event.key)) return
    event.preventDefault()
    focusIndex = event.key === 'Home' ? 0 : event.key === 'End' ? regions.length - 1 : Math.max(0, Math.min(regions.length - 1, focusIndex + (['ArrowRight','ArrowDown'].includes(event.key) ? 1 : -1)))
    onFocus()
  }
  root.addEventListener('blerg-chart-before-snapshot', onLeave)
  root.addEventListener('focus', onFocus)
  root.addEventListener('blur', onLeave)
  root.addEventListener('keydown', onKey)

  root.addEventListener('pointermove', onMove)
  root.addEventListener('pointerdown', onDown)
  root.addEventListener('pointerleave', onLeave)

  return {
    tooltip,
    setRegions: (next, refreshMarks = true) => { regions = next; focusIndex = Math.min(focusIndex, Math.max(0, next.length - 1)); if (refreshMarks) indexMarks(); active = undefined; if(pointer) show(pointer.x,pointer.y); else if(document.activeElement === root) onFocus() },
    dispose: () => {
      indicator.remove()
      root.removeEventListener('blerg-chart-before-snapshot', onLeave)
      root.removeEventListener('focus', onFocus)
      root.removeEventListener('blur', onLeave)
      root.removeEventListener('keydown', onKey)
      root.removeEventListener('pointermove', onMove)
      root.removeEventListener('pointerdown', onDown)
      root.removeEventListener('pointerleave', onLeave)
    },
  }
}
