import { act, renderHook, waitFor } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { useQuestionItems, questionItemCount, collectAttentionItems } from './useQuestionItems'
import { useAttentionItems } from './useSessionAttention'
import { attentionCount } from '../lib/sessionPresentation'
import { useSessionStore } from './useSessionStore'
import { useMessageStore } from './useMessageStore'
import { useDecisionAttention } from './useDecisionAttention'
import { useDismissStore } from '../lib/needsYouDismiss'
import { makeSession } from '../test/fixtures'
vi.mock('../ws', () => import('../test/wsMock'))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
it('keeps the badge, card and list items equal for asks, reviews, plan proposals, failed and paused workers', async () => {
  let pending = true
  useSessionStore.setState({ sessions: [makeSession({ id: 'root' }), makeSession({ id: 'failed', parent_session_id: 'root', started_by: { kind: 'session' }, status: 'error' }), makeSession({ id: 'paused', parent_session_id: 'root', started_by: { kind: 'session' }, status: 'idle', paused_reason: 'restart' })] })
  useMessageStore.setState({ messages: ['a', 'b'].map(id => ({ id, session_id: 'root', kind: 'ask', status: 'open', answerable: true, body: id, created_at: '2026-01-01T00:00:00Z' })) })
  apiFetch.mockImplementation(async (url: string) => new Response(JSON.stringify(url.startsWith('/api/blackboards?') ? { blackboards: [{ id: 'board', name: 'Project', milestones: [] }] } : url.startsWith('/api/blackboards/board?') ? { record: { proposals: pending ? [{ id: 'p', status: 'pending', revision: 1, milestone_id: '', changes: [{ id: 'c', operation: 'add', item: { title: 'Work' } }] }] : [] } } : { session_id: url.split('/')[3], review_requests: url.includes('/root/') ? [{ request_id: 'r', artifact_id: 'file', artifact_name: 'Plan' }] : [] })))
  const hook = renderHook(() => ({ list: useQuestionItems('root'), card: useAttentionItems('root') }))
  await waitFor(() => expect(questionItemCount(hook.result.current.list)).toBe(6))
  expect(attentionCount(hook.result.current.card)).toBe(6)
  pending = false
  act(() => window.dispatchEvent(new Event('blerg:proposals-changed')))
  await waitFor(() => expect(questionItemCount(hook.result.current.list)).toBe(5))
  expect(attentionCount(hook.result.current.card)).toBe(5)
  hook.unmount()
})

it('lists two asks and one pending plan proposal as three items, then two after resolution', () => {
  const root = makeSession({ id: 'root', status: 'idle' })
  const messages = ['a', 'b'].map(id => ({ id, session_id: 'root', kind: 'ask' as const, status: 'open' as const, answerable: true, body: id, created_at: '' }))
  const plan = { milestoneName: 'Project', proposal: { id: 'proposal', milestone_id: '', revision: 1, status: 'pending', changes: [] } }
  expect(collectAttentionItems([root], messages, {}, [plan], new Set()).map(item => item.kind)).toEqual(['ask', 'ask', 'plan'])
  expect(collectAttentionItems([root], messages, {}, [], new Set())).toHaveLength(2)
})

it('isolates attention from telemetry and unrelated decisions while retaining visible updates', async () => {
  useSessionStore.setState({ sessions: [makeSession({ id: 'root', status: 'idle' }), makeSession({ id: 'worker', parent_session_id: 'root', started_by: { kind: 'session' }, status: 'running' }), makeSession({ id: 'other', status: 'idle' })] })
  useMessageStore.setState({ messages: [] })
  useDecisionAttention.setState({ counts: {}, reviews: {}, live: {}, plans: {} })
  apiFetch.mockImplementation(async (url: string) => new Response(JSON.stringify(url.startsWith('/api/blackboards?') ? { blackboards: [] } : { session_id: url.split('/')[3], asks: [] })))
  const renders = vi.fn()
  const hook = renderHook(() => { renders(); return useQuestionItems('root') })
  await waitFor(() => expect(useDecisionAttention.getState().live.worker).toBeDefined())
  act(() => useMessageStore.setState({ messages: [{ id: 'ask', session_id: 'root', kind: 'ask', status: 'open', answerable: true, body: 'Continue?', created_at: '' }] }))
  expect(hook.result.current.items.map(item => item.id)).toEqual(['ask:ask'])
  const before = renders.mock.calls.length
  act(() => useSessionStore.setState(s => ({ sessions: s.sessions.map(session => ({ ...session, background_tasks: 3 })) })))
  act(() => useDecisionAttention.setState(s => ({ live: { ...s.live, other: { proposals: 2 } } })))
  act(() => useMessageStore.setState(s => ({ messages: [...s.messages, { id: 'unrelated', session_id: 'other', kind: 'ask', status: 'open', body: 'Other?', created_at: '' }] })))
  expect(renders).toHaveBeenCalledTimes(before)
  act(() => useSessionStore.setState(s => ({ sessions: s.sessions.map(session => session.id === 'root' ? { ...session, title: 'Renamed Main' } : session) })))
  expect(hook.result.current.items[0].session.title).toBe('Renamed Main')
  act(() => useSessionStore.setState(s => ({ sessions: s.sessions.map(session => session.id === 'worker' ? { ...session, status: 'error' } : session) })))
  expect(hook.result.current.items.map(item => item.kind)).toEqual(['ask', 'failed'])
  act(() => useDismissStore.setState({ hidden: new Set(['ask:ask']) }))
  expect(hook.result.current.items.map(item => item.kind)).toEqual(['failed'])
  act(() => useSessionStore.setState(s => ({ sessions: s.sessions.map(session => session.id === 'worker' ? { ...session, parent_session_id: 'other' } : session) })))
  expect(hook.result.current.items).toEqual([])
  act(() => useSessionStore.setState(s => ({ sessions: [...s.sessions, makeSession({ id: 'channel', parent_session_id: 'root', status: 'waiting' })] })))
  expect(hook.result.current.items.map(item => [item.kind, item.session.id])).toEqual([['waiting', 'channel']])
  hook.unmount()
})

it('lists and counts only person-answerable questions, including when a worker is selected', () => {
 const main = makeSession({id:'main'})
 const worker = makeSession({id:'worker',parent_session_id:'main',started_by:{kind:'session'}})
 const channel = makeSession({id:'channel',parent_session_id:'main'})
 const messages = [
  {id:'main-ask',session_id:'main',answerable:true},
  {id:'channel-ask',session_id:'channel',answerable:true},
  {id:'worker-ask',session_id:'worker',answerable:false},
  {id:'escalated',session_id:'worker',answerable:true},
  {id:'unknown',session_id:'worker'},
 ].map(m=>({...m,kind:'ask' as const,status:'open' as const,body:m.id,created_at:''}))
 expect(collectAttentionItems([main,worker,channel],messages,{},[],new Set()).map(i=>i.id)).toEqual(['ask:main-ask','ask:escalated','ask:channel-ask'])
 expect(collectAttentionItems([worker],messages,{},[],new Set()).map(i=>i.id)).toEqual(['ask:escalated'])
 expect(messages).toHaveLength(5) // The worker conversation retains its questions.
})
