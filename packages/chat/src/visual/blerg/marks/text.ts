import {axisOptions, axisTickCount} from '../axisOptions'
// BLG-452 design doc §3.3 table row 3/mark type: text. v1 scope: continuous x and continuous y
// (matching point.ts), with a text channel naming the field drawn at each point. No color/size —
// NotShippedYet, same discipline as bar/point.
import { linearScale, niceLinearDomain } from '../scales'
import { continuousTicks, densityTickCount, drawLinearAxisBottom, drawLinearAxisLeft, formatTicks, preciseDensityTickCount } from '../axes'
import { plotLayoutContinuousX } from '../layout'
import { markStyle } from '../color'
import { group, line, text as svgText } from '../svg'
import { tooltipRegionText, type TooltipFieldDef, type TooltipRegion } from '../tooltip'
import type { MarkDeclaration } from '../declare'
import type { VisualTheme } from '../../theme'

export class NotShippedYet extends Error {}

export const declaration: MarkDeclaration = {
  mark: 'text',
  channels: {
    x: {types: new Set(['quantitative'])},
    y: {types: new Set(['quantitative'])},
    text: {types: new Set(['quantitative', 'nominal', 'ordinal', 'temporal'])},
    tooltip: {types: new Set(['quantitative', 'nominal', 'ordinal', 'temporal'])},
  },
  markProperties: new Set(['color', 'opacity']),
}

interface FieldDef { field?: string; type?: string; bin?: unknown; axis?: {title?: unknown} }
interface TextEncoding { x?: FieldDef; y?: FieldDef; text?: FieldDef; color?: FieldDef; size?: FieldDef; tooltip?: TooltipFieldDef | TooltipFieldDef[] }
interface TextMark { color?: unknown; opacity?: unknown }

const HIT_RADIUS = 10

export interface TextDrawing { svg: string; accessiblePoints: Array<{label: string; value: string}>; tooltipRegions: TooltipRegion[] }

export function drawText(rows: Array<Record<string, unknown>>, encoding: TextEncoding, mark: TextMark, width: number, height: number, theme: VisualTheme): TextDrawing {
  if (encoding.color || encoding.size) throw new NotShippedYet('text: color/size is not implemented yet')
  const xField = encoding.x?.field, yField = encoding.y?.field, textField = encoding.text?.field
  if (typeof xField !== 'string' || typeof yField !== 'string' || typeof textField !== 'string') throw new NotShippedYet('text: missing field')
  if (encoding.x?.type !== 'quantitative') throw new NotShippedYet('text: only a quantitative x is implemented yet')
  if (encoding.x?.bin || encoding.y?.bin) throw new NotShippedYet('text: bin is not implemented yet')

  const xValues = rows.map(row => Number(row[xField]))
  const yValues = rows.map(row => Number(row[yField]))
  // Review finding: same gap as point.ts's own (caught by the new tick-value differential gate)
  // — Vega-Lite's default is zero:true for x too, confirmed empirically.
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

  // Task 2, item 1: a literal /mark/color overrides the default text fill; /mark/opacity is a
  // single generic SVG `opacity` attribute — both confirmed empirically (markStyle's own header).
  const {color: markColor, opacity: markOpacity} = markStyle(mark)
  const centers = rows.map((_row, i) => ({cx: xScale(xValues[i]), cy: yScale(yValues[i])}))
  const marks = rows.map((row, i) => svgText(centers[i].cx, centers[i].cy, String(row[textField]), {
    fill: markColor ?? theme.fg, opacity: markOpacity, 'text-anchor': 'middle', 'font-size': theme.fontSize, 'font-family': theme.fontFamily,
  }))

  const grid = group((axisOptions('y').grid===false ? [] : yTickValues).map(value => line(plot.x, yScale(value), plot.x + plot.width, yScale(value), {stroke: theme.border, 'stroke-opacity': 0.35, 'data-grid':'y'})))
  const axes = group([drawLinearAxisBottom(xScale, xDomain, plot, theme, preciseDensityTickCount(plot.width)), drawLinearAxisLeft(yScale, yDomain, plot, theme, yTickCount)])
  const accessiblePoints = rows.map((row, i) => ({label: String(row[textField]), value: `(${xValues[i]}, ${yValues[i]})`}))
  const tooltipRegions = centers.map(({cx, cy}, i) => ({
    x1: cx - HIT_RADIUS, y1: cy - HIT_RADIUS, x2: cx + HIT_RADIUS, y2: cy + HIT_RADIUS,
    text: tooltipRegionText(encoding.tooltip, rows[i]),
  }))
  return {svg: group([grid, group(marks), axes]), accessiblePoints, tooltipRegions}
}
