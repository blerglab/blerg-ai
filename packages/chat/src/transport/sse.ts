// A server-sent-events reader over fetch. EventSource cannot carry a header or a body stream the
// app controls, so the proxy transport reads `text/event-stream` itself: a parser that takes the
// bytes as they come (a frame can be cut anywhere, even inside a field name) and a loop that
// reconnects with backoff, asking again from the last persisted seq it saw (the `id:` field).
//
// Frames follow the WHATWG spec: `field: value` lines, a block ends at a blank line, a line
// starting with ':' is a comment (the runner's keepalive), CRLF / LF / CR all end a line, and a
// block with no data is dropped.

export interface SseFrame {
  /** The `event:` field, 'message' when the block had none. */
  event: string
  /** The data lines joined with '\n'. */
  data: string
  /** The `id:` field of this block, when it had one. */
  id?: string
}

export class SseParser {
  /** The last `id:` seen, across frames — what a reconnect resumes from. */
  lastId: string | undefined
  /** The last `retry:` the server asked for, in milliseconds. */
  retry: number | undefined
  private buf = ''
  private event = ''
  private data: string[] = []
  private id: string | undefined

  /** Feeds a chunk of text and returns the frames it completed. */
  push(chunk: string): SseFrame[] {
    this.buf += chunk
    const out: SseFrame[] = []
    let start = 0
    for (;;) {
      // The next line end: \r\n, \n or \r. A \r as the very last character may be the first half
      // of a \r\n split across chunks, so it waits for the next chunk.
      let end = -1
      let skip = 1
      for (let i = start; i < this.buf.length; i++) {
        const c = this.buf.charCodeAt(i)
        if (c === 10) { end = i; break }
        if (c === 13) {
          if (i === this.buf.length - 1) break
          end = i
          if (this.buf.charCodeAt(i + 1) === 10) skip = 2
          break
        }
      }
      if (end < 0) break
      const line = this.buf.slice(start, end)
      start = end + skip
      const frame = this.line(line)
      if (frame) out.push(frame)
    }
    this.buf = this.buf.slice(start)
    return out
  }

  private line(line: string): SseFrame | null {
    if (line === '') {
      const frame = this.data.length > 0
        ? { event: this.event || 'message', data: this.data.join('\n'), ...(this.id !== undefined ? { id: this.id } : {}) }
        : null
      this.event = ''
      this.data = []
      this.id = undefined
      return frame
    }
    if (line.startsWith(':')) return null
    const colon = line.indexOf(':')
    const field = colon < 0 ? line : line.slice(0, colon)
    let value = colon < 0 ? '' : line.slice(colon + 1)
    if (value.startsWith(' ')) value = value.slice(1)
    switch (field) {
      case 'event': this.event = value; break
      case 'data': this.data.push(value); break
      case 'id':
        // An id holding a NUL is ignored, as the spec says.
        if (!value.includes('\0')) { this.id = value; this.lastId = value }
        break
      case 'retry': {
        const n = Number(value)
        if (/^\d+$/.test(value) && Number.isFinite(n)) this.retry = n
        break
      }
      default: break // unknown fields are ignored
    }
    return null
  }
}

export interface SseStreamOptions {
  /** The URL to open: given the seq to resume after (0 at first) and whether this is the first
   *  attempt (the only one that may ask for a tail page). */
  url: (afterSeq: number, first: boolean) => string
  fetch?: typeof fetch
  headers?: Record<string, string>
  /** The seq to start after (what the caller already has). */
  afterSeq?: number
  onFrame: (frame: SseFrame) => void
  /** A response is open and frames may follow (once per connection). */
  onOpen?: () => void
  /** The stream ended or failed and a new attempt follows after `delayMs`. */
  onDrop?: (delayMs: number) => void
  /** No more attempts: close() was called, or the server refused the request (4xx). */
  onEnd?: (end: { reason: 'closed' } | { reason: 'refused'; status: number }) => void
  backoff?: { initial?: number; max?: number }
}

export interface SseStream {
  /** Aborts the request in flight and stops reconnecting. */
  close(): void
  connected(): boolean
  /** The highest numeric `id:` seen so far (what a reconnect asks after). */
  lastSeq(): number
}

const DEFAULT_BACKOFF = { initial: 1000, max: 30000 }

export function openSseStream(opts: SseStreamOptions): SseStream {
  const fetchImpl = opts.fetch ?? globalThis.fetch.bind(globalThis)
  const initial = opts.backoff?.initial ?? DEFAULT_BACKOFF.initial
  const max = opts.backoff?.max ?? DEFAULT_BACKOFF.max
  let closed = false
  let connected = false
  let seq = opts.afterSeq ?? 0
  let delay = initial
  let ctl: AbortController | null = null
  let wake: (() => void) | null = null
  let ended = false

  const end = (e: Parameters<NonNullable<SseStreamOptions['onEnd']>>[0]) => {
    if (ended) return
    ended = true
    opts.onEnd?.(e)
  }

  // A pause close() can cut short.
  const sleep = (ms: number) => new Promise<void>(resolve => {
    const t = setTimeout(() => { wake = null; resolve() }, ms)
    wake = () => { clearTimeout(t); wake = null; resolve() }
  })

  const dropAndWait = async () => {
    const wait = delay
    delay = Math.min(delay * 2, max)
    opts.onDrop?.(wait)
    await sleep(wait)
  }

  const run = async () => {
    let first = true
    while (!closed) {
      ctl = new AbortController()
      let res: Response
      try {
        res = await fetchImpl(opts.url(seq, first), {
          headers: { Accept: 'text/event-stream', ...opts.headers },
          signal: ctl.signal,
          cache: 'no-store',
        })
      } catch {
        if (closed) break
        await dropAndWait()
        continue
      }
      first = false
      if (closed) break
      if (!res.ok || !res.body) {
        // The server refused: asking again would get the same answer. A 5xx or a missing body is
        // a hiccup, retried like a drop.
        if (res.status >= 400 && res.status < 500) {
          end({ reason: 'refused', status: res.status })
          return
        }
        await dropAndWait()
        continue
      }
      connected = true
      delay = initial
      opts.onOpen?.()
      const parser = new SseParser()
      const reader = res.body.getReader()
      const decoder = new TextDecoder()
      try {
        for (;;) {
          const { done, value } = await reader.read()
          if (done) break
          for (const frame of parser.push(decoder.decode(value, { stream: true }))) {
            if (frame.id !== undefined) {
              const n = Number(frame.id)
              if (Number.isFinite(n) && n > seq) seq = n
            }
            opts.onFrame(frame)
            if (closed) break
          }
          if (closed) break
        }
      } catch {
        // The connection broke (or close() aborted it); the loop decides which.
      }
      connected = false
      if (closed) break
      if (parser.retry !== undefined) delay = Math.max(initial, Math.min(parser.retry, max))
      await dropAndWait()
    }
    end({ reason: 'closed' })
  }

  void run()

  return {
    close() {
      if (closed) return
      closed = true
      connected = false
      ctl?.abort()
      wake?.()
      end({ reason: 'closed' })
    },
    connected: () => connected,
    lastSeq: () => seq,
  }
}
