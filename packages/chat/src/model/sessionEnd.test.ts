import { describe, expect, it } from 'vitest'
import { END_REASON } from './sessionEnd'

describe('end reasons', () => {
  it('says why a child stopped when its parent ended', () => {
    expect(END_REASON.parent_ended.text(null, false)).toBe('Stopped when the session that started it ended')
  })
})
