import {axisOptions, axisTickCount, recordBottomOverflow, formatAxisValues, type AxisName} from './axisOptions'
// BLG-452 design doc §3.4: tick marks, labels and a domain line for a band or continuous axis on
// either edge — the minimal shared infrastructure every mark needs to be a usable chart. Tick
// generation for continuous axes reuses d3-array's ticks() directly (§3.4/H6), so tick values
// match Vega's by construction, not tolerance.
import { ticks as d3Ticks } from 'd3-array'
import type { BandScale, LinearScale } from './scales'
import { measureText, measureTextBounds } from './layout'
import { group, line, text } from './svg'
import type { VisualTheme } from '../theme'
import { labelRefKey } from '../refChip'

export interface AxisLayout { x: number; y: number; width: number; height: number }

const TICK_LENGTH = 5
const LABEL_GAP = 4
// Review finding (reported against real Vega output: 11 y-ticks drawn here vs. Vega's 5 on a
// line chart, 14 vs. 7 on a bar chart): Vega's own default tick count for a continuous axis
// targets about one tick per 40px of the axis's own pixel length — confirmed empirically (not
// assumed) by comparing drawn axis-label counts at several widths/heights against this formula's
// own result, fed through the same d3-array ticks() call both renderers use (so where the target
// lands on a "nice" step, the resulting values are identical, not just close).
const PIXELS_PER_TICK = 40
// BLG-467 review: Vega-Lite's own ~1-tick-per-40px density rule, carried through unbounded, drew
// dozens of half-step y-ticks on a chart filled to a tall tab/full-screen pane (densityTickCount
// never saw an axis anywhere near that long before this renderer could fill arbitrary space) —
// readable at a message's usual few hundred px, nonsense at a few thousand. Capped the same way
// at both ends: Vega itself draws no more than this many on an axis we size (vega.ts's own
// tickCount override, vegaBudget.ts-adjacent but a readability choice, not a security one).
export const MAX_TICKS = 12

export function continuousTicks(domain: readonly [number, number], count = 10): number[] {
  return d3Ticks(domain[0], domain[1], count)
}

/** Vega's own default tick-count target for a continuous axis of this pixel length. Vega's own
 *  compiled output literally carries `tickCount: {signal: "ceil(width/40)"}` — confirmed by
 *  reading vega-lite's own `compile()` result, not assumed — so `ceil`, not `round`, is Vega's
 *  real formula. Tried switching to it directly: it immediately regressed rule.y (a case that
 *  passed under round()'s wider tolerance for our own margin being slightly off, since round()
 *  only flips the bracket when the error crosses a half-step, where ceil flips at any overshoot
 *  at all) — our own margin isn't measured to pixel parity with Vega's on *every* axis (the left
 *  margin is, via real text measurement; this renderer's fixed top/bottom/right constants aren't,
 *  and closing that gap for axis-label-row height and title reservation turned out to be its own,
 *  separate investigation, not a quick follow-on to the x-axis-width one). Kept as round() —
 *  matching Vega's own formula exactly isn't a net improvement while our margin approximation
 *  has slack on both sides of it; marks/line.ts's own quantitative x channel instead detects when
 *  that slack could flip the bracket and falls back to Vega for those specs, rather than
 *  guessing. Capped at MAX_TICKS (own header): every differential gate's own fixed sizes (640px
 *  or narrower) land well under the cap already, so it never bites there. */
export function densityTickCount(axisLength: number): number {
  return Math.min(MAX_TICKS, Math.max(2, Math.round(axisLength / PIXELS_PER_TICK)))
}

/** Vega's own real formula (`ceil(axisLength/40)`, see densityTickCount's own header) — safe to
 *  use directly only where the caller's own plot dimension is itself measured to close to pixel
 *  parity with Vega's real one (margin parity follow-up: layout.ts's plotLayoutContinuousX, for
 *  the marks whose x channel is continuous). Everywhere else keeps densityTickCount's own
 *  round(), which tolerates this renderer's looser, unmeasured margin constants better.
 *
 *  BLG-467 review: *not* capped at MAX_TICKS, unlike densityTickCount — this is the one path
 *  measured to real pixel parity with Vega (the whole reason it exists, this function's own
 *  header), and GeneratedCorpus.differential.browser.test.tsx's own tick-value gate proves why:
 *  capping it changed which "nice" d3-array step a domain lands on, so the capped count no longer
 *  matched Vega's own real (uncapped) tick values at the gate's fixed 640px width — confirmed by
 *  running it, not assumed safe. The report this round was specifically the y-axis (bar's own
 *  nominal-x chart, where x draws no continuous ticks at all); a quantitative x filled to a very
 *  wide pane (line/point/text) can still draw a dense axis here — a real follow-up, not covered
 *  by this fix. */
export function preciseDensityTickCount(axisLength: number): number {
  return Math.max(2, Math.ceil(axisLength / PIXELS_PER_TICK))
}

// Margin parity follow-up: plotLayoutContinuousX's own width is now measured to within a few px
// of Vega's real one (layout.ts's own CONTINUOUS_X_MARGIN header has the measurements) — but
// *a few px either way*, not consistently one direction like the pre-parity gap this constant
// used to cover (25-37px, always an overshoot) — confirmed directly: line's own real width now
// lands almost exactly on this renderer's own (ourWidth 556 vs. Vega's 559 for one case), while
// area's real width *exceeds* this renderer's own by about 6px for the shared domain the two
// marks render through identical code (556 vs. 562) — the two marks' real margins differ by
// just enough to land on opposite sides of ours. Symmetric, unlike the pre-parity check: a
// one-sided check (shrink-only, matching the old overshoot-only gap) missed area's own case
// entirely. 10 is small enough that BlergAxisDensity's own narrow-width regression case (which
// only becomes unstable past a +30px perturbation, confirmed in the pre-parity investigation)
// still comes out stable, large enough to cover the ~6px residual measured here.
const MARGIN_UNCERTAINTY_PX = 10

/** True when continuousTicks' own result for this domain wouldn't change if the real axis
 *  length were anywhere within MARGIN_UNCERTAINTY_PX of our own computed one — the plausible
 *  margin-approximation error a plot dimension not yet measured to exact pixel parity with
 *  Vega's own can still carry (see the constant's own header). A mark whose plot dimension
 *  depends on one of those constants should check this before trusting its own tick result, and
 *  fall back to Vega rather than risk drawing a tick count Vega wouldn't — narrower than falling
 *  back for every quantitative-x spec, since most domains/sizes land well inside a "nice step"
 *  bracket, not on its edge. Takes the same formula the caller itself renders with
 *  (preciseDensityTickCount for a margin-matched dimension, densityTickCount otherwise) —
 *  checking stability under a different formula than what's actually drawn would prove
 *  nothing. */
export function densityTickCountIsStable(axisLength: number, domain: readonly [number, number], formula: (axisLength: number) => number = densityTickCount): boolean {
  const reference = continuousTicks(domain, formula(axisLength)).length
  const lower = continuousTicks(domain, formula(Math.max(1, axisLength - MARGIN_UNCERTAINTY_PX))).length
  const upper = continuousTicks(domain, formula(axisLength + MARGIN_UNCERTAINTY_PX)).length
  return lower === reference && upper === reference
}

function decimalPlacesOf(value: number): number {
  if (!Number.isFinite(value)) return 0
  const text = Math.abs(value).toString()
  if (text.includes('e')) return Math.max(0, -Number(text.split('e')[1]))
  const dot = text.indexOf('.')
  return dot === -1 ? 0 : text.length - dot - 1
}

/** Vega-Lite's own default tick label format — confirmed empirically (not assumed), not an SI
 *  prefix: every tick in a series is formatted to the same decimal precision (so
 *  "0.0"/"0.1"/.../"0.4" for a 0.1 step, not a bare "0"), with the locale's thousands grouping
 *  for large values ("25,000", "2,500,000"). Precision comes from each tick's own decimal
 *  representation, not from subtracting adjacent ticks (values[1] - values[0]) — float
 *  subtraction on d3-array's own "nice" values can reintroduce the rounding error those values
 *  were specifically constructed to avoid (e.g. 1.4 - 1.2 as a raw float, not as the printed
 *  decimal). */
export function formatTicks(values: readonly number[], axis:AxisName='y'): string[] {
  const decimals = values.length ? Math.max(...values.map(decimalPlacesOf)) : 0
  const formatter = new Intl.NumberFormat('en-US', {minimumFractionDigits: decimals, maximumFractionDigits: decimals})
  return formatAxisValues(values, () => values.map(value => formatter.format(value)), axis)
}

/** A bottom band axis: one tick + label per category, positioned at each band's center. */
export function drawBandAxisBottom(scale: BandScale, layout: AxisLayout, theme: VisualTheme): string {
  const y = layout.y + layout.height
  const domainLine = line(layout.x, y, layout.x + layout.width, y, {stroke: theme.border})
  // BLG-485: a category naming a card or commit (the common "one bar per card" axis) gets the
  // same chip chat text already has — refChip.ts's own whole-label check (not re-derived here),
  // ChipOverlay.tsx finds `data-blerg-chip-key` and lays the hovercard/click proxy over it once
  // the store actually resolves it; still a plain label otherwise.
  let previousRight = -Infinity
  const ticks = scale.domain.map(key => {
    const x = (scale(key) ?? 0) + scale.bandwidth / 2
    const labelWidth = measureText(theme, key)
    const visible = x - labelWidth / 2 >= previousRight + LABEL_GAP
    if (visible) previousRight = x + labelWidth / 2
    if (visible) measureBottomOverflow(theme, key)
    const chip = visible ? labelRefKey(key) : null
    return group([
      ...(axisOptions('x').grid ? [line(x, layout.y, x, y, {stroke:theme.border,'stroke-opacity':0.35,'data-grid':'x'})] : []),
      line(x, y, x, y + TICK_LENGTH, {stroke: theme.border}),
      text(x, y + TICK_LENGTH + LABEL_GAP + theme.fontSize * 0.8, key, {
        fill: chip ? theme.accent : theme.muted, 'text-decoration': chip ? 'underline' : undefined,
        ...bottomLabelStyle(x, y + TICK_LENGTH + LABEL_GAP + theme.fontSize * 0.8), 'font-size': theme.fontSize, 'font-family': theme.fontFamily,
        visibility: visible ? undefined : 'hidden', 'aria-hidden': visible ? undefined : 'true',
      }),
    ], {'data-scene-key': `tick:${key}`, 'data-blerg-chip-key': chip ?? undefined, 'aria-label': chip ? `Open ${key}` : undefined})
  })
  return group([domainLine, ...ticks], {'data-axis':'x'})
}

/** A left continuous axis: one tick + label per generated tick value, gridlines are drawn by the
 *  caller (marks/*.ts) since they span the whole plot width, not just the axis column. Tick
 *  count defaults to Vega's own density rule (densityTickCount, ~1 per 40px of axis length) —
 *  the review finding that this always drew the full default-10 ticks regardless of height. */
export function drawLinearAxisLeft(scale: LinearScale, domain: readonly [number, number], layout: AxisLayout, theme: VisualTheme, tickCount?: number): string {
  const x = layout.x
  const domainLine = line(x, layout.y, x, layout.y + layout.height, {stroke: theme.border})
  const values = continuousTicks(domain, axisTickCount('y', tickCount ?? densityTickCount(layout.height)))
  const labels = formatTicks(values)
  const ticks = values.map((value, i) => {
    const y = scale(value)
    return group([
      line(x - TICK_LENGTH, y, x, y, {stroke: theme.border}),
      text(x - TICK_LENGTH - LABEL_GAP, y + theme.fontSize * 0.3, labels[i], {fill: theme.muted, transform:axisOptions('y').labelAngle===-45 ? `rotate(-45 ${x - TICK_LENGTH - LABEL_GAP} ${y + theme.fontSize * 0.3})` : undefined, 'text-anchor': 'end', 'font-size': theme.fontSize, 'font-family': theme.fontFamily}),
    ], {'data-scene-key': `tick:${value}`})
  })
  return group([domainLine, ...ticks], {'data-axis':'y'})
}

/** A bottom continuous axis — the horizontal counterpart of drawLinearAxisLeft, for marks (point,
 *  line, area, rule) whose x channel is quantitative rather than banded. Tick count defaults to
 *  Vega's own density rule the same way. */
export function drawLinearAxisBottom(scale: LinearScale, domain: readonly [number, number], layout: AxisLayout, theme: VisualTheme, tickCount?: number): string {
  const y = layout.y + layout.height
  const domainLine = line(layout.x, y, layout.x + layout.width, y, {stroke: theme.border})
  const values = continuousTicks(domain, axisTickCount('x', tickCount ?? densityTickCount(layout.width)))
  const labels = formatTicks(values, 'x')
  const ticks = values.map((value, i) => {
    const x = scale(value)
    measureBottomOverflow(theme, labels[i])
    return group([
      ...(axisOptions('x').grid ? [line(x, layout.y, x, y, {stroke:theme.border,'stroke-opacity':0.35,'data-grid':'x'})] : []),
      line(x, y, x, y + TICK_LENGTH, {stroke: theme.border}),
      text(x, y + TICK_LENGTH + LABEL_GAP + theme.fontSize * 0.8, labels[i], {fill: theme.muted, ...bottomLabelStyle(x, y + TICK_LENGTH + LABEL_GAP + theme.fontSize * 0.8), 'font-size': theme.fontSize, 'font-family': theme.fontFamily}),
    ], {'data-scene-key': `tick:${value}`})
  })
  return group([domainLine, ...ticks], {'data-axis':'x'})
}

function bottomLabelStyle(x:number,y:number) {return axisOptions('x').labelAngle===-45 ? {'text-anchor':'end',transform:`rotate(-45 ${x} ${y})`} : {'text-anchor':'middle'}}

function measureBottomOverflow(theme:VisualTheme, label:string):void {
  if (axisOptions('x').labelAngle !== -45) return
  const bounds = measureTextBounds(theme, label), advance = measureText(theme, label)
  const bottom = bounds.y + bounds.height
  // Rotated labels are end-anchored. Project the bottom-left corner around
  // the baseline anchor, then subtract the unrotated label's descent.
  recordBottomOverflow(Math.max(0, (advance - bounds.x + bottom) * Math.SQRT1_2 - bottom))
}
