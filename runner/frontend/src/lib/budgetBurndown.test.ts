import { describe, expect, it } from 'vitest'
import { buildBurndown, WEEK } from './budgetBurndown'
import type { UsageBudgetSample } from './insights'

const DAY = 24 * 3600_000

function sample(partial: Partial<UsageBudgetSample> & { atMs: number; windowStartMs: number; resetsAtMs: number; usedPercent: number }): UsageBudgetSample {
  return {
    provider: 'claude', window_id: '7d',
    used_percent: partial.usedPercent,
    window_start: new Date(partial.windowStartMs).toISOString(),
    resets_at: new Date(partial.resetsAtMs).toISOString(),
    sampled_at: new Date(partial.atMs).toISOString(),
    ...partial,
  }
}

describe('buildBurndown', () => {
  it('returns null for a provider with no samples (a metered engine)', () => {
    expect(buildBurndown([], 'claude')).toBeNull()
    expect(buildBurndown([sample({ atMs: 0, windowStartMs: 0, resetsAtMs: WEEK, usedPercent: 5, provider: 'codex' })], 'claude')).toBeNull()
  })

  it('reads the window start and reset from the current window, with an even-pace reference line', () => {
    const start = Date.parse('2026-11-01T00:00:00Z')
    const reset = start + WEEK
    const series = buildBurndown([sample({ atMs: start + 3 * 3600_000, windowStartMs: start, resetsAtMs: reset, usedPercent: 10 })], 'claude')
    expect(series).not.toBeNull()
    expect(series!.windowStart).toBe(start)
    expect(series!.resetsAt).toBe(reset)
    expect(series!.pace).toEqual([{ at: start, value: 0 }, { at: reset, value: 100 }])
  })

  it('handles 0% used: an actual point at zero, no misreading as "no data"', () => {
    const start = Date.parse('2026-11-01T00:00:00Z')
    const reset = start + WEEK
    const series = buildBurndown([sample({ atMs: start + 10 * 60_000, windowStartMs: start, resetsAtMs: reset, usedPercent: 0 })], 'claude')
    expect(series!.actual).toEqual([{ points: [{ at: start + 10 * 60_000, value: 0 }] }])
    // Under an hour of evidence: no projection yet, same floor as the existing weekly-pace text.
    expect(series!.projection).toBeNull()
  })

  it('splits the actual trace at a gap where usage was not sampled', () => {
    const start = Date.parse('2026-11-01T00:00:00Z')
    const reset = start + WEEK
    const rows = [
      sample({ atMs: start + 10 * 60_000, windowStartMs: start, resetsAtMs: reset, usedPercent: 2 }),
      sample({ atMs: start + 20 * 60_000, windowStartMs: start, resetsAtMs: reset, usedPercent: 4 }),
      // A 2-hour gap: the daemon stopped reporting for a while.
      sample({ atMs: start + 2 * 3600_000 + 20 * 60_000, windowStartMs: start, resetsAtMs: reset, usedPercent: 20 }),
    ]
    const series = buildBurndown(rows, 'claude')
    expect(series!.actual).toHaveLength(2)
    expect(series!.actual[0].points).toHaveLength(2)
    expect(series!.actual[1].points).toHaveLength(1)
  })

  it('projects at the current rate and flags when it would cross 100% before reset, with the crossing time', () => {
    const start = Date.parse('2026-11-01T00:00:00Z')
    const reset = start + WEEK
    // 40% used after one day: at this rate, 100% arrives well before the week is out.
    const at = start + DAY
    const series = buildBurndown([sample({ atMs: at, windowStartMs: start, resetsAtMs: reset, usedPercent: 40 })], 'claude')
    expect(series!.projection).not.toBeNull()
    expect(series!.projection!.exceeds).toBe(true)
    // rate = 40%/day; reaches 100% after 2.5 days from window start.
    const expectedCrossing = start + 2.5 * DAY
    expect(series!.projection!.crossingAt).toBeCloseTo(expectedCrossing, -2)
    expect(series!.projection!.points[1].value).toBe(100)
  })

  it('projects without the exceeds flag when the pace stays under the weekly budget', () => {
    const start = Date.parse('2026-11-01T00:00:00Z')
    const reset = start + WEEK
    // 5% used after one day: on pace to land at 35% by reset.
    const at = start + DAY
    const series = buildBurndown([sample({ atMs: at, windowStartMs: start, resetsAtMs: reset, usedPercent: 5 })], 'claude')
    expect(series!.projection!.exceeds).toBe(false)
    expect(series!.projection!.crossingAt).toBeUndefined()
    expect(series!.projection!.points[1].at).toBe(reset)
    expect(series!.projection!.points[1].value).toBeCloseTo(35, 0)
  })

  it('reads a window reset mid-range: only the current window (the latest window_start) feeds the actual trace', () => {
    const previousStart = Date.parse('2026-10-25T00:00:00Z')
    const previousReset = previousStart + WEEK // == currentStart
    const currentStart = previousReset
    const currentReset = currentStart + WEEK
    const rows = [
      sample({ atMs: previousStart + DAY, windowStartMs: previousStart, resetsAtMs: previousReset, usedPercent: 60 }),
      sample({ atMs: previousStart + 5 * DAY, windowStartMs: previousStart, resetsAtMs: previousReset, usedPercent: 95 }),
      sample({ atMs: currentStart + 2 * 3600_000, windowStartMs: currentStart, resetsAtMs: currentReset, usedPercent: 3 }),
    ]
    const series = buildBurndown(rows, 'claude')
    expect(series!.windowStart).toBe(currentStart)
    expect(series!.actual).toEqual([{ points: [{ at: currentStart + 2 * 3600_000, value: 3 }] }])
  })

  it('shows the previous window shifted onto the current window\'s timeline, faintly (as data, not styling)', () => {
    const previousStart = Date.parse('2026-10-25T00:00:00Z')
    const currentStart = previousStart + WEEK
    const currentReset = currentStart + WEEK
    const rows = [
      sample({ atMs: previousStart + DAY, windowStartMs: previousStart, resetsAtMs: currentStart, usedPercent: 50 }),
      sample({ atMs: currentStart + DAY, windowStartMs: currentStart, resetsAtMs: currentReset, usedPercent: 10 }),
    ]
    const series = buildBurndown(rows, 'claude')
    expect(series!.previous).not.toBeNull()
    expect(series!.previous).toEqual([{ points: [{ at: previousStart + DAY + WEEK, value: 50 }] }])
    // Shifted by exactly one window length, so it lands within the current window's domain.
    expect(series!.previous![0].points[0].at).toBeGreaterThanOrEqual(currentStart)
    expect(series!.previous![0].points[0].at).toBeLessThanOrEqual(currentReset)
  })

  it('has no previous window when only the current one has been sampled', () => {
    const start = Date.parse('2026-11-01T00:00:00Z')
    const series = buildBurndown([sample({ atMs: start + 3600_000, windowStartMs: start, resetsAtMs: start + WEEK, usedPercent: 1 })], 'claude')
    expect(series!.previous).toBeNull()
  })
})
