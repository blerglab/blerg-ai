import { act, renderHook } from '@testing-library/react'
import { beforeEach, expect, it } from 'vitest'
import { useSessionTabs } from './useSessionTabs'

beforeEach(() => localStorage.clear())
it('shares one desktop preview between chats and files, preserving kept chats', () => {
  const { result } = renderHook(() => useSessionTabs('person', 'dock-preview', false))
  act(() => result.current.open({ kind: 'session-chat', sessionId: 'channel', label: 'Discuss' }))
  expect(result.current.tabs[0].preview).toBe(true)
  act(() => result.current.open({ kind: 'session-chat', sessionId: 'worker', label: 'Review' }))
  expect(result.current.tabs.map(t => t.id)).toEqual(['session-chat:worker'])
  act(() => result.current.keep('session-chat:worker'))
  act(() => result.current.open({ kind: 'workspace', path: 'README.md' }))
  act(() => result.current.open({ kind: 'session-chat', sessionId: 'channel', label: 'Discuss' }))
  expect(result.current.tabs.map(t => [t.id, t.preview])).toEqual([['session-chat:worker', false], ['session-chat:channel', true]])
})
it('keeps phone chat tabs permanent and preserves both chats', () => {
  const { result } = renderHook(() => useSessionTabs('person', 'phone-chats', true))
  act(() => result.current.open({ kind: 'session-chat', sessionId: 'one', label: 'One' }))
  act(() => result.current.open({ kind: 'session-chat', sessionId: 'two', label: 'Two' }))
  expect(result.current.tabs.map(t => [t.id, t.preview])).toEqual([['session-chat:one', false], ['session-chat:two', false]])
})
