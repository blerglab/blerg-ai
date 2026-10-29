import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import { MIN_GAP_MS, useTopics } from './useTopics'
import type { TopicsReply } from '../lib/topics'

vi.mock('../ws', () => import('../test/wsMock'))
import { emit } from '../test/wsMock'

vi.mock('../apiFetch', () => ({ apiFetch: vi.fn() }))
import { apiFetch } from '../apiFetch'
const fetchMock = vi.mocked(apiFetch)

const URL_OF = '/api/sessions/s1/topics'
const reply = (over: Partial<TopicsReply> = {}): TopicsReply =>
  ({ session_id: 's1', through_seq: 1, generated_at: '', tokens_hidden: false, unmarked: null, topics: [], ...over })
// Plain response objects (no body stream to read under fake timers).
const res = (status: number, type: string, body: unknown = null, etag = '') => ({
  ok: status >= 200 && status < 300, status,
  headers: new Headers({ ...(type ? { 'Content-Type': type } : {}), ...(etag ? { ETag: etag } : {}) }),
  json: () => (type === 'application/json' ? Promise.resolve(body) : Promise.reject(new SyntaxError('not JSON'))),
}) as unknown as Response
const json = (body: unknown, etag = '"1"', status = 200) => res(status, 'application/json', body, etag)
const flush = () => act(async () => { await vi.advanceTimersByTimeAsync(0); await vi.advanceTimersByTimeAsync(0) })
const wait = (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms) })

let seq = 0
function ingest(kind: string) {
  seq++
  act(() => {
    useTranscriptStore.getState().ingest({ type: 'agent_event', session_id: 's1', client_event_id: `x${seq}`, seq, ts: new Date().toISOString(), kind, payload: {} } as AgentEvent)
  })
}

beforeEach(() => {
  // The unit project fakes only setTimeout and rAF; the 5 s gap is measured with Date.now().
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] })
  fetchMock.mockReset()
  useTranscriptStore.setState({ sessions: {} })
})
afterEach(() => {
  vi.useRealTimers()
})

describe('useTopics', () => {
  it('refreshes when a question closes without an answer, including while History is hidden', async () => {
    fetchMock.mockImplementation(() => Promise.resolve(json(reply())))
    const { rerender } = renderHook(({ on }) => useTopics('s1', on, ''), { initialProps: { on: true } })
    await flush()
    const closed = (session_id: string) => act(() => emit({ type: 'message_answered', message: { id: 'ask1', session_id, kind: 'ask', status: 'answered', answer: '', body: 'Question', created_at: '2026-10-24T10:00:00Z' } }))
    closed('other')
    await wait(MIN_GAP_MS)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    closed('s1')
    await flush()
    expect(fetchMock).toHaveBeenCalledTimes(2)
    rerender({ on: false })
    closed('s1')
    await wait(MIN_GAP_MS)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    rerender({ on: true })
    await flush()
    expect(fetchMock).toHaveBeenCalledTimes(3)
  })

  it('reads nothing while History is not on screen', async () => {
    renderHook(() => useTopics('s1', false, ''))
    ingest('topic')
    await wait(MIN_GAP_MS * 2)
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('reads when it comes on screen, sends If-None-Match after, and keeps the reply on a 304', async () => {
    fetchMock.mockResolvedValueOnce(json(reply({ through_seq: 7 }), '"a"'))
    const { result, rerender } = renderHook(({ on }) => useTopics('s1', on, ''), { initialProps: { on: true } })
    await flush()
    expect(fetchMock).toHaveBeenCalledWith(URL_OF, { headers: {}, signal: expect.any(AbortSignal) })
    expect(result.current).toEqual({ status: 'ready', reply: reply({ through_seq: 7 }) })
    fetchMock.mockResolvedValueOnce(res(304, ''))
    rerender({ on: false })
    rerender({ on: true })
    await wait(MIN_GAP_MS)
    expect(fetchMock).toHaveBeenLastCalledWith(URL_OF, { headers: { 'If-None-Match': '"a"' }, signal: expect.any(AbortSignal) })
    expect(result.current).toEqual({ status: 'ready', reply: reply({ through_seq: 7 }) })
  })

  it('starts reads at least 5 s apart and folds early triggers into one trailing read', async () => {
    fetchMock.mockImplementation(() => Promise.resolve(json(reply())))
    const { rerender } = renderHook(({ k }) => useTopics('s1', true, k), { initialProps: { k: 'a' } })
    await flush()
    expect(fetchMock).toHaveBeenCalledTimes(1)
    rerender({ k: 'a,b' })
    rerender({ k: 'a,b,c' })
    await wait(MIN_GAP_MS - 100)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    await wait(100)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    await wait(MIN_GAP_MS * 2)
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it('runs one read at a time: a trigger during a read waits for it', async () => {
    let resolve!: (r: Response) => void
    fetchMock.mockImplementationOnce(() => new Promise<Response>(r => { resolve = r }))
    fetchMock.mockImplementation(() => Promise.resolve(json(reply())))
    const { rerender } = renderHook(({ k }) => useTopics('s1', true, k), { initialProps: { k: 'a' } })
    await flush()
    await wait(MIN_GAP_MS + 1000)
    rerender({ k: 'b' })
    await flush()
    expect(fetchMock).toHaveBeenCalledTimes(1)
    await act(async () => { resolve(json(reply())); await vi.advanceTimersByTimeAsync(0) })
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it('reads again for a held topic, artifact, subagent start or end, or turn end; not for a subagent update', async () => {
    fetchMock.mockImplementation(() => Promise.resolve(json(reply())))
    renderHook(() => useTopics('s1', true, ''))
    await flush()
    ingest('subagent_update')
    ingest('assistant_text')
    await wait(MIN_GAP_MS)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    for (const kind of ['topic', 'subagent_started', 'subagent_done', 'artifact', 'turn_done']) {
      ingest(kind)
      await wait(MIN_GAP_MS)
    }
    expect(fetchMock).toHaveBeenCalledTimes(6)
  })

  it('does not render again for an event that is not a trigger', async () => {
    fetchMock.mockImplementation(() => Promise.resolve(json(reply())))
    let renders = 0
    renderHook(() => { renders++; return useTopics('s1', true, '') })
    await flush()
    const before = renders
    ingest('tool_call')
    ingest('subagent_update')
    expect(renders).toBe(before)
    ingest('turn_done')
    expect(renders).toBeGreaterThan(before)
  })

  it('reads again when the page comes back', async () => {
    fetchMock.mockImplementation(() => Promise.resolve(json(reply())))
    renderHook(() => useTopics('s1', true, ''))
    await flush()
    await wait(MIN_GAP_MS)
    act(() => { window.dispatchEvent(new Event('focus')) })
    await flush()
    expect(fetchMock).toHaveBeenCalledTimes(2)
    await wait(MIN_GAP_MS)
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'visible' })
    act(() => { document.dispatchEvent(new Event('visibilitychange')) })
    await flush()
    expect(fetchMock).toHaveBeenCalledTimes(3)
  })

  it('an app page or a plain-text 404/405 means the server has no such route', async () => {
    for (const r of [res(200, 'text/html; charset=utf-8'), res(404, 'text/plain; charset=utf-8'), res(405, 'text/plain; charset=utf-8')]) {
      fetchMock.mockResolvedValueOnce(r)
      const { result, unmount } = renderHook(() => useTopics('s1', true, ''))
      await flush()
      expect(result.current).toEqual({ status: 'no-route' })
      unmount()
    }
  })

  it('a JSON error keeps the last list', async () => {
    fetchMock.mockResolvedValueOnce(json(reply({ through_seq: 3 })))
    const { result, rerender } = renderHook(({ k }) => useTopics('s1', true, k), { initialProps: { k: 'a' } })
    await flush()
    fetchMock.mockResolvedValueOnce(json({ error: 'not found' }, '', 404))
    rerender({ k: 'b' })
    await wait(MIN_GAP_MS)
    expect(result.current).toEqual({ status: 'error', reply: reply({ through_seq: 3 }), message: 'HTTP 404' })
  })

  it('keeps the ETag of the last 200 only: an error reply carries one too', async () => {
    fetchMock.mockResolvedValueOnce(json(reply({ through_seq: 3 }), '"a"'))
    const { result, rerender } = renderHook(({ k }) => useTopics('s1', true, k), { initialProps: { k: 'a' } })
    await flush()
    fetchMock.mockResolvedValueOnce(json({ error: 'internal' }, '"broken"', 500))
    rerender({ k: 'b' })
    await wait(MIN_GAP_MS)
    expect(result.current).toEqual({ status: 'error', reply: reply({ through_seq: 3 }), message: 'HTTP 500' })
    fetchMock.mockResolvedValueOnce(res(304, ''))
    rerender({ k: 'c' })
    await wait(MIN_GAP_MS)
    expect(fetchMock).toHaveBeenLastCalledWith(URL_OF, { headers: { 'If-None-Match': '"a"' }, signal: expect.any(AbortSignal) })
    expect(result.current).toEqual({ status: 'ready', reply: reply({ through_seq: 3 }) })
  })

  it('a proxy page or a plain 401 is an error with its status, not an older server', async () => {
    for (const [r, status] of [[res(502, 'text/html'), 502], [res(503, 'text/html; charset=utf-8'), 503], [res(401, 'text/plain'), 401]] as const) {
      fetchMock.mockResolvedValueOnce(r)
      const { result, unmount } = renderHook(() => useTopics('s1', true, ''))
      await flush()
      expect(result.current).toEqual({ status: 'error', reply: null, message: `HTTP ${status}` })
      unmount()
    }
  })

  it('a new session starts over: the old read is dropped, a fresh one starts at once without the old ETag', async () => {
    fetchMock.mockResolvedValueOnce(json(reply({ through_seq: 3 }), '"a"'))
    const { result, rerender } = renderHook(({ id, k }) => useTopics(id, true, k), { initialProps: { id: 's1', k: 'a' } })
    await flush()
    let resolveOld!: (r: Response) => void
    fetchMock.mockImplementationOnce(() => new Promise<Response>(r => { resolveOld = r }))
    rerender({ id: 's1', k: 'b' })
    await wait(MIN_GAP_MS)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    fetchMock.mockResolvedValueOnce(json(reply({ session_id: 's2', through_seq: 9 }), '"b"'))
    rerender({ id: 's2', k: 'b' })
    expect(result.current).toEqual({ status: 'loading' })
    await flush()
    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect(fetchMock).toHaveBeenLastCalledWith('/api/sessions/s2/topics', { headers: {}, signal: expect.any(AbortSignal) })
    expect(result.current).toEqual({ status: 'ready', reply: reply({ session_id: 's2', through_seq: 9 }) })
    await act(async () => { resolveOld(json(reply({ through_seq: 4 }), '"old"')); await vi.advanceTimersByTimeAsync(0) })
    expect(result.current).toEqual({ status: 'ready', reply: reply({ session_id: 's2', through_seq: 9 }) })
    fetchMock.mockResolvedValueOnce(res(304, ''))
    rerender({ id: 's2', k: 'c' })
    await wait(MIN_GAP_MS)
    expect(fetchMock).toHaveBeenLastCalledWith('/api/sessions/s2/topics', { headers: { 'If-None-Match': '"b"' }, signal: expect.any(AbortSignal) })
  })
})
