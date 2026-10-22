import { describe, it, expect, vi, afterEach, type Mock } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { createBlergTransport, GAP_WAIT_MS, MAX_FORWARD_PAGES, type BlergSocket, type ChatBrowserMessage } from './blerg'
import type { SubscribeHandlers } from './types'
import { ArtifactGoneError, FileRequestError, WorkspaceFileError, fileErrorText, isArtifactGone, UPLOAD_CUT_OFF_MESSAGE } from './files'
import type { AgentEvent } from '../types'
import { useSession } from '../hooks/useSession'
import { useTranscriptStore } from '../hooks/useTranscriptStore'

// ─── a socket the tests drive ────────────────────────────────────────────────

function fakeSocket(opts: { open?: boolean } = {}) {
  const sent: unknown[] = []
  const handlers = new Map<string, Set<(msg: unknown) => void>>()
  const opens = new Set<() => void>()
  const closes = new Set<() => void>()
  let open = opts.open ?? true
  const socket: BlergSocket & {
    sent: typeof sent
    emit(msg: object): void
    reopen(): void
    drop(): void
    handlerCount(type: string): number
  } = {
    sent,
    send(msg) {
      if (!open) return false
      sent.push(msg)
      return true
    },
    onMessage(type, handler) {
      if (!handlers.has(type)) handlers.set(type, new Set())
      handlers.get(type)!.add(handler as (msg: unknown) => void)
      return () => { handlers.get(type)?.delete(handler as (msg: unknown) => void) }
    },
    onOpen(handler) {
      opens.add(handler)
      if (open) handler()
      return () => { opens.delete(handler) }
    },
    onClose(handler) {
      closes.add(handler)
      return () => { closes.delete(handler) }
    },
    connected: () => open,
    emit(msg: object) { handlers.get((msg as { type: string }).type)?.forEach(h => h(msg)) },
    reopen() { open = true; opens.forEach(h => h()) },
    drop() { open = false; closes.forEach(h => h()) },
    handlerCount: (type) => handlers.get(type)?.size ?? 0,
  }
  return socket
}

// The members every subscriber gives (onEvents, onReset and onAskClosed are optional).
type MockHandlers = { [K in keyof Omit<SubscribeHandlers, 'onEvents' | 'onReset' | 'onAskClosed'>]: Mock<SubscribeHandlers[K]> }
function handlers(): MockHandlers {
  return {
    onEvent: vi.fn<SubscribeHandlers['onEvent']>(),
    onStreaming: vi.fn<SubscribeHandlers['onStreaming']>(),
    onReplayDone: vi.fn<SubscribeHandlers['onReplayDone']>(),
    onStatus: vi.fn<SubscribeHandlers['onStatus']>(),
    onConnection: vi.fn<SubscribeHandlers['onConnection']>(),
  }
}

const ev = (seq: number, extra: Partial<AgentEvent> = {}): AgentEvent => ({
  type: 'agent_event', session_id: 's1', client_event_id: `c${seq}`, seq, ts: '2026-10-06T00:00:00.000Z',
  kind: 'assistant_text', payload: { text: `t${seq}`, done: true }, ...extra,
})

const getToken = async () => 'tok'

function make(socket = fakeSocket(), extra: Partial<Parameters<typeof createBlergTransport>[0]> = {}) {
  const fetch = vi.fn()
  const t = createBlergTransport({ baseUrl: '', getToken, socket, fetch: fetch as unknown as typeof fetch, ...extra })
  return { t, socket, fetch }
}

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

// ─── a type check: the runner's ws.ts module fits the socket slot ────────────

type ServerLike = { type: 'agent_event'; session_id: string } | { type: 'initial_state'; sessions: unknown[] }
// ws.ts's BrowserMessage is a wider union than the chat sends; the extra members are harmless.
type BrowserLike = ChatBrowserMessage | { type: 'resize_session'; session_id: string; cols: number; rows: number }
const wsLike = {
  send: (_msg: BrowserLike): boolean => true,
  onMessage: <T extends ServerLike>(_type: T['type'], _handler: (msg: T) => void): (() => void) => () => {},
  onOpen: (_handler: () => void): (() => void) => () => {},
}
const _socketTypeCheck: BlergSocket = wsLike
void _socketTypeCheck

// ─── subscribe ───────────────────────────────────────────────────────────────

describe('createBlergTransport subscribe', () => {
  it('asks for the newest page first when tail is set, exactly as the runner does', () => {
    const { t, socket } = make()
    const h = handlers()
    t.subscribe('s1', { tail: true }, h)
    expect(socket.sent).toEqual([{ type: 'subscribe_agent_events', session_id: 's1', after_seq: 0, tail: 200 }])
    expect(h.onConnection).toHaveBeenCalledWith(true)
  })

  it('without tail it replays forward from afterSeq', () => {
    const { t, socket } = make()
    t.subscribe('s1', { afterSeq: 17 }, handlers())
    expect(socket.sent).toEqual([{ type: 'subscribe_agent_events', session_id: 's1', after_seq: 17 }])
  })

  it('maps replay_done and chains the next forward page while has_more', () => {
    const { t, socket } = make()
    const h = handlers()
    t.subscribe('s1', { afterSeq: 0 }, h)
    socket.emit({ type: 'agent_events_replay_done', session_id: 's1', last_seq: 200, has_more: true, server_time: '2026-10-06T00:00:00Z' })
    expect(h.onReplayDone).toHaveBeenCalledWith({ lastSeq: 200, hasMore: true, older: undefined, hasOlder: undefined, firstSeq: undefined, serverTime: '2026-10-06T00:00:00Z' })
    expect(socket.sent[1]).toEqual({ type: 'subscribe_agent_events', session_id: 's1', after_seq: 200 })
    socket.emit({ type: 'agent_events_replay_done', session_id: 's2', last_seq: 9, has_more: true })
    expect(h.onReplayDone).toHaveBeenCalledTimes(1)
    expect(socket.sent).toHaveLength(2)
  })

  it('a tail page is mapped with its older fields and never chained forward (the hook pages older itself)', () => {
    const { t, socket } = make()
    const h = handlers()
    t.subscribe('s1', { tail: true }, h)
    socket.emit({ type: 'agent_events_replay_done', session_id: 's1', last_seq: 40, has_more: true, older: true, has_older: true, first_seq: 1 })
    expect(h.onReplayDone).toHaveBeenCalledWith({ lastSeq: 40, hasMore: true, older: true, hasOlder: true, firstSeq: 1, serverTime: undefined })
    expect(socket.sent).toHaveLength(1)
  })

  it('accepts sparse lifecycle context without a false gap or clearing live streaming', async () => {
    vi.useFakeTimers()
    useTranscriptStore.setState({ sessions: {} })
    const { t, socket } = make()
    const hook = renderHook(() => useSession(t, 's1', { meta: { status: 'running', started_at: '2026-10-06T00:00:00Z' } }))
    act(() => {
      socket.emit(ev(20, { kind: 'subagent_started', payload: { child_id: 'old-child', summary: 'Earlier review' } }))
      socket.emit(ev(40, { kind: 'subagent_done', payload: { child_id: 'old-child', summary: 'Review finished' } }))
      for (let seq = 801; seq <= 1000; seq++) socket.emit(ev(seq, { kind: 'user_message', payload: { text: `m${seq}`, source: 'chat' } }))
      socket.emit({ type: 'agent_events_replay_done', session_id: 's1', older: true, first_seq: 801, last_seq: 1000, has_more: false, has_older: true })
      socket.emit(ev(0, { seq: undefined, client_event_id: 'typing', transient: true, payload: { text: 'Still writing', done: false } }))
    })
    expect(useTranscriptStore.getState().sessions.s1.firstSeq).toBe(801)
    expect(socket.sent).toContainEqual({ type: 'subscribe_agent_events', session_id: 's1', before_seq: 801, limit: 200 })
    act(() => {
      // A second old lifecycle fact arrives with an older page, while the main agent types.
      socket.emit(ev(50, { kind: 'subagent_done', payload: { child_id: 'another-old-child', summary: 'Also finished' } }))
      for (let seq = 601; seq <= 800; seq++) socket.emit(ev(seq, { kind: 'user_message', payload: { text: `m${seq}`, source: 'chat' } }))
      socket.emit({ type: 'agent_events_replay_done', session_id: 's1', older: true, first_seq: 601, last_seq: 800, has_more: false, has_older: true })
    })
    await act(async () => { await vi.advanceTimersByTimeAsync(GAP_WAIT_MS + 1) })
    expect(socket.sent.filter(m => (m as { after_seq?: number }).after_seq != null)).toEqual([
      { type: 'subscribe_agent_events', session_id: 's1', after_seq: 0, tail: 200 },
    ])
    expect(useTranscriptStore.getState().sessions.s1.streaming).toBe('Still writing')
    expect(useTranscriptStore.getState().sessions.s1.firstSeq).toBe(601)
    hook.unmount()
    useTranscriptStore.setState({ sessions: {} })
    vi.useRealTimers()
  })

  it('hands persisted events to onEvent and typing deltas to onStreaming, for this session only', () => {
    const { t, socket } = make()
    const h = handlers()
    t.subscribe('s1', {}, h)
    socket.emit(ev(1))
    socket.emit(ev(0, { seq: undefined, client_event_id: 'd1', transient: true, payload: { text: 'He', done: false } }))
    socket.emit({ ...ev(2), session_id: 's2' })
    expect(h.onEvent).toHaveBeenCalledTimes(1)
    expect(h.onEvent).toHaveBeenCalledWith(ev(1))
    expect(h.onStreaming).toHaveBeenCalledWith('He')
  })

  it('reports status from session_state_changed and from initial_state', () => {
    const { t, socket } = make()
    const h = handlers()
    t.subscribe('s1', {}, h)
    socket.emit({ type: 'session_state_changed', session_id: 's1', status: 'waiting', unread: false })
    socket.emit({ type: 'session_state_changed', session_id: 's2', status: 'error', unread: false })
    socket.emit({ type: 'initial_state', daemons: [], sessions: [{ id: 's2', status: 'idle' }, { id: 's1', status: 'running' }] })
    socket.emit({ type: 'initial_state', daemons: [], sessions: [] })
    expect(h.onStatus.mock.calls).toEqual([['waiting'], ['running']])
  })

  it('a drop locks the composer and a reopen resubscribes from the last seq seen', () => {
    const { t, socket } = make()
    const h = handlers()
    t.subscribe('s1', { tail: true }, h)
    socket.emit(ev(5))
    socket.emit({ type: 'agent_events_replay_done', session_id: 's1', last_seq: 7, has_more: false, older: true })
    socket.emit(ev(8))
    socket.emit(ev(9))
    socket.drop()
    expect(h.onConnection).toHaveBeenLastCalledWith(false)
    expect(t.connected()).toBe(false)
    socket.reopen()
    expect(h.onConnection).toHaveBeenLastCalledWith(true)
    expect(socket.sent.at(-1)).toEqual({ type: 'subscribe_agent_events', session_id: 's1', after_seq: 9 })
    expect(t.connected()).toBe(true)
  })

  it('a reopen before any event arrived asks for the tail again', () => {
    const { t, socket } = make()
    t.subscribe('s1', { tail: true }, handlers())
    socket.drop()
    socket.reopen()
    expect(socket.sent.at(-1)).toEqual({ type: 'subscribe_agent_events', session_id: 's1', after_seq: 0, tail: 200 })
  })

  it('unsubscribing tells the server and detaches every handler', () => {
    const { t, socket } = make()
    const h = handlers()
    const off = t.subscribe('s1', {}, h)
    off()
    expect(socket.sent.at(-1)).toEqual({ type: 'unsubscribe_session', session_id: 's1' })
    socket.emit(ev(1))
    socket.emit({ type: 'session_state_changed', session_id: 's1', status: 'waiting', unread: false })
    socket.reopen()
    expect(h.onEvent).not.toHaveBeenCalled()
    expect(h.onStatus).not.toHaveBeenCalled()
    expect(h.onConnection).toHaveBeenCalledTimes(1)
    for (const type of ['agent_event', 'agent_events_replay_done', 'session_state_changed', 'initial_state']) {
      expect(socket.handlerCount(type)).toBe(0)
    }
  })
})

// ─── replay pages in one batch, and the bound on a forward replay ─────────────

/** Handlers with the two optional members a batching subscriber (useSession) gives. */
function batchHandlers() {
  return {
    ...handlers(),
    onEvents: vi.fn<NonNullable<SubscribeHandlers['onEvents']>>(),
    onReset: vi.fn<NonNullable<SubscribeHandlers['onReset']>>(),
  }
}

const done = (lastSeq: number, hasMore: boolean, extra: object = {}) =>
  ({ type: 'agent_events_replay_done', session_id: 's1', last_seq: lastSeq, has_more: hasMore, ...extra })

/** Plays one full forward page of the server's 200 events after `after`, ending in has_more. */
function fullPage(socket: ReturnType<typeof fakeSocket>, after: number, hasMore = true) {
  for (let s = after + 1; s <= after + 200; s++) socket.emit(ev(s))
  socket.emit(done(after + 200, hasMore))
}

describe('createBlergTransport replay batches', () => {
  it('hands a replay page to onEvents in one call, before its onReplayDone', () => {
    const { t, socket } = make()
    const h = batchHandlers()
    t.subscribe('s1', { afterSeq: 10 }, h)
    socket.emit(ev(11))
    socket.emit(ev(12))
    socket.emit(ev(13))
    expect(h.onEvents).not.toHaveBeenCalled()
    socket.emit(done(13, false))
    expect(h.onEvents).toHaveBeenCalledTimes(1)
    expect(h.onEvents).toHaveBeenCalledWith([ev(11), ev(12), ev(13)])
    expect(h.onEvent).not.toHaveBeenCalled()
    expect(h.onEvents.mock.invocationCallOrder[0]).toBeLessThan(h.onReplayDone.mock.invocationCallOrder[0])
  })

  it('hands live events after the replay to onEvent one by one, in order', () => {
    const { t, socket } = make()
    const h = batchHandlers()
    t.subscribe('s1', { afterSeq: 10 }, h)
    socket.emit(ev(11))
    socket.emit(done(11, false))
    socket.emit(ev(12))
    socket.emit(ev(13))
    expect(h.onEvents).toHaveBeenCalledTimes(1)
    expect(h.onEvent.mock.calls).toEqual([[ev(12)], [ev(13)]])
  })

  it('a tail page and every following forward page are batched too; a live event amid a page rides with it', () => {
    const { t, socket } = make()
    const h = batchHandlers()
    t.subscribe('s1', { tail: true }, h)
    socket.emit(ev(5))
    socket.emit(ev(6))
    socket.emit(done(6, false, { older: true, has_older: false, first_seq: 5 }))
    expect(h.onEvents.mock.calls).toEqual([[[ev(5), ev(6)]]])
    const h2 = batchHandlers()
    t.subscribe('s1', { afterSeq: 0 }, h2)
    socket.emit(ev(1))
    socket.emit(done(200, true))
    socket.emit(ev(201))
    socket.emit(ev(900, { client_event_id: 'live900' })) // live, while the next page is on its way
    socket.emit(ev(202))
    socket.emit(done(202, false))
    expect(h2.onEvents.mock.calls).toEqual([[[ev(1)]], [[ev(201), ev(900, { client_event_id: 'live900' }), ev(202)]]])
    expect(h2.onEvent).not.toHaveBeenCalled()
  })

  it('a drop in the middle of a page hands over what arrived, and the reopen asks for the page again', () => {
    const { t, socket } = make()
    const h = batchHandlers()
    t.subscribe('s1', { afterSeq: 10 }, h)
    socket.emit(ev(11))
    socket.emit(ev(12))
    socket.drop()
    expect(h.onEvents).toHaveBeenCalledWith([ev(11), ev(12)])
    socket.reopen()
    // From where the replay started: 11 and 12 come again (the transcript drops them as repeats),
    // since a live event among them would have been no sign the replay got that far.
    expect(socket.sent.at(-1)).toEqual({ type: 'subscribe_agent_events', session_id: 's1', after_seq: 10 })
    socket.emit(ev(11))
    socket.emit(ev(12))
    socket.emit(ev(13))
    socket.emit(done(13, false))
    expect(h.onEvents).toHaveBeenLastCalledWith([ev(11), ev(12), ev(13)])
  })

  it('pages a short gap forward and keeps what is held', () => {
    const { t, socket } = make()
    const h = batchHandlers()
    t.subscribe('s1', { afterSeq: 10 }, h)
    fullPage(socket, 10)
    for (let s = 211; s <= 250; s++) socket.emit(ev(s))
    socket.emit(done(250, false))
    expect(h.onReset).not.toHaveBeenCalled()
    expect(h.onEvents).toHaveBeenCalledTimes(2)
    expect(socket.sent).toEqual([
      { type: 'subscribe_agent_events', session_id: 's1', after_seq: 10 },
      { type: 'subscribe_agent_events', session_id: 's1', after_seq: 210 },
    ])
  })

  it(`gives up on a forward replay still not done after ${MAX_FORWARD_PAGES} full pages and loads from the end instead`, () => {
    expect(MAX_FORWARD_PAGES).toBe(5)
    const { t, socket } = make()
    const h = batchHandlers()
    t.subscribe('s1', { afterSeq: 10 }, h)
    // The server holds six full pages and more past seq 10.
    for (let p = 0; p < MAX_FORWARD_PAGES; p++) fullPage(socket, 10 + p * 200)
    expect(h.onReset).toHaveBeenCalledTimes(1)
    const forward = socket.sent.filter(m => (m as { tail?: number; before_seq?: number }).tail == null)
    expect(forward).toHaveLength(MAX_FORWARD_PAGES) // the sixth page is never asked for
    expect(socket.sent.at(-1)).toEqual({ type: 'subscribe_agent_events', session_id: 's1', after_seq: 0, tail: 200 })
    // The page that ran over is dropped with the rest; the reset came after the pages before it.
    expect(h.onEvents).toHaveBeenCalledTimes(MAX_FORWARD_PAGES - 1)
    expect(h.onReplayDone).toHaveBeenCalledTimes(MAX_FORWARD_PAGES - 1)
    expect(h.onEvents.mock.invocationCallOrder.at(-1)!).toBeLessThan(h.onReset.mock.invocationCallOrder[0])

    // The newest page comes back as a batch and an older replay_done, as on a first visit.
    socket.emit(ev(5000))
    socket.emit(ev(5001))
    socket.emit(done(5001, false, { older: true, has_older: true, first_seq: 5000 }))
    expect(h.onEvents).toHaveBeenLastCalledWith([ev(5000), ev(5001)])
    expect(h.onReplayDone).toHaveBeenLastCalledWith(expect.objectContaining({ older: true, hasOlder: true, firstSeq: 5000 }))
    socket.emit(ev(5002))
    expect(h.onEvent).toHaveBeenCalledWith(ev(5002))
  })

  it('a drop before the newest page arrives asks for the tail again, not forward from a live seq', () => {
    const { t, socket } = make()
    const h = batchHandlers()
    t.subscribe('s1', { afterSeq: 10 }, h)
    for (let p = 0; p < MAX_FORWARD_PAGES; p++) fullPage(socket, 10 + p * 200)
    socket.emit(ev(7000)) // live, before the tail answered
    socket.drop()
    socket.reopen()
    expect(socket.sent.at(-1)).toEqual({ type: 'subscribe_agent_events', session_id: 's1', after_seq: 0, tail: 200 })
  })

  it('a loadOlder cut off by a drop is finished, so the tail asked for after a reset still lands', async () => {
    const { t, socket } = make()
    const h = batchHandlers()
    t.subscribe('s1', { afterSeq: 10 }, h)
    const older = t.loadOlder('s1', 5, 200)
    for (let p = 0; p < MAX_FORWARD_PAGES; p++) fullPage(socket, 10 + p * 200)
    expect(h.onReset).toHaveBeenCalledTimes(1)
    socket.emit(ev(5000)) // the tail is coming in
    socket.drop()
    await expect(older).rejects.toThrow(/not connected/i)
    const batchesBefore = h.onEvents.mock.calls.length
    socket.reopen()
    expect(socket.sent.at(-1)).toEqual({ type: 'subscribe_agent_events', session_id: 's1', after_seq: 0, tail: 200 })
    socket.emit(ev(5000))
    socket.emit(ev(5001))
    socket.emit(done(5001, false, { older: true, has_older: true, first_seq: 5000 }))
    expect(h.onEvents.mock.calls.length - batchesBefore).toBe(1)
    expect(h.onEvents).toHaveBeenLastCalledWith([ev(5000), ev(5001)])
    expect(h.onReplayDone).toHaveBeenLastCalledWith(expect.objectContaining({ older: true, hasOlder: true, firstSeq: 5000 }))
    socket.emit(ev(5002))
    expect(h.onEvent).toHaveBeenCalledWith(ev(5002))
  })

  it('a drop in the middle of a forward replay resumes from the last page it finished, not from a live event that came in', () => {
    const { t, socket } = make()
    const h = batchHandlers()
    t.subscribe('s1', { afterSeq: 10 }, h)
    fullPage(socket, 10) // 11..210, has_more
    socket.emit(ev(900, { client_event_id: 'live900' })) // live, while page 2 comes in
    for (let s = 211; s <= 300; s++) socket.emit(ev(s))
    socket.drop()
    socket.reopen()
    expect(socket.sent.at(-1)).toEqual({ type: 'subscribe_agent_events', session_id: 's1', after_seq: 210 })
    // Once the replay is through, a reconnect resumes from the newest seq seen again.
    for (let s = 211; s <= 320; s++) socket.emit(ev(s))
    socket.emit(done(320, false))
    for (let s = 321; s < 900; s++) socket.emit(ev(s))
    socket.drop()
    socket.reopen()
    expect(socket.sent.at(-1)).toEqual({ type: 'subscribe_agent_events', session_id: 's1', after_seq: 900 })
  })

  it('typing deltas that arrive while a page is held wait in it, behind the events before them', () => {
    const { t, socket } = make()
    const h = batchHandlers()
    t.subscribe('s1', { afterSeq: 10 }, h)
    const closing = ev(11, { kind: 'turn_done', payload: {} })
    const delta = ev(0, { seq: undefined, client_event_id: 'd1', transient: true, payload: { text: 'Hel', done: false } })
    socket.emit(closing)
    socket.emit(delta)
    expect(h.onStreaming).not.toHaveBeenCalled()
    socket.emit(done(11, false))
    expect(h.onEvents).toHaveBeenCalledWith([closing, delta])
    socket.emit({ ...delta, client_event_id: 'd2', payload: { text: 'lo', done: false } })
    expect(h.onStreaming).toHaveBeenCalledWith('lo')
  })

  it('a subscriber that cannot reset (no onReset) is replayed forward however far it is, one event at a time without onEvents', () => {
    const { t, socket } = make()
    const h = handlers()
    t.subscribe('s1', { afterSeq: 10 }, h)
    for (let p = 0; p < MAX_FORWARD_PAGES + 1; p++) fullPage(socket, 10 + p * 200)
    expect(socket.sent.at(-1)).toEqual({ type: 'subscribe_agent_events', session_id: 's1', after_seq: 10 + (MAX_FORWARD_PAGES + 1) * 200 })
    expect(h.onEvent).toHaveBeenCalledTimes((MAX_FORWARD_PAGES + 1) * 200)
  })
})

describe('useSession over the blerg transport', () => {
  afterEach(() => {
    useTranscriptStore.setState({ sessions: {} })
  })

  /** A session visited before: ten events held, the start reached. */
  function cached() {
    const s = useTranscriptStore.getState()
    s.ingestMany('s1', Array.from({ length: 10 }, (_, i) => ev(i + 1)))
    s.ingestReplayDone('s1', { lastSeq: 10, hasMore: false, older: true, hasOlder: false, firstSeq: 1, serverTime: new Date(Date.now() - 5000).toISOString() })
  }

  /** Counts the updates that changed the session's events. */
  function countEventUpdates() {
    const n = { count: 0 }
    const off = useTranscriptStore.subscribe((now, before) => {
      if (now.sessions.s1?.events !== before.sessions.s1?.events) n.count++
    })
    return { n, off }
  }

  const held = () => useTranscriptStore.getState().sessions.s1?.events.map(e => e.seq) ?? []

  it('a revisit commits a replay page in one store update and live events after it singly', () => {
    cached()
    const { t, socket } = make()
    renderHook(() => useSession(t, 's1', { meta: { status: 'idle', started_at: '2026-10-13T00:00:00Z' } }))
    expect(socket.sent).toEqual([{ type: 'subscribe_agent_events', session_id: 's1', after_seq: 10 }])
    const { n, off } = countEventUpdates()
    act(() => {
      socket.emit(ev(11))
      socket.emit(ev(12))
      socket.emit(ev(13))
      socket.emit(done(13, false))
    })
    expect(n.count).toBe(1)
    act(() => {
      socket.emit(ev(14))
      socket.emit(ev(13)) // a replay/live overlap
      socket.emit(ev(15))
    })
    off()
    expect(n.count).toBe(3)
    expect(held()).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15])
  })

  it(`a revisit more than ${MAX_FORWARD_PAGES} pages behind drops what is held and loads from the end, keeping the clock offset`, () => {
    cached()
    const offset = useTranscriptStore.getState().sessions.s1.clockOffset
    const { t, socket } = make()
    renderHook(() => useSession(t, 's1', { meta: { status: 'idle', started_at: '2026-10-13T00:00:00Z' } }))
    act(() => { for (let p = 0; p < MAX_FORWARD_PAGES; p++) fullPage(socket, 10 + p * 200) })
    expect(held()).toEqual([])
    expect(useTranscriptStore.getState().sessions.s1).toMatchObject({ lastSeq: 0, firstSeq: 0, hasOlder: false, clockOffset: offset })
    expect(socket.sent.at(-1)).toEqual({ type: 'subscribe_agent_events', session_id: 's1', after_seq: 0, tail: 200 })

    act(() => {
      for (let s = 1801; s <= 2000; s++) socket.emit(ev(s))
      socket.emit(done(2000, false, { older: true, has_older: true, first_seq: 1801 }))
    })
    expect(held()).toHaveLength(200)
    expect(held()[0]).toBe(1801)
    // The older part follows a page at a time, from before the newest page.
    expect(socket.sent.at(-1)).toEqual({ type: 'subscribe_agent_events', session_id: 's1', before_seq: 1801, limit: 200 })
  })

  it('a typing delta after a closing event in a held page survives the page', () => {
    cached()
    const { t, socket } = make()
    renderHook(() => useSession(t, 's1', { meta: { status: 'idle', started_at: '2026-10-13T00:00:00Z' } }))
    act(() => {
      socket.emit(ev(11, { kind: 'turn_done', payload: {} }))
      socket.emit(ev(0, { seq: undefined, client_event_id: 'd1', transient: true, payload: { text: 'Hel', done: false } }))
      socket.emit(done(11, false))
    })
    expect(useTranscriptStore.getState().sessions.s1.streaming).toBe('Hel')
  })

  it('leaving with a page in flight and coming back asks for that page again', () => {
    cached()
    const { t, socket } = make()
    const opts = { meta: { status: 'idle' as const, started_at: '2026-10-13T00:00:00Z' } }
    const first = renderHook(() => useSession(t, 's1', opts))
    act(() => {
      fullPage(socket, 10) // 11..210
      for (let s = 211; s <= 300; s++) socket.emit(ev(s)) // page 2 cut short by leaving
    })
    first.unmount()
    renderHook(() => useSession(t, 's1', opts))
    expect(socket.sent.at(-1)).toEqual({ type: 'subscribe_agent_events', session_id: 's1', after_seq: 210 })
    act(() => {
      for (let s = 211; s <= 320; s++) socket.emit(ev(s))
      socket.emit(done(320, false))
    })
    expect(held()).toEqual(Array.from({ length: 320 }, (_, i) => i + 1))
  })

  it('leaving mid-replay after a live event came in resumes from the replay, not the live event, and the gap arrives', () => {
    cached()
    const { t, socket } = make()
    const opts = { meta: { status: 'idle' as const, started_at: '2026-10-13T00:00:00Z' } }
    const first = renderHook(() => useSession(t, 's1', opts))
    act(() => {
      fullPage(socket, 10) // 11..210
      for (let s = 211; s <= 300; s++) socket.emit(ev(s))
      socket.emit(ev(900, { client_event_id: 'live900' })) // live, rides with page 2
      for (let s = 301; s <= 410; s++) socket.emit(ev(s))
      socket.emit(done(410, true))
    })
    expect(useTranscriptStore.getState().sessions.s1.lastSeq).toBe(900)
    first.unmount()
    renderHook(() => useSession(t, 's1', opts))
    expect(socket.sent.at(-1)).toEqual({ type: 'subscribe_agent_events', session_id: 's1', after_seq: 410 })
    act(() => {
      fullPage(socket, 410) // 411..610
      fullPage(socket, 610) // 611..810
      for (let s = 811; s <= 899; s++) socket.emit(ev(s))
      socket.emit(done(899, false))
    })
    expect(held()).toEqual([...Array.from({ length: 899 }, (_, i) => i + 1), 900])
    expect(useTranscriptStore.getState().sessions.s1).toMatchObject({ hasMore: false, lastSeq: 900 })
  })

  it('the tail\'s own replay_done does not answer the older page asked for during it: the page lands whole, shown as loading until then, and nothing more is asked for', async () => {
    // A socket that dispatches to a Set (the runner's ws.ts, this fake) also calls a handler added
    // while it dispatches: the older page's, added by the hook as the tail's replay_done is handled.
    const { t, socket } = make()
    const { result } = renderHook(() => useSession(t, 's1', { meta: { status: 'running', started_at: '2026-10-13T00:00:00Z' } }))
    const range = (from: number, to: number) => Array.from({ length: to - from + 1 }, (_, i) => ev(from + i))
    await act(async () => {
      for (const e of range(401, 600)) socket.emit(e)
      socket.emit({ type: 'agent_events_replay_done', session_id: 's1', last_seq: 600, has_more: false, older: true, has_older: true, first_seq: 401 })
    })
    const backward = () => socket.sent.filter(m => (m as { before_seq?: number }).before_seq != null)
    expect(backward()).toEqual([{ type: 'subscribe_agent_events', session_id: 's1', before_seq: 401, limit: 200 }])
    expect(result.current.loadingOlder).toBe(true)
    const { n, off } = countEventUpdates()
    await act(async () => {
      for (const e of range(201, 400)) socket.emit(e)
      socket.emit({ type: 'agent_events_replay_done', session_id: 's1', last_seq: 0, has_more: false, older: true, has_older: true, first_seq: 201 })
    })
    off()
    expect(n.count).toBe(1)
    expect(result.current.loadingOlder).toBe(false)
    expect(held()).toEqual(range(201, 600).map(e => e.seq))
    expect(backward()).toHaveLength(1)
  })

  it('a revisit two pages behind keeps what is held and adds the gap', () => {
    cached()
    const { t, socket } = make()
    renderHook(() => useSession(t, 's1', { meta: { status: 'idle', started_at: '2026-10-13T00:00:00Z' } }))
    act(() => {
      fullPage(socket, 10)
      fullPage(socket, 210, false)
    })
    expect(held()).toHaveLength(410)
    expect(held()[0]).toBe(1)
    expect(socket.sent.some(m => (m as { tail?: number }).tail != null)).toBe(false)
  })
})

// ─── loadOlder ───────────────────────────────────────────────────────────────

describe('createBlergTransport loadOlder', () => {
  it('asks for the page before the seq and resolves it from the backward replay, keeping those events out of onEvent', async () => {
    const { t, socket } = make()
    const h = handlers()
    t.subscribe('s1', {}, h)
    const page = t.loadOlder('s1', 41, 200)
    expect(socket.sent.at(-1)).toEqual({ type: 'subscribe_agent_events', session_id: 's1', before_seq: 41, limit: 200 })
    socket.emit(ev(39))
    socket.emit(ev(40))
    socket.emit(ev(50)) // live, not part of the page
    socket.emit({ type: 'agent_events_replay_done', session_id: 's1', last_seq: 50, has_more: false, older: true, has_older: true, first_seq: 39, server_time: '2026-10-06T00:00:01Z' })
    await expect(page).resolves.toEqual({ events: [ev(39), ev(40)], hasOlder: true, firstSeq: 39, serverTime: '2026-10-06T00:00:01Z' })
    expect(h.onEvent.mock.calls).toEqual([[ev(50)]])
    expect(h.onReplayDone).not.toHaveBeenCalled()
  })

  it('works without a subscription and fails at once when the socket is down', async () => {
    const { t, socket } = make()
    const page = t.loadOlder('s1', 10, 50)
    socket.emit(ev(9))
    socket.emit({ type: 'agent_events_replay_done', session_id: 's1', last_seq: 9, has_more: false, older: true, has_older: false, first_seq: 9 })
    await expect(page).resolves.toMatchObject({ events: [ev(9)], hasOlder: false, firstSeq: 9 })
    socket.drop()
    await expect(t.loadOlder('s1', 10, 50)).rejects.toThrow(/not connected/i)
  })
})

// ─── send, stop, setModel ────────────────────────────────────────────────────

describe('createBlergTransport messages', () => {
  it('send carries the text as agent_user_message and says whether it went', async () => {
    const { t, socket } = make()
    await expect(t.send('s1', 'hello')).resolves.toEqual({ queued: true })
    expect(socket.sent).toEqual([{ type: 'agent_user_message', session_id: 's1', text: 'hello' }])
    socket.drop()
    await expect(t.send('s1', 'again')).resolves.toEqual({ queued: false })
    expect(socket.sent).toHaveLength(1)
  })

  it('send carries the client id the daemon echoes', async () => {
    const { t, socket } = make()
    await t.send('s1', 'hello', { clientId: 'q-1' })
    expect(socket.sent).toEqual([{ type: 'agent_user_message', session_id: 's1', text: 'hello', client_id: 'q-1' }])
  })

  it('an answer names its ask, and the transport says it can', async () => {
    const { t, socket } = make()
    expect(t.answersAsks).toBe(true)
    await t.send('s1', 'yes', { clientId: 'q-2', answerTo: 'm1' })
    expect(socket.sent).toEqual([{ type: 'agent_user_message', session_id: 's1', text: 'yes', client_id: 'q-2', answer_message_id: 'm1' }])
  })

  it('reports the asks of its session the server closed: live, and those closed while away', () => {
    const { t, socket } = make()
    const onAskClosed = vi.fn()
    t.subscribe('s1', {}, { ...handlers(), onAskClosed })
    socket.emit({ type: 'message_answered', message: { id: 'm1', session_id: 's1', kind: 'ask', status: 'answered', answer: 'yes' } })
    socket.emit({ type: 'message_answered', message: { id: 'm9', session_id: 's2', kind: 'ask', status: 'answered', answer: 'no' } })
    socket.emit({ type: 'initial_state', daemons: [], sessions: [], messages: [
      { id: 'm2', session_id: 's1', kind: 'ask', status: 'answered' },
      { id: 'm3', session_id: 's1', kind: 'ask', status: 'open' },
    ] })
    expect(onAskClosed.mock.calls).toEqual([['m1', 'yes', false, undefined], ['m2', '', false, undefined]])
  })

  // Card #155 review: a dismissal says whether this viewer made it.
  it('says who dismissed a question, as this viewer reads it', () => {
    const socket = fakeSocket()
    const { t } = make(socket, { viewerId: () => 'acct-me' })
    const onAskClosed = vi.fn()
    t.subscribe('s1', {}, { ...handlers(), onAskClosed })
    socket.emit({ type: 'message_answered', message: { id: 'm1', session_id: 's1', kind: 'ask', status: 'answered', answer: '', dismissed: true, dismissed_by: 'acct-me' } })
    socket.emit({ type: 'message_answered', message: { id: 'm2', session_id: 's1', kind: 'ask', status: 'answered', answer: '', dismissed: true, dismissed_by: 'acct-other' } })
    socket.emit({ type: 'message_answered', message: { id: 'm3', session_id: 's1', kind: 'ask', status: 'answered', answer: '', dismissed: true } })
    expect(onAskClosed.mock.calls).toEqual([['m1', '', true, 'you'], ['m2', '', true, 'other'], ['m3', '', true, undefined]])
  })

  it('setModel sends set_session_model with only the fields given', async () => {
    const { t, socket } = make()
    await t.setModel!('s1', 'claude-opus-4-1')
    await t.setModel!('s1', undefined, 'high')
    expect(socket.sent).toEqual([
      { type: 'set_session_model', session_id: 's1', model: 'claude-opus-4-1' },
      { type: 'set_session_model', session_id: 's1', effort: 'high' },
    ])
  })

  it('interrupt sends interrupt_session and fails when the socket is down', async () => {
    const { t, socket } = make()
    await expect(t.interrupt!('s1')).resolves.toBeUndefined()
    expect(socket.sent).toEqual([{ type: 'interrupt_session', session_id: 's1' }])
    socket.drop()
    await expect(t.interrupt!('s1')).rejects.toThrow(/not connected/i)
  })

  it('stop ends the session through the browser route with the bearer token', async () => {
    const { t, fetch } = make()
    fetch.mockResolvedValue(new Response(null, { status: 204 }))
    await t.stop('s 1')
    expect(fetch).toHaveBeenCalledWith('/api/sessions/s%201', expect.objectContaining({ method: 'DELETE', headers: { Authorization: 'Bearer tok' } }))
    fetch.mockResolvedValue(new Response('{}', { status: 500 }))
    await expect(t.stop('s1')).rejects.toThrow('HTTP 500')
  })

  it.each([true, false])('preserves declared session steering support (%s)', async steering_supported => {
    const { t, fetch } = make()
    fetch.mockResolvedValue(new Response(JSON.stringify([{ id: 's1', status: 'running', engine: 'future-engine', steering_supported, started_at: '' }])))
    await expect(t.session('s1')).resolves.toMatchObject({ steering_supported })
  })

  it('session reads the session list and maps the one asked for', async () => {
    const { t, fetch } = make(fakeSocket(), { baseUrl: 'https://runner.example' })
    fetch.mockImplementation(async () => new Response(JSON.stringify([
      { id: 's0', status: 'idle', started_at: 'a' },
      { id: 's1', status: 'waiting', engine: 'codex', runtime: 'cluster', model: 'm', effort: 'low', started_at: '2026-10-06T00:00:00Z', ended_at: null, title: 'x', end_reason: 'stopped_by_user', ended_by: { kind: 'human', self: true }, error_reason: 'e', message: null },
    ])))
    await expect(t.session('s1')).resolves.toEqual({
      status: 'waiting', engine: 'codex', runtime: 'cluster', model: 'm', effort: 'low', started_at: '2026-10-06T00:00:00Z', ended_at: null,
      end_reason: 'stopped_by_user', ended_by: { kind: 'human', self: true }, error_reason: 'e', message: null,
    })
    expect(fetch).toHaveBeenCalledWith('https://runner.example/api/sessions', expect.objectContaining({ headers: { Authorization: 'Bearer tok' } }))
    await expect(t.session('nope')).rejects.toThrow(/not found/i)
  })
})

// ─── files ───────────────────────────────────────────────────────────────────

describe('createBlergTransport files', () => {
  it('lists with the bearer token', async () => {
    const { t, fetch } = make()
    fetch.mockResolvedValue(new Response(JSON.stringify({ artifacts: [{ id: 'a1', name: 'f.txt', size: 1 }, 'junk'] })))
    await expect(t.files!.list('s1')).resolves.toEqual([{ id: 'a1', name: 'f.txt', size: 1 }])
    expect(fetch).toHaveBeenCalledWith('/api/sessions/s1/artifacts', expect.objectContaining({ headers: { Authorization: 'Bearer tok' } }))
  })

  it('raw and remove use the artifact routes', async () => {
    const { t, fetch } = make()
    fetch.mockResolvedValueOnce(new Response('bytes', { status: 200 }))
    const blob = await t.files!.raw('s1', 'a/1')
    expect(await blob.text()).toBe('bytes')
    expect(fetch.mock.calls[0][0]).toBe('/api/sessions/s1/artifacts/a%2F1/raw')
    fetch.mockResolvedValueOnce(new Response(null, { status: 404 }))
    await expect(t.files!.remove('s1', 'a1')).resolves.toBeUndefined()
    expect(fetch.mock.calls[1]).toEqual(['/api/sessions/s1/artifacts/a1', expect.objectContaining({ method: 'DELETE' })])
  })

  it('workspaceFile sends the path encoded once and returns the decoded bytes', async () => {
    const { t, fetch } = make()
    const body = { path: 'docs/a b.md', size: 5, view: 'markdown', content_type: 'text/markdown; charset=utf-8', truncated: false, content_b64: btoa('hello'), mtime: '2026-10-17T00:00:00Z' }
    fetch.mockResolvedValueOnce(new Response(JSON.stringify(body)))
    const f = await t.files!.workspaceFile!('s/1', 'docs/a b.md')
    expect(fetch.mock.calls[0][0]).toBe('/api/sessions/s%2F1/workspace-file?path=docs%2Fa%20b.md')
    expect(fetch.mock.calls[0][1]).toMatchObject({ headers: { Authorization: 'Bearer tok' } })
    expect(f).toMatchObject({ path: 'docs/a b.md', view: 'markdown', size: 5, mtime: '2026-10-17T00:00:00Z' })
    expect(await f.blob.text()).toBe('hello')
  })

  it('workspaceFile throws WorkspaceFileError with the status and the error id', async () => {
    const { t, fetch } = make()
    fetch.mockResolvedValueOnce(new Response(JSON.stringify({ error: 'The workspace file was not found.', error_id: 'e-1' }), { status: 404 }))
    const e = await t.files!.workspaceFile!('s1', 'x.md').catch((x: unknown) => x)
    expect(e).toBeInstanceOf(WorkspaceFileError)
    expect(e).toMatchObject({ status: 404, errorId: 'e-1', serverText: 'The workspace file was not found.' })
  })

  it('workspaceFile keeps the server’s machine-readable reason (code), and only a well-formed one', async () => {
    const { t, fetch } = make()
    fetch.mockResolvedValueOnce(new Response(JSON.stringify({ error: 'The workspace file reader is busy.', code: 'busy', error_id: 'e-2' }), { status: 409 }))
    expect(await t.files!.workspaceFile!('s1', 'x.png').catch((x: unknown) => x)).toMatchObject({ status: 409, code: 'busy' })
    fetch.mockResolvedValueOnce(new Response(JSON.stringify({ error: 'Busy.', code: 'Busy <b>' }), { status: 409 }))
    expect((await t.files!.workspaceFile!('s1', 'x.png').catch((x: unknown) => x) as WorkspaceFileError).code).toBeUndefined()
  })

  it('workspaceFile can be cancelled', async () => {
    const { t, fetch } = make()
    fetch.mockImplementation((_u: unknown, init?: RequestInit) => new Promise((_res, rej) => {
      if (init?.signal?.aborted) rej(new DOMException('x', 'AbortError'))
      init?.signal?.addEventListener('abort', () => rej(new DOMException('x', 'AbortError')))
    }))
    const ac = new AbortController()
    const p = t.files!.workspaceFile!('s1', 'x.md', ac.signal)
    ac.abort()
    await expect(p).rejects.toMatchObject({ name: 'AbortError' })
  })

  it('a 410 (listed, bytes gone) is a distinct error on raw and download', async () => {
    const { t, fetch } = make()
    const gone = () => new Response('{"error":"file no longer available"}', { status: 410 })
    fetch.mockResolvedValueOnce(gone())
    const raw = await t.files!.raw('s1', 'a1').catch((e: unknown) => e)
    expect(isArtifactGone(raw)).toBe(true)
    expect(raw).toBeInstanceOf(ArtifactGoneError)
    expect((raw as Error).message).toBe('This file is no longer available.')
    fetch.mockResolvedValueOnce(gone())
    expect(isArtifactGone(await t.files!.download('s1', 'a1').catch((e: unknown) => e))).toBe(true)
    fetch.mockResolvedValueOnce(new Response('', { status: 500 }))
    expect(isArtifactGone(await t.files!.raw('s1', 'a1').catch((e: unknown) => e))).toBe(false)
  })

  it('a 401 signs the person out and throws', async () => {
    const onUnauthorized = vi.fn()
    const { t, fetch } = make(fakeSocket(), { onUnauthorized })
    fetch.mockResolvedValue(new Response('', { status: 401 }))
    await expect(t.files!.list('s1')).rejects.toThrow()
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
    await expect(t.session('s1')).rejects.toThrow()
    expect(onUnauthorized).toHaveBeenCalledTimes(2)
  })

  it('sends no Authorization header when there is no token', async () => {
    const { t, fetch } = make(fakeSocket(), { getToken: async () => null })
    fetch.mockResolvedValue(new Response(JSON.stringify({ artifacts: [] })))
    await t.files!.list('s1')
    expect((fetch.mock.calls[0][1] as RequestInit).headers).toEqual({})
  })

  it('a refused read says the server’s sentence and keeps its error id', async () => {
    const { t, fetch } = make()
    fetch.mockResolvedValueOnce(new Response(JSON.stringify({ error: 'The runner could not read the file because of an unexpected storage error; try again.', error_id: '0badf00d' }), { status: 500 }))
    const e = await t.files!.download('s1', 'a1').catch((x: unknown) => x)
    expect(e).toBeInstanceOf(FileRequestError)
    expect((e as FileRequestError).message).toBe('The runner could not read the file because of an unexpected storage error; try again.')
    expect((e as FileRequestError).errorId).toBe('0badf00d')
    expect(fileErrorText(e, 'x')).toEqual({ message: (e as Error).message, errorId: '0badf00d' })
    // A proxy's plain-text refusal is shown as it is; a web page or nothing falls back.
    fetch.mockResolvedValueOnce(new Response('forbidden\n', { status: 403, headers: { 'Content-Type': 'text/plain' } }))
    expect(((await t.files!.download('s1', 'a1').catch((x: unknown) => x)) as Error).message).toBe('forbidden')
    fetch.mockResolvedValueOnce(new Response('<!doctype html><p>gateway</p>', { status: 502 }))
    expect(((await t.files!.raw('s1', 'a1').catch((x: unknown) => x)) as Error).message).toBe('Couldn’t load this file (HTTP 502).')
    fetch.mockResolvedValueOnce(new Response('', { status: 500 }))
    expect(((await t.files!.download('s1', 'a1').catch((x: unknown) => x)) as Error).message).toBe('Download failed (HTTP 500).')
  })

  function refusingXhr(status: number, body: string, contentType = 'application/json') {
    return {
      upload: { onprogress: null },
      onload: null as (() => void) | null, onerror: null as (() => void) | null, onabort: null, status, responseText: body,
      open() {}, setRequestHeader() {},
      getResponseHeader: () => contentType,
      send() { if (status === 0) this.onerror?.(); else this.onload?.() },
      abort() {},
    }
  }

  it('an upload refusal is the server’s sentence with its error id, plain text included', async () => {
    const { t } = make()
    const file = new File(['abc'], 'a.txt')
    const cases: Array<[number, string, string, string, string | undefined]> = [
      [413, '{"error":"This file is 130 MiB; the runner takes at most 100 MiB per file."}', 'application/json', 'This file is 130 MiB; the runner takes at most 100 MiB per file.', undefined],
      [413, '', 'application/json', 'That file is larger than the server takes.', undefined],
      [500, '{"error":"The runner could not save the file because its disk is full.","error_id":"ab12cd34"}', 'application/json', 'The runner could not save the file because its disk is full.', 'ab12cd34'],
      [403, 'forbidden\n', 'text/plain; charset=utf-8', 'forbidden', undefined],
      [502, 'runner unreachable\n', 'text/plain; charset=utf-8', 'runner unreachable', undefined],
      [502, '\u001b[31mbad gateway\u001b[0m\n', 'text/plain', '[31mbad gateway[0m', undefined],
      [500, '<html><body>boom</body></html>', 'text/html', 'Upload failed (HTTP 500).', undefined],
    ]
    for (const [status, body, ct, message, errorId] of cases) {
      const xhr = refusingXhr(status, body, ct)
      vi.stubGlobal('XMLHttpRequest', function FakeXhr() { return xhr })
      const e = await t.files!.upload('s1', file).catch((x: unknown) => x)
      expect(e, `${status} ${body}`).toBeInstanceOf(FileRequestError)
      expect((e as FileRequestError).message).toBe(message)
      expect((e as FileRequestError).errorId).toBe(errorId)
      vi.unstubAllGlobals()
    }
    // No answer at all: the connection went (a proxy that refused the size cuts it).
    const xhr = refusingXhr(0, '')
    vi.stubGlobal('XMLHttpRequest', function FakeXhr() { return xhr })
    await expect(t.files!.upload('s1', file)).rejects.toThrow(UPLOAD_CUT_OFF_MESSAGE)
    vi.unstubAllGlobals()
  })

  it('uploads raw bytes with the percent-encoded name, reporting progress', async () => {
    const xhr = {
      headers: {} as Record<string, string>, sent: null as unknown, url: '',
      upload: { onprogress: null as ((e: { lengthComputable: boolean; loaded: number; total: number }) => void) | null },
      onload: null as (() => void) | null, onerror: null, onabort: null, status: 200, responseText: '{"id":"u1","name":"ü x.txt","size":3}',
      open(_m: string, url: string) { this.url = url },
      setRequestHeader(k: string, v: string) { this.headers[k] = v },
      getResponseHeader: () => 'application/json',
      send(body: unknown) {
        this.sent = body
        this.upload.onprogress?.({ lengthComputable: true, loaded: 2, total: 3 })
        this.onload?.()
      },
      abort() {},
    }
    vi.stubGlobal('XMLHttpRequest', function FakeXhr() { return xhr })
    const { t } = make()
    const progress = vi.fn()
    const file = new File(['abc'], 'ü x.txt')
    await expect(t.files!.upload('s1', file, progress)).resolves.toEqual({ id: 'u1', name: 'ü x.txt', size: 3 })
    expect(xhr.url).toBe('/api/sessions/s1/uploads')
    expect(xhr.headers).toEqual({ Authorization: 'Bearer tok', 'Content-Type': 'application/octet-stream', 'X-Artifact-Name': '%C3%BC%20x.txt' })
    expect(xhr.sent).toBe(file)
    expect(progress).toHaveBeenCalledWith(2, 3)
    vi.unstubAllGlobals()
  })
})

// ─── holes in the live seqs ──────────────────────────────────────────────────

describe('createBlergTransport gaps', () => {
  afterEach(() => { vi.useRealTimers() })

  /** Subscribed from seq 10 with the forward replay over: live from here. */
  function live() {
    const { t, socket } = make()
    const h = batchHandlers()
    const off = t.subscribe('s1', { afterSeq: 10 }, h)
    socket.emit(done(10, false))
    socket.sent.length = 0
    return { t, socket, h, off }
  }

  it('the server\'s gap notice replays forward from below the first event it dropped', () => {
    const { socket, h } = live()
    socket.emit(ev(11))
    socket.emit(ev(14)) // 12 and 13 were dropped for this browser
    socket.emit({ type: 'agent_events_gap', session_id: 's1', from_seq: 12 })
    expect(socket.sent).toEqual([{ type: 'subscribe_agent_events', session_id: 's1', after_seq: 11 }])
    // The replay brings what was missed (and 14 again, which the transcript dedupes), in one batch.
    socket.emit(ev(12))
    socket.emit(ev(13))
    socket.emit(ev(14))
    socket.emit(done(14, false))
    expect(h.onEvents).toHaveBeenLastCalledWith([ev(12), ev(13), ev(14)])
    expect(h.onReplayDone).toHaveBeenLastCalledWith(expect.objectContaining({ lastSeq: 14, hasMore: false }))
  })

  it('ignores a gap notice for another session', () => {
    const { socket } = live()
    socket.emit({ type: 'agent_events_gap', session_id: 's2', from_seq: 3 })
    expect(socket.sent).toEqual([])
  })

  it('a hole in the live seqs that stays open is fetched after GAP_WAIT_MS', () => {
    vi.useFakeTimers()
    const { socket } = live()
    socket.emit(ev(11))
    socket.emit(ev(13))
    vi.advanceTimersByTime(GAP_WAIT_MS - 1)
    expect(socket.sent).toEqual([])
    vi.advanceTimersByTime(1)
    expect(socket.sent).toEqual([{ type: 'subscribe_agent_events', session_id: 's1', after_seq: 11 }])
  })

  it('an event that only came out of order closes its hole and nothing is fetched', () => {
    vi.useFakeTimers()
    const { socket, h } = live()
    socket.emit(ev(12))
    socket.emit(ev(11))
    vi.advanceTimersByTime(GAP_WAIT_MS * 3)
    expect(socket.sent).toEqual([])
    expect(h.onEvent.mock.calls).toEqual([[ev(12)], [ev(11)]])
  })

  it('a seq the server never stored is fetched once, not again and again', () => {
    vi.useFakeTimers()
    const { socket } = live()
    socket.emit(ev(11))
    socket.emit(ev(13))
    vi.advanceTimersByTime(GAP_WAIT_MS)
    expect(socket.sent).toHaveLength(1)
    socket.emit(ev(13))
    socket.emit(done(13, false))
    socket.emit(ev(14))
    vi.advanceTimersByTime(GAP_WAIT_MS * 5)
    expect(socket.sent).toHaveLength(1)
  })

  it('a gap notice during a replay waits for it to end', () => {
    const { t, socket } = make()
    const h = batchHandlers()
    t.subscribe('s1', { afterSeq: 10 }, h)
    socket.emit(ev(11))
    socket.emit({ type: 'agent_events_gap', session_id: 's1', from_seq: 30 })
    expect(socket.sent).toHaveLength(1) // only the subscribe
    socket.emit(done(11, false))
    expect(socket.sent.at(-1)).toEqual({ type: 'subscribe_agent_events', session_id: 's1', after_seq: 11 })
  })

  it('a reconnect with a hole open resumes from below it', () => {
    const { socket } = live()
    socket.emit(ev(11))
    socket.emit(ev(13))
    socket.drop()
    socket.reopen()
    expect(socket.sent.at(-1)).toEqual({ type: 'subscribe_agent_events', session_id: 's1', after_seq: 11 })
  })

  it('a gap while an older page is in flight: each answer goes where it belongs', async () => {
    const { t, socket, h } = live()
    const older = t.loadOlder('s1', 5, 3)
    socket.emit(ev(11))
    socket.emit(ev(13))
    socket.emit({ type: 'agent_events_gap', session_id: 's1', from_seq: 12 })
    expect(socket.sent.at(-1)).toEqual({ type: 'subscribe_agent_events', session_id: 's1', after_seq: 11 })
    // The older page and the refill arrive interleaved.
    socket.emit(ev(2))
    socket.emit(ev(12))
    socket.emit(ev(3))
    socket.emit(ev(13))
    socket.emit(done(13, false))
    socket.emit({ type: 'agent_events_replay_done', session_id: 's1', last_seq: 4, has_more: false, older: true, has_older: true, first_seq: 2 })
    const page = await older
    expect(page.events.map(e => e.seq)).toEqual([2, 3])
    expect(h.onEvents).toHaveBeenLastCalledWith([ev(12), ev(13)])
    expect(h.onReplayDone).toHaveBeenLastCalledWith(expect.objectContaining({ lastSeq: 13, hasMore: false }))
  })

  it('a drop while a refill is in flight resumes the refill on reconnect', () => {
    const { socket, h } = live()
    socket.emit(ev(11))
    socket.emit(ev(14))
    socket.emit({ type: 'agent_events_gap', session_id: 's1', from_seq: 12 })
    socket.emit(ev(12)) // the refill had begun
    socket.drop()
    socket.reopen()
    expect(socket.sent.at(-1)).toEqual({ type: 'subscribe_agent_events', session_id: 's1', after_seq: 11 })
    for (const s of [12, 13, 14]) socket.emit(ev(s))
    socket.emit(done(14, false))
    expect(h.onEvents).toHaveBeenLastCalledWith([ev(12), ev(13), ev(14)])
    // Whole again: a later reconnect resumes from the end.
    socket.drop()
    socket.reopen()
    expect(socket.sent.at(-1)).toEqual({ type: 'subscribe_agent_events', session_id: 's1', after_seq: 14 })
  })

  it('unsubscribing cancels a pending gap fetch', () => {
    vi.useFakeTimers()
    const { socket, off } = live()
    socket.emit(ev(11))
    socket.emit(ev(13))
    off()
    vi.advanceTimersByTime(GAP_WAIT_MS * 2)
    expect(socket.sent).toEqual([{ type: 'unsubscribe_session', session_id: 's1' }])
  })

  it('through useSession the missed events land in the transcript in seq order, without duplicates', async () => {
    const socket = fakeSocket()
    const { t } = make(socket)
    const sid = 'gap-session'
    useTranscriptStore.getState().clear(sid)
    const e = (seq: number) => ev(seq, { session_id: sid })
    const { result } = renderHook(() => useSession(t, sid))
    await act(async () => {
      socket.emit(e(1))
      socket.emit({ type: 'agent_events_replay_done', session_id: sid, last_seq: 1, has_more: false, older: true })
    })
    await act(async () => {
      socket.emit(e(2))
      socket.emit(e(4))
      socket.emit({ type: 'agent_events_gap', session_id: sid, from_seq: 3 })
    })
    expect(socket.sent.at(-1)).toEqual({ type: 'subscribe_agent_events', session_id: sid, after_seq: 2 })
    await act(async () => {
      socket.emit(e(3))
      socket.emit(e(4))
      socket.emit({ type: 'agent_events_replay_done', session_id: sid, last_seq: 4, has_more: false })
    })
    expect(result.current.events.map(x => x.seq)).toEqual([1, 2, 3, 4])
  })
})

describe('published artifact session URL', () => {
  it('uses the configured runner origin, encoding the native session ID', () => {
    const t = createBlergTransport({ baseUrl: 'https://runner.example.com', getToken: async () => null, socket: fakeSocket() })
    expect(t.publishedSessionUrl?.('native/session')).toBe('https://runner.example.com/sessions/native%2Fsession')
  })

  it('uses the current runner page origin when no base URL is configured', () => {
    const t = createBlergTransport({ getToken: async () => null, socket: fakeSocket() })
    expect(t.publishedSessionUrl?.('s1')).toBe(`${location.origin}/sessions/s1`)
  })
})

// ─── delegations ─────────────────────────────────────────────────────────────

describe('createBlergTransport delegations', () => {
  const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

  it('decideDelegation posts the choice', async () => {
    const { t, fetch } = make()
    fetch.mockResolvedValue(json(200, { request_id: 'r/1', status: 'allowed', choice: 'once', child_session_id: 'c1' }))
    const out = await t.decideDelegation!('r/1', 'once')
    expect(out.child_session_id).toBe('c1')
    const [url, init] = fetch.mock.calls[0]
    expect(url).toBe('/api/delegation-requests/r%2F1/decide')
    expect(init.method).toBe('POST')
    expect(JSON.parse(init.body)).toEqual({ choice: 'once' })
    expect(init.headers.Authorization).toBe('Bearer tok')
  })

  it('decideDelegation rejects with the server message', async () => {
    const { t, fetch } = make()
    fetch.mockResolvedValue(json(409, { error: 'already decided' }))
    await expect(t.decideDelegation!('r1', 'deny')).rejects.toMatchObject({ message: 'already decided', status: 409 })
  })

  it('delegationRequest reads the request', async () => {
    const { t, fetch } = make()
    fetch.mockResolvedValue(json(200, { request_id: 'r1', prompt: 'the whole prompt' }))
    await expect(t.delegationRequest!('r1')).resolves.toEqual({ prompt: 'the whole prompt' })
    expect(fetch.mock.calls[0][0]).toBe('/api/delegation-requests/r1')
  })
})

describe('subagent typing transport isolation', () => {
  it('preserves child scope instead of mixing its deltas into the main typing callback', () => {
    const { t, socket } = make()
    const h = handlers()
    const off = t.subscribe('s1', {}, h)
    socket.emit({ type: 'agent_events_replay_done', session_id: 's1', last_seq: 0, has_more: false })
    const child = ev(0, { seq: undefined, client_event_id: 'child-delta', transient: true, payload: { text: 'child words', done: false, parent_call_id: 'child-call' } })
    socket.emit(ev(0, { seq: undefined, client_event_id: 'root-delta', transient: true, payload: { text: 'root words', done: false } }))
    socket.emit(child)
    expect(h.onStreaming.mock.calls).toEqual([['root words']])
    expect(h.onEvent).toHaveBeenCalledWith(child)
    off()
  })
})
