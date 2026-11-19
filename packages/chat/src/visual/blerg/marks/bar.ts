import {axisOptions, axisTickCount} from '../axisOptions'
import {supportsHistogram} from './histogram'
import {datumKey} from '../dataIdentity'
// BLG-452 design doc §3.3 table row 3/mark type: the bar mark. A band x channel and a
// quantitative y channel, with or without an aggregate. A color channel stacks by default
// (Vega-Lite's own default for a shared position channel) unless an xOffset channel is present,
// in which case bars are grouped (dodged) side by side instead — both confirmed against real
// Vega output, including stack order and categorical color assignment (design doc §3.3/priority
// 2). classify.ts's allow-list is about spec *shape*; this file still throws NotShippedYet for
// anything beyond that shape (size encoding, bin), caught by render.ts's dispatcher (case 4, M8).
import { bandScale, linearScale, niceLinearDomain } from '../scales'
import { continuousTicks, densityTickCount, drawBandAxisBottom, drawLinearAxisLeft, formatTicks } from '../axes'
import { plotLayout } from '../layout'
import { categoricalColorScale, isImplementedSort, markStyle, sortedDomain, type EncodingSort } from '../color'
import { legendWidth, drawCategoricalLegend } from '../legend'
import { rect, group, line } from '../svg'
import { tooltipRegionText, type TooltipFieldDef, type TooltipRegion } from '../tooltip'
import type { MarkDeclaration } from '../declare'
import type { VisualTheme } from '../../theme'

export class NotShippedYet extends Error {}

interface FieldDef { field?: string; aggregate?: string; type?: string; stack?: unknown; sort?: EncodingSort; bin?: unknown }
interface BarEncoding { x?: FieldDef; y?: FieldDef; color?: FieldDef; xOffset?: FieldDef; size?: FieldDef; tooltip?: TooltipFieldDef | TooltipFieldDef[] }
interface BarMark { color?: unknown; opacity?: unknown }

const STACK_DEFAULTS = new Set<unknown>([undefined, true, 'zero'])

const AGGREGATORS: Record<string, (values: number[]) => number> = {
  sum: values => values.reduce((a, b) => a + b, 0),
  mean: values => values.reduce((a, b) => a + b, 0) / values.length,
  median: values => { const sorted = [...values].sort((a, b) => a - b); const mid = Math.floor(sorted.length / 2); return sorted.length % 2 ? sorted[mid] : (sorted[mid - 1] + sorted[mid]) / 2 },
  min: values => Math.min(...values),
  max: values => Math.max(...values),
  count: values => values.length,
  distinct: values => new Set(values).size,
}

export const declaration: MarkDeclaration = {
  mark: 'bar',
  supportsEncoding: encoding => {
    if (Object.values(encoding).some(def=>def && typeof def==='object' && 'timeUnit' in def)) return false
    if (encoding.x2) return supportsHistogram(encoding)
    const x=encoding.x as FieldDef | undefined
    return !x?.bin && (!x || x.type==='nominal'||x.type==='ordinal')
  },
  channels: {
    x: {types: new Set(['nominal', 'ordinal', 'quantitative']), sort: true, bin: true},
    x2: {types: new Set(['quantitative'])},
    y: {types: new Set(['quantitative']), aggregate: new Set(Object.keys(AGGREGATORS)), stack: new Set(['zero'])},
    color: {types: new Set(['nominal', 'ordinal']), sort: true},
    xOffset: {types: new Set(['nominal', 'ordinal'])},
    tooltip: {types: new Set(['quantitative', 'nominal', 'ordinal', 'temporal'])},
  },
  markProperties: new Set(['color', 'opacity']),
}

export interface BarDrawing {
  svg: string
  points: Array<{category: string; value: number; x: number; y: number; width: number; height: number}>
  accessiblePoints: Array<{label: string; value: string}>
  tooltipRegions: TooltipRegion[]
}

export function drawBar(rows: Array<Record<string, unknown>>, encoding: BarEncoding, mark: BarMark, width: number, height: number, theme: VisualTheme): BarDrawing {
  const xField = encoding.x?.field, yField = encoding.y?.field
  if (typeof xField !== 'string' || typeof yField !== 'string') throw new NotShippedYet('bar: missing field')
  if (encoding.x?.bin || encoding.y?.bin) throw new NotShippedYet('bar: bin is not implemented yet')
  if (encoding.size) throw new NotShippedYet('bar: size is not implemented yet')
  // Review finding: this file always treated x as the band/category field and y as the
  // Number()-coerced value, regardless of declared type — a horizontal bar (y nominal, x
  // quantitative) silently fed a category string like "Sales" through Number(), drawing NaN
  // geometry. Only the one orientation this file actually implements is accepted now; the
  // reverse falls back to Vega rather than drawing broken bars.
  // (a temporal x falls under this same guard — BLG-452 priority 5's finding that no mark here
  // buckets by calendar unit; this is the same "only the implemented shape" guard, not a second
  // special case for it.)
  if (encoding.x?.type !== 'nominal' && encoding.x?.type !== 'ordinal') throw new NotShippedYet('bar: only a nominal/ordinal x (vertical bars) is implemented yet')
  if (encoding.y?.type !== 'quantitative') throw new NotShippedYet('bar: only a quantitative y is implemented yet')
  // Review finding: stack was always drawn as Vega-Lite's own "zero" default whenever a color
  // channel was present, regardless of an explicit encoding.y.stack — "normalize"/"center"/false
  // (none implemented) silently drew zero-stacked bars instead. Only the implemented default is
  // now accepted.
  if (!STACK_DEFAULTS.has(encoding.y?.stack)) throw new NotShippedYet('bar: only the default (zero) stack is implemented yet')
  // Review finding, extended (custom sort): categories were always sorted ascending
  // alphabetically (sortedDomain), ignoring any explicit encoding.x.sort/encoding.color.sort —
  // ascending, descending, null (encounter order) and an explicit array are now all implemented
  // (sortedDomain's own header); a sort-by-another-field object still isn't, and falls back.
  if (!isImplementedSort(encoding.x?.sort)) throw new NotShippedYet('bar: this sort is not implemented yet')
  if (!isImplementedSort(encoding.color?.sort)) throw new NotShippedYet('bar: this sort is not implemented yet')
  const colorField = encoding.color?.field
  const offsetField = encoding.xOffset?.field
  if (offsetField !== undefined && typeof offsetField !== 'string') throw new NotShippedYet('bar: xOffset needs a field')
  if (offsetField && !colorField) throw new NotShippedYet('bar: xOffset without color is not implemented yet')

  const categories = sortedDomain(rows.map(row => String(row[xField])), encoding.x?.sort)
  const aggregate = encoding.y?.aggregate
  const reduce = (values: number[]) => aggregate ? AGGREGATORS[aggregate]!(values) : values[0]!

  const colorDomain = colorField ? sortedDomain(rows.map(row => String(row[colorField])), encoding.color?.sort) : []
  const colorScale = colorField ? categoricalColorScale(colorDomain, theme.palette) : undefined
  const grouped = Boolean(offsetField)

  // One bar per (x, color) combination when color is present, else one bar per x category.
  interface Bar { category: string; color?: string; value: number; base: number }
  const bars: Bar[] = []
  if (!colorField) {
    for (const category of categories) {
      const values = rows.filter(row => String(row[xField]) === category).map(row => Number(row[yField]))
      bars.push({category, value: reduce(values), base: 0})
    }
  } else if (grouped) {
    for (const category of categories) {
      for (const color of colorDomain) {
        const values = rows.filter(row => String(row[xField]) === category && String(row[colorField]) === color).map(row => Number(row[yField]))
        if (values.length) bars.push({category, color, value: reduce(values), base: 0})
      }
    }
  } else {
    // Stacked: Vega-Lite's default stack order places the *last* color-domain value touching
    // zero and the first on top — confirmed against real Vega output, not assumed.
    for (const category of categories) {
      let base = 0
      for (const color of [...colorDomain].reverse()) {
        const values = rows.filter(row => String(row[xField]) === category && String(row[colorField]) === color).map(row => Number(row[yField]))
        if (!values.length) continue
        const value = reduce(values)
        bars.push({category, color, value: base + value, base})
        base += value
      }
    }
  }

  const stackTotals = colorField && !grouped
    ? categories.map(category => bars.filter(bar => bar.category === category).reduce((max, bar) => Math.max(max, bar.value), 0))
    : bars.map(bar => bar.value)
  const [dataMin, dataMax] = [Math.min(0, ...stackTotals), Math.max(0, ...stackTotals)]
  const niced = niceLinearDomain([dataMin, dataMax])
  const legendReserve = colorField ? legendWidth(colorDomain, encoding.color?.field, theme) : 0
  const plotWidth = width - legendReserve
  // BLG-467 review: the margin must be sized from the same tick count the axis actually draws
  // (densityTickCount, driven by the real plot height) — not a flat default-10 guess, which left
  // a taller chart's own (denser, wider) labels clipped at the left edge. The plot height itself
  // doesn't depend on the y labels (plotLayout's own bottom margin is fixed), so it's measured
  // first, with no labels, purely to get that height.
  const yTickCount = axisTickCount('y', densityTickCount(plotLayout(plotWidth, height, [], theme).height))
  const yTickValues = continuousTicks(niced, yTickCount)
  const yTickLabels = formatTicks(yTickValues)
  const plot = plotLayout(plotWidth, height, yTickLabels, theme)
  const xScale = bandScale(categories, [plot.x, plot.x + plot.width], 0.1, 0.05)
  const yScale = linearScale(niced, [plot.y + plot.height, plot.y])
  const offsetScale = grouped ? bandScale(colorDomain, [0, xScale.bandwidth], 0, 0) : undefined

  const geometry = bars.map(bar => {
    const groupX = xScale(bar.category) ?? 0
    const x = grouped && bar.color !== undefined ? groupX + (offsetScale!(bar.color) ?? 0) : groupX
    const barWidth = grouped ? offsetScale!.bandwidth : xScale.bandwidth
    const top = yScale(Math.max(bar.base, bar.value))
    const barHeight = Math.abs(yScale(bar.value) - yScale(bar.base))
    return {...bar, x, y: top, width: barWidth, height: barHeight}
  })

  // Review finding (task 2, item 1): a literal /mark/color only overrides the default fill when
  // there's no data-driven color encoding — Vega-Lite's own per-category scale still wins when
  // one is present, confirmed empirically. /mark/opacity is a single generic SVG `opacity`
  // attribute (not fill-opacity), applied either way.
  const {color: markColor, opacity: markOpacity} = markStyle(mark)
  const defaultFill = markColor ?? theme.accent
  const keys = new Map<string, string[]>(), declaredKeys = new Set<string>()
  for (const row of rows) {
    const category = JSON.stringify([String(row[xField]), colorField ? String(row[colorField]) : null])
    if (row.id != null || row.key != null) declaredKeys.add(category)
    const group = keys.get(category) ?? [];group.push(datumKey(row,xField,colorField));keys.set(category,group)
  }
  const markKey = (bar: {category:string; color?:string}) => {
    const category=JSON.stringify([bar.category,bar.color ?? null]), matches=keys.get(category)
    return !aggregate && matches?.length===1 && declaredKeys.has(category) ? matches[0] : 'aggregate:'+datumKey({id:category})
  }
  const shapes = geometry.map(bar => rect(bar.x, bar.y, bar.width, bar.height, {'data-entrance':'bar', 'data-baseline':yScale(bar.base), 'data-datum-key':markKey(bar),'data-scene-key':markKey(bar), fill: bar.color !== undefined ? colorScale!(bar.color) : defaultFill, opacity: markOpacity}))
  const grid = group((axisOptions('y').grid===false ? [] : yTickValues).map(value => line(plot.x, yScale(value), plot.x + plot.width, yScale(value), {stroke: theme.border, 'stroke-opacity': 0.35, 'data-grid':'y'})))
  const axes = group([drawBandAxisBottom(xScale, plot, theme), drawLinearAxisLeft(yScale, niced, plot, theme, yTickCount)], {'data-scene-key': 'axes'})
  const legend = colorField ? drawCategoricalLegend(colorDomain, colorScale!, plot.x + plot.width + 12, plot.y, theme, encoding.color?.field) : ''

  const accessiblePoints = geometry.map(bar => ({key: markKey(bar), label: bar.color !== undefined ? `${bar.category}, ${bar.color}` : bar.category, value: String(bar.value - bar.base)}))
  const tooltipRegions = geometry.map(bar => ({key: markKey(bar),
    x1: bar.x, y1: bar.y, x2: bar.x + bar.width, y2: bar.y + bar.height,
    text: tooltipRegionText(encoding.tooltip, colorField ? {[xField]: bar.category, [colorField]: bar.color, [yField]: bar.value - bar.base} : {[xField]: bar.category, [yField]: bar.value}),
  }))
  const points = geometry.map(bar => ({category: bar.color !== undefined ? `${bar.category}, ${bar.color}` : bar.category, value: bar.value - bar.base, x: bar.x, y: bar.y, width: bar.width, height: bar.height}))
  return {svg: group([grid, group(shapes, {'data-scene-key': 'marks'}), axes, legend]), points, accessiblePoints, tooltipRegions}
}
