import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { useBlackboardDraft } from './useBlackboardDraft'
import { blackboardOrigin } from '../lib/blackboardChanges'
import { emit } from '../test/wsMock'
import { useSessionStore } from './useSessionStore'
import { makeSession } from '../test/fixtures'

vi.mock('../ws', () => import('../test/wsMock'))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
beforeEach(() => {
 apiFetch.mockReset()
 useSessionStore.setState({ sessions: [makeSession({ id: 'main' }), makeSession({ id: 'channel', parent_session_id: 'main' })] })
})
afterEach(() => { vi.restoreAllMocks() })

it('refreshes a channel for its workspace root while ignoring other workspaces', async () => {
 apiFetch.mockImplementation(async () => new Response(JSON.stringify({ draft: null })))
 const view = renderHook(() => useBlackboardDraft('channel'))
 await waitFor(() => expect(view.result.current).toBeNull())
 act(() => emit({ type: 'blackboard_draft_changed', session_id: 'unrelated' }))
 expect(apiFetch).toHaveBeenCalledTimes(1)
 apiFetch.mockImplementation(async () => new Response(JSON.stringify({ draft: { untrusted_data: { name: 'Channel plan' } } })))
 act(() => emit({ type: 'blackboard_draft_changed', session_id: 'main' }))
 await waitFor(() => expect(view.result.current?.name).toBe('Channel plan'))
 expect(apiFetch).toHaveBeenLastCalledWith('/api/blackboards/draft?session=channel')
})

it('retains the visible draft after a failed refresh and logs the status without its body', async () => {
 const log = vi.spyOn(console, 'error').mockImplementation(() => {})
 apiFetch.mockImplementation(async () => new Response(JSON.stringify({ draft: { untrusted_data: { name: 'Visible plan' } } })))
 const view = renderHook(() => useBlackboardDraft('main'))
 await waitFor(() => expect(view.result.current?.name).toBe('Visible plan'))
 apiFetch.mockImplementation(async () => new Response('private-body-canary', { status: 503 }))
 act(() => emit({ type: 'blackboard_draft_changed', session_id: 'main' }))
 await waitFor(() => expect(log).toHaveBeenCalledWith('Could not read the blackboard draft.', { cause: 'response', status: 503 }))
 expect(view.result.current?.name).toBe('Visible plan')
 expect(JSON.stringify(log.mock.calls)).not.toContain('private-body-canary')
})

it('debounces a burst on the trailing edge', async () => {
 apiFetch.mockImplementation(async () => new Response(JSON.stringify({ draft: null })))
 const view = renderHook(() => useBlackboardDraft('main'))
 await waitFor(() => expect(view.result.current).toBeNull())
 vi.useFakeTimers()
 try {
  act(() => emit({ type: 'blackboard_draft_changed', session_id: 'main' }))
  await act(() => vi.advanceTimersByTimeAsync(150))
  act(() => emit({ type: 'blackboard_draft_changed', session_id: 'main' }))
  await act(() => vi.advanceTimersByTimeAsync(199))
  expect(apiFetch).toHaveBeenCalledTimes(1)
  apiFetch.mockImplementation(async () => new Response(JSON.stringify({ draft: null })))
  await act(() => vi.advanceTimersByTimeAsync(1))
  expect(apiFetch).toHaveBeenCalledTimes(2)
 } finally { vi.useRealTimers() }
})

it('ignores a public workspace event when its draft is not readable', async () => {
 apiFetch.mockImplementation(async () => new Response(JSON.stringify({ draft: null })))
 const view = renderHook(() => useBlackboardDraft('main'))
 await waitFor(() => expect(view.result.current).toBeNull())
 vi.useFakeTimers()
 try {
  act(() => emit({ type: 'blackboard_draft_changed', session_id: 'main', draft_readable: false }))
  await act(() => vi.advanceTimersByTimeAsync(500))
  expect(apiFetch).toHaveBeenCalledTimes(1)
 } finally { vi.useRealTimers() }
})

it('ignores the echo of this browser’s action', async () => {
 apiFetch.mockImplementation(async () => new Response(JSON.stringify({ draft: null })))
 const view = renderHook(() => useBlackboardDraft('main'))
 await waitFor(() => expect(view.result.current).toBeNull())
 vi.useFakeTimers()
 try {
  act(() => emit({ type: 'blackboard_draft_changed', session_id: 'main', origin_id: blackboardOrigin('main') }))
  await act(() => vi.advanceTimersByTimeAsync(500))
  expect(apiFetch).toHaveBeenCalledTimes(1)
 } finally { vi.useRealTimers() }
})

it('refreshes Main for a channel action in the same browser', async () => {
 apiFetch.mockImplementation(async () => new Response(JSON.stringify({ draft: null })))
 const view = renderHook(() => useBlackboardDraft('main'))
 await waitFor(() => expect(view.result.current).toBeNull())
 vi.useFakeTimers()
 try {
  act(() => emit({ type: 'blackboard_draft_changed', session_id: 'main', origin_id: blackboardOrigin('channel') }))
  await act(() => vi.advanceTimersByTimeAsync(200))
  expect(apiFetch).toHaveBeenCalledTimes(2)
 } finally { vi.useRealTimers() }
})
