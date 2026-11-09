// BLG-452 design doc §3.4/§3.10: the plot rectangle every mark draws into, and the margin
// reserved for its axes. The left margin is sized from the *actual rendered width* of the axis's
// own tick labels — not a guessed constant — because that's what Vega does too, and matching it
// is what makes the differential gate's absolute-pixel comparison possible at all (§3.10's own
// finding: jsdom estimates text width instead of measuring it; real measurement needs a real
// browser, which is exactly where this renders and where the gate runs).
import {BoundedCache} from '../boundedCache'
import { recordPlotBounds } from './svg'
import type { VisualTheme } from '../theme'

export interface PlotLayout { x: number; y: number; width: number; height: number }

const TICK_LENGTH = 5
const LABEL_GAP = 4
const MARGIN = {right: 12, top: 12}

const metrics = new BoundedCache<number>(256 << 10, 2048)
export function clearTextMetrics(): void { metrics.clear() }
let measurer: SVGTextElement | undefined

/** The real rendered width of a label at the chart's own font, matching how a real browser (and
 *  therefore Vega's own axis layout) measures it. Falls back to a rough estimate where real SVG
 *  text metrics aren't available (e.g. jsdom) — rendering still works there, just not pixel-exact
 *  against Vega, which is why the differential gate itself only runs in a real browser. */
export function measureText(theme: VisualTheme, value: string, weight = 'normal'): number {
  const key = JSON.stringify([theme.fontFamily, theme.fontSize, weight, value])
  const cached = metrics.get(key)
  if (cached !== undefined) return cached
  if (typeof document === 'undefined') return value.length * theme.fontSize * 0.6
  try {
    if (!measurer?.isConnected) {
      const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg')
      svg.setAttribute('style', 'position:absolute;visibility:hidden;width:0;height:0;overflow:hidden')
      measurer = document.createElementNS('http://www.w3.org/2000/svg', 'text')
      svg.append(measurer)
      document.body.append(svg)
    }
    measurer.setAttribute('font-size', String(theme.fontSize))
    measurer.setAttribute('font-family', theme.fontFamily)
    measurer.setAttribute('font-weight', weight)
    measurer.textContent = value
    const width = measurer.getComputedTextLength()
    if (Number.isFinite(width) && width > 0) { metrics.set(key, width, 8); return width }
  } catch { /* real SVG text metrics unavailable in this environment */ }
  return value.length * theme.fontSize * 0.6
}

/** Font bounds in baseline coordinates, for projecting a rotated tick label. */
export function measureTextBounds(theme: VisualTheme, value: string): {x:number; y:number; width:number; height:number} {
  const width = measureText(theme, value)
  try {
    if (measurer?.isConnected && typeof measurer.getBBox === 'function') {
      measurer.setAttribute('font-size', String(theme.fontSize))
      measurer.setAttribute('font-family', theme.fontFamily)
      measurer.setAttribute('font-weight', 'normal')
      measurer.textContent = value
      const bounds = measurer.getBBox()
      if (bounds.height > 0) return {x:bounds.x, y:bounds.y, width:bounds.width, height:bounds.height}
    }
  } catch (error) {
    console.warn('Native text bounds unavailable', {cause:error instanceof Error ? error.name : 'UnknownError'})
  }
  return {x:0, y:-theme.fontSize * .8, width, height:theme.fontSize}
}

/** width/height are the whole chart's drawn size; yTickLabels are the left axis's own generated
 *  tick label strings (computed from the data before layout, since the left margin depends on
 *  their rendered width). */
export function plotLayout(width: number, height: number, yTickLabels: readonly string[], theme: VisualTheme): PlotLayout {
  const left = (yTickLabels.length ? Math.max(...yTickLabels.map(label => measureText(theme, label))) : 0) + TICK_LENGTH + LABEL_GAP + 2
  const bottom = theme.fontSize + TICK_LENGTH + LABEL_GAP + 4
  const bounds = {
    x: left, y: MARGIN.top,
    width: Math.max(1, width - left - MARGIN.right),
    height: Math.max(1, height - MARGIN.top - bottom),
  }
  recordPlotBounds(bounds)
  return bounds
}

// BLG-452 review follow-up (margin parity, so line/area's densityTickCountIsStable guard rarely
// fires): these four replace plotLayout's own constants above for the marks whose x channel is
// itself continuous (point/circle/square/text/line/area) — measured directly against Vega's own
// rendered SVG (real getBoundingClientRect() boxes on tick/label/title elements, not scenegraph
// bounds, which are relative to a nested local frame and easy to misread — a mistake made and
// caught earlier in this same investigation), at several widths/heights/domains/label contents.
// Confirmed fixed (not width-, domain- or label-content-dependent) by varying each independently:
//   top = 15.5px, always (nothing else sits above the plot; this is Vega's own base reservation,
//     not derived from text metrics)
//   right = 22.5px, always — tested against both a 3-char and a 7-character last x-label and
//     against widths from 200 to 640px; never moved
//   bottom = TICK_LENGTH + (one label row, fontSize + 2) + 15.5, +17 more when the x-axis draws
//     its own title (field name) — Vega's default, unless the spec's own encoding sets
//     axis.title to null
//   left = (widest y-label's own measured width) + 23.8, +17 more when the y-axis draws its own
//     title — same default/override rule, mirrored
// The +17 (both sides) is the title row itself (one more line of label-sized text plus its own
// small gap) — confirmed by toggling axis.title: null and diffing the two real renders, not
// guessed. These numbers are calibrated against this theme's own fontSize (13); not re-verified
// at another one, since the app has exactly one.
const CONTINUOUS_X_MARGIN = {top: 15.5, right: 22.5, base: 15.5, leftBase: 23.8, titleReserve: 17}

function hasAxisTitle(def: {axis?: {title?: unknown}} | undefined): boolean {
  return def?.axis?.title !== null
}

/** The same plot rectangle as plotLayout, but for a mark whose x channel is itself continuous —
 *  see CONTINUOUS_X_MARGIN's own header for where these numbers come from and why they replace
 *  plotLayout's fixed MARGIN/bottom constants here specifically (not for every mark: a nominal/
 *  ordinal x axis's own label rotation behavior isn't covered by this measurement, so bar/rule/
 *  boxplot/rect keep using plain plotLayout, unchanged). */
export function plotLayoutContinuousX(
  width: number, height: number, yTickLabels: readonly string[], theme: VisualTheme,
  xEncoding: {axis?: {title?: unknown}} | undefined, yEncoding: {axis?: {title?: unknown}} | undefined,
): PlotLayout {
  const widestYLabel = yTickLabels.length ? Math.max(...yTickLabels.map(label => measureText(theme, label))) : 0
  const left = widestYLabel + CONTINUOUS_X_MARGIN.leftBase + (hasAxisTitle(yEncoding) ? CONTINUOUS_X_MARGIN.titleReserve : 0)
  const bottom = TICK_LENGTH + (theme.fontSize + 2) + CONTINUOUS_X_MARGIN.base + (hasAxisTitle(xEncoding) ? CONTINUOUS_X_MARGIN.titleReserve : 0)
  const bounds = {
    x: left, y: CONTINUOUS_X_MARGIN.top,
    width: Math.max(1, width - left - CONTINUOUS_X_MARGIN.right),
    height: Math.max(1, height - CONTINUOUS_X_MARGIN.top - bottom),
  }
  recordPlotBounds(bounds)
  return bounds
}
