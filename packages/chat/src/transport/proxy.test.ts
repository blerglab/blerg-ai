import { describe, it, expect, vi, type Mock } from 'vitest'
import { createProxyTransport } from './proxy'
import type { SubscribeHandlers } from './types'

// ─── a fetch that routes by URL, streaming SSE bodies ────────────────────────

type Route = (url: URL, init: RequestInit) => Response | Promise<Response>

function sse(chunks: string[], hang = false): Response {
  const enc = new TextEncoder()
  const body = new ReadableStream<Uint8Array>({
    start(ctl) {
      for (const c of chunks) ctl.enqueue(enc.encode(c))
      if (!hang) ctl.close()
    },
  })
  return new Response(body, { status: 200, headers: { 'Content-Type': 'text/event-stream' } })
}

const json = (body: unknown, status = 200, headers: Record<string, string> = {}) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...headers } })

function router() {
  const calls: Array<{ method: string; url: string; body?: string; headers?: Record<string, string> }> = []
  const routes: Array<[RegExp, Route]> = []
  const impl = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = new URL(String(input), 'http://app.example')
    calls.push({ method: init.method ?? 'GET', url: url.pathname + url.search, body: typeof init.body === 'string' ? init.body : undefined, headers: init.headers as Record<string, string> })
    for (const [re, route] of routes) if (re.test(url.pathname + url.search)) return route(url, init)
    return new Response('no route', { status: 404 })
  })
  return {
    calls,
    on: (re: RegExp, route: Route) => routes.push([re, route]),
    fetch: impl as unknown as typeof fetch,
  }
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

const tick = () => new Promise(r => setTimeout(r, 5))
const until = async (cond: () => boolean) => {
  for (let i = 0; i < 200 && !cond(); i++) await tick()
  expect(cond()).toBe(true)
}

const frame = (event: string, data: unknown, id?: number) =>
  `${id !== undefined ? `id: ${id}\n` : ''}event: ${event}\ndata: ${JSON.stringify(data)}\n\n`

const make = (r = router()) => ({ r, t: createProxyTransport({ baseUrl: '/blerg', fetch: r.fetch, backoff: { initial: 1, max: 5 } }) })

// ─── subscribe ───────────────────────────────────────────────────────────────

describe('createProxyTransport subscribe', () => {
  it('opens the live stream with tail and maps every frame kind', async () => {
    const { r, t } = make()
    r.on(/\/events\/live/, () => sse([
      frame('status', { session_id: 's1', status: 'running' }),
      frame('agent_event', { type: 'agent_event', session_id: 's1', client_event_id: 'c1', seq: 1, ts: 't', kind: 'user_message', payload: { text: 'hi', source: 'user' } }, 1),
      frame('agent_event', { type: 'agent_event', session_id: 's1', client_event_id: 'd', ts: 't', kind: 'assistant_text', payload: { text: 'He', done: false }, transient: true }),
      frame('replay_done', { type: 'agent_events_replay_done', session_id: 's1', last_seq: 1, has_more: false, older: true, has_older: false, first_seq: 1, server_time: 'now' }),
    ], true))
    const h = handlers()
    const off = t.subscribe('s1', { tail: true }, h)
    await until(() => h.onReplayDone.mock.calls.length === 1)
    expect(r.calls[0].url).toBe('/blerg/sessions/s1/events/live?after_seq=0&tail=1&limit=200')
    expect(h.onConnection).toHaveBeenCalledWith(true)
    expect(t.connected()).toBe(true)
    expect(h.onStatus).toHaveBeenCalledWith('running')
    expect(h.onEvent).toHaveBeenCalledTimes(1)
    expect(h.onEvent.mock.calls[0][0]).toMatchObject({ seq: 1, kind: 'user_message', session_id: 's1' })
    expect(h.onStreaming).toHaveBeenCalledWith('He')
    expect(h.onReplayDone).toHaveBeenCalledWith({ lastSeq: 1, hasMore: false, older: true, hasOlder: false, firstSeq: 1, serverTime: 'now' })
    off()
    expect(t.connected()).toBe(false)
  })

  it('fills in the fields a bare runner event lacks', async () => {
    const { r, t } = make()
    r.on(/\/events\/live/, () => sse([frame('agent_event', { seq: 3, ts: 't', kind: 'turn_done', payload: {} }, 3)], true))
    const h = handlers()
    const off = t.subscribe('s1', {}, h)
    await until(() => h.onEvent.mock.calls.length === 1)
    expect(h.onEvent.mock.calls[0][0]).toEqual({ type: 'agent_event', session_id: 's1', client_event_id: 'seq:3', seq: 3, ts: 't', kind: 'turn_done', payload: {} })
    off()
  })

  it('a dropped stream locks the composer and reconnects after the last seq, without the tail', async () => {
    const { r, t } = make()
    let n = 0
    r.on(/\/events\/live/, () => (++n === 1
      ? sse([frame('agent_event', { seq: 5, ts: 't', kind: 'turn_done', payload: {} }, 5), frame('agent_event', { seq: 8, ts: 't', kind: 'turn_done', payload: {} }, 8)])
      : sse([], true)))
    const h = handlers()
    const off = t.subscribe('s1', { tail: true }, h)
    await until(() => r.calls.length === 2)
    expect(h.onConnection.mock.calls.map(c => c[0])).toEqual([true, false, true])
    expect(r.calls[1].url).toBe('/blerg/sessions/s1/events/live?after_seq=8&limit=200')
    off()
  })

  it('an end frame reports the final status and stops reconnecting', async () => {
    const { r, t } = make()
    r.on(/\/events\/live/, () => sse([frame('end', { lifecycle: 'ended', terminal: true })]))
    const h = handlers()
    t.subscribe('s1', {}, h)
    await until(() => h.onStatus.mock.calls.length === 1)
    expect(h.onStatus).toHaveBeenCalledWith('ended')
    await tick()
    await tick()
    expect(r.calls).toHaveLength(1)
    expect(h.onConnection).not.toHaveBeenCalledWith(false)
  })

  it('an end frame for a failed session says error, and a status in it wins', async () => {
    const { r, t } = make()
    let n = 0
    r.on(/\/events\/live/, () => sse([frame('end', ++n === 1 ? { lifecycle: 'error' } : { status: 'stopped' })]))
    const h1 = handlers()
    t.subscribe('s1', {}, h1)
    await until(() => h1.onStatus.mock.calls.length === 1)
    expect(h1.onStatus).toHaveBeenCalledWith('error')
    const h2 = handlers()
    t.subscribe('s2', {}, h2)
    await until(() => h2.onStatus.mock.calls.length === 1)
    expect(h2.onStatus).toHaveBeenCalledWith('stopped')
  })

  it('a refused stream (the app said no) reports the link down and gives up', async () => {
    const { r, t } = make()
    r.on(/\/events\/live/, () => new Response('', { status: 403 }))
    const h = handlers()
    t.subscribe('s1', {}, h)
    await until(() => h.onConnection.mock.calls.length === 1)
    expect(h.onConnection).toHaveBeenCalledWith(false)
    await tick()
    expect(r.calls).toHaveLength(1)
  })

  it('unknown frames and malformed data are ignored', async () => {
    const { r, t } = make()
    r.on(/\/events\/live/, () => sse(['event: agent_event\ndata: {not json\n\n', frame('mystery', {}), frame('status', { session_id: 's1', status: 'idle' })], true))
    const h = handlers()
    const off = t.subscribe('s1', {}, h)
    await until(() => h.onStatus.mock.calls.length === 1)
    expect(h.onEvent).not.toHaveBeenCalled()
    off()
  })
})

// ─── the request operations ──────────────────────────────────────────────────

describe('createProxyTransport requests', () => {
  it('loadOlder reads the older page and maps it', async () => {
    const { r, t } = make()
    r.on(/\/events\?/, () => json({ events: [{ seq: 10, ts: 't', kind: 'turn_done', payload: {} }, { seq: 11, ts: 't', kind: 'turn_done', payload: {} }], has_older: true, first_seq: 10, server_time: 'srv' }))
    await expect(t.loadOlder('s1', 12, 2)).resolves.toEqual({
      events: [
        { type: 'agent_event', session_id: 's1', client_event_id: 'seq:10', seq: 10, ts: 't', kind: 'turn_done', payload: {} },
        { type: 'agent_event', session_id: 's1', client_event_id: 'seq:11', seq: 11, ts: 't', kind: 'turn_done', payload: {} },
      ],
      hasOlder: true, firstSeq: 10, serverTime: 'srv',
    })
    expect(r.calls[0]).toMatchObject({ method: 'GET', url: '/blerg/sessions/s1/events?before_seq=12&limit=2' })
  })

  it('loadOlder falls back to has_more, the first event and the Date header when the page says less', async () => {
    const { r, t } = make()
    r.on(/\/events\?/, () => json({ events: [{ seq: 4, ts: 't', kind: 'turn_done', payload: {} }], has_more: true }, 200, { Date: 'Mon, 06 Oct 2026 00:00:00 GMT' }))
    await expect(t.loadOlder('s1', 5, 1)).resolves.toMatchObject({ hasOlder: true, firstSeq: 4, serverTime: 'Mon, 06 Oct 2026 00:00:00 GMT' })
    const r2 = router()
    r2.on(/\/events\?/, () => json({}, 500))
    const { t: t2 } = make(r2)
    await expect(t2.loadOlder('s1', 5, 1)).rejects.toThrow('HTTP 500')
  })

  it('session maps SessionMeta as the proxy sends it, or from the runner status body', async () => {
    const { r, t } = make()
    r.on(/\/sessions\/s1$/, () => json({ status: 'waiting', engine: 'claude', model: 'm', effort: 'high', runtime: 'cluster', started_at: 'a', ended_at: null }))
    await expect(t.session('s1')).resolves.toEqual({ status: 'waiting', engine: 'claude', model: 'm', effort: 'high', runtime: 'cluster', started_at: 'a', ended_at: null })
    r.on(/\/sessions\/s2$/, () => json({ lifecycle: 'running', runtime: 'daemon', error_reason: '', end_reason: '', ended_by: null }))
    await expect(t.session('s2')).resolves.toEqual({
      status: 'running', runtime: 'daemon', started_at: '', engine: undefined, model: undefined, effort: undefined, ended_at: undefined,
      end_reason: undefined, ended_by: null, error_reason: undefined, message: undefined,
    })
    r.on(/\/sessions\/s3$/, () => json({ lifecycle: 'ended', end_reason: 'stopped_by_agent', ended_by: { kind: 'agent' } }))
    await expect(t.session('s3')).resolves.toMatchObject({ status: 'ended', end_reason: 'stopped_by_agent', ended_by: { kind: 'agent' } })
    r.on(/\/sessions\/s5$/, () => json({ lifecycle: 'error', error_reason: 'image pull failed' }))
    await expect(t.session('s5')).resolves.toMatchObject({ status: 'error', error_reason: 'image pull failed' })
    r.on(/\/sessions\/s4$/, () => json({}, 404))
    await expect(t.session('s4')).rejects.toThrow('HTTP 404')
  })

  it('send posts the text and reports whether the app took it', async () => {
    const { r, t } = make()
    let ok = true
    r.on(/\/messages$/, () => new Response(ok ? '{}' : 'no', { status: ok ? 202 : 503 }))
    await expect(t.send('s1', 'hello')).resolves.toEqual({ queued: true })
    expect(r.calls[0]).toMatchObject({ method: 'POST', url: '/blerg/sessions/s1/messages', body: '{"text":"hello"}', headers: { 'Content-Type': 'application/json' } })
    ok = false
    await expect(t.send('s1', 'again')).resolves.toEqual({ queued: false })
  })

  it('stop posts to the stop route; a session already gone is fine', async () => {
    const { r, t } = make()
    let status = 204
    r.on(/\/stop$/, () => new Response(null, { status }))
    await expect(t.stop('s1')).resolves.toBeUndefined()
    expect(r.calls[0]).toMatchObject({ method: 'POST', url: '/blerg/sessions/s1/stop' })
    status = 404
    await expect(t.stop('s1')).resolves.toBeUndefined()
    status = 500
    await expect(t.stop('s1')).rejects.toThrow('HTTP 500')
  })

  it('interrupt posts to the interrupt route; an app without it (404/501) is not an error', async () => {
    const { r, t } = make()
    let status = 204
    r.on(/\/interrupt$/, () => new Response(null, { status }))
    await expect(t.interrupt!('s1')).resolves.toBeUndefined()
    expect(r.calls[0]).toMatchObject({ method: 'POST', url: '/blerg/sessions/s1/interrupt' })
    status = 404
    await expect(t.interrupt!('s1')).resolves.toBeUndefined()
    status = 501
    await expect(t.interrupt!('s1')).resolves.toBeUndefined()
    status = 500
    await expect(t.interrupt!('s1')).rejects.toThrow('HTTP 500')
  })

  it('setModel posts model and effort; an app without the route (501) is not an error', async () => {
    const { r, t } = make()
    let status = 204
    r.on(/\/model$/, () => new Response(null, { status }))
    await expect(t.setModel!('s1', 'm', 'low')).resolves.toBeUndefined()
    expect(r.calls[0]).toMatchObject({ method: 'POST', url: '/blerg/sessions/s1/model', body: '{"model":"m","effort":"low"}' })
    status = 501
    await expect(t.setModel!('s1', 'm')).resolves.toBeUndefined()
    status = 400
    await expect(t.setModel!('s1', 'm')).rejects.toThrow('HTTP 400')
  })

  it('files go through the proxy routes with the same shapes', async () => {
    const { r, t } = make()
    r.on(/\/artifacts$/, () => json({ artifacts: [{ id: 'a1', name: 'f', size: 1 }] }))
    r.on(/\/artifacts\/a1\/raw$/, () => new Response('bytes'))
    r.on(/\/artifacts\/a1$/, () => new Response(null, { status: 204 }))
    await expect(t.files!.list('s1')).resolves.toEqual([{ id: 'a1', name: 'f', size: 1 }])
    expect(await (await t.files!.raw('s1', 'a1')).text()).toBe('bytes')
    await t.files!.remove('s1', 'a1')
    expect(r.calls.map(c => `${c.method} ${c.url}`)).toEqual([
      'GET /blerg/sessions/s1/artifacts',
      'GET /blerg/sessions/s1/artifacts/a1/raw',
      'DELETE /blerg/sessions/s1/artifacts/a1',
    ])
  })

  it('connected() is false until a stream is open', () => {
    const { t } = make()
    expect(t.connected()).toBe(false)
  })
})
