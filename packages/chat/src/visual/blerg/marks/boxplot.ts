import {axisOptions, axisTickCount} from '../axisOptions'
// BLG-452 design doc §3.3 table row 3/mark type: boxplot (priority 4, the policy change already
// shipped — vegaPolicy.ts's MARKS gained 'boxplot', §3.1/case 2b). v1 scope is Vega-Lite's own
// default appearance: a band x category, a quantitative y, Tukey's 1.5*IQR rule for whiskers and
// outliers — confirmed against real Vega output, not assumed:
//   - quartiles use d3-array's quantileSorted() (type-7/linear interpolation), the same canonical
//     math Vega itself uses (§3.4/H6: reuse, don't reinvent) — byte-identical, not tolerance-close.
//   - the box's fixed width is a literal 14px, independent of the category band's own width
//     (Vega-Lite's own boxplot default, confirmed empirically — not derived from bandwidth).
//   - the median mark is a thin (~2px) filled rect; Vega-Lite's own default is a literal white,
//     but BLG-473 overrides that with the theme's surface token so it never reads as a stray
//     white block on a dark theme.
//   - the whisker rules default to a literal "black" stroke in Vega-Lite (same observed default
//     as a rule mark elsewhere, render.ts's literalMarkColor); BLG-473 overrides that with the
//     theme's fg token so whiskers stay visible on dark themes too.
//   - the box fill and the outlier marker's stroke both use the theme's own accent color.
// No color encoding, no sub-mark overrides (box/median/outliers/rule/ticks), no boxplot config —
// NotShippedYet, a stated v1 gap (design doc's own intro: "boxplot's config/sub-mark coverage"
// is explicitly not required).
import { quantileSorted } from 'd3-array'
import { bandScale, linearScale, niceLinearDomain } from '../scales'
import { continuousTicks, densityTickCount, drawBandAxisBottom, drawLinearAxisLeft, formatTicks } from '../axes'
import { plotLayout } from '../layout'
import { isImplementedSort, markStyle, sortedDomain, type EncodingSort } from '../color'
import { circle, group, line, rect } from '../svg'
import { tooltipRegionText, type TooltipFieldDef, type TooltipRegion } from '../tooltip'
import type { MarkDeclaration } from '../declare'
import type { VisualTheme } from '../../theme'

export class NotShippedYet extends Error {}

export const declaration: MarkDeclaration = {
  mark: 'boxplot',
  channels: {
    x: {types: new Set(['nominal', 'ordinal']), sort: true},
    y: {types: new Set(['quantitative'])},
    tooltip: {types: new Set(['quantitative', 'nominal', 'ordinal', 'temporal'])},
  },
  markProperties: new Set(['color', 'opacity']),
}

interface FieldDef { field?: string; type?: string; sort?: EncodingSort; bin?: unknown }
interface BoxplotEncoding { x?: FieldDef; y?: FieldDef; color?: FieldDef; size?: FieldDef; tooltip?: TooltipFieldDef | TooltipFieldDef[] }
interface BoxplotMark { box?: unknown; median?: unknown; outliers?: unknown; rule?: unknown; ticks?: unknown; color?: unknown; opacity?: unknown }

const BOX_WIDTH = 14
const MEDIAN_HEIGHT = 2
const OUTLIER_RADIUS = 3
const EXTENT = 1.5 // Tukey's rule, Vega-Lite's own default boxplot "extent"

export interface BoxplotDrawing { svg: string; accessiblePoints: Array<{label: string; value: string}>; tooltipRegions: TooltipRegion[] }

function quartiles(values: number[]): {q1: number; median: number; q3: number} {
  const sorted = [...values].sort((a, b) => a - b)
  return {q1: quantileSorted(sorted, 0.25)!, median: quantileSorted(sorted, 0.5)!, q3: quantileSorted(sorted, 0.75)!}
}

export function drawBoxplot(rows: Array<Record<string, unknown>>, encoding: BoxplotEncoding, mark: BoxplotMark, width: number, height: number, theme: VisualTheme): BoxplotDrawing {
  if (encoding.color) throw new NotShippedYet('boxplot: color is not implemented yet')
  if (encoding.size) throw new NotShippedYet('boxplot: size is not implemented yet')
  if (mark.box || mark.median || mark.outliers || mark.rule || mark.ticks) throw new NotShippedYet('boxplot: sub-mark overrides are not implemented yet')
  const xField = encoding.x?.field, yField = encoding.y?.field
  if (typeof xField !== 'string' || typeof yField !== 'string') throw new NotShippedYet('boxplot: missing field')
  if (encoding.x?.bin || encoding.y?.bin) throw new NotShippedYet('boxplot: bin is not implemented yet')
  // Review finding: no type check at all meant any x (including temporal, priority 5's finding)
  // coerced to a plain string category; now scoped to exactly the shape this file draws.
  if (encoding.x?.type !== 'nominal' && encoding.x?.type !== 'ordinal') throw new NotShippedYet('boxplot: only a nominal/ordinal x is implemented yet')
  if (encoding.y?.type !== 'quantitative') throw new NotShippedYet('boxplot: only a quantitative y is implemented yet')
  // Review finding, extended (custom sort): same sort gap as bar.ts's own guard, now implements
  // the same forms.
  if (!isImplementedSort(encoding.x?.sort)) throw new NotShippedYet('boxplot: this sort is not implemented yet')

  const categories = sortedDomain(rows.map(row => String(row[xField])), encoding.x?.sort)
  const groups = categories.map(category => {
    const values = rows.filter(row => String(row[xField]) === category).map(row => Number(row[yField]))
    const {q1, median, q3} = quartiles(values)
    const iqr = q3 - q1
    const lowerFence = q1 - EXTENT * iqr, upperFence = q3 + EXTENT * iqr
    const within = values.filter(v => v >= lowerFence && v <= upperFence)
    const outliers = values.filter(v => v < lowerFence || v > upperFence)
    const whiskerMin = within.length ? Math.min(...within) : q1, whiskerMax = within.length ? Math.max(...within) : q3
    return {category, values, q1, median, q3, whiskerMin, whiskerMax, outliers}
  })

  const allValues = groups.flatMap(g => g.values)
  const niced = niceLinearDomain([Math.min(0, ...allValues), Math.max(0, ...allValues)])
  // BLG-467 review: margin sized from the real drawn tick count, not a flat default — see bar.ts's
  // own comment on this same fix.
  const yTickCount = axisTickCount('y', densityTickCount(plotLayout(width, height, [], theme).height))
  const yTickValues = continuousTicks(niced, yTickCount)
  const yTickLabels = formatTicks(yTickValues)
  const plot = plotLayout(width, height, yTickLabels, theme)
  const xScale = bandScale(categories, [plot.x, plot.x + plot.width], 0.1, 0.05)
  const yScale = linearScale(niced, [plot.y + plot.height, plot.y])

  // Task 2, item 1: a literal /mark/color overrides the box's own fill (confirmed empirically —
  // median/whisker/outlier colors are documented, unaffected gaps, same scope as before);
  // /mark/opacity is a generic SVG attribute.
  const {color: markColor, opacity: markOpacity} = markStyle(mark)
  const shapes = groups.map(g => {
    const cx = (xScale(g.category) ?? 0) + xScale.bandwidth / 2
    const boxTop = yScale(g.q3), boxBottom = yScale(g.q1)
    const whiskerLower = line(cx, yScale(g.whiskerMin), cx, boxBottom, {stroke: theme.fg})
    const whiskerUpper = line(cx, boxTop, cx, yScale(g.whiskerMax), {stroke: theme.fg})
    const box = rect(cx - BOX_WIDTH / 2, boxTop, BOX_WIDTH, boxBottom - boxTop, {fill: markColor ?? theme.accent, opacity: markOpacity})
    const medianMark = rect(cx - BOX_WIDTH / 2, yScale(g.median) - MEDIAN_HEIGHT / 2, BOX_WIDTH, MEDIAN_HEIGHT, {fill: theme.surface})
    const outlierMarks = g.outliers.map(v => circle(cx, yScale(v), OUTLIER_RADIUS, {stroke: theme.accent, 'stroke-width': 2, fill: 'transparent'}))
    return {svg: group([whiskerLower, whiskerUpper, box, medianMark, group(outlierMarks)], {'data-datum-key':g.category}), cx, boxTop, boxBottom, g}
  })

  const grid = group((axisOptions('y').grid===false ? [] : yTickValues).map(value => line(plot.x, yScale(value), plot.x + plot.width, yScale(value), {stroke: theme.border, 'stroke-opacity': 0.35, 'data-grid':'y'})))
  const axes = group([drawBandAxisBottom(xScale, plot, theme), drawLinearAxisLeft(yScale, niced, plot, theme, yTickCount)])
  const accessiblePoints = groups.map(g => ({label: g.category, value: `median ${g.median}, Q1 ${g.q1}, Q3 ${g.q3}`}))
  const tooltipRegions = shapes.map(s => ({key:s.g.category,
    x1: s.cx - BOX_WIDTH / 2, y1: s.boxTop, x2: s.cx + BOX_WIDTH / 2, y2: s.boxBottom,
    text: tooltipRegionText(encoding.tooltip, {[xField]: s.g.category, median: s.g.median, q1: s.g.q1, q3: s.g.q3}),
  }))
  return {svg: group([grid, group(shapes.map(s => s.svg)), axes]), accessiblePoints, tooltipRegions}
}
