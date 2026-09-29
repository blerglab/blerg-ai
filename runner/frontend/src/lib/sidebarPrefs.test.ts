import { describe, it, expect, beforeEach } from 'vitest'
import { getFocus, setFocus, getCollapsed, setCollapsed } from './sidebarPrefs'

const FOCUS_KEY = 'blerg-runner.sidebar.focus'
const COLLAPSED_KEY = 'blerg-runner.sidebar.collapsed'

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

  describe('getCollapsed / setCollapsed', () => {
    it('returns empty object when key is absent', () => {
      expect(getCollapsed()).toEqual({})
    })

    it('round-trips a collapsed map', () => {
      const map = { d1: true, d2: false }
      setCollapsed(map)
      expect(getCollapsed()).toEqual(map)
    })

    it('returns empty object on garbage JSON', () => {
      localStorage.setItem(COLLAPSED_KEY, '{{bad')
      expect(getCollapsed()).toEqual({})
    })

    it('returns empty object if stored value is an array (not a plain object)', () => {
      localStorage.setItem(COLLAPSED_KEY, JSON.stringify(['d1', 'd2']))
      expect(getCollapsed()).toEqual({})
    })

    it('returns empty object if stored value is null JSON', () => {
      localStorage.setItem(COLLAPSED_KEY, JSON.stringify(null))
      expect(getCollapsed()).toEqual({})
    })

    it('persists the map to localStorage', () => {
      setCollapsed({ d1: true })
      expect(JSON.parse(localStorage.getItem(COLLAPSED_KEY)!)).toEqual({ d1: true })
    })
  })
})
