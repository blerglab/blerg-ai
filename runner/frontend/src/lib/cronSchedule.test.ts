import { describe, it, expect } from 'vitest'
import { buildExpression, parseExpression, describeSchedule, hasFiveFields } from './cronSchedule'

describe('buildExpression', () => {
  it('builds a daily schedule from one time', () => {
    expect(buildExpression({ kind: 'daily', times: ['08:30'], dow: 1 })).toEqual({ expr: '30 8 * * *' })
  })
  it('builds N times a day, sorted and deduplicated', () => {
    expect(buildExpression({ kind: 'daily', times: ['17:30', '08:30', '12:30', '08:30'], dow: 1 })).toEqual({ expr: '30 8,12,17 * * *' })
  })
  it('builds weekdays and a single weekday', () => {
    expect(buildExpression({ kind: 'weekdays', times: ['07:00'], dow: 1 })).toEqual({ expr: '0 7 * * 1-5' })
    expect(buildExpression({ kind: 'weekly', times: ['07:00'], dow: 0 })).toEqual({ expr: '0 7 * * 0' })
  })
  it('refuses times with different minutes: a five-field expression cannot say them', () => {
    const r = buildExpression({ kind: 'daily', times: ['08:00', '17:30'], dow: 1 })
    expect('error' in r && r.error).toMatch(/same minute/i)
  })
  it('refuses a missing or malformed time', () => {
    expect('error' in buildExpression({ kind: 'daily', times: [], dow: 1 })).toBe(true)
    expect('error' in buildExpression({ kind: 'daily', times: [''], dow: 1 })).toBe(true)
    expect('error' in buildExpression({ kind: 'daily', times: ['25:00'], dow: 1 })).toBe(true)
  })
})

describe('parseExpression', () => {
  it('round-trips what the presets build', () => {
    for (const p of [
      { kind: 'daily' as const, times: ['08:30', '12:30', '17:30'], dow: 1 },
      { kind: 'weekdays' as const, times: ['09:00'], dow: 1 },
      { kind: 'weekly' as const, times: ['06:15'], dow: 3 },
    ]) {
      const b = buildExpression(p)
      expect('expr' in b).toBe(true)
      if ('expr' in b) expect(parseExpression(b.expr)).toMatchObject({ kind: p.kind, times: p.times })
    }
  })
  it('returns null for anything else', () => {
    for (const e of ['*/20 * * * *', '0 8 1 * *', '0 8 * 6 *', '0 8-9 * * *', 'x y z', '61 8 * * *', '0 24 * * *']) {
      expect(parseExpression(e), e).toBeNull()
    }
  })
})

describe('describeSchedule', () => {
  it('says presets in words', () => {
    expect(describeSchedule('30 8 * * *')).toBe('Every day at 08:30')
    expect(describeSchedule('30 8,12,17 * * *')).toBe('3 times a day, at 08:30, 12:30 and 17:30')
    expect(describeSchedule('0 7 * * 1-5')).toBe('Weekdays at 07:00')
    expect(describeSchedule('0 7 * * 0')).toBe('Every Sunday at 07:00')
    expect(describeSchedule('0 9 * * 3')).toBe('Every Wednesday at 09:00')
  })
  it('shows a custom expression as is', () => {
    expect(describeSchedule('*/20 * * * *')).toBe('Custom schedule: */20 * * * *')
  })
})

describe('hasFiveFields', () => {
  it('counts whitespace separated fields', () => {
    expect(hasFiveFields('0 8 * * *')).toBe(true)
    expect(hasFiveFields('  0   8 * * *  ')).toBe(true)
    expect(hasFiveFields('0 8 * *')).toBe(false)
    expect(hasFiveFields('0 0 8 * * *')).toBe(false)
    expect(hasFiveFields('')).toBe(false)
  })
})
