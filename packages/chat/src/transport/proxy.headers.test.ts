// ProxyTransport for an app whose own sign-in is a bearer token rather than a cookie: the host's
// headers ride on every request (the live stream's among them), and a 401 is reported to it.
import { describe, expect, it, vi } from 'vitest'
import { createProxyTransport } from './proxy'
import type { SubscribeHandlers } from './types'

type Call = { method: string; url: string; headers?: Record<string, string> }

function router(routes: Array<[RegExp, () => Response]>) {
  const calls: Call[] = []
  const impl = async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = new URL(String(input), 'http://app.example')
    calls.push({ method: init.method ?? 'GET', url: url.pathname + url.search, headers: init.headers as Record<string, string> })
    for (const [re, route] of routes) if (re.test(url.pathname + url.search)) return route()
    return new Response('no route', { status: 404 })
  }
  return { calls, fetch: impl as unknown as typeof fetch }
}

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

// A stream that sends one frame and stays open.
function sse(text: string): Response {
  const body = new ReadableStream<Uint8Array>({ start(ctl) { ctl.enqueue(new TextEncoder().encode(text)) } })
  return new Response(body, { status: 200, headers: { 'Content-Type': 'text/event-stream' } })
}

function handlers(): SubscribeHandlers & { replays: number } {
  const h = {
    replays: 0,
    onEvent: () => {},
    onStreaming: () => {},
    onReplayDone: () => { h.replays++ },
    onStatus: () => {},
    onConnection: () => {},
  }
  return h
}

const until = async (cond: () => boolean) => {
  for (let i = 0; i < 200 && !cond(); i++) await new Promise(r => setTimeout(r, 5))
  expect(cond()).toBe(true)
}

const routes = (): Array<[RegExp, () => Response]> => [
  [/\/sessions\/s1$/, () => json({ status: 'idle', started_at: '2026-10-06T00:00:00Z' })],
  [/\/events\/live/, () => sse('event: replay_done\ndata: {"last_seq":0,"has_more":false}\n\n')],
  [/\/messages$/, () => json({ ok: true })],
]

describe('ProxyTransport with host headers', () => {
  it('sends the host’s headers on every request, read fresh each time, the live stream included', async () => {
    const r = router(routes())
    let n = 0
    const t = createProxyTransport({
      baseUrl: '/api/chat',
      fetch: r.fetch,
      backoff: { initial: 1, max: 5 },
      headers: async () => ({ Authorization: `Bearer tok-${++n}` }),
    })
    await t.session('s1')
    const h = handlers()
    const off = t.subscribe('s1', {}, h)
    await until(() => h.replays === 1)
    await t.send('s1', 'hi')
    off()
    const auth = r.calls.map(c => c.headers?.Authorization)
    expect(auth).toHaveLength(3)
    expect(new Set(auth).size).toBe(3) // a fresh token per request
    expect(auth.every(a => /^Bearer tok-\d+$/.test(a ?? ''))).toBe(true)
    // The request's own headers survive beside the host's.
    expect(r.calls[1].headers?.Accept).toBe('text/event-stream')
    expect(r.calls[2].headers?.['Content-Type']).toBe('application/json')
  })

  it('reports a 401 to the host', async () => {
    const r = router([[/\/sessions\/s1$/, () => new Response('no', { status: 401 })]])
    const onUnauthorized = vi.fn()
    const t = createProxyTransport({ baseUrl: '/api/chat', fetch: r.fetch, onUnauthorized })
    await expect(t.session('s1')).rejects.toThrow()
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
  })

  it('sends no extra headers when the host gives none', async () => {
    const r = router(routes())
    const t = createProxyTransport({ baseUrl: '/api/chat', fetch: r.fetch })
    await t.session('s1')
    expect(r.calls[0].headers?.Authorization).toBeUndefined()
  })
})
