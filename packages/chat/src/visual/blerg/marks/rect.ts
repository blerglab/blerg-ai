import {heatmapRange} from '../../theme'
import {datumKey} from '../dataIdentity'
// BLG-452 design doc §3.3 table row 3/mark type: rect (a heatmap) — band x, band y, a
// quantitative color channel interpolated between theme.surface2 and theme.accent (the same
// mix-based approach theme.ts's mermaidTheme() already uses for its own two-color ramps, design
// doc §3.9). v1 scope: no faceting, no size channel — NotShippedYet for those.
import { bandScale, niceLinearDomain } from '../scales'
import { continuousTicks, drawBandAxisBottom } from '../axes'
import { plotLayout } from '../layout'
import { group, line, rect as drawRect, text } from '../svg'
import { isImplementedSort, sortedDomain, type EncodingSort } from '../color'
import { drawContinuousLegend, legendWidth } from '../legend'
import { tooltipRegionText, type TooltipFieldDef, type TooltipRegion } from '../tooltip'
import type { MarkDeclaration } from '../declare'
import type { VisualTheme } from '../../theme'

export class NotShippedYet extends Error {}

export const declaration: MarkDeclaration = {
  mark: 'rect',
  supportsEncoding: encoding => {
    const {x,x2,y,color}=encoding as RectEncoding & {x2?:FieldDef}
    if (Object.values(encoding).some(def=>def && typeof def==='object' && 'timeUnit' in def)) return false
    if (x2 && x?.sort !== undefined) return false
    const band=(type?:string)=>type==='nominal'||type==='ordinal'
    return x2 ? x?.type==='temporal' && x2.type===x?.type && band(y?.type) && band(color?.type)
      : band(x?.type) && band(y?.type) && color?.type==='quantitative'
  },
  channels: {
    x: {types: new Set(['nominal', 'ordinal', 'temporal']), sort: true},
    x2: {types: new Set(['temporal'])},
    y: {types: new Set(['nominal', 'ordinal']), sort: true},
    color: {types: new Set(['quantitative', 'nominal', 'ordinal']), sort: true},
    tooltip: {types: new Set(['quantitative', 'nominal', 'ordinal', 'temporal'])},
  },
  markProperties: new Set(),
}

interface FieldDef { field?: string; type?: string; sort?: EncodingSort; bin?: unknown }
interface RectEncoding { x?: FieldDef; y?: FieldDef; color?: FieldDef; size?: FieldDef; tooltip?: TooltipFieldDef | TooltipFieldDef[] }

export interface RectDrawing { svg: string; accessiblePoints: Array<{label: string; value: string}>; tooltipRegions: TooltipRegion[] }

function hexToRgb(hex: string): [number, number, number] {
  const n = parseInt(hex.slice(1), 16)
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255]
}
function mix(a: string, b: string, t: number): string {
  const [ar, ag, ab] = hexToRgb(a), [br, bg, bb] = hexToRgb(b)
  const channel = (x: number, y: number) => Math.round(x + (y - x) * t)
  return '#' + [channel(ar, br), channel(ag, bg), channel(ab, bb)].map(c => Math.max(0, Math.min(255, c)).toString(16).padStart(2, '0')).join('')
}

export function drawRectHeatmap(rows: Array<Record<string, unknown>>, encoding: RectEncoding, width: number, height: number, theme: VisualTheme): RectDrawing {
  const xField = encoding.x?.field, yField = encoding.y?.field, colorField = encoding.color?.field
  if (typeof xField !== 'string' || typeof yField !== 'string' || typeof colorField !== 'string') throw new NotShippedYet('rect: missing field')
  if (encoding.x?.bin || encoding.y?.bin) throw new NotShippedYet('rect: bin is not implemented yet')
  if (encoding.color?.type !== 'quantitative') throw new NotShippedYet('rect: quantitative heatmap color required')
  if (encoding.size) throw new NotShippedYet('rect: size is not implemented yet')
  // Review finding: no type check at all meant either axis (including temporal, priority 5's
  // finding) coerced to a plain string category; now scoped to exactly the band x band shape
  // this file draws.
  const bandTypes = new Set(['nominal', 'ordinal'])
  if (!bandTypes.has(encoding.x?.type ?? '') || !bandTypes.has(encoding.y?.type ?? '')) throw new NotShippedYet('rect: only a nominal/ordinal x and y are implemented yet')
  // Review finding, extended (custom sort): same sort gap as bar.ts's own guard, on both axes —
  // now implements the same forms.
  if (!isImplementedSort(encoding.x?.sort) || !isImplementedSort(encoding.y?.sort)) {
    throw new NotShippedYet('rect: this sort is not implemented yet')
  }

  const xCategories = sortedDomain(rows.map(row => String(row[xField])), encoding.x?.sort)
  const yCategories = sortedDomain(rows.map(row => String(row[yField])), encoding.y?.sort)
  const known = (row: Record<string, unknown>) => typeof row[colorField] === 'number' && Number.isFinite(row[colorField])
  const colorValues = rows.filter(known).map(row => Number(row[colorField]))
  const ramp=heatmapRange(theme)
  const [colorMin, colorMax] = colorValues.length ? [Math.min(...colorValues), Math.max(...colorValues)] : [0, 0]

  // A heatmap's left margin is sized from the y category labels directly (they're drawn, not
  // tick-generated), unlike a quantitative axis's generated tick labels. The right margin
  // reserves room for the continuous color legend, the same way bar.ts reserves one for its
  // categorical legend (design doc §3.4).
  const colorDomain = colorMin === colorMax ? [colorMin, colorMax] as [number, number] : niceLinearDomain([colorMin, colorMax])
  const integerCounts=colorValues.every(Number.isInteger)
  const legendTicks = continuousTicks(colorDomain).filter(tick => (!integerCounts || Number.isInteger(tick)) && tick >= colorDomain[0] && tick <= colorDomain[1])
  const legendReserve = legendWidth(colorValues.length ? legendTicks.map(String) : ['Unknown'], colorField, theme)
  const plot = plotLayout(width - legendReserve, height, yCategories, theme)
  // Vega-Lite's default band padding for a rect/heatmap position scale is 0 on both ends (cells
  // touch, unlike bar's 0.1/0.05) — confirmed against Vega's own compiled band scale, not assumed.
  const xScale = bandScale(xCategories, [plot.x, plot.x + plot.width], 0, 0)
  const yScale = bandScale(yCategories, [plot.y, plot.y + plot.height], 0, 0)

  const cellPositions = rows.map(row => ({x: xScale(String(row[xField])) ?? 0, y: yScale(String(row[yField])) ?? 0}))
  const key = (row: Record<string, unknown>) => datumKey(row, xField, yField)
  const cells = rows.map((row, i) => {
    const value = Number(row[colorField])
    const t = colorMax === colorMin ? 0.5 : (value - colorMin) / (colorMax - colorMin)
    return drawRect(cellPositions[i].x, cellPositions[i].y, xScale.bandwidth, yScale.bandwidth, {'data-datum-key': key(row), 'data-scene-key': key(row), 'data-value-state': known(row) ? 'known' : 'unknown', fill: known(row) ? mix(ramp[0], ramp[1], t) : theme.surface2, stroke: known(row) ? undefined : theme.muted, 'stroke-dasharray': known(row) ? undefined : '2 2'})
  })

  const yLabels = yCategories.map(category => {
    const y = (yScale(category) ?? 0) + yScale.bandwidth / 2
    return text(plot.x - 9, y + theme.fontSize * 0.3, category, {fill: theme.muted, 'text-anchor': 'end', 'font-size': theme.fontSize, 'font-family': theme.fontFamily})
  })
  const domainLine = line(plot.x, plot.y, plot.x, plot.y + plot.height, {stroke: theme.border})

  const axes = group([domainLine, group(yLabels), drawBandAxisBottom(xScale, plot, theme)], {'data-scene-key': 'axes'})
  const legend = colorValues.length ? drawContinuousLegend(colorDomain, t => mix(ramp[0], ramp[1], colorMax === colorMin ? 0.5 : (t - colorMin) / (colorMax - colorMin)), plot.x + plot.width + 12, plot.y, plot.height, theme, colorField, integerCounts) : group([text(plot.x + plot.width + 12, plot.y + theme.fontSize, colorField, {fill: theme.fg, 'font-size': theme.fontSize, 'font-family': theme.fontFamily, 'font-weight': 'bold'}), text(plot.x + plot.width + 12, plot.y + theme.fontSize * 3, 'Unknown', {fill: theme.muted, 'font-size': theme.fontSize, 'font-family': theme.fontFamily})])
  const accessiblePoints = rows.map(row => ({key: key(row), label: `${String(row[xField])}, ${String(row[yField])}`, value: known(row) ? String(row[colorField]) : 'Unknown'}))
  const tooltipRegions = rows.map((row, i) => ({key: key(row),
    x1: cellPositions[i].x, y1: cellPositions[i].y, x2: cellPositions[i].x + xScale.bandwidth, y2: cellPositions[i].y + yScale.bandwidth,
    text: tooltipRegionText(encoding.tooltip, row),
  }))
  return {svg: group([group(cells, {'data-scene-key': 'marks'}), axes, group([legend], {'data-scene-key': 'legend'})]), accessiblePoints, tooltipRegions}
}
