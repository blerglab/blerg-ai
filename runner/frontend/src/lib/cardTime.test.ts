import { describe, expect, it } from 'vitest'
import { cardTimeGlyphLabel, formatCardTimeDuration, type CardTimeSummary } from './cardTime'

describe('card-time glyph label (BLG-444)', () => {
  it('under budget: no summary at all, nothing to show', () => {
    expect(cardTimeGlyphLabel(undefined)).toBeNull()
    expect(cardTimeGlyphLabel(null)).toBeNull()
  })

  it('total and round count, no review detail yet', () => {
    const s: CardTimeSummary = { seconds: 6 * 3600 + 42 * 60, rounds: 0 }
    expect(cardTimeGlyphLabel(s)).toBe('6h42m worker time')
  })

  it('singular round, with verdict and open finding counts', () => {
    const s: CardTimeSummary = { seconds: 4 * 3600, rounds: 1, verdict: 'changes', openHigh: 1, openMedium: 2 }
    expect(cardTimeGlyphLabel(s)).toBe('4h worker time · 1 review round · changes requested · 1 high, 2 medium open')
  })

  it('plural rounds, approved with nothing open', () => {
    const s: CardTimeSummary = { seconds: 8 * 3600, rounds: 3, verdict: 'approved', openHigh: 0, openMedium: 0 }
    expect(cardTimeGlyphLabel(s)).toBe('8h worker time · 3 review rounds · approved')
  })

  it('rejected verdict reads as rejected, not approved-ish', () => {
    const s: CardTimeSummary = { seconds: 12 * 3600, rounds: 2, verdict: 'rejected', openHigh: 2, openMedium: 0 }
    expect(cardTimeGlyphLabel(s)).toBe('12h worker time · 2 review rounds · rejected · 2 high open')
  })

  it('no open high or medium findings omits that clause entirely, even with a verdict', () => {
    const s: CardTimeSummary = { seconds: 3600, rounds: 1, verdict: 'approved', openHigh: 0, openMedium: 0 }
    expect(cardTimeGlyphLabel(s)).toBe('1h worker time · 1 review round · approved')
  })
})

describe('formatCardTimeDuration', () => {
  it('whole hours with no minutes', () => {
    expect(formatCardTimeDuration(4 * 3600)).toBe('4h')
  })
  it('hours and minutes', () => {
    expect(formatCardTimeDuration(6 * 3600 + 42 * 60)).toBe('6h42m')
  })
  it('under an hour shows minutes only', () => {
    expect(formatCardTimeDuration(45 * 60)).toBe('45m')
  })
  it('zero or negative is empty (callers should not be showing a glyph at all)', () => {
    expect(formatCardTimeDuration(0)).toBe('0m')
  })
})
