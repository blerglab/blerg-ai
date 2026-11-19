// BLG-471 §3/§7: no graph solver — a gantt is a time axis and one row per task, closer to
// blergSequence/layout.ts's own hand-rolled pass than blergFlow's dagre pass (design doc §7's own
// words). `pxPerDay` is the one knob zoom (render.ts) reruns this at — never a CSS transform on
// the old layout, so a zoomed-in gantt is a different layout solution, same discipline
// blergFlow/render.ts's own fill-scale loop already applies to font size.
import type { GanttGraph, GanttTask } from './ast'
import { measureLabel } from '../blergFlow/sizing'

export interface GanttBar {
  task: GanttTask
  x: number
  width: number
  y: number
  height: number
}
export interface GanttSectionLane { name: string; y: number; height: number }
export interface GanttAxisTick { x: number; date: Date; label: string }

export interface GanttLayout {
  width: number
  height: number
  laneColumnWidth: number
  rowHeight: number
  chartLeft: number
  axisY: number
  minDate: Date
  maxDate: Date
  pxPerDay: number
  bars: readonly GanttBar[]
  lanes: readonly GanttSectionLane[]
  axisTicks: readonly GanttAxisTick[]
  /** `null`: `now` falls outside `[minDate, maxDate]` — no today line to draw. */
  todayX: number | null
}

const MS_PER_DAY = 86_400_000
const MARGIN = 16
const MIN_PX_PER_DAY = 4
const MAX_PX_PER_DAY = 200
export const DEFAULT_PX_PER_DAY = 28

export function clampPxPerDay(value: number): number {
  return Math.max(MIN_PX_PER_DAY, Math.min(MAX_PX_PER_DAY, value))
}

function daysBetween(from: Date, to: Date): number {
  return (to.getTime() - from.getTime()) / MS_PER_DAY
}

const MONTH_NAMES = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']
const DAY_NAMES = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat']
const pad2 = (n: number) => String(n).padStart(2, '0')

/** A small strftime-like subset — enough for the common `axisFormat` tokens agents actually
 *  write (`%m-%d`, `%Y-%m-%d`, `%b %d`). An unrecognized specifier is left literal rather than
 *  failing the parse: axisFormat is cosmetic (design doc §1), never structural. */
export function formatAxisDate(date: Date, axisFormat: string | null): string {
  const pattern = axisFormat ?? '%m-%d'
  return pattern.replace(/%[A-Za-z%]/g, token => {
    switch (token) {
      case '%Y': return String(date.getUTCFullYear())
      case '%y': return pad2(date.getUTCFullYear() % 100)
      case '%m': return pad2(date.getUTCMonth() + 1)
      case '%d': return pad2(date.getUTCDate())
      case '%b': return MONTH_NAMES[date.getUTCMonth()]
      case '%B': return MONTH_NAMES[date.getUTCMonth()]
      case '%a': return DAY_NAMES[date.getUTCDay()]
      case '%A': return DAY_NAMES[date.getUTCDay()]
      case '%%': return '%'
      default: return token
    }
  })
}

function axisStepDays(totalDays: number): number {
  if (totalDays <= 14) return 1
  if (totalDays <= 60) return 7
  if (totalDays <= 240) return 30
  return 90
}

export function layoutGantt(graph: GanttGraph, pxPerDay: number, fontSize: number, now: Date): GanttLayout {
  const scale = fontSize / 14
  const rowHeight = Math.round(32 * scale)
  const margin = Math.round(MARGIN * scale)
  const titleHeight = graph.title ? Math.round(28 * scale) : 0
  const axisHeight = Math.round(24 * scale)

  const laneColumnWidth = Math.max(...graph.sections.map(name => measureLabel(name, fontSize).width), Math.round(80 * scale))

  const minDate = new Date(Math.min(...graph.tasks.map(task => task.start.getTime())))
  let maxDate = new Date(Math.max(...graph.tasks.map(task => task.end.getTime())))
  if (maxDate.getTime() <= minDate.getTime()) maxDate = new Date(minDate.getTime() + MS_PER_DAY)
  const totalDays = Math.max(1, daysBetween(minDate, maxDate))

  const chartLeft = laneColumnWidth + margin
  const axisY = titleHeight + axisHeight
  const x = (date: Date) => chartLeft + daysBetween(minDate, date) * pxPerDay

  const bars: GanttBar[] = graph.tasks.map((task, index) => {
    const barX = x(task.start)
    // Review (first round): a milestone diamond at the plain 0.6*rowHeight bar size read as
    // "nearly invisible" next to a full-width task bar — sized closer to the row itself (0.85x,
    // against a plain bar's 0.6x height) so it reads as a real marker, not a stray speck.
    const barWidth = task.milestone ? rowHeight * 0.85 : Math.max(2, (x(task.end) - barX))
    const barHeight = task.milestone ? rowHeight * 0.85 : rowHeight * 0.6
    return { task, x: task.milestone ? barX - barWidth / 2 : barX, width: barWidth, y: axisY + margin + index * rowHeight + (rowHeight - barHeight) / 2, height: barHeight }
  })

  const lanes: GanttSectionLane[] = []
  for (const name of graph.sections) {
    const rows = graph.tasks.filter(task => task.section === name)
    if (rows.length === 0) continue
    const first = graph.tasks.indexOf(rows[0])
    lanes.push({ name, y: axisY + margin + first * rowHeight, height: rows.length * rowHeight })
  }

  const step = axisStepDays(totalDays)
  const axisTicks: GanttAxisTick[] = []
  for (let day = 0; day <= totalDays; day += step) {
    const date = new Date(minDate.getTime() + day * MS_PER_DAY)
    axisTicks.push({ x: x(date), date, label: formatAxisDate(date, graph.axisFormat) })
  }

  const todayX = now.getTime() >= minDate.getTime() && now.getTime() <= maxDate.getTime() ? x(now) : null

  const width = Math.max(1, chartLeft + totalDays * pxPerDay + margin)
  const height = Math.max(1, axisY + margin + graph.tasks.length * rowHeight + margin)

  return { width, height, laneColumnWidth, rowHeight, chartLeft, axisY, minDate, maxDate, pxPerDay, bars, lanes, axisTicks, todayX }
}
