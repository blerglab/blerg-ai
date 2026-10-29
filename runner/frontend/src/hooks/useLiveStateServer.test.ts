import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import { LIVE_MIN_GAP_MS as GAP, useLiveStateServer } from './useLiveStateServer'
import type { LiveReply } from '../lib/liveState'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn() }))
import { apiFetch } from '../apiFetch'
const fetchMock = vi.mocked(apiFetch)

const URL_OF = '/api/sessions/s1/live'
const reply = (over: Partial<LiveReply> = {}): LiveReply => ({
  session_id: 's1', through_seq: 0, generated_at: new Date().toISOString(), tokens_hidden: false,
  asks: [], subagents: [], tasks: [], tool_changes: 0, counts: { needs_you: 0, running: 0 }, ...over,
})
const res = (status: number, type: string, body: unknown = null, etag = '') => ({
  ok: status >= 200 && status < 300, status,
  headers: new Headers({ ...(type ? { 'Content-Type': type } : {}), ...(etag ? { ETag: etag } : {}) }),
  json: () => (type === 'application/json' ? Promise.resolve(body) : Promise.reject(new SyntaxError('not JSON'))),
}) as unknown as Response
const json = (body: unknown, etag = '"1"', status = 200) => res(status, 'application/json', body, etag)
const flush = () => act(async () => { await vi.advanceTimersByTimeAsync(0); await vi.advanceTimersByTimeAsync(0) })
const wait = (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms) })

let seq = 0
function ingest(kind: string, payload: unknown = {}, sessionId = 's1') {
  seq++
  act(() => {
    useTranscriptStore.getState().ingest({ type: 'agent_event', session_id: sessionId, client_event_id: `x${seq}`, seq, ts: new Date().toISOString(), kind, payload } as AgentEvent)
  })
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date'] })
  fetchMock.mockReset()
  useTranscriptStore.setState({ sessions: {} })
  seq = 0
})
afterEach(() => {
  vi.useRealTimers()
})

describe('useLiveStateServer', () => {
  it('reads nothing while it is off', async () => {
    renderHook(() => useLiveStateServer('s1', false, ''))
    ingest('ask')
    await wait(GAP * 2)
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('reads at once, sends If-None-Match after, and keeps the view on a 304', async () => {
    const r = reply({ through_seq: 7, tool_changes: 1, counts: { needs_you: 1, running: 0 } })
    fetchMock.mockResolvedValueOnce(json(r, '"a"'))
    const { result, rerender } = renderHook(({ k }) => useLiveStateServer('s1', true, k), { initialProps: { k: 'a' } })
    await flush()
    expect(fetchMock).toHaveBeenCalledWith(URL_OF, { headers: {} })
    expect(result.current.status).toBe('ready')
    if (result.current.status !== 'ready') return
    expect(result.current.view.counts).toEqual({ needsYou: 1 })
    fetchMock.mockResolvedValueOnce(res(304, ''))
    rerender({ k: 'b' })
    await wait(GAP)
    expect(fetchMock).toHaveBeenLastCalledWith(URL_OF, { headers: { 'If-None-Match': '"a"' } })
    expect(result.current.status).toBe('ready')
  })

  it('starts reads at least the gap apart and folds early triggers into one trailing read', async () => {
    fetchMock.mockImplementation(() => Promise.resolve(json(reply())))
    renderHook(() => useLiveStateServer('s1', true, ''))
    await flush()
    expect(fetchMock).toHaveBeenCalledTimes(1)
    ingest('ask')
    ingest('subagent_started', { child_id: 'k' })
    await wait(GAP - 100)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    await wait(100)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    await wait(GAP * 2)
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it('reads again for each live kind and a turn end; not for a tool call, a message or a subagent update (folded)', async () => {
    fetchMock.mockImplementation(() => Promise.resolve(json(reply())))
    renderHook(() => useLiveStateServer('s1', true, ''))
    await flush()
    ingest('tool_call')
    ingest('assistant_text')
    ingest('subagent_update')
    await wait(GAP)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const kinds = ['ask', 'subagent_started', 'subagent_done', 'background_task_started', 'background_task_done', 'turn_done']
    for (const kind of kinds) {
      ingest(kind)
      await wait(GAP)
    }
    expect(fetchMock).toHaveBeenCalledTimes(1 + kinds.length)
  })

  it('reads again when the transcript is loaded again (a reconnect), when refresh changes and when the page comes back', async () => {
    fetchMock.mockImplementation(() => Promise.resolve(json(reply())))
    const { rerender } = renderHook(({ k }) => useLiveStateServer('s1', true, k), { initialProps: { k: 'running' } })
    await flush()
    await wait(GAP)
    act(() => { useTranscriptStore.getState().ingestReplayDone('s1', { lastSeq: 0, hasMore: false }) })
    await flush()
    expect(fetchMock).toHaveBeenCalledTimes(2)
    await wait(GAP)
    rerender({ k: 'stopped' })
    await flush()
    expect(fetchMock).toHaveBeenCalledTimes(3)
    await wait(GAP)
    act(() => { window.dispatchEvent(new Event('focus')) })
    await flush()
    expect(fetchMock).toHaveBeenCalledTimes(4)
  })

  it('folds what came after the reply at once, before the next read', async () => {
    fetchMock.mockResolvedValueOnce(json(reply({ through_seq: 0 })))
    let resolve!: (r: Response) => void
    fetchMock.mockImplementation(() => new Promise<Response>(r => { resolve = r }))
    const { result } = renderHook(() => useLiveStateServer('s1', true, ''))
    await flush()
    ingest('ask', { body: 'Ship it?', message_id: 'm1' })
    ingest('subagent_started', { child_id: 'k1', summary: 'Implement' })
    expect(result.current.status).toBe('ready')
    if (result.current.status !== 'ready') return
    expect(result.current.view.asks.map(a => a.question)).toEqual(['Ship it?'])
    expect(result.current.view.subagents.map(s => s.key)).toEqual(['k1'])
    expect(result.current.view.counts).toEqual({ needsYou: 1 })
    ingest('user_message', { source: 'ask_answer', message_id: 'm1', text: 'yes' })
    expect(result.current.status === 'ready' && result.current.view.asks).toEqual([])
    // The next read covers them: nothing is folded twice.
    await wait(GAP)
    await act(async () => {
      resolve(json(reply({
        through_seq: 3,
        subagents: [{ key: 'k1', title: 'Implement', type: '', model: '', tokens: null, doing: '', started_at: new Date().toISOString(), active_at: new Date().toISOString(), background: false, seq: 2 }],
        counts: { needs_you: 0, running: 1 },
      })))
      await vi.advanceTimersByTimeAsync(0)
    })
    expect(result.current.status === 'ready' && result.current.view.subagents.map(s => s.key)).toEqual(['k1'])
  })

  it('does not render again for an event it does not fold', async () => {
    fetchMock.mockImplementation(() => Promise.resolve(json(reply())))
    let renders = 0
    renderHook(() => { renders++; return useLiveStateServer('s1', true, '') })
    await flush()
    const before = renders
    ingest('tool_call')
    ingest('assistant_text')
    ingest('tool_call', {}, 'other-session')
    expect(renders).toBe(before)
    ingest('ask', { body: 'x' })
    expect(renders).toBeGreaterThan(before)
  })

  it('an app page or a plain-text 404/405 means the server has no such route', async () => {
    for (const r of [res(200, 'text/html; charset=utf-8'), res(404, 'text/plain; charset=utf-8'), res(405, 'text/plain; charset=utf-8')]) {
      fetchMock.mockResolvedValueOnce(r)
      const { result, unmount } = renderHook(() => useLiveStateServer('s1', true, ''))
      await flush()
      expect(result.current).toEqual({ status: 'no-route' })
      unmount()
    }
  })

  it('a JSON reply that is not a live state is an error, not a list', async () => {
    fetchMock.mockResolvedValueOnce(json({}))
    const { result } = renderHook(() => useLiveStateServer('s1', true, ''))
    await flush()
    expect(result.current).toEqual({ status: 'error', view: null, reply: null, clockOffset: 0, message: 'not a live state' })
  })

  it('asks an older server once per session: no read follows a no-route answer until the session changes', async () => {
    fetchMock.mockResolvedValue(res(404, 'text/plain; charset=utf-8'))
    const { result, rerender } = renderHook(({ id }) => useLiveStateServer(id, true, ''), { initialProps: { id: 's1' } })
    await flush()
    expect(result.current).toEqual({ status: 'no-route' })
    ingest('ask')
    await wait(GAP)
    act(() => { window.dispatchEvent(new Event('focus')) })
    await wait(GAP)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    rerender({ id: 's2' })
    await flush()
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it('shows a session it read before at once when the page comes back to it, then reads with its ETag', async () => {
    fetchMock.mockResolvedValueOnce(json(reply({ tool_changes: 3 }), '"a1"'))
    const { result, rerender } = renderHook(({ id }) => useLiveStateServer(id, true, ''), { initialProps: { id: 's1' } })
    await flush()
    let answerS2!: (r: Response) => void
    fetchMock.mockImplementationOnce(() => new Promise<Response>(r => { answerS2 = r }))
    rerender({ id: 's2' })
    expect(result.current).toEqual({ status: 'loading' }) // never read: nothing to show yet
    await act(async () => { answerS2(json(reply({ session_id: 's2' }), '"b1"')); await vi.advanceTimersByTimeAsync(0) })
    let answerS1!: (r: Response) => void
    fetchMock.mockImplementationOnce(() => new Promise<Response>(r => { answerS1 = r }))
    rerender({ id: 's1' })
    expect(result.current.status === 'ready' && result.current.view.toolChanges).toBe(3)
    expect(fetchMock).toHaveBeenLastCalledWith(URL_OF, { headers: { 'If-None-Match': '"a1"' } })
    await act(async () => { answerS1(json(reply({ tool_changes: 1 }), '"a2"')); await vi.advanceTimersByTimeAsync(0) })
    expect(result.current.status === 'ready' && result.current.view.toolChanges).toBe(1)
  })

  it('an error keeps the last view and the last 200\'s ETag', async () => {
    fetchMock.mockResolvedValueOnce(json(reply({ through_seq: 3, tool_changes: 2 }), '"a"'))
    const { result, rerender } = renderHook(({ k }) => useLiveStateServer('s1', true, k), { initialProps: { k: 'a' } })
    await flush()
    fetchMock.mockResolvedValueOnce(json({ error: 'internal' }, '"broken"', 500))
    rerender({ k: 'b' })
    await wait(GAP)
    expect(result.current.status).toBe('error')
    if (result.current.status !== 'error') return
    expect(result.current.message).toBe('HTTP 500')
    expect(result.current.view?.toolChanges).toBe(2)
    fetchMock.mockResolvedValueOnce(res(304, ''))
    rerender({ k: 'c' })
    await wait(GAP)
    expect(fetchMock).toHaveBeenLastCalledWith(URL_OF, { headers: { 'If-None-Match': '"a"' } })
    expect(result.current.status).toBe('ready')
  })

  it('a new session starts over: the old read is dropped, a fresh one starts at once without the old ETag', async () => {
    fetchMock.mockResolvedValueOnce(json(reply({ through_seq: 3 }), '"a"'))
    const { result, rerender } = renderHook(({ id }) => useLiveStateServer(id, true, ''), { initialProps: { id: 's1' } })
    await flush()
    let resolveOld!: (r: Response) => void
    fetchMock.mockImplementationOnce(() => new Promise<Response>(r => { resolveOld = r }))
    ingest('ask')
    await wait(GAP)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    fetchMock.mockResolvedValueOnce(json(reply({ session_id: 's2', tool_changes: 4 }), '"b"'))
    rerender({ id: 's2' })
    expect(result.current).toEqual({ status: 'loading' })
    await flush()
    expect(fetchMock).toHaveBeenLastCalledWith('/api/sessions/s2/live', { headers: {} })
    expect(result.current.status === 'ready' && result.current.view.toolChanges).toBe(4)
    await act(async () => { resolveOld(json(reply({ tool_changes: 9 }), '"old"')); await vi.advanceTimersByTimeAsync(0) })
    expect(result.current.status === 'ready' && result.current.view.toolChanges).toBe(4)
  })
})

it('refreshes on children_changed for this parent only', async () => {
  fetchMock.mockResolvedValue(json(reply()))
  const { emit } = await import('../test/wsMock')
  renderHook(() => useLiveStateServer('s1', true, ''))
  await flush()
  act(() => emit({ type: 'children_changed', session_id: 'other' }))
  await wait(GAP)
  expect(fetchMock).toHaveBeenCalledTimes(1)
  act(() => emit({ type: 'children_changed', session_id: 's1' }))
  await flush()
  expect(fetchMock).toHaveBeenCalledTimes(2)
})

it('coalesces child signals without rendering and preserves unchanged parent chat inputs', async () => {
  const { emit } = await import('../test/wsMock')
  const child = { id: 'builder', title: 'Builder', engine: 'codex', model: 'model-next', status: 'running', started_at: new Date().toISOString(), asking: false, tokens: null }
  const original = reply({
    children: [child],
    checklist: { items: [{ id: 'task', text: 'Parent task', done: false }], event_id: 'list', updated_at: new Date().toISOString() },
    mcp_hold: { since: new Date(Date.now() - 1000).toISOString(), until: new Date(Date.now() + 600_000).toISOString() },
  })
  fetchMock.mockResolvedValueOnce(json(original))
  let renders = 0
  const { result } = renderHook(() => { renders++; return useLiveStateServer('s1', true, '') })
  await flush()
  const first = result.current
  expect(first.status).toBe('ready')
  if (first.status !== 'ready') return
  const before = renders
  const updated = structuredClone(original)
  updated.children![0].activity = 'Compiling'
  fetchMock.mockResolvedValueOnce(json(updated, '"2"'))
  act(() => { for (let i = 0; i < 30; i++) emit({ type: 'children_changed', session_id: 's1' }) })
  expect(renders).toBe(before)
  expect(fetchMock).toHaveBeenCalledTimes(1)
  await wait(GAP)
  expect(fetchMock).toHaveBeenCalledTimes(2)
  const second = result.current
  expect(second.status).toBe('ready')
  if (second.status !== 'ready') return
  expect(second.view.children?.[0].activity).toBe('Compiling')
  expect(second.view.checklist?.items).toBe(first.view.checklist?.items)
  expect(second.view.mcpHold).toBe(first.view.mcpHold)
  const changed = structuredClone(updated)
  changed.checklist!.items[0].done = true
  changed.mcp_hold!.until = new Date(Date.now() + 700_000).toISOString()
  fetchMock.mockResolvedValueOnce(json(changed, '"3"'))
  act(() => emit({ type: 'children_changed', session_id: 's1' }))
  await wait(GAP)
  const third = result.current
  expect(third.status).toBe('ready')
  if (third.status !== 'ready') return
  expect(third.view.checklist?.items).not.toBe(first.view.checklist?.items)
  expect(third.view.checklist?.items[0].done).toBe(true)
  expect(third.view.mcpHold).not.toBe(first.view.mcpHold)
})
