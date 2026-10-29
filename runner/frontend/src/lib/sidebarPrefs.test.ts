import { describe, it, expect, beforeEach, vi } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { getFocus, setFocus, useChildrenExpanded, setChildrenExpanded } from './sidebarPrefs'

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

it('updates mounted card and tile choices while leaving unrelated parents alone', () => {
  localStorage.clear()
  const card = renderHook(() => useChildrenExpanded('parent'))
  const tile = renderHook(() => useChildrenExpanded('parent'))
  let siblingRenders = 0
  const sibling = renderHook(() => { siblingRenders++; return useChildrenExpanded('other') })
  const before = siblingRenders
  act(() => setChildrenExpanded('parent', false))
  expect(card.result.current).toBe(false)
  expect(tile.result.current).toBe(false)
  expect(sibling.result.current).toBe(true)
  expect(siblingRenders).toBe(before)
  act(() => setChildrenExpanded('parent', true))
  expect(card.result.current).toBe(true)
  expect(tile.result.current).toBe(true)
})

it.each([false, true])('keeps collapse controls usable when storage writes fail (reads blocked=%s)', blockedReads => {
  localStorage.clear()
  const read = vi.spyOn(localStorage, 'getItem')
  if (blockedReads) read.mockImplementation(() => { throw new Error('blocked') })
  const write = vi.spyOn(localStorage, 'setItem').mockImplementation(() => { throw new Error('blocked') })
  try {
    const card = renderHook(() => useChildrenExpanded('storage-blocked-parent'))
    expect(card.result.current).toBe(true)
    act(() => setChildrenExpanded('storage-blocked-parent', false))
    expect(card.result.current).toBe(false)
    act(() => setChildrenExpanded('storage-blocked-parent', true))
    expect(card.result.current).toBe(true)
  } finally { read.mockRestore(); write.mockRestore() }
})
