import { afterEach, describe, expect, it, vi } from 'vitest'
import { browserWidths } from './browserWidths'

afterEach(() => vi.unstubAllEnvs())

describe('browser viewport phases', () => {
  it('retains every existing viewport by default and in the final gate', () => {
    vi.stubEnv('VITE_BLERG_TEST_PHASE', 'final')
    expect(browserWidths([390, 1440, 3840])).toEqual([390, 1440, 3840])
    expect(browserWidths([390, 1440, 3840], 'final')).toEqual([390, 1440, 3840])
    expect(browserWidths([390, 3840], 'unknown')).toEqual([390, 3840])
  })
  it('selects a representative existing viewport only in the touched phase', () => {
    expect(browserWidths([390, 1440, 3840], 'touched')).toEqual([390])
    expect(browserWidths([1440, 3840], 'touched')).toEqual([1440])
  })
  it('uses the phase supplied by the focused test command', () => {
    vi.stubEnv('VITE_BLERG_TEST_PHASE', 'touched')
    expect(browserWidths([390, 3840])).toEqual([390])
  })
  it('rejects an empty matrix instead of silently omitting layout coverage', () => {
    expect(() => browserWidths([], 'final')).toThrow('at least one width')
    expect(() => browserWidths([], 'touched')).toThrow('at least one width')
  })
})
