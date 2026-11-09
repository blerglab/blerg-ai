import {axisOptions, axisTickCount} from '../axisOptions'
import {datumKey} from '../dataIdentity'
// BLG-452 design doc §3.3 table row 3/mark type: line and area. v1 scope: continuous x and
// continuous y, points connected in x order. With a color channel: multi-series line draws one
// path per series, overlaid, not stacked (confirmed against real Vega — a line mark never gets
// the default stack transform); multi-series area stacks by default, same stack-order rule as
// bar's (the last, reverse-sorted color-domain value touches the zero baseline), also confirmed
// against real Vega output, not assumed. No bin — NotShippedYet, same discipline as other marks.
import { linearScale, niceLinearDomain } from '../scales'
import { continuousTicks, densityTickCount, densityTickCountIsStable, drawLinearAxisBottom, drawLinearAxisLeft, formatTicks, preciseDensityTickCount } from '../axes'
import { plotLayoutContinuousX } from '../layout'
import { categoricalColorScale, isImplementedSort, markStyle, sortedDomain, type EncodingSort } from '../color'
import { legendWidth, drawCategoricalLegend } from '../legend'
import { path, group, line as gridline } from '../svg'
import { tooltipRegionText, type TooltipFieldDef, type TooltipRegion } from '../tooltip'
import type { MarkDeclaration } from '../declare'
import type { VisualTheme } from '../../theme'

export class NotShippedYet extends Error {}

const lineDeclaration = (mark: 'line' | 'area'): MarkDeclaration => ({
  mark,
  channels: {
    x: {types: new Set(['quantitative'])},
    y: {types: new Set(['quantitative']), ...(mark === 'area' ? {stack: new Set(['zero'])} : {})},
    color: {types: new Set(['nominal', 'ordinal']), sort: true},
    tooltip: {types: new Set(['quantitative', 'nominal', 'ordinal', 'temporal'])},
  },
  markProperties: new Set(['color', 'opacity']),
})
export const declarations: MarkDeclaration[] = [lineDeclaration('line'), lineDeclaration('area')]

interface FieldDef { field?: string; type?: string; sort?: EncodingSort; stack?: unknown; bin?: unknown; axis?: {title?: unknown} }
interface LineEncoding { x?: FieldDef; y?: FieldDef; color?: FieldDef; size?: FieldDef; tooltip?: TooltipFieldDef | TooltipFieldDef[] }
interface LineMark { color?: unknown; opacity?: unknown }

const STACK_DEFAULTS = new Set<unknown>([undefined, true, 'zero'])

const HIT_RADIUS = 8

export interface LineDrawing { svg: string; accessiblePoints: Array<{label: string; value: string}>; tooltipRegions: TooltipRegion[] }

/** Keep both extrema of each x-ordered bucket plus endpoints. Interaction and the data
 * table retain every original observation; only the painted path is bounded. */
export function decimateLine<T extends {y:number;base?:number}>(points: readonly T[], limit:number): readonly T[] {
 if(points.length<=limit)return points
 const baseline=points.some(point=>point.base!==points[0].base)
 const buckets=Math.max(1,Math.floor((limit-2)/(baseline?4:2))),size=(points.length-2)/buckets
 const selected=[points[0]]
 for(let bucket=0;bucket<buckets;bucket++){
  const lo=1+Math.floor(bucket*size),hi=Math.min(points.length-1,1+Math.floor((bucket+1)*size))
  let min=lo,max=lo,minBase=lo,maxBase=lo
  for(let i=lo+1;i<hi;i++){
   if(points[i].y<points[min].y)min=i
   if(points[i].y>points[max].y)max=i
   if((points[i].base??0)<(points[minBase].base??0))minBase=i
   if((points[i].base??0)>(points[maxBase].base??0))maxBase=i
  }
  for(const index of [...new Set(baseline?[min,max,minBase,maxBase]:[min,max])].sort((a,b)=>a-b))selected.push(points[index])
 }
 selected.push(points[points.length-1]);return selected
}

export function drawLine(markType: 'line' | 'area', rows: Array<Record<string, unknown>>, encoding: LineEncoding, mark: LineMark, width: number, height: number, theme: VisualTheme, mountedDataUpdate = false): LineDrawing {
  if (encoding.size) throw new NotShippedYet(`${markType}: size is not implemented yet`)
  const xField = encoding.x?.field, yField = encoding.y?.field
  if (typeof xField !== 'string' || typeof yField !== 'string') throw new NotShippedYet(`${markType}: missing field`)
  if (encoding.x?.type !== 'quantitative') throw new NotShippedYet(`${markType}: only a quantitative x is implemented yet`)
  if (encoding.x?.bin || encoding.y?.bin) throw new NotShippedYet(`${markType}: bin is not implemented yet`)
  // Review finding, extended (custom sort): the color-series domain was always sorted ascending
  // alphabetically (sortedDomain), ignoring an explicit encoding.color.sort — ascending,
  // descending, null and an explicit array are now implemented (sortedDomain's own header); a
  // sort-by-field object still falls back.
  if (!isImplementedSort(encoding.color?.sort)) throw new NotShippedYet(`${markType}: this sort is not implemented yet`)
  // Review finding: area with color always stacked as the "zero" default regardless of an
  // explicit encoding.y.stack ("normalize"/"center"/false, none implemented, would silently draw
  // zero-stacked). Area's default when color is present IS stacking, so this only has teeth when
  // a non-default stack is explicitly requested.
  if (markType === 'area' && !STACK_DEFAULTS.has(encoding.y?.stack)) throw new NotShippedYet('area: only the default (zero) stack is implemented yet')
  const colorField = encoding.color?.field

  const colorDomain = colorField ? sortedDomain(rows.map(row => String(row[colorField])), encoding.color?.sort) : []
  const colorScale = colorField ? categoricalColorScale(colorDomain, theme.palette) : undefined
  const legendReserve = colorField ? legendWidth(colorDomain, encoding.color?.field, theme) : 0

  const xValues = [...new Set(rows.map(row => Number(row[xField])))].sort((a, b) => a - b)
  const xDomain = niceLinearDomain([Math.min(...xValues), Math.max(...xValues)])

  // One series (array of {x, y, row} sorted by x) per color-domain value, or a single series when
  // there's no color encoding.
  interface Point { x: number; y: number; base: number; row: Record<string, unknown> }
  const series: Array<{key?: string; points: Point[]}> = []
  if (!colorField) {
    const points = rows.map(row => ({x: Number(row[xField]), y: Number(row[yField]), base: 0, row})).sort((a, b) => a.x - b.x)
    series.push({points})
  } else if (markType === 'line') {
    // Overlaid, not stacked — confirmed against real Vega (a line mark never gets the default
    // stack transform, unlike bar/area).
    for (const key of colorDomain) {
      const points = rows.filter(row => String(row[colorField]) === key)
        .map(row => ({x: Number(row[xField]), y: Number(row[yField]), base: 0, row})).sort((a, b) => a.x - b.x)
      series.push({key, points})
    }
  } else {
    // Area stacks by default, same stack-order rule as bar's: the *last* (reverse-sorted)
    // color-domain value touches the zero baseline, confirmed against real Vega's own y/y2.
    const bases = new Map<number, number>(xValues.map(x => [x, 0]))
    for (const key of [...colorDomain].reverse()) {
      const points: Point[] = []
      for (const x of xValues) {
        const matches = rows.filter(row => String(row[colorField]) === key && Number(row[xField]) === x)
        if (!matches.length) continue
        const value = Number(matches[0][yField])
        const base = bases.get(x) ?? 0
        points.push({x, y: base + value, base, row: matches[0]})
        bases.set(x, base + value)
      }
      series.push({key, points})
    }
  }

  const allTops = series.flatMap(s => s.points.map(p => p.y))
  const yDomain = niceLinearDomain([Math.min(0, ...allTops), Math.max(0, ...allTops)])
  // BLG-467 review: margin sized from the real drawn tick count, not a flat default — see
  // marks/bar.ts's own comment on this same fix.
  const yTickCount = axisTickCount('y', densityTickCount(plotLayoutContinuousX(width - legendReserve, height, [], theme, encoding.x, encoding.y).height))
  const yTickValues = continuousTicks(yDomain, yTickCount)
  const yTickLabels = formatTicks(yTickValues)
  const plot = plotLayoutContinuousX(width - legendReserve, height, yTickLabels, theme, encoding.x, encoding.y)
  // Lines follow the measured pane even beside a nice-step boundary. Keep area's
  // existing margin-parity admission unchanged; native line layout must not depend on it.
  if(markType==='area' && !mountedDataUpdate && !densityTickCountIsStable(plot.width,xDomain,preciseDensityTickCount))throw new NotShippedYet('area: x-axis tick density is too close to a nice-step boundary')
  const xScale = linearScale(xDomain, [plot.x, plot.x + plot.width])
  const yScale = linearScale(yDomain, [plot.y + plot.height, plot.y])

  // Task 2, item 1: a literal /mark/color only overrides the default when there's no data-driven
  // color encoding — Vega-Lite's own per-series scale still wins when one is present, same rule
  // as bar's. /mark/color overrides *stroke* for line, *fill* for area (both confirmed
  // empirically); /mark/opacity is a single generic SVG `opacity` attribute, applied either way.
  const {color: markColor, opacity: markOpacity} = markStyle(mark)
  const marks = series.map(s => {
    const painted=decimateLine(s.points,Math.max(32,Math.floor(Math.min(1024,plot.width*2)/Math.max(1,series.length))))
    const pixels = painted.map(p => ({x: xScale(p.x), y: yScale(p.y), base: yScale(p.base)}))
    const seriesColor = s.key !== undefined ? colorScale!(s.key) : (markColor ?? theme.accent)
    if (markType === 'area') {
      const top = pixels.map(p => `${p.x},${p.y}`).join(' L ')
      const bottom = [...pixels].reverse().map(p => `${p.x},${p.base}`).join(' L ')
      const d = `M ${top} L ${bottom} Z`
      return {svg: path({d, 'data-entrance':markType, 'data-scene-key': 'series:'+datumKey({id:s.key ?? ''}), 'data-datum-key': 'series:'+datumKey({id:s.key ?? ''}), 'data-vertex-keys':JSON.stringify([...painted.map(p=>datumKey(p.row,xField,colorField)+':top'), ...[...painted].reverse().map(p=>datumKey(p.row,xField,colorField)+':base')]), fill: seriesColor, 'fill-opacity': markOpacity === undefined ? 0.5 : undefined, opacity: markOpacity, stroke: 'none'}), pixels, points: s.points, markKey:'series:'+datumKey({id:s.key ?? ''})}
    }
    const d = 'M ' + pixels.map(p => `${p.x},${p.y}`).join(' L ')
    return {svg: path({d, 'data-entrance':markType, 'data-scene-key': 'series:'+datumKey({id:s.key ?? ''}), 'data-datum-key': 'series:'+datumKey({id:s.key ?? ''}), 'data-vertex-keys':JSON.stringify(painted.map(p=>datumKey(p.row,xField,colorField))), fill: 'none', stroke: seriesColor, 'stroke-width': 2, opacity: markOpacity}), pixels, points: s.points, markKey:'series:'+datumKey({id:s.key ?? ''})}
  })

  const grid = group((axisOptions('y').grid===false ? [] : yTickValues).map(value => gridline(plot.x, yScale(value), plot.x + plot.width, yScale(value), {stroke: theme.border, 'stroke-opacity': 0.35, 'data-grid':'y'})))
  const axes = group([drawLinearAxisBottom(xScale, xDomain, plot, theme, preciseDensityTickCount(plot.width)), drawLinearAxisLeft(yScale, yDomain, plot, theme, yTickCount)], {'data-scene-key': 'axes'})
  // Review finding: confirmed empirically — a line-chart legend's own symbol is a short stroke
  // (shape:"stroke", not a filled square); area's own is a filled circle (a smaller, documented
  // v1 gap — not the swatch vs. no-swatch mismatch the stroke case is, so left as the existing
  // square swatch for now).
  const legend = colorField ? drawCategoricalLegend(colorDomain, colorScale!, plot.x + plot.width + 12, plot.y, theme, encoding.color?.field, markType === 'line' ? 'stroke' : 'swatch') : ''

  const accessiblePoints = marks.flatMap(m => m.points.map(p => ({key:datumKey(p.row,xField,colorField),label: String(p.x), value: String(p.y - p.base)})))
  // No mark is drawn per point (one <path> per series), so hover/touch regions are small
  // invisible boxes at each original data point's pixel position, not interpolated along the
  // path — matching the common "hover near a point to see its value" expectation.
  const tooltipRegions = marks.flatMap(m => m.points.map((point, i) => ({x:xScale(point.x),y:yScale(point.y),i})).map((pixel) => ({markKey:m.markKey,key:datumKey(m.points[pixel.i].row,xField,colorField),
    x1: pixel.x - HIT_RADIUS, y1: pixel.y - HIT_RADIUS, x2: pixel.x + HIT_RADIUS, y2: pixel.y + HIT_RADIUS,
    text: tooltipRegionText(encoding.tooltip, m.points[pixel.i].row),
  })))
  return {svg: group([grid, group(marks.map(m => m.svg), {'data-scene-key': 'marks'}), axes, legend]), accessiblePoints, tooltipRegions}
}
