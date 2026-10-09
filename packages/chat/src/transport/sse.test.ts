import { describe, it, expect, vi } from 'vitest'
import { SseParser, openSseStream, type SseFrame } from './sse'

// ─── parser ──────────────────────────────────────────────────────────────────

describe('SseParser', () => {
  it('yields a frame per blank-line-terminated block with event, data and id', () => {
    const p = new SseParser()
    const frames = p.push('event: agent_event\nid: 7\ndata: {"a":1}\n\n')
    expect(frames).toEqual([{ event: 'agent_event', id: '7', data: '{"a":1}' }])
  })

  it('reassembles a frame split across chunks, even mid-line', () => {
    const p = new SseParser()
    expect(p.push('event: repl')).toEqual([])
    expect(p.push('ay_done\nda')).toEqual([])
    expect(p.push('ta: {"last_seq":3}\n')).toEqual([])
    expect(p.push('\n')).toEqual([{ event: 'replay_done', data: '{"last_seq":3}' }])
  })

  it('ignores comment lines (the keepalive) and blocks with no data', () => {
    const p = new SseParser()
    expect(p.push(': keepalive\n\n')).toEqual([])
    expect(p.push('event: status\n\n')).toEqual([])
    expect(p.push(': ping\ndata: x\n\n')).toEqual([{ event: 'message', data: 'x' }])
  })

  it('accepts CRLF and bare CR line endings, including a CR at a chunk boundary', () => {
    const p = new SseParser()
    expect(p.push('event: a\r\ndata: 1\r\n\r\n')).toEqual([{ event: 'a', data: '1' }])
    // A \r at the very end of a chunk may be half of a \r\n: it waits for the next chunk.
    expect(p.push('event: b\rdata: 2\r\r')).toEqual([])
    expect(p.push('data: 3\r')).toEqual([{ event: 'b', data: '2' }])
    // ...and when \n follows it, that was one CRLF: 3 and 4 are two data lines of one frame.
    expect(p.push('\ndata: 4\n\n')).toEqual([{ event: 'message', data: '3\n4' }])
  })

  it('joins several data lines with newlines and strips one leading space after the colon', () => {
    const p = new SseParser()
    expect(p.push('data: one\ndata:two\ndata:  three\n\n')).toEqual([{ event: 'message', data: 'one\ntwo\n three' }])
  })

  it('carries the id only on the frame that set it, and reports the last id seen', () => {
    const p = new SseParser()
    const frames = p.push('id: 5\ndata: a\n\ndata: b\n\n')
    expect(frames.map(f => f.id)).toEqual(['5', undefined])
    expect(p.lastId).toBe('5')
  })

  it('reads a retry: field as milliseconds and ignores unknown fields', () => {
    const p = new SseParser()
    p.push('retry: 2500\nfoo: bar\ndata: x\n\n')
    expect(p.retry).toBe(2500)
  })
})

// ─── stream ──────────────────────────────────────────────────────────────────

/** A fetch whose nth call answers with the nth script: a body of chunks, or a status. */
function scriptedFetch(scripts: Array<{ chunks?: string[]; status?: number; hang?: boolean; fail?: boolean }>) {
  const calls: string[] = []
  const controllers: ReadableStreamDefaultController<Uint8Array>[] = []
  const impl = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push(String(input))
    const s = scripts[Math.min(calls.length - 1, scripts.length - 1)]
    if (s.fail) throw new TypeError('network down')
    if (s.status && s.status >= 400) return new Response('nope', { status: s.status })
    const enc = new TextEncoder()
    const body = new ReadableStream<Uint8Array>({
      start(ctl) {
        controllers.push(ctl)
        for (const c of s.chunks ?? []) ctl.enqueue(enc.encode(c))
        if (!s.hang) ctl.close()
      },
      cancel() {},
    })
    init?.signal?.addEventListener('abort', () => {
      const ctl = controllers[controllers.length - 1]
      try { ctl.error(new DOMException('aborted', 'AbortError')) } catch { /* already closed */ }
    })
    return new Response(body, { status: 200, headers: { 'Content-Type': 'text/event-stream' } })
  })
  return { impl: impl as unknown as typeof fetch, calls, controllers }
}

const tick = () => new Promise(r => setTimeout(r, 5))
const until = async (cond: () => boolean) => {
  for (let i = 0; i < 200 && !cond(); i++) await tick()
  expect(cond()).toBe(true)
}

describe('openSseStream', () => {
  it('delivers frames across chunks and reports the connection', async () => {
    const f = scriptedFetch([{ chunks: ['event: a\ndata: 1\n\nev', 'ent: b\ndata: 2\n\n'], hang: true }])
    const frames: SseFrame[] = []
    const onOpen = vi.fn()
    const s = openSseStream({ url: () => '/live', fetch: f.impl, onFrame: fr => frames.push(fr), onOpen })
    await until(() => frames.length === 2)
    expect(frames.map(fr => fr.event)).toEqual(['a', 'b'])
    expect(onOpen).toHaveBeenCalledTimes(1)
    expect(s.connected()).toBe(true)
    s.close()
    expect(s.connected()).toBe(false)
  })

  it('sends Accept: text/event-stream and the extra headers', async () => {
    const f = scriptedFetch([{ hang: true }])
    const s = openSseStream({ url: () => '/live', fetch: f.impl, headers: { 'X-Thing': 'y' }, onFrame: () => {} })
    await until(() => f.calls.length === 1)
    const init = (f.impl as unknown as ReturnType<typeof vi.fn>).mock.calls[0][1] as RequestInit
    expect(init.headers).toMatchObject({ Accept: 'text/event-stream', 'X-Thing': 'y' })
    s.close()
  })

  it('reconnects after a dropped stream, asking from the last id it saw, with the first flag off', async () => {
    const f = scriptedFetch([
      { chunks: ['id: 4\nevent: agent_event\ndata: {}\n\nid: 9\nevent: agent_event\ndata: {}\n\n'] }, // then the server closes
      { hang: true },
    ])
    const onDrop = vi.fn()
    const onOpen = vi.fn()
    const urls: Array<[number, boolean]> = []
    const s = openSseStream({
      url: (after, first) => { urls.push([after, first]); return `/live?after_seq=${after}` },
      fetch: f.impl, backoff: { initial: 1, max: 5 }, onFrame: () => {}, onDrop, onOpen,
    })
    await until(() => f.calls.length === 2)
    expect(urls).toEqual([[0, true], [9, false]])
    expect(onDrop).toHaveBeenCalledTimes(1)
    expect(onOpen).toHaveBeenCalledTimes(2)
    expect(s.lastSeq()).toBe(9)
    s.close()
  })

  it('backs off exponentially after a network failure, then recovers', async () => {
    const f = scriptedFetch([{ fail: true }, { fail: true }, { hang: true }])
    const delays: number[] = []
    const s = openSseStream({
      url: () => '/live', fetch: f.impl, backoff: { initial: 2, max: 100 }, onFrame: () => {},
      onDrop: (delay) => delays.push(delay),
    })
    await until(() => f.calls.length === 3)
    expect(delays).toEqual([2, 4])
    s.close()
  })

  it('stops for good on a 4xx and says so once', async () => {
    const f = scriptedFetch([{ status: 404 }])
    const onEnd = vi.fn()
    const onDrop = vi.fn()
    const s = openSseStream({ url: () => '/live', fetch: f.impl, backoff: { initial: 1, max: 1 }, onFrame: () => {}, onEnd, onDrop })
    await until(() => onEnd.mock.calls.length === 1)
    expect(onEnd).toHaveBeenCalledWith({ reason: 'refused', status: 404 })
    await tick()
    expect(f.calls.length).toBe(1)
    expect(onDrop).not.toHaveBeenCalled()
    expect(s.connected()).toBe(false)
  })

  it('close() aborts the request and never reconnects, even from inside onFrame', async () => {
    const f = scriptedFetch([{ chunks: ['event: end\ndata: {}\n\n'] }, { hang: true }])
    const onEnd = vi.fn()
    const onDrop = vi.fn()
    let stream: ReturnType<typeof openSseStream> | null = null
    stream = openSseStream({
      url: () => '/live', fetch: f.impl, backoff: { initial: 1, max: 1 },
      onFrame: fr => { if (fr.event === 'end') stream?.close() }, onEnd, onDrop,
    })
    await until(() => onEnd.mock.calls.length === 1)
    expect(onEnd).toHaveBeenCalledWith({ reason: 'closed' })
    await tick()
    await tick()
    expect(f.calls.length).toBe(1)
    expect(onDrop).not.toHaveBeenCalled()
  })

  it('a 5xx is a drop: retried with backoff', async () => {
    const f = scriptedFetch([{ status: 503 }, { hang: true }])
    const onDrop = vi.fn()
    const s = openSseStream({ url: () => '/live', fetch: f.impl, backoff: { initial: 1, max: 1 }, onFrame: () => {}, onDrop })
    await until(() => f.calls.length === 2)
    expect(onDrop).toHaveBeenCalledTimes(1)
    s.close()
  })
})
