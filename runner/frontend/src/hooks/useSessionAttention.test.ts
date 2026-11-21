import { act, renderHook, waitFor } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { useSessionDecisionCounts, useDecisionAttention } from './useSessionAttention'
import { collectAttentionItems } from './useQuestionItems'
import { makeSession } from '../test/fixtures'
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
vi.mock('../ws', () => import('../test/wsMock'))
it('reads every pending decision kind for unopened chats, shares reads, and clears decided attention', async () => {
  useDecisionAttention.setState({ counts: {} })
  let pending = true
  apiFetch.mockImplementation(async (url: string) => ({ ok: true, json: async () => ({ session_id: url.split('/')[3], proposals: pending ? 1 : 0, tool_changes: pending ? 1 : 0, review_requests: pending ? [{ request_id: 'review-one' }] : [], answer_requests: pending ? [{}] : [] }) }))
  const first = renderHook(() => useSessionDecisionCounts(['worker']))
  const second = renderHook(() => useSessionDecisionCounts(['worker']))
  await waitFor(() => expect(first.result.current.worker).toBe(4))
  expect(second.result.current.worker).toBe(4)
  expect(useDecisionAttention.getState().reviews.worker).toEqual([{ request_id: 'review-one' }])
  expect(apiFetch).toHaveBeenCalledTimes(1)
  pending = false
  act(() => window.dispatchEvent(new Event('blerg:review-requests-changed')))
  await waitFor(() => expect(first.result.current.worker).toBe(0))
  expect(useDecisionAttention.getState().reviews.worker).toEqual([])
  first.unmount(); second.unmount()
})
it('keeps parent-handled worker waits quiet, but surfaces orphan, root and channel waits', () => {
  const root = makeSession({ id: 'root', status: 'idle' })
  const worker = makeSession({ id: 'worker', title: 'Delegated', parent_session_id: 'root', started_by: { kind: 'session' }, status: 'waiting' })
  const channel = makeSession({ id: 'channel', title: 'Discussion', parent_session_id: 'root', status: 'waiting' })
  expect(collectAttentionItems([root, worker, channel], [], {}, [], new Set()).map(x => x.session.id)).toEqual(['channel'])
  expect(collectAttentionItems([root, worker], [], { worker: { proposals: 1 } }, [], new Set()).map(x => x.session.id)).toEqual(['worker'])
  expect(collectAttentionItems([worker], [], {}, [], new Set()).map(x => x.session.id)).toEqual(['worker'])
  expect(collectAttentionItems([{ ...root, started_by: { kind: 'agent' }, status: 'waiting' }], [], {}, [], new Set())).toHaveLength(1)

})
it('counts repository requests and refreshes after a decision', async () => {
  useDecisionAttention.setState({ counts: {} })
  let pending = 1
  apiFetch.mockImplementation(async () => ({ ok: true, json: async () => ({ session_id: 'main', repository_requests: pending }) }))
  const hook = renderHook(() => useSessionDecisionCounts(['main']))
  await waitFor(() => expect(hook.result.current.main).toBe(1))
  pending = 0
  act(() => window.dispatchEvent(new Event('blerg:repository-requests-changed')))
  await waitFor(() => expect(hook.result.current.main).toBe(0))
  hook.unmount()
})
