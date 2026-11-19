// BLG-471 §4/§6/§7: the one canonical SVG string this renderer draws, from the parsed graph
// (ast.ts) and its layout (layout.ts) alone — theme tokens only (VisualTheme), same discipline as
// blergFlow/draw.ts and blergSequence/draw.ts. Every stroke/pad below is `* scale` (fontSize /
// 14), carried forward from day one (the lesson blergFlow/blergSequence only adopted after
// review).
import { escapeXml, group, line as svgLine, rect as svgRect, text as svgText } from '../blerg/svg'
import type { GanttGraph } from './ast'
import type { GanttLayout } from './layout'
import type { VisualTheme } from '../theme'
import { labelRefKey } from '../refChip'

export interface DrawnGantt { svg: string; width: number; height: number }

function diamond(cx: number, cy: number, size: number, attrs: Record<string, string | number | undefined | false>): string {
  const half = size / 2
  const points = `${cx},${cy - half} ${cx + half},${cy} ${cx},${cy + half} ${cx - half},${cy}`
  const extra = Object.entries(attrs).filter(([, v]) => v !== undefined && v !== false)
    .map(([k, v]) => ` ${k}="${typeof v === 'string' ? escapeXml(v) : v}"`).join('')
  return `<polygon points="${points}"${extra}/>`
}

const ISO = (date: Date) => date.toISOString().slice(0, 10)
function durationLabel(start: Date, end: Date): string {
  const days = Math.round((end.getTime() - start.getTime()) / 86_400_000)
  return days === 1 ? '1 day' : `${days} days`
}

export function drawGantt(graph: GanttGraph, layout: GanttLayout, theme: VisualTheme): DrawnGantt {
  const fontSize = theme.fontSize
  const scale = fontSize / 14
  const strokeWidth = 1.5 * scale
  const width = Math.max(1, Math.ceil(layout.width))
  const height = Math.max(1, Math.ceil(layout.height))

  const titleEl = graph.title ? svgText(layout.chartLeft, Math.round(18 * scale), graph.title, {
    'font-size': fontSize * 1.1, fill: theme.fg, 'font-weight': 'bold',
  }) : ''

  const laneBg = layout.lanes.map((lane, index) => svgRect(0, lane.y, width, lane.height, {
    fill: index % 2 === 0 ? theme.surface : theme.surface2, 'fill-opacity': 0.5,
  }))
  // Drawn as one group, translated on scroll (render.ts) rather than CSS `position: sticky` —
  // sticky positioning on an element inside an SVG's own coordinate space isn't reliable across
  // browsers the way it is on a regular HTML ancestor. Placed last in `body` below (painted last,
  // on top) so it stays opaque over bars/ticks sliding underneath it while pinned.
  const laneColumn = group([
    svgRect(0, layout.axisY, layout.laneColumnWidth, height - layout.axisY, { fill: theme.surface }),
    ...layout.lanes.map(lane => svgText(Math.round(4 * scale), lane.y + lane.height / 2, lane.name, {
      'font-size': fontSize, fill: theme.fg, 'dominant-baseline': 'middle',
    })),
    svgLine(layout.laneColumnWidth, layout.axisY, layout.laneColumnWidth, height, { stroke: theme.border, 'stroke-width': strokeWidth }),
  ], { 'data-blerg-gantt-lane-column': 'true' })

  const axisTicks = layout.axisTicks.map(tick => group([
    svgLine(tick.x, layout.axisY, tick.x, height, { stroke: theme.border, 'stroke-width': strokeWidth * 0.5, 'stroke-dasharray': `${Math.round(2 * scale)} ${Math.round(3 * scale)}` }),
    svgText(tick.x, layout.axisY - Math.round(6 * scale), tick.label, { 'font-size': fontSize * 0.85, fill: theme.muted, 'text-anchor': 'middle' }),
  ]))

  const depLines = layout.bars.flatMap(bar => bar.task.dependsOn.map(depId => {
    const dep = layout.bars.find(b => b.task.id === depId)
    if (!dep) return ''
    const fromX = dep.x + dep.width
    const fromY = dep.y + dep.height / 2
    const toX = bar.x
    const toY = bar.y + bar.height / 2
    const midX = (fromX + toX) / 2
    const d = `M ${fromX},${fromY} L ${midX},${fromY} L ${midX},${toY} L ${toX},${toY}`
    return `<path d="${d}" fill="none" stroke="${theme.muted}" stroke-width="${strokeWidth}" opacity="0" data-blerg-gantt-dep-line="${escapeXml(depId)}->${escapeXml(bar.task.id)}"/>`
  }))

  const bars = layout.bars.map(bar => {
    const task = bar.task
    const chip = labelRefKey(task.label)
    const fill = task.active ? theme.accent : theme.surface2
    const fillOpacity = task.active ? 0.3 : (task.done ? 0.5 : 1)
    // Review (first round): a milestone used to pick up the same low-contrast surface2/border
    // colors as a plain bar unless it also happened to be `crit` — the "nearly invisible" finding
    // on the far-right diamond. A milestone marks a real event regardless of `crit`, so it always
    // gets the accent token (a filled diamond, not just an outline) and a larger, bolder outline —
    // `crit` on top of that only thickens it further, same as a plain bar's own crit treatment.
    const stroke = task.milestone ? theme.fg : task.crit ? theme.accent : theme.border
    // Highlight toggles `stroke` between these two data attributes (accessibility.ts) rather
    // than needing the theme at that layer — the two colors this shape can ever be outlined with
    // are both already known here, at draw time.
    const highlightAttrs = { 'data-blerg-gantt-stroke-default': stroke, 'data-blerg-gantt-stroke-highlight': theme.accent }
    const shape = task.milestone
      ? diamond(bar.x + bar.width / 2, bar.y + bar.height / 2, bar.height, {
          fill: theme.accent, stroke, 'stroke-width': task.crit ? strokeWidth * 1.6 : strokeWidth,
          'data-blerg-gantt-bar-shape': task.id, ...highlightAttrs,
        })
      : svgRect(bar.x, bar.y, bar.width, bar.height, {
          fill, 'fill-opacity': fillOpacity, stroke, 'stroke-width': task.crit ? strokeWidth * 1.6 : strokeWidth, rx: 3, ry: 3,
          'data-blerg-gantt-bar-shape': task.id, ...highlightAttrs,
        })
    const labelX = task.milestone ? bar.x + bar.width + Math.round(6 * scale) : bar.x + Math.round(6 * scale)
    const labelEl = svgText(labelX, bar.y + bar.height / 2, task.label, {
      'font-size': fontSize * 0.9, fill: chip ? theme.accent : theme.fg, 'text-decoration': chip ? 'underline' : undefined,
      'dominant-baseline': 'middle', 'text-anchor': task.milestone ? 'start' : 'start',
    })
    const ariaLabel = `${task.label}: ${ISO(task.start)} to ${ISO(task.end)}, ${durationLabel(task.start, task.end)}`
      + (task.dependsOn.length ? `, after ${task.dependsOn.join(', ')}` : '')
      + (task.milestone ? ', milestone' : '') + (task.crit ? ', critical' : '') + (task.active ? ', active' : '') + (task.done ? ', done' : '')
    return group([shape, labelEl], {
      'data-blerg-gantt-task': task.id,
      'data-blerg-gantt-depends': task.dependsOn.join(','),
      'data-blerg-chip-key': chip ?? undefined,
      role: chip ? 'button' : 'group',
      tabindex: 0,
      'aria-label': chip ? `Open ${ariaLabel}` : ariaLabel,
    })
  })

  // Review (first round): the "Today" label read as tiny next to the axis tick labels beside it
  // (0.8x font, same weight) — bumped to a full, bold accent label with its own background so it
  // stays readable over whatever lane/tick lines sit behind it, the same contrast-first reasoning
  // the milestone diamond above just got.
  const todayLabelY = layout.axisY - Math.round(6 * scale)
  const todayLabelWidth = Math.round(34 * scale)
  const todayLine = layout.todayX !== null ? group([
    svgLine(layout.todayX, layout.axisY, layout.todayX, height, { stroke: theme.accent, 'stroke-width': strokeWidth * 1.3, 'stroke-dasharray': `${Math.round(4 * scale)} ${Math.round(2 * scale)}` }),
    svgRect(layout.todayX + Math.round(2 * scale), todayLabelY - fontSize, todayLabelWidth, fontSize * 1.2, { fill: theme.surface, 'fill-opacity': 0.9 }),
    svgText(layout.todayX + Math.round(4 * scale), todayLabelY, 'Today', { 'font-size': fontSize, 'font-weight': 'bold', fill: theme.accent }),
  ], { 'data-blerg-gantt-today': 'true' }) : ''

  const body = titleEl + laneBg.join('') + axisTicks.join('') + depLines.join('') + bars.join('') + todayLine + laneColumn
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="${height}" viewBox="0 0 ${width} ${height}" font-family="${escapeXml(theme.fontFamily)}">${body}</svg>`
  return { svg, width, height }
}
