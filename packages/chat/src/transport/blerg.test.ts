import { describe, it, expect, vi, afterEach, type Mock } from 'vitest'
import { createBlergTransport, type BlergSocket, type ChatBrowserMessage } from './blerg'
import type { SubscribeHandlers } from './types'
import type { AgentEvent } from '../types'

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

type MockHandlers = { [K in keyof SubscribeHandlers]: Mock<SubscribeHandlers[K]> }
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
