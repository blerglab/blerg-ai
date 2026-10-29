import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import { DEFER_RECHECK_MS, REFRESH_DEBOUNCE_MS, RefreshScheduler, watchTurnEnds, type Readiness, type RunOutcome } from './explorerAutoRefresh'

let seq = 0
const ev = (kind: AgentEvent['kind'], payload: unknown, extra: Partial<AgentEvent> = {}): AgentEvent =>
  ({ type: 'agent_event', session_id: 's', client_event_id: `e${++seq}`, seq, ts: '2026-10-27T12:00:00Z', kind, payload, ...extra })
const call = (tool: string, id: string) => ev('tool_call', { tool, call_id: id, input: {} })
const result = (id: string) => ev('tool_result', { call_id: id, output: '', is_error: false, duration_ms: 1 })
const LONG = 10 * 60_000

beforeEach(() => { vi.useFakeTimers(); seq = 0; useTranscriptStore.setState({ sessions: {} }) })
afterEach(() => { vi.clearAllTimers(); vi.useRealTimers() })

/** A scheduler whose runs are counted; each run waits for finish() when held. */
function scheduler(ready: () => Readiness = () => true) {
  let runs = 0
  let hold = false
  let finish: (outcome: RunOutcome) => void = () => {}
  const s = new RefreshScheduler(() => { runs++; return hold ? new Promise<RunOutcome>(r => { finish = r }) : Promise.resolve<RunOutcome>('done') }, ready)
  return { s, runs: () => runs, hold: () => { hold = true }, finish: (outcome: RunOutcome = 'done') => { hold = false; finish(outcome) } }
}

it('turns triggers that come together (a turn end, focus and visibility) into one refresh', async () => {
  const { s, runs } = scheduler()
  s.request(); s.request(); s.request()
  await vi.advanceTimersByTimeAsync(REFRESH_DEBOUNCE_MS - 1)
  expect(runs()).toBe(0)
  await vi.advanceTimersByTimeAsync(1)
  expect(runs()).toBe(1)
  await vi.advanceTimersByTimeAsync(LONG)
  expect(runs()).toBe(1)
  s.stop()
})

it('reads once more after a run for a trigger that came during it, never twice at once', async () => {
  const { s, runs, hold, finish } = scheduler()
  hold()
  s.request()
  await vi.advanceTimersByTimeAsync(REFRESH_DEBOUNCE_MS)
  s.request(); s.request()
  await vi.advanceTimersByTimeAsync(LONG)
  expect(runs()).toBe(1)
  finish()
  await vi.advanceTimersByTimeAsync(LONG)
  expect(runs()).toBe(2)
  s.stop()
})

it('holds a refresh while it must wait, and while hidden only marks it stale until woken', async () => {
  let readiness: Readiness = 'later'
  const { s, runs } = scheduler(() => readiness)
  s.request()
  await vi.advanceTimersByTimeAsync(DEFER_RECHECK_MS * 3)
  expect(runs()).toBe(0)
  readiness = true
  await vi.advanceTimersByTimeAsync(DEFER_RECHECK_MS)
  expect(runs()).toBe(1)
  readiness = 'hidden'
  s.request()
  await vi.advanceTimersByTimeAsync(LONG)
  expect(runs()).toBe(1)
  expect(s.stale).toBe(true)
  readiness = true
  s.wake()
  expect(runs()).toBe(2)
  expect(s.stale).toBe(false)
  s.stop()
})

it('a failed refresh stays stale with no timed retry; the next trigger tries once more', async () => {
  const { s, runs, hold, finish } = scheduler()
  hold()
  s.request()
  await vi.advanceTimersByTimeAsync(REFRESH_DEBOUNCE_MS)
  finish('failed')
  await vi.advanceTimersByTimeAsync(LONG)
  expect(runs()).toBe(1)
  s.request()
  await vi.advanceTimersByTimeAsync(REFRESH_DEBOUNCE_MS)
  expect(runs()).toBe(2)
  s.stop()
})

it('Refresh supersedes a pending or running refresh without reviving it; an expand cancelling one asks again', async () => {
  const { s, runs, hold, finish } = scheduler()
  s.request(); s.satisfied()
  await vi.advanceTimersByTimeAsync(LONG)
  expect(runs()).toBe(0)
  hold()
  s.request()
  await vi.advanceTimersByTimeAsync(REFRESH_DEBOUNCE_MS)
  expect(runs()).toBe(1)
  s.satisfied(); finish('cancelled')
  await vi.advanceTimersByTimeAsync(LONG)
  expect(runs()).toBe(1)
  hold()
  s.request()
  await vi.advanceTimersByTimeAsync(REFRESH_DEBOUNCE_MS)
  expect(runs()).toBe(2)
  finish('cancelled')
  await vi.advanceTimersByTimeAsync(LONG)
  expect(runs()).toBe(3)
  s.stop()
})

it('stops: nothing runs after it', async () => {
  const { s, runs } = scheduler()
  s.request(); s.stop(); s.wake()
  await vi.advanceTimersByTimeAsync(LONG)
  expect(runs()).toBe(0)
})

it('reports new turn ends of the main agent, never history, tool calls, subagents or other sessions', () => {
  let ended = 0
  const store = useTranscriptStore.getState()
  const stop = watchTurnEnds('s', () => ended++)
  // The first load is history.
  store.ingestMany('s', [call('Edit', 'old'), result('old'), ev('turn_done', {})])
  store.ingestReplayDone('s', { lastSeq: seq, hasMore: false })
  expect(ended).toBe(0)
  store.ingest(call('Edit', 'w1')); store.ingest(result('w1'))
  store.ingest(call('Bash', 'w2')); store.ingest(result('w2'))
  expect(ended).toBe(0)
  store.ingest(ev('turn_done', {}))
  expect(ended).toBe(1)
  // A turn-ending error ends it too; a subagent's turn_done does not.
  store.ingest(ev('turn_done', { parent_call_id: 'sub' }))
  expect(ended).toBe(1)
  store.ingest(ev('error', { message: 'The engine stopped.' }))
  expect(ended).toBe(2)
  store.appendStreaming('s', 'hello')
  store.ingest({ ...ev('turn_done', {}), session_id: 'other' })
  expect(ended).toBe(2)
  // An older page is history.
  store.ingestOlder('s', { events: [{ ...ev('turn_done', {}), seq: -5 }], hasOlder: false, firstSeq: 1 } as never)
  expect(ended).toBe(2)
  // A reconnect brings what happened meanwhile in one page: news, once.
  store.ingestMany('s', [call('Write', 'm1'), result('m1'), ev('turn_done', {}), ev('turn_done', {})])
  expect(ended).toBe(3)
  stop()
  store.ingest(ev('turn_done', {}))
  expect(ended).toBe(3)
})

it('a seq-less live turn end counts once', () => {
  let ended = 0
  const store = useTranscriptStore.getState()
  store.ingestReplayDone('s', { lastSeq: 0, hasMore: false })
  const stop = watchTurnEnds('s', () => ended++)
  store.ingest(ev('turn_done', {}, { seq: undefined, client_event_id: 'live-1' }))
  store.ingest(ev('user_message', { text: 'next', source: 'chat' }, { seq: undefined, client_event_id: 'live-2' }))
  expect(ended).toBe(1)
  stop()
})

it('adds no work to streaming: a typing delta never reads the transcript', () => {
  const events = Array.from({ length: 5000 }, (_, i) => ({ ...call('Read', `r${i}`) }))
  let reads = 0
  const counted = new Proxy(events, { get(target, key, receiver) { if (typeof key === 'string' && /^\d+$/.test(key)) reads++; return Reflect.get(target, key, receiver) } })
  useTranscriptStore.setState({ sessions: { s: { events: counted, streaming: '', lastSeq: seq, replayDone: true, hasMore: false, hasOlder: false, firstSeq: 1, clockOffset: 0 } } })
  let ended = 0
  const stop = watchTurnEnds('s', () => ended++)
  reads = 0
  for (let i = 0; i < 200; i++) useTranscriptStore.getState().appendStreaming('s', 'x')
  // Another session's events neither.
  for (let i = 0; i < 50; i++) useTranscriptStore.getState().ingest({ ...ev('assistant_text', { text: 'x', done: true }), session_id: 'other' })
  expect(reads).toBe(0)
  expect(ended).toBe(0)
  stop()
})

it('a failed refresh leaves the view stale, and showing the explorer tries it once more', async () => {
  const { s, runs, hold, finish } = scheduler()
  hold()
  s.request()
  await vi.advanceTimersByTimeAsync(REFRESH_DEBOUNCE_MS)
  finish('failed')
  await vi.advanceTimersByTimeAsync(LONG)
  expect(runs()).toBe(1)
  expect(s.stale).toBe(true)
  s.wake()
  expect(runs()).toBe(2)
  expect(s.stale).toBe(false)
  // Succeeded: showing it again reads nothing.
  s.wake()
  await vi.advanceTimersByTimeAsync(LONG)
  expect(runs()).toBe(2)
  s.stop()
})

it('a trigger that came during a failed refresh is read afterwards, once', async () => {
  const { s, runs, hold, finish } = scheduler()
  hold()
  s.request()
  await vi.advanceTimersByTimeAsync(REFRESH_DEBOUNCE_MS)
  s.request()
  await vi.advanceTimersByTimeAsync(REFRESH_DEBOUNCE_MS) // its debounce ends while the read runs
  finish('failed')
  await vi.advanceTimersByTimeAsync(LONG)
  expect(runs()).toBe(2)
  await vi.advanceTimersByTimeAsync(LONG)
  expect(runs()).toBe(2)
  s.stop()
})
