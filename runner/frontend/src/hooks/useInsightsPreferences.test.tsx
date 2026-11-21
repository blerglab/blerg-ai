import { act, renderHook } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import { useInsightsPreference } from './useInsightsPreferences'
const identity = vi.hoisted(() => ({ account: 'first' }))
vi.mock('../lib/liveBoardCard', () => ({ signedInAccount: () => identity.account }))
beforeEach(() => { identity.account = 'first'; localStorage.clear() })
it('restores each person’s filters without sharing them with another person', () => {
  const view = renderHook(() => useInsightsPreference('range', '7d'))
  act(() => view.result.current[1]('30d'))
  identity.account = 'second'
  view.rerender()
  expect(view.result.current[0]).toBe('7d')
  act(() => view.result.current[1]('90d'))
  identity.account = 'first'
  view.rerender()
  expect(view.result.current[0]).toBe('30d')
  view.unmount()
  const restored = renderHook(() => useInsightsPreference('range', '7d'))
  expect(restored.result.current[0]).toBe('30d')
})
it('keeps anonymous choices in memory and tolerates unavailable storage', () => {
  identity.account = ''
  const view = renderHook(() => useInsightsPreference('range', '7d'))
  act(() => view.result.current[1]('30d'))
  expect(view.result.current[0]).toBe('30d')
  expect(localStorage.length).toBe(0)
})
