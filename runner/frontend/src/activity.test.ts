import { describe, it, expect, beforeEach, vi } from 'vitest'

vi.mock('./ws', () => import('./test/wsMock'))

import { recordInteraction, resetActivity } from './activity'

describe('activity', () => {
  beforeEach(() => resetActivity())

  it('throttles activity pings to one per window', () => {
    expect(recordInteraction(0)).toBe(true) // first interaction always sends
    expect(recordInteraction(10_000)).toBe(false) // within 30s throttle
    expect(recordInteraction(31_000)).toBe(true) // past the throttle window
  })
})
