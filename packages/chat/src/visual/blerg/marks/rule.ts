import {axisOptions, axisTickCount} from '../axisOptions'
// BLG-452 design doc §3.3 table row 3/mark type: rule. v1 scope: a range rule per category — a
// band x channel with y and y2 quantitative channels (e.g. an error bar or a min/max range),
// drawn as one vertical line segment per category. A rule with only a single datum/value channel
// and no other scale-establishing channel only makes sense layered on another chart (an
// annotation line); layering is BLG-452 step 4, so that form throws NotShippedYet for now.
import { bandScale, linearScale, niceLinearDomain } from '../scales'
import { continuousTicks, densityTickCount, drawBandAxisBottom, drawLinearAxisLeft, formatTicks } from '../axes'
import { plotLayout } from '../layout'
import { group, line } from '../svg'
import { isImplementedSort, markStyle, sortedDomain, type EncodingSort } from '../color'
import { tooltipRegionText, type TooltipFieldDef, type TooltipRegion } from '../tooltip'
import type { MarkDeclaration } from '../declare'
import type { VisualTheme } from '../../theme'

export class NotShippedYet extends Error {}

export const declaration: MarkDeclaration = {
  mark: 'rule',
  channels: {
    x: {types: new Set(['nominal', 'ordinal']), sort: true},
    y: {types: new Set(['quantitative'])},
    y2: {types: new Set(['quantitative'])},
    tooltip: {types: new Set(['quantitative', 'nominal', 'ordinal', 'temporal'])},
  },
  markProperties: new Set(['color', 'opacity', 'strokeDash']),
}

interface FieldDef { field?: string; type?: string; sort?: EncodingSort; bin?: unknown }
interface RuleEncoding { x?: FieldDef; y?: FieldDef; y2?: FieldDef; color?: FieldDef; size?: FieldDef; tooltip?: TooltipFieldDef | TooltipFieldDef[] }
interface RuleMark { color?: unknown; opacity?: unknown; strokeDash?: unknown }

const HIT_HALF_WIDTH = 8

export interface RuleDrawing { svg: string; accessiblePoints: Array<{label: string; value: string}>; tooltipRegions: TooltipRegion[] }

export function drawRule(rows: Array<Record<string, unknown>>, encoding: RuleEncoding, mark: RuleMark, width: number, height: number, theme: VisualTheme): RuleDrawing {
  if (encoding.color) throw new NotShippedYet('rule: color is not implemented yet')
  if (encoding.size) throw new NotShippedYet('rule: size is not implemented yet')
  const xField = encoding.x?.field, yField = encoding.y?.field, y2Field = encoding.y2?.field
  if (typeof xField !== 'string' || typeof yField !== 'string' || typeof y2Field !== 'string') {
    throw new NotShippedYet('rule: only an x-band range (y and y2 both set) is implemented yet — a layered annotation rule needs composition (BLG-452 step 4)')
  }
  if (encoding.y?.bin || encoding.y2?.bin) throw new NotShippedYet('rule: bin is not implemented yet')
  // Review finding: no type check at all meant any x (including temporal, priority 5's finding)
  // coerced to a plain string category; now scoped to exactly the shape this file draws.
  if (encoding.x?.type !== 'nominal' && encoding.x?.type !== 'ordinal') throw new NotShippedYet('rule: only a nominal/ordinal x is implemented yet')
  // Review finding, extended (custom sort): same sort gap as bar.ts's own guard, now implements
  // the same forms.
  if (!isImplementedSort(encoding.x?.sort)) throw new NotShippedYet('rule: this sort is not implemented yet')

  const categories = sortedDomain(rows.map(row => String(row[xField])), encoding.x?.sort)
  const yValues = rows.map(row => Number(row[yField]))
  const y2Values = rows.map(row => Number(row[y2Field]))
  const yDomain = niceLinearDomain([Math.min(0, ...yValues, ...y2Values), Math.max(0, ...yValues, ...y2Values)])
  // BLG-467 review: margin sized from the real drawn tick count, not a flat default — see bar.ts's
  // own comment on this same fix.
  const yTickCount = axisTickCount('y', densityTickCount(plotLayout(width, height, [], theme).height))
  const yTickValues = continuousTicks(yDomain, yTickCount)
  const yTickLabels = formatTicks(yTickValues)
  const plot = plotLayout(width, height, yTickLabels, theme)
  const xScale = bandScale(categories, [plot.x, plot.x + plot.width], 0.1, 0.05)
  const yScale = linearScale(yDomain, [plot.y + plot.height, plot.y])

  // Task 2, item 1: a literal /mark/color overrides the default stroke; /mark/opacity is a
  // single generic SVG `opacity` attribute; /mark/strokeDash passes straight through to
  // stroke-dasharray — all confirmed empirically against real Vega output.
  const {color: markColor, opacity: markOpacity} = markStyle(mark)
  const strokeDash = Array.isArray(mark.strokeDash) ? (mark.strokeDash as number[]).join(',') : undefined
  const segmentX = rows.map(row => (xScale(String(row[xField])) ?? 0) + xScale.bandwidth / 2)
  const segments = rows.map((_row, i) => line(segmentX[i], yScale(yValues[i]), segmentX[i], yScale(y2Values[i]), {
    stroke: markColor ?? theme.accent, 'stroke-width': 2, opacity: markOpacity, 'stroke-dasharray': strokeDash,
  }))

  const grid = group((axisOptions('y').grid===false ? [] : yTickValues).map(value => line(plot.x, yScale(value), plot.x + plot.width, yScale(value), {stroke: theme.border, 'stroke-opacity': 0.35, 'data-grid':'y'})))
  const axes = group([drawBandAxisBottom(xScale, plot, theme), drawLinearAxisLeft(yScale, yDomain, plot, theme, yTickCount)])
  const accessiblePoints = rows.map((row, i) => ({label: String(row[xField]), value: `${yValues[i]}–${y2Values[i]}`}))
  const tooltipRegions = rows.map((row, i) => {
    const y1 = Math.min(yScale(yValues[i]), yScale(y2Values[i])), y2 = Math.max(yScale(yValues[i]), yScale(y2Values[i]))
    return {x1: segmentX[i] - HIT_HALF_WIDTH, y1, x2: segmentX[i] + HIT_HALF_WIDTH, y2, text: tooltipRegionText(encoding.tooltip, row)}
  })
  return {svg: group([grid, group(segments), axes]), accessiblePoints, tooltipRegions}
}
