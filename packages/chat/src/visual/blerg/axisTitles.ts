import {withAxisOptions, type AxisOptions} from './axisOptions'
// Shared unit/layer guide layout reserves titles and rotated labels. Guide options
// are scoped to the synchronous mark draw; marks receive the reserved inner rectangle.
import {wrapMeasured} from '../textLayout'
import {measureText} from './layout'
import {currentPlotBounds, recordPlotBounds, group, text, probeSvgLayout} from './svg'
import type {VisualTheme} from '../theme'
import type {TooltipRegion} from './tooltip'

interface AxisField {axis?: AxisOptions}
interface AxisEncoding {x?: AxisField; y?: AxisField}

export function withAxisTitles<T extends AxisEncoding, R extends {svg: string; tooltipRegions: TooltipRegion[]}>(
  encoding: T, width: number, height: number, theme: VisualTheme,
  draw: (clean: T, width: number, height: number) => R,
): R {
  const labels = (title: unknown, span: number) => typeof title === 'string' && title.length
    ? wrapMeasured(title, Math.max(24, span), value => measureText(theme, value, 'bold')) : []
  const ys = labels(encoding.y?.axis?.title, height - 80)
  const left = ys.length ? (ys.length + 1) * theme.fontSize + 8 : 0
  const xs = labels(encoding.x?.axis?.title, width - left - 24)
  const titleBottom = xs.length ? (xs.length + 1) * theme.fontSize + 8 : 0
  const clean = {...encoding}
  // Suppress mark-local defaults: this shared layout owns the authored titles.
  for (const axis of ['x', 'y'] as const) {
    if (encoding[axis]?.axis) clean[axis] = {...encoding[axis], axis: {title: null}}
  }
  let overflow = 0
  if (encoding.x?.axis?.labelAngle === -45) {
    // Tick values and formatting belong to the mark's guide pass. Measure that
    // pass at the final width, discarding its temporary scene before drawing.
    probeSvgLayout(() => withAxisOptions(encoding,
      () => draw(clean, Math.max(1, width - left), Math.max(1, height - titleBottom)),
      value => { overflow = Math.max(overflow, value) }))
  }
  const bottom = titleBottom + overflow
  const body = withAxisOptions(encoding, () => draw(clean, Math.max(1, width - left), Math.max(1, height - bottom)))
  if (!left && !bottom) return body
  const plot = currentPlotBounds()
  if (plot) recordPlotBounds({...plot, x: plot.x + left})
  const cx = left + (plot ? plot.x + plot.width / 2 : (width - left) / 2)
  const cy = plot ? plot.y + plot.height / 2 : (height - bottom) / 2
  const style = {'font-weight': 'bold', fill: theme.fg, 'font-size': theme.fontSize, 'font-family': theme.fontFamily, 'text-anchor': 'middle'}
  const titles = [
    ...xs.map((label, i) => text(cx, height - (xs.length - i) * theme.fontSize - 4, label,
      {...style, 'data-axis-title': 'x', 'data-scene-key': `axis-title:x:${i}`})),
    ...ys.map((label, i) => {
      const x = (i + 1) * theme.fontSize
      return text(x, cy, label, {...style, transform: `rotate(-90 ${x} ${cy})`,
        'data-axis-title': 'y', 'data-scene-key': `axis-title:y:${i}`})
    }),
  ]
  return {...body,
    svg: group([
      group([body.svg], {transform: `translate(${left},0)`, 'data-scene-key': 'axis-content'}),
      group(titles, {'data-scene-key': 'axis-titles'}),
    ]),
    tooltipRegions: body.tooltipRegions.map(region => ({...region, x1: region.x1 + left, x2: region.x2 + left})),
  }
}
