import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import ChatView from './ChatView'
import { createFakeTransport, type FakeTransport } from '../test/fakeTransport'
import { SESSION, ev as mk, meta, resetChatState } from '../test/chat'
import type { SessionMeta } from '../transport/types'
import type { AgentEvent } from '../types'

// The working line while a long history loads: its "waiting on" suffix and its background words are
// read from the events held, and a page that has not arrived yet (a call's result, a task's end, the
// start of the turn) must not make them name calls and tasks long over, nor change with each page.

const S = 1000
const H = 3600 * S
const NOW = new Date(2026, 9, 15, 12, 0, 0).getTime()

let seq = 0
function ev(kind: string, payload: unknown, at: number): AgentEvent {
  seq++
  return mk({ client_event_id: `e${seq}`, seq, ts: new Date(at).toISOString(), kind, payload })
}
const userMsg = (at: number) => ev('user_message', { text: 'go', source: 'chat' }, at)
const running = (at: number) => ev('status_changed', { status: 'running', reason: 'turn' }, at)
const call = (id: string, at: number, tool = 'Bash') => ev('tool_call', { tool, call_id: id, input: { command: `make ${id}` } }, at)
const result = (id: string, at: number) => ev('tool_result', { call_id: id, output: 'ok', is_error: false, duration_ms: 1 }, at)
const turnDone = (at: number) => ev('turn_done', { stop_reason: 'end_turn', model: 'm', usage: {} }, at)
const started = (task: string, callId: string, at: number) =>
  ev('background_task_started', { task_id: task, call_id: callId, kind: 'bash', description: 'Wait for the deploy', started_at: new Date(at).toISOString() }, at)
const done = (task: string, callId: string, at: number) =>
  ev('background_task_done', { task_id: task, call_id: callId, kind: 'bash', status: 'completed', summary: 'done' }, at)

const tick = (ms: number) => act(() => { vi.advanceTimersByTime(ms) })
const deliver = (t: FakeTransport, events: AgentEvent[], done: Parameters<FakeTransport['replayDone']>[0]) =>
  act(() => { for (const e of events) t.emit(e); t.replayDone(done) })
const answerOlder = (t: FakeTransport, events: AgentEvent[], hasOlder = false) =>
  act(async () => { t.answerOlder({ events, hasOlder, firstSeq: events[0].seq ?? 0 }) })

function mount(status: SessionMeta['status'] = 'running', t: FakeTransport = createFakeTransport()) {
  return { t, ...render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status })} />) }
}

// Every text the working line's suffix and background words took, as the DOM changed.
function watchLine() {
  const seen: string[] = []
  const look = () => {
    for (const el of document.querySelectorAll('[data-testid="waiting-on"]')) seen.push(`on:${el.textContent?.replace(/ [\d:]+$/, '')}`)
    for (const el of document.querySelectorAll('[data-background="true"]')) seen.push(`bg:${el.textContent?.replace(/ [\d:]+$/, '')}`)
  }
  const obs = new MutationObserver(look)
  obs.observe(document.body, { childList: true, subtree: true, characterData: true, attributes: true })
  return { seen, look, stop: () => obs.disconnect() }
}

describe('the working line while the history loads', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'Date'] })
    vi.setSystemTime(NOW)
    resetChatState()
    seq = 0
    Element.prototype.scrollIntoView = vi.fn()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('names no call while a forward replay brings a call and, a page later, its result; nor after', () => {
    const { t } = mount('running')
    const w = watchLine()
    deliver(t, [userMsg(NOW - 9 * H), running(NOW - 9 * H), call('a', NOW - 9 * H)], { lastSeq: 3, hasMore: true })
    tick(5 * S)
    deliver(t, [
      result('a', NOW - 9 * H + S), turnDone(NOW - 9 * H + 2 * S),
      userMsg(NOW - 20 * S), running(NOW - 20 * S), call('b', NOW - 19 * S), result('b', NOW - 18 * S),
    ], { lastSeq: 9, hasMore: false })
    expect(screen.getByTestId('working')).toBeTruthy()
    tick(5 * S)
    w.look()
    w.stop()
    expect(w.seen).toEqual([])
  })

  it('names no call from a tail whose turn began before it, nor as older pages come in', async () => {
    // The newest page holds no start of the turn, and neither does the older page behind it, which
    // brings an orphan call from nine hours ago (no result will ever come) and a finished one. The
    // turn's start (first) is further back still.
    const { t } = mount('running')
    const first = [userMsg(NOW - 10 * H), running(NOW - 10 * H), call('w', NOW - 9.5 * H), ...Array.from({ length: 4 }, (_, i) => [call(`o${i}`, NOW - 9.4 * H + i * S), result(`o${i}`, NOW - 9.4 * H + i * S + 1)]).flat()]
    const middle = [call('x', NOW - 9 * H), call('f1', NOW - 8 * H), result('f1', NOW - 8 * H + S)]
    const tail = [call('f2', NOW - 60 * S), result('f2', NOW - 59 * S), call('f3', NOW - 30 * S), result('f3', NOW - 29 * S)]
    const w = watchLine()
    deliver(t, tail, { lastSeq: tail[tail.length - 1].seq, hasMore: false, older: true, hasOlder: true, firstSeq: tail[0].seq })
    expect(screen.getByTestId('working')).toBeTruthy()
    tick(5 * S)
    await answerOlder(t, middle, true)
    tick(5 * S)
    w.look()
    w.stop()
    expect(w.seen).toEqual([])
    // The rest (the turn's start) is fetched only when the reader scrolls up to it.
    expect(t.pendingOlder).toBe(0)
    expect(first.length).toBeGreaterThan(0)
  })

  it('never names a call left without a result in an earlier turn', () => {
    const { t } = mount('running')
    deliver(t, [
      userMsg(NOW - 9 * H), running(NOW - 9 * H), call('orphan', NOW - 9 * H), turnDone(NOW - 9 * H + S),
      ev('status_changed', { status: 'idle', reason: 'turn_done' }, NOW - 9 * H + S),
      userMsg(NOW - 10 * S), running(NOW - 10 * S),
    ], { lastSeq: 7, hasMore: false })
    tick(10 * S)
    expect(screen.getByTestId('working').textContent).toMatch(/^Working…/)
    expect(screen.queryByTestId('waiting-on')).toBeNull()
  })

  it('keeps what the line said of the settled transcript while an older page loads, and names a new call after 2 s', async () => {
    const { t } = mount('running')
    const page2 = [call('old2', NOW - 3 * H), result('old2', NOW - 3 * H + S)]
    const page1 = [userMsg(NOW - 2 * H), running(NOW - 2 * H), call('old1', NOW - 2 * H), result('old1', NOW - 2 * H + S), turnDone(NOW - 2 * H + S)]
    const tail = [userMsg(NOW - 30 * S), running(NOW - 30 * S), call('a', NOW - 10 * S)]
    deliver(t, tail, { lastSeq: tail[2].seq, hasMore: false, older: true, hasOlder: true, firstSeq: tail[0].seq })
    // The page behind the tail is on its way at once: until it is in, nothing is named.
    expect(t.pendingOlder).toBe(1)
    expect(screen.getByTestId('working')).toBeTruthy()
    expect(screen.queryByTestId('waiting-on')).toBeNull()
    await answerOlder(t, page1, true)
    expect(screen.getByTestId('waiting-on').textContent).toBe(' · waiting on Bash · make a 0:10')
    // Another older page on its way (a reconnect asks for one): the line holds.
    act(() => { t.setConnected(false); t.setConnected(true) })
    expect(t.pendingOlder).toBe(1)
    tick(S)
    expect(screen.getByTestId('waiting-on').textContent).toBe(' · waiting on Bash · make a 0:11')
    await answerOlder(t, page2)
    expect(screen.getByTestId('waiting-on').textContent).toBe(' · waiting on Bash · make a 0:11')
    // Live, after the load: the call ends, another starts, and is named once it has run 2 s (and
    // the 3 s hold has passed).
    act(() => { t.emit(result('a', NOW + S)); t.emit(call('b', NOW + S, 'Read')) })
    tick(S)
    expect(screen.getByTestId('waiting-on').textContent).toContain('waiting on Bash')
    tick(2 * S)
    expect(screen.getByTestId('waiting-on').textContent).toBe(' · waiting on Read 0:03')
  })
})

describe('what the working line holds while an older page loads', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'Date'] })
    vi.setSystemTime(NOW)
    resetChatState()
    // The tail's seqs start past the pages made after it, which go before it.
    seq = 100
    Element.prototype.scrollIntoView = vi.fn()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  // A tail and the page behind it, so the line has settled; then a reconnect asks for another older
  // page, which is left on its way.
  async function settleThenLoadOlder(t: FakeTransport, tail: AgentEvent[]) {
    const page1 = [userMsg(NOW - 2 * H), call('old1', NOW - 2 * H), result('old1', NOW - 2 * H + S), turnDone(NOW - 2 * H + S)]
    page1.forEach((e, i) => { e.seq = 50 + i })
    deliver(t, tail, { lastSeq: tail[tail.length - 1].seq, hasMore: false, older: true, hasOlder: true, firstSeq: tail[0].seq })
    await answerOlder(t, page1, true)
    act(() => { t.setConnected(false); t.setConnected(true) })
    expect(t.pendingOlder).toBe(1)
    const page2 = [call('older', NOW - 3 * H), result('older', NOW - 3 * H + S)]
    page2.forEach((e, i) => { e.seq = 10 + i })
    return async () => answerOlder(t, page2)
  }

  it('drops the call it names when a new turn starts, and names none of the new turn until the page is in', async () => {
    const { t } = mount('running')
    const finish = await settleThenLoadOlder(t, [userMsg(NOW - 30 * S), running(NOW - 30 * S), call('a', NOW - 10 * S)])
    expect(screen.getByTestId('waiting-on').textContent).toBe(' · waiting on Bash · make a 0:10')
    // The turn ends with the call cut off (no result), and the next one starts with a call of its own.
    act(() => { t.emit(turnDone(NOW)); t.emit(userMsg(NOW)); t.emit(running(NOW)); t.emit(call('b', NOW, 'Read')) })
    tick(4 * S)
    expect(screen.getByTestId('working')).toBeTruthy()
    expect(screen.queryByTestId('waiting-on')).toBeNull()
    await finish()
    // Named once the suffix has held its last change (the drop) for 3 s.
    tick(3 * S)
    expect(screen.getByTestId('waiting-on').textContent).toMatch(/^ · waiting on Read 0:0\d$/)
  })

  it('holds the background words while the page loads, and does not bring them back once the session was not live', async () => {
    const { t, rerender } = mount('idle')
    const tail = [userMsg(NOW - 70 * S), call('c1', NOW - 66 * S), result('c1', NOW - 65 * S), started('b1', 'c1', NOW - 65 * S), turnDone(NOW - 60 * S)]
    const finish = await settleThenLoadOlder(t, tail)
    expect(screen.getByTestId('working').textContent).toBe('Waiting on a background task: Wait for the deploy 1:05')
    // A task that starts while the page loads is not counted until it is in.
    act(() => { t.emit(started('b2', 'c1', NOW)) })
    expect(screen.getByTestId('working').textContent).toBe('Waiting on a background task: Wait for the deploy 1:05')
    // The session drops off and comes back while the page still loads: the held words stay gone.
    rerender(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'disconnected' })} />)
    expect(screen.queryByTestId('working')).toBeNull()
    rerender(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'idle' })} />)
    expect(screen.queryByTestId('working')).toBeNull()
    // The page is in: the line reads the tasks again.
    await finish()
    expect(screen.getByTestId('working').textContent).toBe('Waiting on 2 background tasks 1:05')
  })
})

describe('the background words while the history loads', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'Date'] })
    vi.setSystemTime(NOW)
    resetChatState()
    seq = 0
    Element.prototype.scrollIntoView = vi.fn()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('says nothing of a task whose end is a page later in a forward replay', () => {
    const { t } = mount('idle')
    const w = watchLine()
    deliver(t, [userMsg(NOW - 3 * H), call('c1', NOW - 3 * H), result('c1', NOW - 3 * H), started('b1', 'c1', NOW - 3 * H)], { lastSeq: 4, hasMore: true })
    tick(5 * S)
    deliver(t, [done('b1', 'c1', NOW - 2 * H), turnDone(NOW - 2 * H)], { lastSeq: 6, hasMore: false })
    tick(5 * S)
    w.look()
    w.stop()
    expect(w.seen).toEqual([])
    expect(screen.queryByTestId('working')).toBeNull()
  })

  it('does not take a task as running when an older page brings the start of one whose end is held', async () => {
    const { t } = mount('idle')
    const older = [userMsg(NOW - 3 * H), call('c1', NOW - 3 * H), result('c1', NOW - 3 * H), started('b1', 'c1', NOW - 3 * H)]
    const tail = [done('b1', 'c1', NOW - 2 * H), turnDone(NOW - 2 * H)]
    const w = watchLine()
    deliver(t, tail, { lastSeq: tail[1].seq, hasMore: false, older: true, hasOlder: true, firstSeq: tail[0].seq })
    await answerOlder(t, older)
    tick(5 * S)
    w.look()
    w.stop()
    expect(w.seen).toEqual([])
    expect(screen.queryByTestId('working')).toBeNull()
  })
})
