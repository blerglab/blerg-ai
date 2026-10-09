import { describe, expect, it } from 'vitest'
import { absoluteLabel, dayKey, dayLabel, formatElapsed, parseTs, relativeLabel, turnDuration } from './timeLabel'

const en = { locale: 'en-US', timeZone: 'America/New_York' }
// Recent ICU puts a narrow no-break space before AM/PM.
const norm = (s: string) => s.replace(/[\u202f\u00a0]/g, ' ')
const rel = (ms: number, now: number, o = en) => norm(relativeLabel(ms, now, o))

// Fri 13 Mar 2026, 14:00 in New York (EDT).
const NOW = Date.parse('2026-03-13T18:00:00Z')
const ago = (ms: number) => NOW - ms
const S = 1000
const M = 60 * S
const H = 60 * M

describe('parseTs', () => {
  it('parses ISO strings and numbers, rejects the rest', () => {
    expect(parseTs('2026-03-13T18:00:00Z')).toBe(NOW)
    expect(parseTs(NOW)).toBe(NOW)
    expect(parseTs('')).toBeNull()
    expect(parseTs(undefined)).toBeNull()
    expect(parseTs(null)).toBeNull()
    expect(parseTs('not a date')).toBeNull()
    expect(parseTs(NaN)).toBeNull()
  })
})

describe('relativeLabel', () => {
  it('reads "just now" under a minute', () => {
    expect(rel(ago(0), NOW)).toBe('just now')
    expect(rel(ago(59 * S), NOW)).toBe('just now')
  })

  it('switches to minutes at 60 s and hours at 60 min', () => {
    expect(rel(ago(60 * S), NOW)).toBe('1 min ago')
    expect(rel(ago(4 * M + 20 * S), NOW)).toBe('4 min ago')
    expect(rel(ago(59 * M + 59 * S), NOW)).toBe('59 min ago')
    expect(rel(ago(60 * M), NOW)).toBe('1 hr ago')
    expect(rel(ago(23 * H + 59 * M), NOW)).toBe('23 hr ago')
  })

  it('names yesterday with the time, past 24 h', () => {
    expect(rel(NOW - 24 * H, NOW)).toBe('yesterday 2:00 PM')
    expect(rel(Date.parse('2026-03-12T13:30:00Z'), NOW)).toBe('yesterday 9:30 AM')
  })

  it('names the weekday within the last week', () => {
    expect(rel(Date.parse('2026-03-09T18:00:00Z'), NOW)).toBe('Mon 2:00 PM')
    expect(rel(Date.parse('2026-03-07T18:00:00Z'), NOW)).toBe('Sat 1:00 PM')
  })

  it('gives the date once it is a week old or more', () => {
    expect(rel(Date.parse('2026-03-06T18:00:00Z'), NOW)).toBe('Mar 6 1:00 PM')
    expect(rel(Date.parse('2025-12-31T18:00:00Z'), NOW)).toBe('Dec 31, 2025 1:00 PM')
  })

  it('follows the locale (24 h clock, day-first)', () => {
    const de = { locale: 'de-DE', timeZone: 'Europe/Berlin' }
    expect(norm(relativeLabel(Date.parse('2026-03-06T18:00:00Z'), NOW, de))).toBe('6. März 19:00')
  })

  it('clamps a timestamp from the future (clock skew) to "just now"', () => {
    expect(rel(NOW + 5 * M, NOW)).toBe('just now')
  })

  it('returns an empty string for unusable input, never Invalid Date or NaN', () => {
    expect(relativeLabel(NaN, NOW, en)).toBe('')
    expect(relativeLabel(NOW, NaN, en)).toBe('')
    expect(absoluteLabel(NaN, en)).toBe('')
  })

  it('counts calendar days across a DST change, not 24 h blocks', () => {
    // US clocks jumped forward on Sun 8 Mar 2026: Sat 20:00 EST to Sun 21:00
    // EDT is 24 h of wall time but 24 h + 1 h of calendar; still "yesterday".
    const sat = Date.parse('2026-03-08T01:00:00Z') // Sat 7 Mar 20:00 EST
    const sunNight = Date.parse('2026-03-09T01:00:00Z') // Sun 8 Mar 21:00 EDT
    expect(rel(sat, sunNight)).toBe('yesterday 8:00 PM')
    // Mon 9th, 00:30 EDT, versus Sun 8th, 23:30 EDT: 1 h apart, so hours.
    expect(rel(Date.parse('2026-03-09T03:30:00Z'), Date.parse('2026-03-09T04:30:00Z'))).toBe('1 hr ago')
  })
})

describe('absoluteLabel', () => {
  it('spells out the full local date and time', () => {
    expect(norm(absoluteLabel(NOW, en))).toBe('Friday, March 13, 2026 at 2:00:00 PM')
  })
})

describe('dayKey and dayLabel', () => {
  it('changes at local midnight, not UTC midnight', () => {
    const before = Date.parse('2026-03-14T03:59:00Z') // 23:59 EDT on the 13th
    const after = Date.parse('2026-03-14T04:01:00Z') // 00:01 EDT on the 14th
    expect(dayKey(before, en)).not.toBe(dayKey(after, en))
    expect(dayKey(before, en)).toBe(dayKey(NOW, en))
  })

  it('captions Today, Yesterday, and older days', () => {
    expect(dayLabel(NOW, NOW, en)).toBe('Today')
    expect(dayLabel(NOW - 24 * H, NOW, en)).toBe('Yesterday')
    expect(dayLabel(Date.parse('2026-03-09T18:00:00Z'), NOW, en)).toBe('Mon, Mar 9')
    expect(dayLabel(Date.parse('2025-03-09T18:00:00Z'), NOW, en)).toBe('Sun, Mar 9, 2025')
    expect(dayLabel(NaN, NOW, en)).toBe('')
  })
})

describe('formatElapsed', () => {
  it('reads m:ss, and h:mm:ss past an hour, clamping negatives', () => {
    expect(formatElapsed(0)).toBe('0:00')
    expect(formatElapsed(59 * S)).toBe('0:59')
    expect(formatElapsed(3599 * S)).toBe('59:59')
    expect(formatElapsed(3600 * S + 5 * S)).toBe('1:00:05')
    expect(formatElapsed(-5 * S)).toBe('0:00')
  })
})

describe('turnDuration', () => {
  it('formats short and long turns', () => {
    expect(turnDuration(0, 42 * S)).toBe('took 42 s')
    expect(turnDuration(0, 185 * S)).toBe('took 3 min 5 s')
    expect(turnDuration(0, 180 * S)).toBe('took 3 min')
    expect(turnDuration(0, 62 * M)).toBe('took 1 h 2 min')
  })

  it('says nothing when an end is unknown or the span is negative', () => {
    expect(turnDuration(null, 5)).toBe('')
    expect(turnDuration(5, null)).toBe('')
    expect(turnDuration(10, 5)).toBe('')
  })
})
