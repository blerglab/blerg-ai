import {usesContinuousLine,isContinuousEncoding,isFieldRuleLayer} from './continuousSpec'
// BLG-452 design doc §3.3: a static, deterministic allow-list of every key at every path a spec
// visits. Runs on the object parseVegaLite already returned (policy-validated, bounded). Never
// renders anything and never throws: an unrecognized key, value shape or combination anywhere in
// the tree always means 'fallback' for the whole spec, never a partial draw.
//
// Continuous line options and the NVDA field-rule overlay are admitted by continuousSpec.ts,
// shared with their dedicated drawing path; other mark families keep the declarations below.
//
// Review follow-up (post priority 1-5): the classifier used to carry its own hand-maintained
// allow-list tables (channel types, aggregates, stack/sort values, scale/axis/legend keys, mark
// properties) that drifted from what the mark files actually draw — a spec could classify 'blerg'
// and still draw silently wrong (or just drop a requested option on the floor) because nothing
// forced the two to agree. Audited while fixing this (not assumed): no mark file anywhere reads
// encoding.*.scale, encoding.*.legend, encoding.*.condition, encoding.*.title,
// encoding.*.format or encoding.*.formatType — every one of those is purely decorative today, so
// this file now rejects all of them unconditionally (any key present, including an explicit
// `null`, falls back) rather than carrying a second, independently-wrong allow-list for each.
// The shared guide layout consumes title, format, tickCount, grid and labelAngle.
// Specialized layouts retain their narrower guide contract below.
// Everything else (channel types, aggregate, bin, stack, sort, mark properties) is now read
// directly from each mark file's own exported MarkDeclaration (declare.ts) — the union across
// shipped marks, never a separately-maintained table. A mark's declaration IS its contract with
// this file; changing what a mark draws and changing what classify.ts accepts for it are the same
// edit now, not two that can drift apart.
//
// classify() is the pure allow-list walk, parameterized by which marks are currently shipped, so
// it can be tested against the full grammar today (step 1 of BLG-452's build order) before any
// mark has passed the differential gate (§3.10, step 3). classifyChart() is the only production
// entry point: it always uses SHIPPED_MARKS below.
import {isAxisOptions} from './axisOptions'
import { AXIS_CONFIG, LITERAL_CONFIG, MARKS } from '../vegaPolicy'
import { MAX_POINTS, pointBudgetFor } from './budget'
import type { ChannelDeclaration, MarkDeclaration } from './declare'
import { declaration as barDeclaration } from './marks/bar'
import { declarations as lineDeclarations } from './marks/line'
import { declarations as pointDeclarations } from './marks/point'
import { declaration as textDeclaration } from './marks/text'
import { declaration as ruleDeclaration } from './marks/rule'
import { declaration as rectDeclaration } from './marks/rect'
import { declaration as arcDeclaration } from './marks/arc'
import { declaration as boxplotDeclaration } from './marks/boxplot'

export type Classification = 'blerg' | 'fallback'

const ALL_DECLARATIONS: readonly MarkDeclaration[] = [
  barDeclaration, ...lineDeclarations, ...pointDeclarations, textDeclaration, ruleDeclaration, rectDeclaration, arcDeclaration, boxplotDeclaration,
]
const DECLARATIONS_BY_MARK: ReadonlyMap<string, MarkDeclaration> = new Map(ALL_DECLARATIONS.map(d => [d.mark, d]))

/** One source of truth (review follow-up): each mark's own declared /mark/<property> keys,
 *  unioned here for the coverage test and for isAllowedMarkObject below — never hand-maintained
 *  separately from what the mark file actually reads. */
export const MARK_PROPERTIES: Record<string, ReadonlySet<string>> = Object.fromEntries(ALL_DECLARATIONS.map(d => [d.mark, d.markProperties]))

/** Marks whose differential corpus (BLG-452 step 3) has passed and is pinned in CI. Grows one
 *  entry at a time, in the commit that adds the corpus proving it — never ahead of that proof.
 *  Empty until the first mark ships: until then classifyChart always returns 'fallback', which
 *  is correct, not a bug, since nothing has been proven against Vega yet. */
export const SHIPPED_MARKS: ReadonlySet<string> = new Set([
  'bar', // BLG-452 step 3: passed its differential corpus, BlergBar.differential.browser.test.tsx
  'point', 'circle', 'square', // BlergPoint.differential.browser.test.tsx
  'text', // BlergText.differential.browser.test.tsx
  'line', 'area', // BlergLine.differential.browser.test.tsx
  'rule', // BlergRule.differential.browser.test.tsx
  'rect', // BlergRect.differential.browser.test.tsx
  'arc', // BlergArc.differential.browser.test.tsx
  'boxplot', // BlergBoxplot.differential.browser.test.tsx
])

// getChartModel owns the bounded source cache and deep-freezes validated models.
// Reuse classification by immutable model identity without serializing large rows.
const classifications=new WeakMap<Record<string,unknown>,Classification>()
export function classifyChart(spec: Record<string,unknown>):Classification {
 return classifications.get(spec) ?? classify(spec,SHIPPED_MARKS)
}
/** Called only after policy validation and deep freezing by the bounded model cache. */
export function cacheChartClassification(spec:Record<string,unknown>):void {
 classifications.set(spec,classify(spec,SHIPPED_MARKS))
}

/** The allow-list walk, parameterized by which marks are shipped. Not the production entry point
 *  — exported so tests can exercise the full path grammar before any mark actually ships. */
export function classify(spec: Record<string, unknown>, shippedMarks: ReadonlySet<string>): Classification {
  return classifyNode(spec, shippedMarks, {}, true, {total: 0, sawFirst: false}) ? 'blerg' : 'fallback'
}

/** Policy-legal marks intentionally left on the Vega fallback for v1 (design doc open questions,
 *  resolved: tick and trail are not required marks). The coverage test below asserts every entry
 *  of vegaPolicy.ts's MARKS set is either implemented (above) or listed here — never silently
 *  unaccounted for. */
export const DEFERRED_MARKS: ReadonlySet<string> = new Set(['tick', 'trail'])

const BOXPLOT_SUBMARKS = new Set(['box', 'median', 'outliers', 'rule', 'ticks'])
const SUBMARK_PROPERTIES = new Set(['color', 'opacity', 'size'])
// Review follow-up: condition/title/format/formatType joined scale/axis/legend in the "nothing
// consumes this" audit — see this file's header. A channel def may now only carry field/type/
// aggregate/bin/timeUnit/sort/stack (each still gated per-mark by its declaration below).
const ALLOWED_ENCODING_KEYS = new Set(['field', 'type', 'datum', 'value', 'aggregate', 'bin', 'timeUnit', 'sort', 'stack', 'axis'])
const IMPLEMENTED_TYPES = new Set(['quantitative', 'ordinal', 'nominal', 'temporal'])
// Locale-dependent calendar units (week/quarter boundaries) and pre-binned temporal time units
// are deferred until proven against Vega across pinned timezones (design doc §3.4b/§3.10). This
// is shape-level acceptance only, written ahead of any mark's real temporal support (this file's
// own header: "before any mark has passed the differential gate") — BLG-452 priority 5 audited
// every mark that could reach a temporal/timeUnit channel and found none of them actually bucket
// by calendar unit yet (line/point/text already threw NotShippedYet for any temporal x; bar/
// rule/rect/boxplot did NOT check the type at all, silently coercing it to a plain string and
// sorting alphabetically — a real latent bug, closed this round with the same type of guard).
// So today, in production, every temporal/timeUnit spec still falls back to Vega regardless of
// which unit it names — not just week/quarter/binned — via each mark's own NotShippedYet; this
// regex only controls the shape-level allow-list (M8's wasted-draw-attempt tradeoff, same as the
// 'layer' key before priority 3), not whether anything actually draws it.
const DEFERRED_TIME_UNIT = /week|quarter/i
const COMPOSE_KEYS = ['concat', 'hconcat', 'vconcat'] as const

const isObject = (value: unknown): value is Record<string, unknown> => !!value && typeof value === 'object' && !Array.isArray(value)

function isImplementedTimeUnit(value: unknown): boolean {
  return typeof value === 'string' && !value.startsWith('binned') && !DEFERRED_TIME_UNIT.test(value)
}

interface PointBudget { total: number; sawFirst: boolean }

// ---- Node walk: unit specs (have their own `mark`) vs. composition specs. One shared grammar,
// applied at whatever depth a node sits (design doc §3.3 "path grammar").
function classifyNode(node: unknown, shippedMarks: ReadonlySet<string>, inheritedEncoding: Record<string, unknown>, top: boolean, budget: PointBudget): boolean {
  if (!isObject(node)) return false
  if (top) {
    if ('config' in node && !isAllowedConfig(node.config)) return false
    if ('resolve' in node && !isAllowedResolve(node.resolve)) return false
  }
  if ('title' in node && !isAllowedTitle(node.title)) return false

  // 'layer' (BLG-452 priority 3): v1 scope is exactly two of the "common" overlays the design doc
  // calls out — a line/area with a single-datum rule annotation, or a bar with a text label —
  // sharing one scale, built in render.ts's own drawLayered(), not through the standalone mark
  // files (which each derive their own scale from their own data; layer.ts, if it grows, would
  // own a real shared-scale refactor across all nine marks, not yet done). Any other combination
  // stays on the Vega fallback, same discipline as every other v1 gap here — shape-only here,
  // render.ts's own drawLayered() still throws NotShippedYet for anything its two cases don't
  // cover (e.g. a rule with x/y2 range fields instead of a single datum).
  if ('layer' in node) {
    const children = node.layer
    if (!Array.isArray(children) || ![2,3].includes(children.length)) return false
    const markTypes = children.map(child => {
      const mark = isObject(child) ? child.mark : undefined
      return typeof mark === 'string' ? mark : isObject(mark) ? mark.type : undefined
    })
    const combo = [...markTypes].sort().join('+')
    if (combo !== 'bar+text' && combo !== 'line+rule' && combo !== 'area+rule' && combo !== 'line+rule+text' && combo !== 'area+rule+text') return false
    // A layer inherits its own parent's encoding, same as a unit spec (vegaPolicy.ts's checkSpec:
    // "a layer also inherits its parent's encoding") — unlike concat/facet/repeat, which reset it.
    const layerEncoding = {...inheritedEncoding, ...(isObject(node.encoding) ? node.encoding : {})}
    if(combo==='line+rule' && children.some(c=>isObject(c) && isObject(c.encoding) && isObject(c.encoding.y) && 'field' in c.encoding.y && (c.mark==='rule' || isObject(c.mark) && c.mark.type==='rule'))){
     if(!shippedMarks.has('line') || !shippedMarks.has('rule') || !isFieldRuleLayer(children,layerEncoding))return false
     for(const child of children){const data=isObject(child) ? child.data ?? node.data : undefined;const n=isObject(data)&&Array.isArray(data.values) ? data.values.length : 0;if(n>MAX_POINTS.discrete)return false;budget.total+=n;if(budget.total>MAX_POINTS.discrete)return false;budget.sawFirst=true}
     return true
    }
    for(const axis of ['x','y']){
     for(const key of ['title','format','tickCount','grid','labelAngle']){
      const values=new Set(children.flatMap(child=>{const e={...layerEncoding,...(isObject(child)&&isObject(child.encoding)?child.encoding:{})};const def=e[axis];return isObject(def)&&isObject(def.axis)&&def.axis[key]!==undefined?[def.axis[key]]:[]}))
      if(values.size>1)return false
     }
    }
    // A layer child isn't drawn by its own mark file's standalone drawXxx() — render.ts's own
    // drawLayeredBarText/drawLayeredLineRule draw the whole combo directly, with their own field/
    // shape requirements that don't match either mark's standalone declaration (bar+text's x is
    // nominal, inherited from the bar; text.ts's own standalone declaration needs a quantitative
    // x — two different contracts for the same mark name). So a layer child is validated here
    // against those two functions' own field/shape requirements directly (still one source of
    // truth — just the layer-drawing function's, not the standalone mark file's), not against
    // DECLARATIONS_BY_MARK. Their own NotShippedYet guards (color, aggregate, sort, point overlay,
    // datum-only rule) stay the real backstop regardless, same as every other v1 gap here.
    return children.every(child => isAllowedLayerChild(child, shippedMarks, layerEncoding, budget))
  }
  const composeKey = COMPOSE_KEYS.find(key => key in node)
  if (composeKey) {
    const children = node[composeKey]
    if (!Array.isArray(children) || !children.length) return false
    // concat-family children do not inherit the parent encoding — mirrors vegaPolicy.ts's
    // checkSpec exactly (§3.3 "inheritance"). They DO inherit the parent's own data/datasets when
    // they have none of their own (same rule, same citation) — render.ts's resolveRows() now
    // implements that fallback; classify.ts doesn't need to re-derive it here since a child with
    // no inline data of its own and no shippable mark still just falls back like any other
    // unrecognized shape, and a child that inherits data draws the same shape it would if that
    // data were copied onto it directly.
    return children.every(child => classifyNode(child, shippedMarks, {}, false, budget))
  }
  // Review finding: facet and repeat compute an independent scale per panel today — Vega-Lite's
  // own default is a *shared* domain across all panels (plus row/column header labels, neither
  // built) — so a faceted/repeated chart with varying per-panel ranges would draw each panel to a
  // different, incomparable scale, which is wrong, not just incomplete. Falls back until shared
  // scales (and headers) exist, rather than drawing panels that don't compare.
  if ('facet' in node || 'repeat' in node) return false
  if ('mark' in node) {
    return isAllowedUnitSpec(node, shippedMarks, inheritedEncoding, budget)
  }
  return false
}

/** bar's and text's position channels, as used from inside the bar+text layer combo (nominal x,
 *  shared with the bar) — not their standalone declarations (text.ts alone only draws a
 *  quantitative x). Accepted at classify time; drawLayeredBarText's own guards are the real
 *  backstop for color/aggregate/sort, same discipline as every mark file's NotShippedYet. */
const LAYER_CHANNEL_TYPES = new Set(['quantitative', 'nominal', 'ordinal'])

function isAllowedLayerChild(child: unknown, shippedMarks: ReadonlySet<string>, layerEncoding: Record<string, unknown>, budget: PointBudget): boolean {
  if (!isObject(child) || !('mark' in child)) return false
  const mark = child.mark
  const markType = typeof mark === 'string' ? mark : isObject(mark) ? mark.type : undefined
  if (typeof markType !== 'string' || !shippedMarks.has(markType)) return false
  const data = child.data
  const n = isObject(data) && Array.isArray(data.values) ? data.values.length : 0
  if (n > pointBudgetFor(markType)) return false
  budget.total += n
  if (budget.sawFirst && budget.total > MAX_POINTS.discrete) return false
  budget.sawFirst = true
  if(markType==='text'){
   const e={...layerEncoding,...(isObject(child.encoding)?child.encoding:{})}
   if(isObject(e.text) && typeof e.text.value==='string'){
    const position=isObject(e.x)?e.x:e.y
    return Object.keys(e).every(key=>['x','y','text'].includes(key)) && !(e.x && e.y)
      && isObject(position) && Object.keys(position).length===1 && typeof position.datum==='number' && Number.isFinite(position.datum)
      && Object.keys(e.text).length===1 && (mark==='text' || (isObject(mark) && Object.entries(mark).every(([key,value])=>key==='type' || (['dx','dy'].includes(key) && typeof value==='number' && Number.isFinite(value) && Math.abs(value)<=100))))
   }
  }
  const declaration = DECLARATIONS_BY_MARK.get(markType)
  if (!declaration) return false
  if (isObject(mark) && !isAllowedMarkObject(markType, mark, declaration)) return false
  const encoding = {...layerEncoding, ...(isObject(child.encoding) ? child.encoding : {})}
  if(markType==='line' && usesContinuousLine(encoding))return (!isObject(mark) || Object.keys(mark).every(k=>k==='type'||k==='opacity')) && isContinuousEncoding(encoding)
  if (declaration.supportsEncoding && !declaration.supportsEncoding(encoding)) return false
  for (const [channel, def] of Object.entries(encoding)) {
    if (channel === 'row' || channel === 'column' || channel === 'facet') return false
    if (!isObject(def)) return false
    if ('condition' in def) return false
    if ('datum' in def) { if (!Object.keys(def).every(key => key === 'datum')) return false; continue }
    if ('value' in def) { if (Object.keys(def).length !== 1) return false; continue }
    if (typeof def.field !== 'string') return false
    if (typeof def.type !== 'string' || !LAYER_CHANNEL_TYPES.has(def.type)) return false
    for (const key of Object.keys(def)) {
      if (!ALLOWED_ENCODING_KEYS.has(key)) return false
      if(key==='axis' && (!['x','y'].includes(channel)||!isAxisOptions(def.axis) || (isObject(def.axis) && 'format' in def.axis && def.type!=='quantitative')))return false
    }
  }
  return true
}

function isAllowedUnitSpec(node: Record<string, unknown>, shippedMarks: ReadonlySet<string>, inheritedEncoding: Record<string, unknown>, budget: PointBudget): boolean {
  const mark = node.mark
  const markType = typeof mark === 'string' ? mark : isObject(mark) ? mark.type : undefined
  if (typeof markType !== 'string' || !shippedMarks.has(markType)) return false
  const declaration = DECLARATIONS_BY_MARK.get(markType)
  if (!declaration) return false
  if (isObject(mark) && !isAllowedMarkObject(markType, mark, declaration)) return false
  // §3.6/M10: exceeding our own (smaller than policy's) point budget is a classification
  // outcome, never a thrown error — this node's own inline data.values length against its own
  // mark's single-chart ceiling, same as before.
  const data = node.data
  const n = isObject(data) && Array.isArray(data.values) ? data.values.length : 0
  if (n > pointBudgetFor(markType)) return false
  // Review finding: that per-node check alone lets many panels (concat/hconcat/vconcat/layer),
  // each individually under budget, sum to a synchronous main-thread draw far past it. A second
  // sibling anywhere in the whole spec drops the *cumulative* ceiling to the smaller discrete
  // budget, regardless of which marks are mixed in — simple and conservative, not a per-mark
  // formula, on the stated principle that falling back too eagerly beats drawing something slow.
  if (budget.sawFirst) { budget.total += n; if (budget.total > MAX_POINTS.discrete) return false }
  else { budget.total += n; budget.sawFirst = true }
  const encoding = {...inheritedEncoding, ...(isObject(node.encoding) ? node.encoding : {})}
  if(markType==='line' && usesContinuousLine(encoding))return (!isObject(mark) || Object.keys(mark).every(k=>k==='type'||k==='opacity')) && isContinuousEncoding(encoding)
  // These specialized layouts have their own guides; admit only their existing title contract.
  if(markType==='rect' || (markType==='bar' && encoding.x2)){
   for(const channel of ['x','y']){const def=encoding[channel];if(isObject(def)&&isObject(def.axis)&&Object.keys(def.axis).some(key=>key!=='title'))return false}
  }
  if (declaration.supportsEncoding && !declaration.supportsEncoding(encoding)) return false
  for (const [channel, def] of Object.entries(encoding)) {
    // Table row 18: the channel-shaped facet forms stay on Vega; row/column/facet never classify.
    if (channel === 'row' || channel === 'column' || channel === 'facet') return false
    const channelDeclaration = declaration.channels[channel]
    if (!channelDeclaration) return false
    if (channel === 'tooltip' && Array.isArray(def)) {
      if (!def.every(entry => isAllowedChannelDef(entry, channelDeclaration))) return false
      continue
    }
    if (!isAllowedChannelDef(def, channelDeclaration, channel==='x'||channel==='y')) return false
  }
  return true
}

function isAllowedMarkObject(markType: string, mark: Record<string, unknown>, declaration: MarkDeclaration): boolean {
  for (const [key, value] of Object.entries(mark)) {
    if (key === 'type') continue
    // Table row 4: /mark/size and /mark/tooltip are mark-property forms, distinct from the
    // encoding-channel forms of the same key names (table row 5/9) — neither is consumed by any
    // mark file today (render.ts doesn't pass the raw mark object to most draw functions at all),
    // so both now fall back like any other undeclared property rather than being silently
    // accepted and ignored.
    if (markType === 'boxplot' && BOXPLOT_SUBMARKS.has(key)) {
      if (!isAllowedBoxplotSubmark(value)) return false
      continue
    }
    if (!declaration.markProperties.has(key) || !isAllowedMarkPropertyValue(key, value)) return false
  }
  return true
}

// Table row 3b: a boxplot sub-mark override is true/false, or a literal object of its own
// (color/opacity/size) — not a second recursion into MARK_PROPERTIES, since outliers/box/etc.
// aren't actually their own mark type. drawBoxplot() itself still throws NotShippedYet when any
// of these is actually present (a stated v1 gap, design doc's own intro) — accepted here only so
// that guard is what routes the spec to fallback, not a second, duplicate rejection.
function isAllowedBoxplotSubmark(value: unknown): boolean {
  if (typeof value === 'boolean') return true
  if (!isObject(value)) return false
  return Object.entries(value).every(([key, item]) => SUBMARK_PROPERTIES.has(key) && isAllowedMarkPropertyValue(key, item))
}

function isAllowedMarkPropertyValue(_key: string, value: unknown): boolean {
  if (typeof value === 'boolean' || typeof value === 'string') return true
  if (typeof value === 'number') return Number.isFinite(value)
  if (Array.isArray(value)) return value.every(item => typeof item === 'number' || typeof item === 'string')
  return false
}


function isAllowedChannelDef(def: unknown, channelDeclaration: ChannelDeclaration, axisTitle=false): boolean {
  if (!isObject(def)) return false
  // Review finding: a condition object (table row 17) is never evaluated by any mark — only the
  // base field/datum/value shape ever gets drawn — so it now falls back like scale/axis/legend
  // rather than being shape-checked and then silently dropped.
  if ('condition' in def) return false
  if ('value' in def) return Object.keys(def).length === 1
  if ('datum' in def) return Object.keys(def).every(key => key === 'datum')
  const field = def.field
  const fieldOk = typeof field === 'string'
    || (isObject(field) && typeof field.repeat === 'string' && ['row', 'column', 'layer', 'repeat'].includes(field.repeat) && Object.keys(field).length === 1)
  if (!fieldOk) return false
  // Table row 6: type must be present — replicating Vega-Lite's type-inference heuristics for an
  // omitted type is deferred.
  if (typeof def.type !== 'string' || !IMPLEMENTED_TYPES.has(def.type) || !channelDeclaration.types.has(def.type as never)) return false
  for (const key of Object.keys(def)) {
    if (!ALLOWED_ENCODING_KEYS.has(key)) return false
    switch (key) {
      case 'axis': if(!axisTitle || !isAxisOptions(def.axis) || (isObject(def.axis) && 'format' in def.axis && def.type!=='quantitative'))return false;break
      case 'aggregate': if (!channelDeclaration.aggregate?.has(def.aggregate as string)) return false; break
      case 'bin': if (!channelDeclaration.bin || (def.bin !== true && def.bin !== 'binned')) return false; break
      case 'timeUnit': if (!isImplementedTimeUnit(def.timeUnit)) return false; break
      case 'sort': if (!isAllowedSort(def.sort, channelDeclaration)) return false; break
      case 'stack': if (!channelDeclaration.stack?.has(String(normalizeStack(def.stack)))) return false; break
      // field/type: already literal/bounded by vegaPolicy.ts.
    }
  }
  return true
}

/** Vega-Lite's own default stack value (an absent key or `true`) is "zero" — normalized once here
 *  so a mark's declared stack set only ever needs to name the string forms it draws. */
function normalizeStack(stack: unknown): unknown {
  return stack === true ? 'zero' : stack
}

function isAllowedSort(sort: unknown, channelDeclaration: ChannelDeclaration): boolean {
  // Vega-Lite's own default (an absent key or the literal 'ascending') is what every mark here
  // already draws; anything else needs the mark's own declaration to say it's implemented.
  if (sort === 'ascending') return true
  return channelDeclaration.sort === true && (sort === null || sort === 'descending'
    || (Array.isArray(sort) && sort.every(value => typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean'))
    || (isObject(sort) && typeof sort.field === 'string'))
}

function isAllowedTitle(_title: unknown): boolean {
  // Root/panel titles belong to VisualBlock chrome. Encoding axis.title is
  // validated separately and rendered by the shared native guide layout.
  return true
}

function isAllowedResolve(resolve: unknown): boolean {
  if (!isObject(resolve)) return false
  for (const key of Object.keys(resolve)) {
    if (key !== 'scale' && key !== 'axis' && key !== 'legend') return false
    const section = resolve[key]
    if (!isObject(section)) return false
    if (!Object.values(section).every(value => value === 'shared' || value === 'independent')) return false
  }
  return true
}

function isAllowedConfig(config: unknown): boolean {
  if (!isObject(config)) return false
  for (const key of Object.keys(config)) {
    // Table row 25: any explicit config.invalid falls back, regardless of its value.
    if (key === 'invalid') return false
    const isMarkConfig = key === 'mark' || MARKS.has(key)
    const isAxisConfig = AXIS_CONFIG.test(key)
    const isLiteralConfig = LITERAL_CONFIG.has(key)
    const isMiscLiteral = key === 'background' || key === 'font' || key === 'padding' || key === 'range' || key === 'autosize' || key === 'style'
    if (!isMarkConfig && !isAxisConfig && !isLiteralConfig && !isMiscLiteral) return false
  }
  return true
}
