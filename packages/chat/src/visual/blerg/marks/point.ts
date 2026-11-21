import {axisOptions, axisTickCount} from '../axisOptions'
import {datumKey} from '../dataIdentity'
// BLG-452 design doc §3.3 table row 3/mark type: point, circle and square — all drawn as a dot of
// one shape per row, continuous x and continuous y (a scatter plot). v1 scope: no color/size
// encoding, no 'shape' mark property beyond the mark's own type — NotShippedYet for those, caught
// by render.ts's dispatcher and falls back to Vega (case 4, M8), same discipline as bar.ts.
import { linearScale, niceLinearDomain } from '../scales'
import { continuousTicks, densityTickCount, drawLinearAxisBottom, drawLinearAxisLeft, formatTicks, preciseDensityTickCount } from '../axes'
import { plotLayoutContinuousX } from '../layout'
import { markStyle } from '../color'
import { circle, group, line, rect } from '../svg'
import { tooltipRegionText, type TooltipFieldDef, type TooltipRegion } from '../tooltip'
import type { MarkDeclaration } from '../declare'
import type { VisualTheme } from '../../theme'

export class NotShippedYet extends Error {}

const pointDeclaration = (mark: 'point' | 'circle' | 'square'): MarkDeclaration => ({
  mark,
  channels: {
    x: {types: new Set(['quantitative'])},
    y: {types: new Set(['quantitative'])},
    tooltip: {types: new Set(['quantitative', 'nominal', 'ordinal', 'temporal'])},
  },
  markProperties: new Set(['color', 'opacity']),
})
export const declarations: MarkDeclaration[] = [pointDeclaration('point'), pointDeclaration('circle'), pointDeclaration('square')]

interface FieldDef { field?: string; type?: string; bin?: unknown; axis?: {title?: unknown} }
interface PointEncoding { x?: FieldDef; y?: FieldDef; color?: FieldDef; size?: FieldDef; tooltip?: TooltipFieldDef | TooltipFieldDef[] }
interface PointMark { color?: unknown; opacity?: unknown }

const RADIUS = 3
// A little larger than the drawn radius, so hovering/tapping near a small mark still hits it.
const HIT_RADIUS = 8

export interface PointDrawing { svg: string; accessiblePoints: Array<{label: string; value: string}>; tooltipRegions: TooltipRegion[] }

export function drawPoint(markType: 'point' | 'circle' | 'square', rows: Array<Record<string, unknown>>, encoding: PointEncoding, mark: PointMark, width: number, height: number, theme: VisualTheme): PointDrawing {
  if (encoding.color || encoding.size) throw new NotShippedYet(`${markType}: color/size is not implemented yet`)
  const xField = encoding.x?.field, yField = encoding.y?.field
  if (typeof xField !== 'string' || typeof yField !== 'string') throw new NotShippedYet(`${markType}: missing field`)
  if (encoding.x?.type !== 'quantitative') throw new NotShippedYet(`${markType}: only a quantitative x (a scatter plot) is implemented yet`)
  if (encoding.x?.bin || encoding.y?.bin) throw new NotShippedYet(`${markType}: bin is not implemented yet`)

  const xValues = rows.map(row => Number(row[xField]))
  const yValues = rows.map(row => Number(row[yField]))
  // Review finding (caught by the new tick-value differential gate): Vega-Lite's default is
  // zero:true for x too, confirmed empirically — this only zeroed y, so a non-zero-starting x
  // domain (and therefore its tick count/values) silently diverged from Vega's own.
  const xDomain = niceLinearDomain([Math.min(0, ...xValues), Math.max(0, ...xValues)])
  const yDomain = niceLinearDomain([Math.min(0, ...yValues), Math.max(0, ...yValues)])
  // BLG-467 review: margin sized from the real drawn tick count, not a flat default — see
  // marks/bar.ts's own comment on this same fix.
  const yTickCount = axisTickCount('y', densityTickCount(plotLayoutContinuousX(width, height, [], theme, encoding.x, encoding.y).height))
  const yTickValues = continuousTicks(yDomain, yTickCount)
  const yTickLabels = formatTicks(yTickValues)
  const plot = plotLayoutContinuousX(width, height, yTickLabels, theme, encoding.x, encoding.y)
  const xScale = linearScale(xDomain, [plot.x, plot.x + plot.width])
  const yScale = linearScale(yDomain, [plot.y + plot.height, plot.y])

  // Review finding: Vega-Lite's own default is a hollow (stroke-only) marker for the 'point'
  // mark specifically — circle/square default to filled — confirmed empirically (not assumed),
  // not the uniformly solid-filled dot this file drew for all three before.
  // Task 2, item 1: a literal /mark/color overrides the default — the *stroke* for point's own
  // hollow default, the *fill* for circle/square's filled default (confirmed empirically); no
  // color encoding exists on this mark yet (encoding.color throws above), so it always applies.
  // /mark/opacity is a single generic SVG `opacity` attribute.
  const {color: markColor, opacity: markOpacity} = markStyle(mark)
  const style = markType === 'point'
    ? {fill: 'transparent', stroke: markColor ?? theme.accent, opacity: markOpacity}
    : {fill: markColor ?? theme.accent, opacity: markOpacity}
  const centers = rows.map((_row, i) => ({cx: xScale(xValues[i]), cy: yScale(yValues[i])}))
  const marks = centers.map(({cx, cy}, index) => markType === 'square'
    ? rect(cx - RADIUS, cy - RADIUS, RADIUS * 2, RADIUS * 2, {...style, 'data-datum-key': datumKey(rows[index], xField) , 'data-scene-key': datumKey(rows[index], xField)})
    : circle(cx, cy, RADIUS, {...style, 'data-datum-key': datumKey(rows[index], xField), 'data-scene-key': datumKey(rows[index], xField)}))

  const grid = group((axisOptions('y').grid===false ? [] : yTickValues).map(value => line(plot.x, yScale(value), plot.x + plot.width, yScale(value), {stroke: theme.border, 'stroke-opacity': 0.35, 'data-grid':'y'})))
  const axes = group([drawLinearAxisBottom(xScale, xDomain, plot, theme, (width > 640 ? densityTickCount(plot.width) : preciseDensityTickCount(plot.width))), drawLinearAxisLeft(yScale, yDomain, plot, theme, yTickCount)], {'data-scene-key': 'axes'})
  const accessiblePoints = rows.map((_row, i) => ({key: datumKey(rows[i],xField), label: `(${xValues[i]}, ${yValues[i]})`, value: String(yValues[i])}))
  const tooltipRegions = centers.map(({cx, cy}, i) => ({key: datumKey(rows[i],xField),
    x1: cx - HIT_RADIUS, y1: cy - HIT_RADIUS, x2: cx + HIT_RADIUS, y2: cy + HIT_RADIUS,
    text: tooltipRegionText(encoding.tooltip, rows[i]),
  }))
  return {svg: group([grid, group(marks, {'data-scene-key': 'marks'}), axes]), accessiblePoints, tooltipRegions}
}
