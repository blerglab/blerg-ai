import {drawContinuousLine,type ContinuousEncoding} from './continuousLine'
import {usesContinuousLine,continuousGuides,isFieldRuleLayer} from './continuousSpec'
import {axisOptions, axisTickCount} from './axisOptions'
import {withAxisTitles} from './axisTitles'
import {nativeDataMark as liveMark} from './liveDataSupport'
import {MAX_CHART_AREA,MAX_CHART_SIDE} from '../vegaBudget'
import {datumKey, sameDataStructure} from './dataIdentity'
import {animateGeometry, captureGeometry} from './dataMotion'
import {categoricalLegendHeight} from './legend'
// BLG-452 design doc §3.2/§0: draws a spec classify.ts has accepted. v1 runs synchronously on
// the main thread for every spec (§3.7's static size threshold/worker split is the dense-data
// gate step, not yet built) — classify.ts's own structure keeps specs small enough that this is
// safe for now; the worker split is added without changing this function's contract.
//
// Composition (BLG-452 step 4): concat/hconcat/vconcat/facet/repeat are implemented — each child
// panel gets its own sub-rectangle (its own declared width/height if it has one, else an even
// share of the parent's) and is drawn independently, positioned with an SVG <g transform=
// "translate(...)">. layer (BLG-452 priority 3): v1 scope is exactly the two "common" overlays
// the design doc names — line/area with a single-datum rule annotation, and bar with a text
// label — each built as its own dedicated shared-scale drawing path below (drawLayeredLineRule/
// drawLayeredBarText), not by calling the standalone mark files (which would each derive and draw
// their own independent scale/axes, double-drawing the chrome). Any other mark combination is
// still a real v1 gap: Vega-Lite's general layer model shares one scale across arbitrary
// children, which every mark file would need to accept as an externally computed domain rather
// than deriving its own — a refactor across all nine marks, not yet done. Those fall back to
// Vega, never drawn wrong.
import { wrapMeasured } from '../textLayout'
import { measureText } from './layout'
import {recordPlotBounds} from './svg'
import { commitScene } from './scene'
import {drawHistogram} from './marks/histogram'
import { drawBar, NotShippedYet } from './marks/bar'
import { drawPoint } from './marks/point'
import { drawText } from './marks/text'
import { drawLine } from './marks/line'
import { drawRule } from './marks/rule'
import {drawStatusTimeline} from './marks/timeline'
import { drawRectHeatmap } from './marks/rect'
import { drawArc } from './marks/arc'
import { drawBoxplot } from './marks/boxplot'
import { bandScale, linearScale, niceLinearDomain } from './scales'
import { continuousTicks, densityTickCount, drawBandAxisBottom, drawLinearAxisBottom, drawLinearAxisLeft, formatTicks } from './axes'
import { plotLayout } from './layout'
import { withSeriesDomain, isImplementedSort, sortedDomain, type EncodingSort } from './color'
import { prepareSvgScene, path, svgDocument, group, line as svgLine, rect as svgRect, text as svgText } from './svg'
import { parseVegaLite, validateSvgElement } from '../policy'
import { buildAccessibleExtras, type DrawnPoint } from './accessibility'
import { attachTooltip, tooltipRegionText, type TooltipFieldDef, type TooltipRegion } from './tooltip'
import type { VisualTheme } from '../theme'
import type { RenderedVisual } from '../render'

interface FieldDef { axis?:import('./axisOptions').AxisOptions; field?: string; aggregate?: string; type?: string; datum?: unknown; value?:unknown; sort?: EncodingSort }
interface ColorValueDef { value?: string }
interface Encoding { x?: FieldDef; x2?: FieldDef; y?: FieldDef; y2?: FieldDef; color?: FieldDef & ColorValueDef; text?: FieldDef; theta?: FieldDef; radius?: FieldDef; tooltip?: TooltipFieldDef | TooltipFieldDef[] }
interface DrawnBody { svg: string; accessiblePoints: DrawnPoint[]; tooltipRegions: TooltipRegion[] }

function translateRegions(regions: readonly TooltipRegion[], dx: number, dy: number): TooltipRegion[] {
  return regions.map(region => ({...region, x1: region.x1 + dx, y1: region.y1 + dy, x2: region.x2 + dx, y2: region.y2 + dy, text: region.text}))
}

const isObject = (value: unknown): value is Record<string, unknown> => !!value && typeof value === 'object' && !Array.isArray(value)

/** Review finding: a concat/hconcat/vconcat child with no `data` of its own silently drew empty
 *  (resolveRows found nothing) instead of its parent's data — Vega-Lite's own inheritance rule
 *  (vegaPolicy.ts's checkSpec comment: "its own inline data or named dataset, else its parent's")
 *  applies to every composition type, not just layer (only *encoding* inheritance is layer-
 *  specific). A child keeps its own data/datasets when it has them; only a child with neither
 *  inherits the parent's. */
function inheritData(child: Record<string, unknown>, parent: Record<string, unknown>): Record<string, unknown> {
  if ('data' in child) return child
  const inherited: Record<string, unknown> = {...child, data: parent.data}
  if (!('datasets' in child) && 'datasets' in parent) inherited.datasets = parent.datasets
  return inherited
}

function resolveRows(spec: Record<string, unknown>): Array<Record<string, unknown>> {
  const data = spec.data as {values?: unknown; name?: string} | undefined
  if (data?.values && Array.isArray(data.values)) return data.values as Array<Record<string, unknown>>
  const datasets = spec.datasets as Record<string, unknown> | undefined
  if (data?.name && datasets && Array.isArray(datasets[data.name])) return datasets[data.name] as Array<Record<string, unknown>>
  return []
}

function primaryRows(spec:Record<string,unknown>):Record<string,unknown>[] {
 const primary=Array.isArray(spec.layer) ? (spec.layer as Record<string,unknown>[]).find(unit=>['line','area'].includes(markTypeOf(unit.mark) ?? '')) : undefined
 return resolveRows(primary ? inheritData(primary,spec) : spec)
}

function drawUnitBody(spec: Record<string,unknown>,rows:Record<string,unknown>[],encoding:Encoding,width:number,height:number,theme:VisualTheme,mountedDataUpdate=false):DrawnBody {
 const guides=markTypeOf(spec.mark)==='line' && usesContinuousLine(encoding as Record<string,unknown>) ? continuousGuides(encoding) : encoding
 return withAxisTitles(guides,width,height,theme,(clean,w,h)=>drawRawUnitBody(spec,rows,clean,w,h,theme,mountedDataUpdate))
}

function drawRawUnitBody(spec: Record<string, unknown>, rows: Array<Record<string, unknown>>, encoding: Encoding, width: number, height: number, theme: VisualTheme, mountedDataUpdate = false): DrawnBody {
  const mark = spec.mark
  const markType = typeof mark === 'string' ? mark : (mark as {type?: string})?.type
  const markObject = isObject(mark) ? mark : {}
  if(markType==='line' && usesContinuousLine(encoding as Record<string,unknown>))return drawContinuousLine(rows,encoding,markObject,width,height,theme)
  if (markType === 'bar' && encoding.x2) return drawHistogram(rows, encoding, markObject, width, height, theme)
  if (markType === 'bar') return drawBar(rows, encoding, markObject, width, height, theme)
  if (markType === 'point' || markType === 'circle' || markType === 'square') return drawPoint(markType, rows, encoding, markObject, width, height, theme)
  if (markType === 'text') return drawText(rows, encoding, markObject, width, height, theme)
  if (markType === 'line' || markType === 'area') return drawLine(markType, rows, encoding, markObject, width, height, theme, mountedDataUpdate)
  if (markType === 'rule') return drawRule(rows, encoding, markObject, width, height, theme)
  if (markType === 'rect' && encoding.x2) return drawStatusTimeline(rows, encoding, width, height, theme)
  if (markType === 'rect') return drawRectHeatmap(rows, encoding, width, height, theme)
  if (markType === 'arc') return drawArc(rows, encoding, markObject as {innerRadius?: number}, width, height, theme)
  if (markType === 'boxplot') return drawBoxplot(rows, encoding, markObject, width, height, theme)
  throw new NotShippedYet(`mark "${String(markType)}" is not drawn yet`)
}

/** A {"repeat": "row"|"column"|"layer"|"repeat"} field placeholder (table row 18), replaced with
 *  a literal field name for one repeated panel. */
function substituteRepeat(encoding: unknown, key: string, fieldName: string): Encoding {
  if (!isObject(encoding)) return {}
  const next: Record<string, unknown> = {}
  for (const [channel, def] of Object.entries(encoding)) {
    const field = isObject(def) ? def.field : undefined
    next[channel] = isObject(def) && isObject(field) && field.repeat === key ? {...def, field: fieldName} : def
  }
  return next as Encoding
}

function panelSize(child: Record<string, unknown>, fallbackWidth: number, fallbackHeight: number): [number, number] {
  return [Number(child.width) || fallbackWidth, Number(child.height) || fallbackHeight]
}

function markTypeOf(mark: unknown): string | undefined {
  return typeof mark === 'string' ? mark : isObject(mark) ? (mark.type as string | undefined) : undefined
}

/** BLG-452 priority 3: bar + text label overlay, one of the two supported layer combos. Shares
 *  one band/linear scale (derived from the bar's own x/y fields) and draws the shared grid/axes
 *  once; no color/xOffset/aggregate in this v1 scope (NotShippedYet) — the common "value labels
 *  on top of a plain bar chart" case named in the design doc, not the full grouped/stacked bar
 *  grammar layered with text. */
function drawLayeredBarText(marks: (string | undefined)[], encodings: Encoding[], rows: Array<Record<string, unknown>>, width: number, height: number, theme: VisualTheme): DrawnBody {
  const barIndex = marks.indexOf('bar'), textIndex = marks.indexOf('text')
  const barEncoding = encodings[barIndex], textEncoding = encodings[textIndex]
  if (isObject(barEncoding.color)) throw new NotShippedYet('layer bar+text: bar color is not implemented yet')
  const xField = barEncoding.x?.field, yField = barEncoding.y?.field, textField = textEncoding.text?.field
  if (typeof xField !== 'string' || typeof yField !== 'string' || typeof textField !== 'string') throw new NotShippedYet('layer bar+text: missing field')
  if (barEncoding.y?.aggregate) throw new NotShippedYet('layer bar+text: an aggregated bar y is not implemented yet')
  // Review finding, extended (custom sort): same sort gap bar.ts's own guard closes — now
  // implements the same forms.
  if (!isImplementedSort(barEncoding.x?.sort)) throw new NotShippedYet('layer bar+text: this sort is not implemented yet')

  const categories = sortedDomain(rows.map(row => String(row[xField])), barEncoding.x?.sort)
  const bars = categories.map(category => rows.find(row => String(row[xField]) === category)!)
  const values = bars.map(row => Number(row[yField]))
  const niced = niceLinearDomain([Math.min(0, ...values), Math.max(0, ...values)])
  // BLG-467 review: margin sized from the real drawn tick count, not a flat default — see
  // marks/bar.ts's own comment on this same fix.
  const yTickCount = axisTickCount('y', densityTickCount(plotLayout(width, height, [], theme).height))
  const yTickValues = continuousTicks(niced, yTickCount)
  const yTickLabels = formatTicks(yTickValues)
  const plot = plotLayout(width, height, yTickLabels, theme)
  const xScale = bandScale(categories, [plot.x, plot.x + plot.width], 0.1, 0.05)
  const yScale = linearScale(niced, [plot.y + plot.height, plot.y])

  const geometry = bars.map((row, i) => {
    const x = xScale(categories[i]) ?? 0
    const top = yScale(Math.max(0, values[i])), base = yScale(Math.min(0, values[i]))
    return {row, x, top: Math.min(top, base), height: Math.abs(base - top), label: String(row[textField])}
  })
  const barShapes = geometry.map(g => svgRect(g.x, g.top, xScale.bandwidth, g.height, {fill: theme.accent, 'data-entrance':'bar', 'data-datum-key':datumKey(g.row,xField), 'data-baseline':yScale(0)}))
  const labels = geometry.map(g => svgText(g.x + xScale.bandwidth / 2, g.top, g.label, {
    fill: theme.fg, 'text-anchor': 'middle', 'font-size': theme.fontSize, 'font-family': theme.fontFamily, 'data-chart-label': 'true',
  }))

  const grid = group((axisOptions('y').grid===false ? [] : yTickValues).map(value => svgLine(plot.x, yScale(value), plot.x + plot.width, yScale(value), {stroke: theme.border, 'stroke-opacity': 0.35, 'data-grid':'y'})))
  const axes = group([drawBandAxisBottom(xScale, plot, theme), drawLinearAxisLeft(yScale, niced, plot, theme, yTickCount)])
  const accessiblePoints = categories.map((category, i) => ({label: category, value: String(values[i])}))
  const tooltipRegions = geometry.map(g => ({key:datumKey(g.row,xField),
    x1: g.x, y1: g.top, x2: g.x + xScale.bandwidth, y2: g.top + g.height,
    text: tooltipRegionText(barEncoding.tooltip, g.row),
  }))
  return {svg: group([grid, group(barShapes), group(labels), axes]), accessiblePoints, tooltipRegions}
}

/** BLG-452 priority 3: line/area + a single-datum rule annotation, the other supported layer
 *  combo. Shares one linear scale (derived from the line's own x/y fields, extended to include
 *  the rule's datum value); no color/multi-series line in this v1 scope (NotShippedYet) — the
 *  common "threshold line over a single-series line chart" case named in the design doc. */
function drawLayeredLineRule(marks: (string | undefined)[], rawMarks: unknown[], encodings: Encoding[], rows: Array<Record<string, unknown>>, width: number, height: number, theme: VisualTheme): DrawnBody {
  const lineIndex = marks.findIndex(m => m === 'line' || m === 'area'), ruleIndex = marks.indexOf('rule')
  const lineMarkType = marks[lineIndex] as 'line' | 'area'
  const lineEncoding = encodings[lineIndex], ruleEncoding = encodings[ruleIndex]
  if(lineMarkType==='line' && usesContinuousLine(lineEncoding as Record<string,unknown>)){
   const yd=ruleEncoding.y?.datum,xd=ruleEncoding.x?.datum,axis=typeof yd==='number' && !ruleEncoding.x ? 'y' : typeof xd==='number' && !ruleEncoding.y ? 'x' : undefined
   if(!axis)throw new NotShippedYet('line rule: expected one finite datum')
   const value=(axis==='y' ? yd : xd) as number
   const labelIndex=marks.indexOf('text'),labelEncoding=encodings[labelIndex]
   if(labelEncoding && (axis==='y' ? labelEncoding.y?.datum!==value || labelEncoding.x : labelEncoding.x?.datum!==value || labelEncoding.y))throw new NotShippedYet('line rule: label must share the reference datum')
   const labelMark=isObject(rawMarks[labelIndex]) ? rawMarks[labelIndex] as Record<string,unknown> : {}
   return drawContinuousLine(rows,lineEncoding,isObject(rawMarks[lineIndex]) ? rawMarks[lineIndex] as Record<string,unknown> : {},width,height,theme,undefined,{axis,value,label:typeof labelEncoding?.text?.value==='string' ? labelEncoding.text.value : undefined,dx:Number(labelMark.dx ?? 0),dy:Number(labelMark.dy ?? 0)})
  }
  if (isObject(lineEncoding.color)) throw new NotShippedYet('layer line/area+rule: multi-series color is not implemented yet')
  // A line's own point overlay (mark: {type: 'line', point: true}) isn't drawn by this layer
  // path (it reuses line.ts's own non-overlay drawing, not point.ts's dot-drawing) — falling
  // back rather than silently dropping the overlay Vega would have drawn.
  const lineMark = rawMarks[lineIndex]
  if (isObject(lineMark) && lineMark.point) throw new NotShippedYet('layer line/area+rule: a line point overlay is not implemented yet')
  const xField = lineEncoding.x?.field, yField = lineEncoding.y?.field
  if (typeof xField !== 'string' || typeof yField !== 'string') throw new NotShippedYet('layer line/area+rule: missing field')
  const yDatum = ruleEncoding.y?.datum, xDatum = ruleEncoding.x?.datum
  const horizontal = typeof yDatum === 'number' && Number.isFinite(yDatum) && !ruleEncoding.x
  const vertical = typeof xDatum === 'number' && Number.isFinite(xDatum) && !ruleEncoding.y
  if (!horizontal && !vertical) throw new NotShippedYet('layer line/area+rule: expected one finite x or y datum')
  const reference = (horizontal ? yDatum : xDatum) as number
  if(ruleEncoding.text)throw new NotShippedYet('layer rule: labels use a text layer')

  const points = rows.map(row => ({x: Number(row[xField]), y: Number(row[yField]), row})).sort((a, b) => a.x - b.x)
  const xValues = points.map(p => p.x), yValues = points.map(p => p.y)
  const xDomain = niceLinearDomain([Math.min(0, ...xValues, ...(vertical ? [reference] : [])), Math.max(0, ...xValues, ...(vertical ? [reference] : []))])
  const yDomain = niceLinearDomain([Math.min(0, ...yValues, ...(horizontal ? [reference] : [])), Math.max(0, ...yValues, ...(horizontal ? [reference] : []))])
  // BLG-467 review: margin sized from the real drawn tick count, not a flat default — see
  // marks/bar.ts's own comment on this same fix.
  const yTickCount = axisTickCount('y', densityTickCount(plotLayout(width, height, [], theme).height))
  const yTickValues = continuousTicks(yDomain, yTickCount)
  const yTickLabels = formatTicks(yTickValues)
  const plot = plotLayout(width, height, yTickLabels, theme)
  const xScale = linearScale(xDomain, [plot.x, plot.x + plot.width])
  const yScale = linearScale(yDomain, [plot.y + plot.height, plot.y])
  const zero = yScale(0)

  const pixels = points.map(p => ({x: xScale(p.x), y: yScale(p.y)}))
  let lineShape: string
  if (lineMarkType === 'area') {
    const top = pixels.map(p => `${p.x},${p.y}`).join(' L ')
    const d = `M ${pixels[0].x},${zero} L ${top} L ${pixels[pixels.length - 1].x},${zero} Z`
    lineShape = path({d, 'data-entrance':lineMarkType, 'data-datum-key':'series:single', 'data-scene-key':'series:single', 'data-vertex-keys':JSON.stringify([datumKey(points[0].row,xField)+':base',...points.map(p=>datumKey(p.row,xField)),datumKey(points.at(-1)!.row,xField)+':base']), fill: theme.accent, 'fill-opacity': 0.5, stroke: 'none'})
  } else {
    const d = 'M ' + pixels.map(p => `${p.x},${p.y}`).join(' L ')
    lineShape = path({d, 'data-entrance':lineMarkType, 'data-datum-key':'series:single', 'data-scene-key':'series:single', 'data-vertex-keys':JSON.stringify(points.map(p=>datumKey(p.row,xField))), fill: 'none', stroke: theme.accent, 'stroke-width': 2})
  }
  const ruleAttrs={stroke:theme.muted,'data-reference-rule':horizontal?'y':'x','data-scene-key':'reference-rule'}
  const ruleShape = horizontal
    ? svgLine(plot.x,yScale(reference),plot.x+plot.width,yScale(reference),ruleAttrs)
    : svgLine(xScale(reference),plot.y,xScale(reference),plot.y+plot.height,ruleAttrs)
  const labelIndex=marks.indexOf('text'),labelEncoding=encodings[labelIndex]
  const labelMark=isObject(rawMarks[labelIndex]) ? rawMarks[labelIndex] as Record<string,unknown> : {}
  const dx=Number(labelMark.dx ?? 0),dy=Number(labelMark.dy ?? 0)
  if(labelEncoding && (horizontal ? labelEncoding.y?.datum!==reference || labelEncoding.x : labelEncoding.x?.datum!==reference || labelEncoding.y))throw new NotShippedYet('layer rule: label must share the reference datum')
  const label=typeof labelEncoding?.text?.value==='string' ? labelEncoding.text.value : ''
  const labelShape=label ? svgText((horizontal ? plot.x+plot.width/2 : xScale(reference))+dx, (horizontal ? yScale(reference) : plot.y+plot.height/2)+dy,label,{fill:theme.muted,'font-size':theme.fontSize,'font-family':theme.fontFamily,'text-anchor':'middle','dominant-baseline':'central','data-reference-label':horizontal?'y':'x','data-scene-key':'reference-label'}) : ''

  const grid = group((axisOptions('y').grid===false ? [] : yTickValues).map(value => svgLine(plot.x, yScale(value), plot.x + plot.width, yScale(value), {stroke: theme.border, 'stroke-opacity': 0.35, 'data-grid':'y'})))
  const axes = group([drawLinearAxisBottom(xScale, xDomain, plot, theme), drawLinearAxisLeft(yScale, yDomain, plot, theme, yTickCount)], {'data-scene-key':'axes'})
  const accessiblePoints = points.map(p => ({key:datumKey(p.row,xField),label: String(p.x), value: String(p.y)}))
  const tooltipRegions = pixels.map((pixel, i) => ({markKey:'series:single',key:datumKey(points[i].row,xField),
    x1: pixel.x - 8, y1: pixel.y - 8, x2: pixel.x + 8, y2: pixel.y + 8,
    text: tooltipRegionText(lineEncoding.tooltip, points[i].row),
  }))
  return {svg: group([grid, group([lineShape], {'data-scene-key':'marks'}), ruleShape, labelShape, axes]), accessiblePoints, tooltipRegions}
}

function drawLayered(spec: Record<string, unknown>, inheritedEncoding: Encoding, width: number, height: number, theme: VisualTheme): DrawnBody {
  const children = spec.layer as unknown[]
  if (!Array.isArray(children) || ![2,3].includes(children.length) || !children.every(isObject)) throw new NotShippedYet('layer: expected a supported reference or label overlay')
  const unitSpecs = children as Record<string, unknown>[]
  const marks = unitSpecs.map(child => markTypeOf(child.mark))
  const layerEncoding = {...inheritedEncoding, ...(isObject(spec.encoding) ? spec.encoding as Encoding : {})}
  const encodings = unitSpecs.map(child => ({...layerEncoding, ...(isObject(child.encoding) ? child.encoding as Encoding : {})}))
  const rows = primaryRows(spec)
  const combo = [...marks].sort().join('+')
  if(isFieldRuleLayer(unitSpecs,layerEncoding as Record<string,unknown>)){
   const li=marks.indexOf('line'),ri=marks.indexOf('rule'),le=encodings[li] as ContinuousEncoding
   const reference={rows:resolveRows(inheritData(unitSpecs[ri],spec)),encoding:encodings[ri] as ContinuousEncoding}
   const lineRows=resolveRows(inheritData(unitSpecs[li],spec))
   return withAxisTitles(continuousGuides(le),width,height,theme,(clean,w,h)=>drawContinuousLine(lineRows,clean,isObject(unitSpecs[li].mark)?unitSpecs[li].mark:{},w,h,theme,reference))
  }
  const primary=encodings[marks.findIndex(mark=>mark==='bar'||mark==='line'||mark==='area')] ?? encodings[0]
  const guides=usesContinuousLine(primary as Record<string,unknown>) ? continuousGuides(primary) : {...primary}
  for(const axis of ['x','y'] as const){
   const authored=encodings.flatMap(e=>e[axis]?.axis ? [e[axis]!.axis!] : [])
   if(authored.length)guides[axis]={...primary[axis],axis:Object.assign({},...authored)}
  }
  return withAxisTitles(guides,width,height,theme,(clean,w,h)=>{
   const units=encodings.map(e=>{const next={...e};for(const axis of ['x','y'] as const)if(guides[axis]?.axis && next[axis])next[axis]={...next[axis],axis:clean[axis]?.axis};return next})
   if(combo==='bar+text')return drawLayeredBarText(marks,units,rows,w,h,theme)
   if(['area+rule','line+rule','area+rule+text','line+rule+text'].includes(combo))return drawLayeredLineRule(marks,unitSpecs.map(c=>c.mark),units,rows,w,h,theme)
   throw new NotShippedYet('layer: unsupported combination')
  })
}

// Composed (concat/facet/repeat) accessibility is a follow-up (design doc §3.5 note in
// accessibility.ts); every composed branch below returns no accessiblePoints — only a single,
// uncomposed unit spec gets the roving-focus series/live-region/table extras for now (the two
// supported layer combos above are the exception: a bar+text or line/area+rule layer does draw
// its own accessiblePoints/tooltipRegions, from whichever child carries the real data series).
function drawComposed(spec: Record<string, unknown>, inheritedEncoding: Encoding, width: number, height: number, theme: VisualTheme): DrawnBody {
  if ('layer' in spec) return drawLayered(spec, inheritedEncoding, width, height, theme)

  const composeKey = (['concat', 'hconcat', 'vconcat'] as const).find(key => key in spec)
  if (composeKey) {
    const children = (spec[composeKey] as Array<Record<string, unknown>>).map(child => inheritData(child, spec))
    const horizontal = composeKey !== 'vconcat'
    const n = children.length
    const declared = children.map(child => panelSize(child, horizontal ? width / n : width, horizontal ? height : height / n))
    let offset = 0
    const tooltipRegions: TooltipRegion[] = []
    const panels = children.map((child, i) => {
      const [panelWidth, panelHeight] = declared[i]
      const {svg: body, tooltipRegions: childRegions} = drawComposed(child, {}, panelWidth, panelHeight, theme)
      const dx = horizontal ? offset : 0, dy = horizontal ? 0 : offset
      tooltipRegions.push(...translateRegions(childRegions, dx, dy))
      const translate = `translate(${dx},${dy})`
      offset += horizontal ? panelWidth : panelHeight
      return group([body], {transform: translate, 'data-chart-panel': String(i)})
    })
    return {svg: group(panels), accessiblePoints: [], tooltipRegions}
  }

  if ('facet' in spec) {
    const facetDef = spec.facet as {field?: string}
    const field = facetDef.field
    if (typeof field !== 'string') throw new NotShippedYet('facet: only a single field (not row/column) is implemented yet')
    const rows = resolveRows(spec)
    const categories = [...new Set(rows.map(row => String(row[field])))]
    const cols = Math.max(1, Math.min(Number(spec.columns) || categories.length, categories.length))
    const panelCount = Math.ceil(categories.length / cols)
    const inner = spec.spec as Record<string, unknown>
    const [innerWidth, innerHeight] = panelSize(inner, width / cols, height / panelCount)
    const tooltipRegions: TooltipRegion[] = []
    const panels = categories.map((category, i) => {
      const col = i % cols, row = Math.floor(i / cols)
      const panelSpec = {...inner, data: {values: rows.filter(r => String(r[field]) === category)}}
      const {svg: body, tooltipRegions: childRegions} = drawComposed(panelSpec, {}, innerWidth, innerHeight, theme)
      const dx = col * innerWidth, dy = row * innerHeight
      tooltipRegions.push(...translateRegions(childRegions, dx, dy))
      return group([body], {transform: `translate(${dx},${dy})`, 'data-chart-panel': String(i)})
    })
    return {svg: group(panels), accessiblePoints: [], tooltipRegions}
  }

  if ('repeat' in spec) {
    const repeatDef = spec.repeat
    const inner = spec.spec as Record<string, unknown>
    const rows = resolveRows(spec)
    if (Array.isArray(repeatDef)) {
      const n = repeatDef.length
      const [innerWidth, innerHeight] = panelSize(inner, width / n, height)
      const tooltipRegions: TooltipRegion[] = []
      const panels = repeatDef.map((fieldName, i) => {
        const panelSpec = {...inner, encoding: substituteRepeat(inner.encoding, 'repeat', String(fieldName)), data: {values: rows}}
        const {svg: body, tooltipRegions: childRegions} = drawComposed(panelSpec, {}, innerWidth, innerHeight, theme)
        tooltipRegions.push(...translateRegions(childRegions, i * innerWidth, 0))
        return group([body], {transform: `translate(${i * innerWidth},0)`})
      })
      return {svg: group(panels), accessiblePoints: [], tooltipRegions}
    }
    if (isObject(repeatDef)) {
      const rowFields = (repeatDef.row as string[] | undefined) ?? [undefined as unknown as string]
      const colFields = (repeatDef.column as string[] | undefined) ?? [undefined as unknown as string]
      const [innerWidth, innerHeight] = panelSize(inner, width / colFields.length, height / rowFields.length)
      const panels: string[] = []
      const tooltipRegions: TooltipRegion[] = []
      rowFields.forEach((rowField, ri) => colFields.forEach((colField, ci) => {
        let encoding = inner.encoding
        if (repeatDef.row) encoding = substituteRepeat(encoding, 'row', rowField)
        if (repeatDef.column) encoding = substituteRepeat({...(inner.encoding as object), ...(encoding as object)}, 'column', colField)
        const panelSpec = {...inner, encoding, data: {values: rows}}
        const {svg: body, tooltipRegions: childRegions} = drawComposed(panelSpec, {}, innerWidth, innerHeight, theme)
        const dx = ci * innerWidth, dy = ri * innerHeight
        tooltipRegions.push(...translateRegions(childRegions, dx, dy))
        panels.push(group([body], {transform: `translate(${dx},${dy})`}))
      }))
      return {svg: group(panels), accessiblePoints: [], tooltipRegions}
    }
    throw new NotShippedYet('repeat: unsupported shape')
  }

  const encoding = {...inheritedEncoding, ...(isObject(spec.encoding) ? spec.encoding as Encoding : {})}
  return drawUnitBody(spec, resolveRows(spec), encoding, width, height, theme)
}


/** A mounted native scene. Geometry is prepared as detached, validated SVG nodes; commits
 * reconcile the retained surface, axes and keyed labels synchronously. Static callers keep
 * RenderedVisual's SVG export contract, serialized from the current committed scene. */
export function renderBlergChart(spec: Record<string, unknown>, theme: VisualTheme, container: HTMLElement, width: number, height: number, stretch = false): RenderedVisual {
  let mountedDataUpdate = false
  let validatedSpec: Record<string, unknown> | undefined
  let generation = 0
  let disposed = false
  const colorSource=(model:Record<string,unknown>)=>{
   const primary=Array.isArray(model.layer) ? (model.layer as Record<string,unknown>[]).find(unit=>markTypeOf(unit.mark)==='line') : undefined
   return {...(model.encoding as object),...(primary?.encoding as object)} as {color?:{field?:string;type?:string;sort?:EncodingSort}}
  }
  const colorDefinition=colorSource(spec).color
  const colorRows=(model:Record<string,unknown>)=>Array.isArray(model.layer) ? (model.layer as Record<string,unknown>[]).flatMap(unit=>resolveRows(inheritData(unit,model))) : resolveRows(model)
  let colorHistory=colorDefinition?.field && ['nominal','ordinal'].includes(colorDefinition.type ?? '') ? sortedDomain(colorRows(spec).map(row=>String(row[colorDefinition.field!])),colorDefinition.sort) : undefined
  const prepare = (nextWidth: number, nextHeight: number, tokens: VisualTheme, nextSpec = spec, colors = colorHistory, live = mountedDataUpdate) => {
    const naturalWidth = stretch ? nextWidth : Number(nextSpec.width) > 0 ? Number(nextSpec.width) : 640
    const scale = Math.min(1.2, Math.max(1, nextWidth / naturalWidth))
    const scaledTheme = scale === 1 ? tokens : {...tokens, fontSize: tokens.fontSize * scale}
    const title = typeof nextSpec.title === 'string' ? nextSpec.title : isObject(nextSpec.title) ? nextSpec.title.text : undefined
    const titleLines = typeof title === 'string' ? wrapMeasured(title, Math.max(1, nextWidth - 24), text => measureText({...scaledTheme, fontSize: scaledTheme.fontSize + 1}, text, 'bold')) : Array.isArray(title) ? title.map(String) : []
    const titleHeight = titleLines.length ? titleLines.length * (scaledTheme.fontSize + 5) + 8 : 0
    const units = Array.isArray(nextSpec.layer) ? nextSpec.layer as Record<string, unknown>[] : [nextSpec]
    const legends = units.map(unit => {
      const encoding = {...(nextSpec.encoding as Record<string, unknown> | undefined), ...(unit.encoding as Record<string, unknown> | undefined)}
      const color = encoding.color as {field?: string; type?: string} | undefined
      if (!color?.field || !['nominal', 'ordinal'].includes(color.type ?? '')) return 0
      const rows = resolveRows(unit.data ? unit : nextSpec)
      return categoricalLegendHeight(new Set(rows.map(row => String(row[color.field!]))).size, scaledTheme) + 24
    })
    nextHeight = Math.max(nextHeight, titleHeight + Math.max(scaledTheme.fontSize * 3 + 50, ...legends))
    if(!Number.isFinite(nextWidth) || !Number.isFinite(nextHeight) || nextWidth<=0 || nextHeight<=0 || nextWidth>MAX_CHART_SIDE || nextHeight>MAX_CHART_SIDE || nextWidth*nextHeight>MAX_CHART_AREA)throw Object.assign(new Error('Native chart geometry exceeds the drawing budget'),{name:'NativeChartBudget'})
    let drawing!: DrawnBody
    const root = prepareSvgScene(() => {
      const body: DrawnBody = liveMark(nextSpec) && !primaryRows(nextSpec).length ? (()=>{
        recordPlotBounds({x:12,y:12,width:Math.max(1,nextWidth-24),height:Math.max(1,nextHeight-titleHeight-24)})
        return {svg:group([group([],{'data-scene-key':'grid'}),group([svgText(12,scaledTheme.fontSize+12,'No data',{fill:scaledTheme.muted,'font-family':scaledTheme.fontFamily,'font-size':scaledTheme.fontSize})],{'data-scene-key':'marks'}),group([],{'data-scene-key':'axes'}),group([],{'data-scene-key':'legend'})]),accessiblePoints:[],tooltipRegions:[]}
      })() : withSeriesDomain(colors,()=>live && nextSpec.mark ? drawUnitBody(nextSpec,resolveRows(nextSpec),nextSpec.encoding as Encoding,nextWidth,Math.max(1,nextHeight-titleHeight),scaledTheme,true) : drawComposed(nextSpec, {}, nextWidth, Math.max(1, nextHeight - titleHeight), scaledTheme))
      const heading = titleLines.map((text, index) => svgText(12, (index + 1) * (scaledTheme.fontSize + 5), text, {fill: scaledTheme.fg, 'font-size': scaledTheme.fontSize + 1, 'font-family': scaledTheme.fontFamily, 'font-weight': 'bold'})).join('')
      drawing = {...body, svg: group([heading], {'data-scene-key': 'heading'}) + group([body.svg], {'data-scene-key': 'plot-body', transform: `translate(0,${titleHeight})`}), tooltipRegions: translateRegions(body.tooltipRegions, 0, titleHeight)}
      return svgDocument(nextWidth, nextHeight, drawing.svg, 'transparent', scale)
    }, titleHeight)
    validateSvgElement(root)
    root.setAttribute('data-chart-renderer', 'blerg')
    root.setAttribute('color', tokens.accent)
    root.setAttribute('data-data-state',liveMark(nextSpec) && !primaryRows(nextSpec).length ? 'empty' : 'ready')
    root.setAttribute('role', 'group')
    root.setAttribute('tabindex', '0')
    const mark = nextSpec.mark
    const markType = typeof mark === 'string' ? mark : (mark as {type?: string})?.type
    root.setAttribute('aria-label', typeof title === 'string' ? title : Array.isArray(title) ? title.join(' ') : `${markType ?? 'Data'} chart: ${drawing.accessiblePoints.length} observations`)
    return {...drawing, root, height: nextHeight}
  }
  const initial = prepare(width, height, theme)
  height = initial.height
  const root = initial.root
  const extras = buildAccessibleExtras(initial.accessiblePoints, theme)
  let currentRegions = initial.tooltipRegions
  let cancelMotion: ((finish?:boolean)=>void) | undefined
  const reducedMotion = window.matchMedia?.('(prefers-reduced-motion: reduce)')
  const motionPreference = () => {if(reducedMotion?.matches)cancelMotion?.(true)}
  const finishMotion = () => { cancelMotion?.(true); root.dispatchEvent(new Event('blerg-chart-finish-entrance')) }
  root.addEventListener('blerg-chart-before-snapshot', finishMotion)
  reducedMotion?.addEventListener?.('change', motionPreference)
  const tooltip = attachTooltip(root, container, initial.tooltipRegions)
  container.replaceChildren(root, ...extras, tooltip.tooltip)
  const updateAccessible=(points:readonly DrawnPoint[])=>{
    if(!extras.length && points.length){
      const next=buildAccessibleExtras(points,theme);extras.push(...next);extras.update=next.update
      for(const element of next)container.insertBefore(element,tooltip.tooltip)
    } else extras.update(points)
  }
  const commit = (box: {width: number; height: number}, tokens: VisualTheme, staged?: ReturnType<typeof prepare>, updateExtras = true) => {
    if (disposed || box.width <= 0 || box.height <= 0) return
    // All potentially failing work precedes the first visible mutation.
    const next = staged ?? prepare(box.width, box.height, tokens)
    // Keep the surface's scope id: scoped stroke rules must name the retained root.
    const id = root.id
    next.root.querySelector('style')?.replaceChildren(document.createTextNode(next.root.querySelector('style')!.textContent!.replaceAll(next.root.id, id)))
    next.root.id = id
    commitScene(root, next.root)
    currentRegions = next.tooltipRegions
    tooltip.setRegions(currentRegions)
    if(updateExtras)updateAccessible(next.accessiblePoints)
    for (const extra of extras) for (const table of extra.querySelectorAll<HTMLElement>('table')) {
      table.style.fontSize = `${tokens.fontSize}px`
      table.style.fontFamily = tokens.fontFamily
    }
    width = box.width; height = next.height; theme = tokens
  }
  const captureSvg = () => {
    finishMotion()
    const copy = root.cloneNode(true) as SVGElement
    copy.querySelector('[data-chart-indicator]')?.remove()
    copy.removeAttribute('data-chart-emphasis')
    copy.removeAttribute('aria-describedby')
    copy.querySelectorAll('[data-chart-active]').forEach(mark => mark.removeAttribute('data-chart-active'))
    return new XMLSerializer().serializeToString(copy)
  }
  return {
    get svg() { return captureSvg() },
    get width() { return width },
    get height() { return height },
    resize: (box, nextGeneration) => {
      if (nextGeneration < generation) return
      cancelMotion?.(true)
      commit(box, theme)
      generation = nextGeneration
    },
    applyData: (model, nextGeneration) => {
      if(disposed || nextGeneration <= generation)return false
      const nextSpec=parseVegaLite(JSON.stringify(model)), original=validatedSpec ??= parseVegaLite(JSON.stringify(spec))
      const mark=liveMark(nextSpec)
      if(!mark || !sameDataStructure(original,nextSpec))return false
      const primary=Array.isArray(nextSpec.layer) ? (nextSpec.layer as Record<string,unknown>[]).find(unit=>['line','area'].includes((typeof unit.mark==='string' ? unit.mark : (unit.mark as {type?:string})?.type) ?? '')) : undefined
      const rows=primaryRows(nextSpec), encoding={...(nextSpec.encoding as object),...(primary?.encoding as object)} as {x?:{field?:string};y?:{field?:string};color?:{field?:string}}
      const keys=rows.map(row=>datumKey(row,encoding.x?.field,mark==='rect' ? encoding.y?.field : encoding.color?.field))
      if(mark!=='bar' && new Set(keys).size!==keys.length) {console.warn('Native data update refused',{cause:'ambiguous_datum_identity'});return false}
      const colors=colorHistory ? [...colorHistory] : undefined
      if(colors && colorDefinition?.field) for(const row of colorRows(nextSpec)) {const value=String(row[colorDefinition.field]);if(!colors.includes(value))colors.push(value)}
      if(colors && (colors.length>256 || colors.reduce((bytes,key)=>bytes+32+key.length*2,0)>(64<<10))){console.warn('Native data update refused',{cause:'domain_history_budget'});return false}
      const staged=prepare(width,height,theme,nextSpec,colors,true)
      const markKeys=[...staged.root.querySelectorAll('[data-datum-key]')].map(node=>node.getAttribute('data-datum-key'))
      if(new Set(markKeys).size!==markKeys.length){console.warn('Native data update refused',{cause:'ambiguous_mark_identity'});return false}
      cancelMotion?.()
      const before=captureGeometry(root), priorRegions=currentRegions
      spec=nextSpec;validatedSpec=nextSpec;colorHistory=colors;mountedDataUpdate=true;generation=nextGeneration
      commit({width,height},theme,staged,false)
      const byKey=new Map(priorRegions.map((region,index)=>[region.key ?? String(index),region]))
      const stop=animateGeometry(root,before,!!reducedMotion?.matches,progress=>{
        currentRegions=staged.tooltipRegions.map((region,index)=>{
          const previous=byKey.get(region.key ?? String(index)) ?? region
          return {...region,x1:previous.x1+(region.x1-previous.x1)*progress,x2:previous.x2+(region.x2-previous.x2)*progress,y1:previous.y1+(region.y1-previous.y1)*progress,y2:previous.y2+(region.y2-previous.y2)*progress}
        })
        tooltip.setRegions(currentRegions, false)
        root.dispatchEvent(new Event('blerg-chart-frame',{bubbles:true}))
      },()=>{cancelMotion=undefined;updateAccessible(staged.accessiblePoints)})
      cancelMotion=root.dataset.updateState==='animating' ? stop : undefined
      return true
    },
    setTheme: tokens => {cancelMotion?.(true);commit({width, height}, tokens)},
    snapshot: () => ({svg: captureSvg(), width, height}),
    dispose: () => { disposed = true; root.removeEventListener('blerg-chart-before-snapshot', finishMotion); cancelMotion?.(); cancelMotion=undefined; reducedMotion?.removeEventListener?.('change', motionPreference); tooltip.dispose(); root.remove(); extras.forEach(extra => extra.remove()); tooltip.tooltip.remove() },
  }
}

export { NotShippedYet }
