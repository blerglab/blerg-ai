// BLG-452 design doc §3.4: legend layout — categorical swatches and a continuous gradient ramp,
// reserved as a right-margin column the same way layout.ts measures real label width for the
// left axis margin. Vega's own legend row/padding constants aren't reverse-engineered pixel-for-
// pixel (same documented-tolerance approach as the axis margin, §3.6's bar gate); what must match
// exactly is content: the right category/color pairing and the title/label text.
import {categoricalStyleIndex} from './color'
import { continuousTicks } from './axes'
import { measureText } from './layout'
import { element, group, line, rect, text } from './svg'
import type { ColorScale } from './color'
import type { VisualTheme } from '../theme'
import { labelRefKey } from '../refChip'

const SWATCH = 10
const ROW_HEIGHT = 16
const TITLE_HEIGHT = 18
const GAP = 6

export function categoricalLegendHeight(count: number, theme: VisualTheme): number {
  return Math.max(TITLE_HEIGHT, theme.fontSize * 2) + count * Math.max(ROW_HEIGHT, theme.fontSize + 3)
}

/** How much horizontal space a legend needs — the plot's right margin, design doc §3.4. */
export function legendWidth(labels: readonly string[], title: string | undefined, theme: VisualTheme): number {
  const widest = Math.max(measureText(theme, title ?? ''), ...labels.map(label => measureText(theme, label)), 0)
  return SWATCH + GAP + widest + GAP
}

/** Review finding: every legend used the same filled-square swatch regardless of which mark it
 *  belonged to — confirmed empirically (not assumed) that Vega's own line-chart legend draws a
 *  short *stroke* symbol instead (shape:"stroke", fill:transparent), not a filled square. 'stroke'
 *  here is for exactly that case (line/area's own categorical color legend); every other mark
 *  keeps the filled-swatch default. */
export function drawCategoricalLegend(domain: readonly string[], colorScale: ColorScale, x: number, y: number, theme: VisualTheme, title?: string, symbol: 'swatch' | 'stroke' | 'interval' = 'swatch'): string {
  const titleEl = title ? text(x, y + theme.fontSize, title, {fill: theme.fg, 'font-size': theme.fontSize, 'font-family': theme.fontFamily, 'font-weight': 'bold'}) : ''
  // BLG-485: a series named for a card or commit gets the same chip chat text already has —
  // ChipOverlay.tsx finds the mark below once the store resolves it.
  const rows = domain.map((key, i) => {
    const rowY = y + Math.max(TITLE_HEIGHT, theme.fontSize * 2) + i * Math.max(ROW_HEIGHT, theme.fontSize + 3)
    const mark = symbol === 'stroke'
      ? line(x, rowY + SWATCH / 2, x + SWATCH, rowY + SWATCH / 2, {stroke: colorScale(key), 'stroke-width': 2, 'data-legend-swatch': 'true'})
      : rect(x, rowY, SWATCH, SWATCH, {fill: colorScale(key), stroke: symbol === 'interval' ? theme.fg : undefined, 'stroke-dasharray': symbol === 'interval' && categoricalStyleIndex(key,domain) ? `${categoricalStyleIndex(key,domain)+1} 2` : undefined, 'data-legend-swatch': 'true'})
    const chip = labelRefKey(key)
    return group([
      mark,
      text(x + SWATCH + GAP, rowY + SWATCH * 0.8, key, {
        fill: chip ? theme.accent : theme.fg, 'text-decoration': chip ? 'underline' : undefined,
        'font-size': theme.fontSize, 'font-family': theme.fontFamily,
      }),
    ], {'data-scene-key': `series:${key}`, 'data-blerg-chip-key': chip ?? undefined, 'aria-label': chip ? `Open ${key}` : undefined})
  })
  return group([titleEl, ...rows], {'data-scene-key': 'legend'})
}

/** A continuous color legend: a vertical gradient swatch with one label per tick, the same tick
 *  values (d3-array's ticks(), §3.4/H6) a real axis would show — confirmed against Vega's own
 *  legend scenegraph, which labels ticks within the domain (e.g. 2/4/6/8 for a 1..9 domain), not
 *  just the domain's own min/max. */
export function drawContinuousLegend(domain: readonly [number, number], colorAt: (value: number) => string, x: number, y: number, height: number, theme: VisualTheme, title?: string, integersOnly = false): string {
  const titleEl = title ? text(x, y + theme.fontSize, title, {fill: theme.fg, 'font-size': theme.fontSize, 'font-family': theme.fontFamily, 'font-weight': 'bold'}) : ''
  const gradientId = `blerg-legend-gradient-${Math.random().toString(36).slice(2)}`
  const titleHeight = Math.max(TITLE_HEIGHT, theme.fontSize * 2)
  const gradientTop = y + titleHeight
  const gradientHeight = Math.max(1, height - titleHeight)
  const [lo, hi] = domain
  const stopCount = 10
  const stops = Array.from({length: stopCount + 1}, (_, i) => {
    const t = i / stopCount
    return element('stop', {offset: `${t * 100}%`, 'stop-color': colorAt(hi - (hi - lo) * t)})
  }).join('')
  const gradient = element('defs', {}, [element('linearGradient', {id: gradientId, x1: 0, y1: 0, x2: 0, y2: 1}, [stops])])
  const bar = rect(x, gradientTop, SWATCH, gradientHeight, {fill: `url(#${gradientId})`, 'data-legend-swatch': 'true'})
  const ticks = hi === lo ? [lo] : continuousTicks(domain, Math.max(2, Math.min(20, Math.floor(gradientHeight / (theme.fontSize * 2))))).filter(tick => (!integersOnly || Number.isInteger(tick)) && tick >= lo && tick <= hi)
  let previousY = Infinity
  const labels = ticks.flatMap(tick => {
    const t = hi === lo ? 0.5 : (tick - lo) / (hi - lo)
    const labelY = gradientTop + gradientHeight * (1 - t)
    if (previousY - labelY < theme.fontSize * 1.5) return []
    previousY = labelY
    return [text(x + SWATCH + GAP, labelY + theme.fontSize * 0.3, String(tick), {fill: theme.muted, 'font-size': theme.fontSize, 'font-family': theme.fontFamily})]
  })
  return group([titleEl, gradient, bar, group(labels)])
}
