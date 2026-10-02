import { describe, it, expect, beforeEach } from 'vitest'
import { getDraft, setDraft } from './drafts'

describe('drafts', () => {
  beforeEach(() => localStorage.clear())
  it('round-trips per session and clears on empty', () => {
    setDraft('a', 'hello')
    setDraft('b', 'other')
    expect(getDraft('a')).toBe('hello')
    expect(getDraft('b')).toBe('other')
    setDraft('a', '')
    expect(getDraft('a')).toBe('')
  })
})
