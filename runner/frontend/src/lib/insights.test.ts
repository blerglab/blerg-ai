import { describe, it, expect } from 'vitest'
import { formatDuration, formatTokens, formatUSD, formatPercent, stepLabel, outcomeLabel, shortDay, formatTick, timeAxis } from './insights'

describe('formatDuration', () => {
  it('reads naturally at every scale', () => {
    expect(formatDuration(0.85)).toBe('850 ms')
    expect(formatDuration(4)).toBe('4 s')
    expect(formatDuration(4.5)).toBe('4.5 s')
    expect(formatDuration(42.3)).toBe('42 s')
    expect(formatDuration(60)).toBe('1 min')
    expect(formatDuration(200)).toBe('3 min 20 s')
    expect(formatDuration(7500)).toBe('2 h 5 min')
    expect(formatDuration(7200)).toBe('2 h')
    expect(formatDuration(3 * 86400 + 4 * 3600)).toBe('3 d 4 h')
  })
  it('is "—" for nonsense', () => {
    expect(formatDuration(-1)).toBe('—')
    expect(formatDuration(NaN)).toBe('—')
  })
})

describe('formatTokens', () => {
  it('abbreviates', () => {
    expect(formatTokens(950)).toBe('950')
    expect(formatTokens(1234)).toBe('1.23k')
    expect(formatTokens(12_345)).toBe('12.3k')
    expect(formatTokens(123_456)).toBe('123k')
    expect(formatTokens(440_000)).toBe('440k')
    expect(formatTokens(100_000)).toBe('100k')
    expect(formatTokens(100_000_000)).toBe('100M')
    expect(formatTokens(20_000_000)).toBe('20M')
    expect(formatTokens(10_000)).toBe('10k')
    expect(formatTokens(36_738_204)).toBe('36.7M')
    expect(formatTokens(4_600_000)).toBe('4.6M')
    expect(formatTokens(1_200_000_000)).toBe('1.2B')
    expect(formatTokens(0)).toBe('0')
  })
})

describe('formatUSD', () => {
  it('shows cents, whole dollars when large, and a dash for no estimate', () => {
    expect(formatUSD(null)).toBe('—')
    expect(formatUSD(undefined)).toBe('—')
    expect(formatUSD(0)).toBe('$0.00')
    expect(formatUSD(0.004)).toBe('< $0.01')
    expect(formatUSD(12.345)).toBe('$12.35')
    expect(formatUSD(1234.5)).toBe('$1,235')
  })
})

describe('labels', () => {
  it('names steps, outcomes, days and fractions', () => {
    expect(stepLabel('clone')).toBe('Cloning repo')
    expect(stepLabel('mystery')).toBe('mystery')
    expect(outcomeLabel('stopped_by_user')).toBe('Ended by a person')
    expect(outcomeLabel('some_new_reason')).toBe('some new reason')
    expect(shortDay('2026-10-01')).toBe('Oct 1')
    expect(formatPercent(0.876)).toBe('88%')
  })
})

const step = (p50: number, max: number, min = 0) => ({ p50, max, min, p25: min })

describe('timeAxis', () => {
  it('is linear from zero to a round top with up to six even ticks', () => {
    const a = timeAxis([step(20, 75, 2), step(4, 10, 1)])
    expect(a.kind).toBe('linear')
    expect(a.ticks).toEqual([0, 15, 30, 45, 60, 75])
    expect(a.pos(37.5)).toBeCloseTo(0.5)
    expect(a.pos(500)).toBe(1) // clamps
  })

  it('stays linear while the slowest start is within 20 times the largest median', () => {
    expect(timeAxis([step(10, 190)]).kind).toBe('linear')
  })

  it('goes log when one start is more than 20 times the largest median', () => {
    const a = timeAxis([step(10, 1500, 2), step(1, 3, 0.4)])
    expect(a.kind).toBe('log')
    // from the round tick under the fastest start (0.4 s) to the one over the slowest, thinned to every other
    expect(a.ticks).toEqual([0.1, 1, 15, 300, 3600])
    expect(a.pos(0)).toBe(0)
    expect(a.pos(1500)).toBeLessThanOrEqual(1)
    expect(a.pos(10)).toBeGreaterThan(a.pos(1))
    expect(a.pos(100) - a.pos(10)).toBeCloseTo(a.pos(10) - a.pos(1)) // equal ratios, equal distances
  })

  it('has an axis even with nothing slower than zero', () => {
    const a = timeAxis([step(0, 0)])
    expect(a.kind).toBe('linear')
    expect(a.pos(0)).toBe(0)
  })
})

describe('formatTick', () => {
  it('labels ticks compactly', () => {
    expect([0, 0.5, 15, 60, 90, 300, 3600, 5400].map(formatTick)).toEqual(['0', '500 ms', '15 s', '1 min', '1.5 min', '5 min', '1 h', '1.5 h'])
  })
})
