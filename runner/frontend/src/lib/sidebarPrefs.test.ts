import { describe, it, expect, beforeEach } from 'vitest'
import { getFocus, setFocus } from './sidebarPrefs'

const FOCUS_KEY = 'blerg-runner.sidebar.focus'

describe('sidebarPrefs', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  describe('getFocus / setFocus', () => {
    it('returns null when key is absent', () => {
      expect(getFocus()).toBeNull()
    })

    it('round-trips a daemon id', () => {
      setFocus('daemon-1')
      expect(getFocus()).toBe('daemon-1')
    })

    it('round-trips the cluster focus', () => {
      setFocus('cluster')
      expect(getFocus()).toBe('cluster')
    })

    it('returns null after setFocus(null)', () => {
      setFocus('daemon-1')
      setFocus(null)
      expect(getFocus()).toBeNull()
    })

    it('returns null on garbage JSON', () => {
      localStorage.setItem(FOCUS_KEY, 'not-json{{{')
      expect(getFocus()).toBeNull()
    })

    it('returns null if stored value is not a string', () => {
      localStorage.setItem(FOCUS_KEY, JSON.stringify(42))
      expect(getFocus()).toBeNull()
    })

    it('removes the key from localStorage when setFocus(null)', () => {
      setFocus('daemon-1')
      setFocus(null)
      expect(localStorage.getItem(FOCUS_KEY)).toBeNull()
    })
  })
})
