// BLG-452 design doc §3.4/§3.9: color scales. Categorical colors are confirmed empirically to
// match Vega exactly (vegaTheme()'s config.range.category is literally [...theme.palette], and
// Vega-Lite assigns palette[i] to the i-th value of the field's *sorted* domain) — this file must
// sort the domain the same way or the colors (and, for bar, the stack order) will disagree.
// Continuous color intentionally does NOT try to match Vega's default sequential scheme (a
// generic "YlGnBu"-like ramp): design doc §3.9 maps continuous color to the chart's own theme
// (surface2 -> accent), the same mix-based approach theme.ts's mermaidTheme() already uses, so a
// heatmap looks like *this chart's* theme rather than a generic default — a deliberate choice,
// not a fidelity gap.
export type ColorScale = (key: string) => string

/** Vega-Lite's own encoding.sort values this renderer now implements, confirmed empirically
 *  against real Vega output (not assumed), one spec at a time:
 *  - undefined/'ascending' (the default): ascending alphabetical, not first-appearance order
 *    (a domain of "Jan"/"Feb" sorts to ["Feb","Jan"]).
 *  - 'descending': the exact reverse of ascending.
 *  - null: first-appearance (encounter) order in the data's own row order — not ascending.
 *  - an explicit array: the listed values in the given order, then every other value in the
 *    domain in *its own encounter order* (not ascending) — confirmed with a partial array
 *    (sort: ['B'] on rows arriving C, A, B domains to [B, C, A], the unlisted C/A keeping their
 *    own C-then-A encounter order, not switching to alphabetical).
 *  A sort-by-another-field object ({field, op}) is a real, separate Vega-Lite form (aggregate
 *  another field per category, order by that) — not implemented; callers must still guard for it
 *  themselves (isImplementedSort) and fall back, same discipline as every other NotShippedYet. */
export type EncodingSort = undefined | 'ascending' | 'descending' | null | ReadonlyArray<string | number | boolean>

export function sortedDomain(values: Iterable<string>, sort?: EncodingSort): string[] {
  const encounterOrder = [...new Set(values)]
  if (sort === null) return encounterOrder
  if (Array.isArray(sort)) {
    const listed = sort.map(String).filter(value => encounterOrder.includes(value))
    const listedSet = new Set(listed)
    return [...listed, ...encounterOrder.filter(value => !listedSet.has(value))]
  }
  const ascending = [...encounterOrder].sort((a, b) => a.localeCompare(b))
  return sort === 'descending' ? ascending.reverse() : ascending
}

/** Whether a mark file's own guard should accept this /sort value — classify.ts's own
 *  isAllowedSort is the admission gate (object/null/descending/array only pass when the
 *  channel's declaration says sort:true at all); this is the second half, checking which of
 *  those sortedDomain actually implements (every form except the sort-by-field object). */
export function isImplementedSort(sort: unknown): boolean {
  return sort === undefined || sort === 'ascending' || sort === 'descending' || sort === null || Array.isArray(sort)
}

/** palette[i % palette.length] for the i-th value of the *sorted* domain — exactly matching
 *  Vega-Lite's categorical scale when config.range.category is set to the theme's own palette
 *  (vegaTheme(), confirmed empirically to produce the identical hex values). */
let seriesDomain: readonly string[] | undefined
/** Synchronous renderer-owned palette history; never survives a preparation transaction. */
export function withSeriesDomain<T>(domain: readonly string[] | undefined, draw:()=>T): T {
  const previous=seriesDomain;seriesDomain=domain
  try{return draw()}finally{seriesDomain=previous}
}
export function categoricalStyleIndex(key: string, domain: readonly string[]): number {
  return Math.max(0, (seriesDomain ?? domain).indexOf(key))
}
export function categoricalColorScale(domain: readonly string[], palette: readonly string[]): ColorScale {
  const colors = new Map((seriesDomain ?? domain).map((key, i) => [key, palette[i % palette.length]]))
  return (key: string) => colors.get(key) ?? palette[0]!
}

function hexToRgb(hex: string): [number, number, number] {
  const n = parseInt(hex.slice(1), 16)
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255]
}

/** Linear interpolation between two hex colors, t in [0, 1] — the same approach theme.ts's
 *  mermaidTheme() already uses for its own two-color ramps. */
export function mixHex(a: string, b: string, t: number): string {
  const [ar, ag, ab] = hexToRgb(a), [br, bg, bb] = hexToRgb(b)
  const channel = (x: number, y: number) => Math.round(x + (y - x) * t)
  return '#' + [channel(ar, br), channel(ag, bg), channel(ab, bb)].map(c => Math.max(0, Math.min(255, c)).toString(16).padStart(2, '0')).join('')
}

/** A continuous color scale from a quantitative domain to the theme's own surface2->accent ramp
 *  (§3.9) — not Vega's default scheme, by design. */
export function continuousColorScale(domain: readonly [number, number], surface: string, accent: string): (value: number) => string {
  const [lo, hi] = domain
  return (value: number) => {
    const t = hi === lo ? 0.5 : Math.max(0, Math.min(1, (value - lo) / (hi - lo)))
    return mixHex(surface, accent, t)
  }
}

export interface MarkStyle { color?: string; opacity?: number }

/** BLG-452 review follow-up (task 2, item 1): a mark-object literal /mark/color and /mark/opacity
 *  — confirmed empirically against real Vega output (not assumed): /mark/color sets *fill* for a
 *  filled mark (bar, area, point's own circle/square variants, text, arc, boxplot's own box) and
 *  *stroke* for a stroke-only mark (line, rule, point's own hollow default) — the caller decides
 *  which, this just extracts the literal value. /mark/opacity is a single generic SVG `opacity`
 *  attribute (not fill-opacity/stroke-opacity separately), applied regardless of fill vs. stroke.
 *  Only takes effect when there is no data-driven color encoding for that mark (Vega-Lite's own
 *  default color is what this overrides, not a per-category scale) — each mark's own draw
 *  function is responsible for only calling this in that case. */
export function markStyle(mark: unknown): MarkStyle {
  if (!mark || typeof mark !== 'object') return {}
  const value = mark as {color?: unknown; opacity?: unknown}
  return {
    color: typeof value.color === 'string' ? value.color : undefined,
    opacity: typeof value.opacity === 'number' && Number.isFinite(value.opacity) ? value.opacity : undefined,
  }
}
