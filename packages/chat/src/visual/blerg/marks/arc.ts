// BLG-452 design doc §3.3 table row 3/mark type: arc (pie/donut). v1 scope: theta (a
// quantitative value, slice angle proportional to its share of the total) and color (the
// category); mark.innerRadius makes it a donut. No radius channel yet — NotShippedYet.
import { path, group } from '../svg'
import { categoricalColorScale, isImplementedSort, markStyle, sortedDomain, type EncodingSort } from '../color'
import { tooltipRegionText, type TooltipFieldDef, type TooltipRegion } from '../tooltip'
import type { MarkDeclaration } from '../declare'
import type { VisualTheme } from '../../theme'

export class NotShippedYet extends Error {}

export const declaration: MarkDeclaration = {
  mark: 'arc',
  channels: {
    theta: {types: new Set(['quantitative'])},
    color: {types: new Set(['nominal', 'ordinal']), sort: true},
    tooltip: {types: new Set(['quantitative', 'nominal', 'ordinal', 'temporal'])},
  },
  markProperties: new Set(['innerRadius', 'color', 'opacity']),
}

interface FieldDef { field?: string; type?: string; sort?: EncodingSort; bin?: unknown }
interface ArcEncoding { theta?: FieldDef; color?: FieldDef; radius?: FieldDef; size?: FieldDef; tooltip?: TooltipFieldDef | TooltipFieldDef[] }
interface ArcMark { innerRadius?: number; color?: unknown; opacity?: unknown }

export interface ArcDrawing { svg: string; accessiblePoints: Array<{label: string; value: string}>; tooltipRegions: TooltipRegion[] }

function arcPath(cx: number, cy: number, outerR: number, innerR: number, startAngle: number, endAngle: number): string {
  const point = (r: number, angle: number) => [cx + r * Math.sin(angle), cy - r * Math.cos(angle)]
  const [ox1, oy1] = point(outerR, startAngle), [ox2, oy2] = point(outerR, endAngle)
  const large = endAngle - startAngle > Math.PI ? 1 : 0
  if (innerR <= 0) {
    return `M ${cx},${cy} L ${ox1},${oy1} A ${outerR},${outerR} 0 ${large} 1 ${ox2},${oy2} Z`
  }
  const [ix1, iy1] = point(innerR, startAngle), [ix2, iy2] = point(innerR, endAngle)
  return `M ${ix1},${iy1} L ${ox1},${oy1} A ${outerR},${outerR} 0 ${large} 1 ${ox2},${oy2} L ${ix2},${iy2} A ${innerR},${innerR} 0 ${large} 0 ${ix1},${iy1} Z`
}

export function drawArc(rows: Array<Record<string, unknown>>, encoding: ArcEncoding, mark: ArcMark, width: number, height: number, theme: VisualTheme): ArcDrawing {
  if (encoding.radius) throw new NotShippedYet('arc: radius channel is not implemented yet')
  if (encoding.size) throw new NotShippedYet('arc: size is not implemented yet')
  const thetaField = encoding.theta?.field
  if (typeof thetaField !== 'string') throw new NotShippedYet('arc: missing theta field')
  if (encoding.theta?.bin) throw new NotShippedYet('arc: bin is not implemented yet')
  // Review finding, extended (custom sort): same sort gap as bar.ts's own guard (the color
  // domain, since that's the only thing arc ever sorts) — now implements the same forms.
  if (!isImplementedSort(encoding.color?.sort)) throw new NotShippedYet('arc: this sort is not implemented yet')

  const values = rows.map(row => Number(row[thetaField]))
  const total = values.reduce((a, b) => a + b, 0)
  const cx = width / 2, cy = height / 2
  const margin = 12
  const outerR = Math.max(1, Math.min(width, height) / 2 - margin)
  const innerR = Math.max(0, Math.min(outerR - 1, Number(mark.innerRadius) || 0))

  // Review finding: slices were colored by their row index into the theme palette, not by their
  // category value — two rows sharing a category (or data arriving in a different order) would
  // get different colors on different renders, and never matched Vega's own color-by-domain-value
  // assignment. Confirmed empirically (not assumed): Vega draws slices in the data's own row
  // order (not sorted) but assigns each slice's *color* from the sorted color-domain index — the
  // same categoricalColorScale(sortedDomain(...)) bar.ts/line.ts already use, applied here without
  // reordering the wedges themselves.
  const colorField = encoding.color?.field
  const colorDomain = typeof colorField === 'string' ? sortedDomain(rows.map(row => String(row[colorField])), encoding.color?.sort) : []
  const colorScale = typeof colorField === 'string' ? categoricalColorScale(colorDomain, theme.palette) : undefined
  // Task 2, item 1: a literal /mark/color only overrides the default fill when there's no
  // data-driven color encoding, same rule as bar's; /mark/opacity is a generic SVG attribute.
  const {color: markColor, opacity: markOpacity} = markStyle(mark)
  const defaultFill = markColor ?? theme.accent

  let angle = 0
  const slices: string[] = []
  const hitBoxes: Array<{x1: number; y1: number; x2: number; y2: number}> = []
  for (let i = 0; i < rows.length; i++) {
    const start = angle
    const fraction = total === 0 ? 0 : values[i] / total
    const end = start + fraction * 2 * Math.PI
    angle = end
    const fill = colorScale ? colorScale(String(rows[i][colorField!])) : defaultFill
    slices.push(path({d: arcPath(cx, cy, outerR, innerR, start, end), fill, opacity: markOpacity}))
    // TooltipRegion is axis-aligned, so a wedge's hover target is approximated as a box centered
    // on the slice's midpoint at mid-radius, sized to its angular span — not an exact hit test
    // against the wedge's true shape, but close enough that hovering anywhere visibly inside a
    // slice reaches its tooltip.
    const mid = (start + end) / 2
    const midR = innerR + (outerR - innerR) / 2
    const px = cx + midR * Math.sin(mid), py = cy - midR * Math.cos(mid)
    const half = Math.max(10, Math.min(outerR, midR * (end - start)))
    hitBoxes.push({x1: px - half, y1: py - half, x2: px + half, y2: py + half})
  }

  const accessiblePoints = rows.map((row, i) => ({label: typeof colorField === 'string' ? String(row[colorField]) : `slice ${i + 1}`, value: String(values[i])}))
  const tooltipRegions = hitBoxes.map((box, i) => ({...box, text: tooltipRegionText(encoding.tooltip, rows[i])}))
  return {svg: group(slices), accessiblePoints, tooltipRegions}
}
