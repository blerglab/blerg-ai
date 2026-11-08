import { describe, expect, it } from 'vitest'
import { parseGantt } from './parse'
import { layoutGantt, formatAxisDate, clampPxPerDay, DEFAULT_PX_PER_DAY } from './layout'

const NOW = new Date('2025-01-09T00:00:00Z')
const SOURCE = 'gantt\n title Release plan\n dateFormat YYYY-MM-DD\n axisFormat %m-%d\n section Design\n Plan :done, a, 2025-01-06, 2d\n Build :crit, active, b, after a, 3d\n Ship :milestone, after b, 0d'

describe('layoutGantt', () => {
  it('lays out one row per task, in source order', () => {
    const graph = parseGantt(SOURCE)!
    const layout = layoutGantt(graph, DEFAULT_PX_PER_DAY, 14, NOW)
    expect(layout.bars).toHaveLength(3)
    expect(layout.bars[0].y).toBeLessThan(layout.bars[1].y)
    expect(layout.bars[1].y).toBeLessThan(layout.bars[2].y)
  })

  it('places a dependent task\'s bar starting exactly where its dependency ends', () => {
    const graph = parseGantt(SOURCE)!
    const layout = layoutGantt(graph, DEFAULT_PX_PER_DAY, 14, NOW)
    const [plan, build] = layout.bars
    expect(build.x).toBeCloseTo(plan.x + plan.width, 0)
  })

  it('a bigger pxPerDay widens the chart without changing the row count — a real relayout, not a scale', () => {
    const graph = parseGantt(SOURCE)!
    const narrow = layoutGantt(graph, 10, 14, NOW)
    const wide = layoutGantt(graph, 40, 14, NOW)
    expect(wide.width).toBeGreaterThan(narrow.width)
    expect(wide.bars).toHaveLength(narrow.bars.length)
    expect(wide.rowHeight).toBe(narrow.rowHeight)
  })

  it('groups lanes by section, spanning exactly their own tasks\' rows', () => {
    const graph = parseGantt(SOURCE)!
    const layout = layoutGantt(graph, DEFAULT_PX_PER_DAY, 14, NOW)
    expect(layout.lanes).toHaveLength(1)
    expect(layout.lanes[0].name).toBe('Design')
    expect(layout.lanes[0].height).toBe(layout.rowHeight * 3)
  })

  it('places a today line only when `now` falls within the task range', () => {
    const graph = parseGantt(SOURCE)!
    expect(layoutGantt(graph, DEFAULT_PX_PER_DAY, 14, NOW).todayX).not.toBeNull()
    expect(layoutGantt(graph, DEFAULT_PX_PER_DAY, 14, new Date('2020-01-01')).todayX).toBeNull()
  })

  it('clampPxPerDay bounds zoom to a sane range', () => {
    expect(clampPxPerDay(0)).toBeGreaterThan(0)
    expect(clampPxPerDay(1_000_000)).toBeLessThan(1_000_000)
  })

  it('formatAxisDate applies %Y/%m/%d/%b tokens and leaves an unknown specifier literal', () => {
    const date = new Date('2025-03-04T00:00:00Z')
    expect(formatAxisDate(date, '%Y-%m-%d')).toBe('2025-03-04')
    expect(formatAxisDate(date, '%b %d')).toBe('Mar 04')
    expect(formatAxisDate(date, '%q')).toBe('%q')
    expect(formatAxisDate(date, null)).toBe('03-04')
  })
})
