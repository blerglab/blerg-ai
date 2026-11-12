// BLG-452 design doc §3.5: the live, accessible layer built around the SVG, not baked into it.
// v1 scope: single-series charts only (no mark here draws a color-grouped series yet, §3.3), so
// "one roving focus target per series" (§3.5, the post-review-round-2 fix replacing one focusable
// node per point) reduces to exactly one target for the whole chart; built for a plain unit spec
// only — a composed (concat/facet/repeat) spec's accessibility layer is a follow-up (render.ts's
// renderBlergChart only calls this for a single, uncomposed mark).
import type { VisualTheme } from '../theme'
import {commitScene} from './scene'
import { fillRefText } from '../refText'

/** What the data table and the roving cursor read; not part of BLG-451's RenderedVisual contract
 *  — this is renderBlergChart's own internal "what did we actually draw" record (design doc
 *  M9: post-aggregation/binning values, never the agent's raw input). */
export interface DrawnPoint { key?: string; label: string; value: string }

const TABLE_ROW_CAP = 500

function announcement(point: DrawnPoint): string {
  return `${point.label}: ${point.value}`
}


function buildTable(points: readonly DrawnPoint[], theme: VisualTheme): HTMLTableElement {
  const table = document.createElement('table')
  const shown = points.slice(0, TABLE_ROW_CAP)
  const caption = document.createElement('caption')
  caption.textContent = shown.length < points.length ? `Showing ${shown.length} of ${points.length} rows` : `${points.length} rows`
  table.append(caption)
  const head = document.createElement('tr')
  for (const heading of ['Label', 'Value']) { const th = document.createElement('th'); th.textContent = heading; head.append(th) }
  table.append(head)
  for (const point of shown) {
    const row = document.createElement('tr')
    for (const value of [point.label, point.value]) { const cell = document.createElement('td'); fillRefText(cell, value, 'visual-blerg-table-ref'); row.append(cell) }
    table.append(row)
  }
  table.style.fontSize = `${theme.fontSize}px`
  table.style.fontFamily = theme.fontFamily

  return table
}

/** The roving-focus series target, the aria-live region it announces into, and the collapsible
 *  data table — three sibling elements appended after the chart's own SVG, never inside it. */
export type AccessibleExtras = HTMLElement[] & {update:(next:readonly DrawnPoint[])=>void}
export function buildAccessibleExtras(points: readonly DrawnPoint[], theme: VisualTheme): AccessibleExtras {
  if (!points.length) return Object.assign([], {update: (_next:readonly DrawnPoint[])=>{}})

  const live = document.createElement('div')
  live.setAttribute('aria-live', 'polite')
  live.className = 'visual-blerg-live'
  live.style.cssText = 'position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0,0,0,0)'

  let cursor = 0
  const series = document.createElement('div')
  series.setAttribute('role', 'list')
  series.className = 'visual-blerg-series'
  const item = document.createElement('div')
  item.setAttribute('role', 'listitem')
  item.setAttribute('tabindex', '0')
  item.setAttribute('aria-label', `Series of ${points.length} data points — use arrow keys to hear each value`)
  item.addEventListener('keydown', event => {
    if(!points.length)return
    if (event.key === 'ArrowRight' || event.key === 'ArrowDown') cursor = Math.min(points.length - 1, cursor + 1)
    else if (event.key === 'ArrowLeft' || event.key === 'ArrowUp') cursor = Math.max(0, cursor - 1)
    else if (event.key === 'Home') cursor = 0
    else if (event.key === 'End') cursor = points.length - 1
    else return
    event.preventDefault()
    live.textContent = announcement(points[cursor])
  })
  series.append(item)

  const details = document.createElement('details')
  details.className = 'visual-blerg-table'
  const summary = document.createElement('summary')
  summary.textContent = 'Data table'
  const table = buildTable(points, theme)
  details.append(summary, table)

  return Object.assign([series, live, details], {update:(next:readonly DrawnPoint[])=>{
    const selected=points[cursor]?.key ?? points[cursor]?.label
    const index=next.findIndex(point=>(point.key ?? point.label)===selected)
    cursor=index>=0 ? index : Math.max(0,Math.min(cursor,next.length-1))
    points=next
    item.setAttribute('aria-label', `Series of ${points.length} data points — use arrow keys to hear each value`)
    commitScene(table,buildTable(next,theme))
    if(document.activeElement===item)live.textContent=index<0 ? 'Selected data point was removed.' : announcement(points[cursor])
  }})
}
