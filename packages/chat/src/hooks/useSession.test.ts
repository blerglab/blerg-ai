import { beforeEach, describe, expect, it } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { OLDER_PAGE, useSession } from './useSession'
import { useTranscriptStore } from './useTranscriptStore'
import { createFakeTransport, type FakeTransport } from '../test/fakeTransport'
import { SESSION, meta, resetChatState } from '../test/chat'
import type { AgentEvent } from '../types'

// The older part of a long transcript is fetched lazily: one page right after the tail (so there
// is something to scroll into), then a page only when the view asks for one.

function ev(seq: number): AgentEvent {
  return {
    type: 'agent_event',
    session_id: SESSION,
    client_event_id: `e${seq}`,
    seq,
    ts: '2026-10-13T00:00:00Z',
    kind: 'user_message',
    payload: { text: `message ${seq}`, source: 'chat' },
  }
}
const range = (from: number, to: number) => Array.from({ length: to - from + 1 }, (_, i) => ev(from + i))

const row = meta({ status: 'idle', started_at: '2026-10-13T00:00:00Z' })

function mount(t: FakeTransport = createFakeTransport()) {
  return { t, ...renderHook(() => useSession(t.transport, SESSION, { meta: row })) }
}

// The newest 200 of a 1,000-event transcript.
function deliverTail(t: FakeTransport) {
  act(() => {
    for (const e of range(801, 1000)) t.emit(e)
    t.replayDone({ lastSeq: 1000, hasMore: false, older: true, hasOlder: true, firstSeq: 801 })
  })
}

async function answer(t: FakeTransport, from: number, to: number, hasOlder = true) {
  await act(async () => { t.answerOlder({ events: range(from, to), hasOlder, firstSeq: from }) })
}

describe('useSession fetches older pages lazily', () => {
  beforeEach(() => {
    resetChatState()
  })

  it('pages across sparse lifecycle context without skipping the intervening conversation', async () => {
    const { t, result } = mount()
    const context = [
      { ...ev(20), kind: 'subagent_started' as const, payload: { child_id: 'old-child', summary: 'Earlier review' } },
      { ...ev(40), kind: 'subagent_done' as const, payload: { child_id: 'old-child', summary: 'Review finished' } },
    ]
    act(() => {
      for (const event of [...context, ...range(801, 1000)]) t.emit(event)
      t.replayDone({ lastSeq: 1000, hasMore: false, older: true, hasOlder: true, firstSeq: 801 })
    })
    expect(useTranscriptStore.getState().sessions[SESSION].events[0].seq).toBe(20)
    expect(useTranscriptStore.getState().sessions[SESSION].firstSeq).toBe(801)
    expect(t.older).toEqual([{ sessionId: SESSION, beforeSeq: 801, limit: OLDER_PAGE }])

    await act(async () => t.answerOlder({ events: [...context, ...range(601, 800)], hasOlder: true, firstSeq: 601 }))
    expect(useTranscriptStore.getState().sessions[SESSION].events.filter(e => e.seq === 20)).toHaveLength(1)
    await act(async () => { void result.current.loadOlder() })
    expect(t.older[1]).toEqual({ sessionId: SESSION, beforeSeq: 601, limit: OLDER_PAGE })
    await answer(t, 401, 600)
    expect(useTranscriptStore.getState().sessions[SESSION].firstSeq).toBe(401)
    expect(useTranscriptStore.getState().sessions[SESSION].events.some(e => e.seq === 600)).toBe(true)
    expect(useTranscriptStore.getState().sessions[SESSION].events.some(e => e.seq === 800)).toBe(true)
  })

  it('asks for exactly one older page after the tail, and a second only when asked', async () => {
    const { t, result } = mount()
    deliverTail(t)
    expect(t.older).toEqual([{ sessionId: SESSION, beforeSeq: 801, limit: OLDER_PAGE }])
    expect(result.current.loadingOlder).toBe(true)

    await answer(t, 601, 800)
    // More is held back on the server, but nothing asked for it: no second request, nothing in flight.
    expect(t.older).toHaveLength(1)
    expect(result.current.hasOlder).toBe(true)
    expect(result.current.loadingOlder).toBe(false)
    expect(useTranscriptStore.getState().sessions[SESSION].events).toHaveLength(400)

    // The view asks: one more page, from just before the oldest held.
    await act(async () => { void result.current.loadOlder() })
    expect(t.older).toHaveLength(2)
    expect(t.older[1]).toEqual({ sessionId: SESSION, beforeSeq: 601, limit: OLDER_PAGE })
    expect(result.current.loadingOlder).toBe(true)
    // Asking again while that page is in flight sends nothing.
    await act(async () => { void result.current.loadOlder() })
    expect(t.older).toHaveLength(2)
    await answer(t, 401, 600)
    expect(t.older).toHaveLength(2)
    expect(result.current.loadingOlder).toBe(false)
  })

  it('asks for nothing once the start is reached', async () => {
    const { t, result } = mount()
    deliverTail(t)
    await answer(t, 1, 800, false)
    await act(async () => { void result.current.loadOlder() })
    expect(t.older).toHaveLength(1)
    expect(result.current.hasOlder).toBe(false)
  })

  it('a reconnect asks for at most one page', async () => {
    const { t, result } = mount()
    deliverTail(t)
    await answer(t, 601, 800)
    expect(t.older).toHaveLength(1)

    act(() => t.setConnected(false))
    act(() => t.setConnected(true))
    expect(t.older).toHaveLength(2)
    await answer(t, 401, 600)
    // The page that came in after the reconnect does not start another.
    expect(t.older).toHaveLength(2)
    expect(result.current.loadingOlder).toBe(false)
  })

  it('a reconnect after a drop that cut the page short asks for it again, once', async () => {
    const { t, result } = mount()
    deliverTail(t)
    await act(async () => {
      t.setConnected(false)
      t.failOlder()
    })
    // Nothing is in flight any more.
    expect(result.current.loadingOlder).toBe(false)
    act(() => t.setConnected(true))
    expect(t.older).toHaveLength(2)
    expect(t.older[1]).toEqual({ sessionId: SESSION, beforeSeq: 801, limit: OLDER_PAGE })
    await answer(t, 601, 800)
    expect(t.older).toHaveLength(2)
  })

  it('a later visit asks for at most one page', async () => {
    const t = createFakeTransport()
    const first = mount(t)
    deliverTail(t)
    await answer(t, 601, 800)
    first.unmount()

    const second = mount(t)
    expect(t.older).toHaveLength(2)
    expect(t.older[1]).toEqual({ sessionId: SESSION, beforeSeq: 601, limit: OLDER_PAGE })
    expect(second.result.current.loadingOlder).toBe(true)
    await answer(t, 401, 600)
    expect(t.older).toHaveLength(2)
    expect(second.result.current.loadingOlder).toBe(false)
  })
})

// A session idle for hours was running, waiting and idle again on its way through the replay: the
// statuses in between are not the session's status. Only the last is kept, applied when the replay is done.
describe('useSession and the statuses a replay passes through', () => {
  beforeEach(() => {
    resetChatState()
  })

  const mountLive = (t: FakeTransport) => renderHook(() => useSession(t.transport, SESSION))

  it('holds the statuses during a replay and applies the last one when it is done', () => {
    const t = createFakeTransport({ meta: { status: 'idle' } })
    const { result } = mountLive(t)
    act(() => { t.setStatus('running') })
    expect(result.current.status).toBeNull()
    act(() => { t.replayDone({ lastSeq: 100, hasMore: true }) })
    act(() => { t.setStatus('waiting') })
    act(() => { t.setStatus('idle') })
    // A page with more behind it does not end the replay.
    expect(result.current.status).not.toBe('waiting')
    expect(result.current.status).not.toBe('running')
    act(() => { t.replayDone({ lastSeq: 200, hasMore: false }) })
    expect(result.current.status).toBe('idle')
  })

  it('applies the newest page of a tail as the end of the replay', () => {
    const t = createFakeTransport({ meta: { status: 'idle' } })
    const { result } = mountLive(t)
    act(() => { t.setStatus('running'); t.setStatus('idle') })
    act(() => { t.replayDone({ lastSeq: 1000, hasMore: false, older: true, hasOlder: true, firstSeq: 801 }) })
    expect(result.current.status).toBe('idle')
  })

  it('applies a status that comes after the replay as it comes', () => {
    const t = createFakeTransport({ meta: { status: 'idle' } })
    const { result } = mountLive(t)
    act(() => { t.replayDone({ lastSeq: 10, hasMore: false }) })
    act(() => { t.setStatus('running') })
    expect(result.current.status).toBe('running')
    act(() => { t.setStatus('waiting') })
    expect(result.current.status).toBe('waiting')
  })

  it('applies the held status when the link drops mid-replay', () => {
    const t = createFakeTransport({ meta: { status: 'running' } })
    const { result } = mountLive(t)
    act(() => { t.setStatus('idle') })
    expect(result.current.status).toBeNull()
    act(() => { t.setConnected(false) })
    expect(result.current.status).toBe('idle')
  })

  it('applies a status that ends the session at once, mid-replay', () => {
    for (const end of ['stopped', 'ended', 'error'] as const) {
      resetChatState()
      const t = createFakeTransport({ meta: { status: 'idle' } })
      const { result } = mountLive(t)
      act(() => { t.setStatus('running') })
      expect(result.current.status).toBeNull()
      act(() => { t.setStatus(end) })
      expect(result.current.status).toBe(end)
    }
  })

  it('drops a held status when the transcript is reset', () => {
    const t = createFakeTransport({ meta: { status: 'idle' } })
    const { result } = mountLive(t)
    act(() => { t.setStatus('running') })
    act(() => { t.reset() })
    act(() => { t.replayDone({ lastSeq: 5, hasMore: false }) })
    expect(result.current.status).not.toBe('running')
  })
})

describe('queued messages survive leaving a session', () => {
  beforeEach(resetChatState)

  it('restores an unconsumed message after switching sessions and remounting', async () => {
    const t = createFakeTransport()
    const hook = renderHook(({ id }) => useSession(t.transport, id, { meta: row }), { initialProps: { id: SESSION } })
    await act(async () => { await hook.result.current.send('Please continue') })
    const message = hook.result.current.queued[0]
    expect(message.text).toBe('Please continue')
    hook.rerender({ id: 'another-session' })
    expect(hook.result.current.queued).toEqual([])
    hook.rerender({ id: SESSION })
    expect(hook.result.current.queued).toEqual([message])
    hook.unmount()
    const remounted = renderHook(() => useSession(t.transport, SESSION, { meta: row }))
    expect(remounted.result.current.queued).toEqual([message])
    act(() => t.emit({ ...ev(1), payload: { text: message.text, source: 'chat', client_id: message.id } }))
    expect(remounted.result.current.queued).toEqual([])
  })
})

describe('server accepted messages', () => {
  beforeEach(resetChatState)

  it('restores a pending message from server replay without sending it again', () => {
    const { t, result } = mount()
    act(() => t.emit({ ...ev(1), kind: 'input_accepted', payload: { client_id: 'durable-input', text: 'Do the next step' } }))
    expect(result.current.queued).toEqual([{ id: 'durable-input', text: 'Do the next step', afterSeq: 1, accepted: true }])
    act(() => result.current.giveUpQueued())
    expect(result.current.queued[0].stale).toBeUndefined()
    act(() => t.emit({ ...ev(2), payload: { client_id: 'durable-input', text: 'Do the next step', source: 'chat' } }))
    expect(result.current.queued).toEqual([])
  })

  it('a correlated failure settles a pending acceptance', () => {
    const { t, result } = mount()
    act(() => t.emit({ ...ev(1), kind: 'input_accepted', payload: { client_id: 'rejected-input', text: 'Continue' } }))
    act(() => t.emit({ ...ev(2), kind: 'error', payload: { client_id: 'rejected-input', message: 'Input was refused', retryable: true } }))
    expect(result.current.queued).toEqual([])
  })
})

describe('a send completing after navigation', () => {
  beforeEach(resetChatState)

  it('adds its message to the original session without marking the next one pending', async () => {
    const t = createFakeTransport()
    let complete!: (result: { queued: boolean }) => void
    t.transport.send = () => new Promise(resolve => { complete = resolve })
    const hook = renderHook(({ id }) => useSession(t.transport, id, { meta: row }), { initialProps: { id: SESSION } })
    let sent!: ReturnType<typeof hook.result.current.send>
    act(() => { sent = hook.result.current.send('For the first session') })
    hook.rerender({ id: 'another-session' })
    await act(async () => { complete({ queued: true }); await sent })
    expect(hook.result.current.queued).toEqual([])
    expect(hook.result.current.pending).toBeNull()
    hook.rerender({ id: SESSION })
    expect(hook.result.current.queued[0].text).toBe('For the first session')
  })
})
