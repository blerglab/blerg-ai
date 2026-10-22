// The capped read hovercards use (card #127): never more than `max` bytes are read; a reply that is
// larger stops its download as soon as that is known.
import { describe, expect, it, vi } from 'vitest'
import { createFiles, readCapped } from './files'

function stream(chunks: number[], onCancel: () => void, pulled: { n: number }): ReadableStream<Uint8Array> {
  let i = 0
  return new ReadableStream<Uint8Array>({
    pull(c) {
      if (i >= chunks.length) { c.close(); return }
      pulled.n++
      c.enqueue(new Uint8Array(chunks[i++]))
    },
    cancel() { onCancel() },
  })
}

describe('readCapped', () => {
  it('a Content-Length over the cap: null, the body never read, the download stopped', async () => {
    const cancel = vi.fn()
    const pulled = { n: 0 }
    const abort = new AbortController()
    const r = new Response(stream([10, 10], cancel, pulled), { headers: { 'Content-Length': '2000' } })
    expect(await readCapped(r, 1000, abort)).toBeNull()
    expect(abort.signal.aborted).toBe(true)
    expect(pulled.n).toBeLessThanOrEqual(1)
  })

  it('no Content-Length: streamed, stopped as soon as it passes the cap', async () => {
    const cancel = vi.fn()
    const pulled = { n: 0 }
    const abort = new AbortController()
    const r = new Response(stream([400, 400, 400, 400, 400], cancel, pulled))
    expect(await readCapped(r, 1000, abort)).toBeNull()
    expect(cancel).toHaveBeenCalled()
    expect(abort.signal.aborted).toBe(true)
    expect(pulled.n).toBeLessThan(5)
  })

  it('within the cap: the bytes', async () => {
    const r = new Response(stream([3, 4], () => {}, { n: 0 }), { headers: { 'Content-Type': 'text/x-diff' } })
    const b = await readCapped(r, 1000)
    expect(b?.size).toBe(7)
  })

  it('no stream and no length: the size cannot be told, so nothing', async () => {
    const r = { headers: new Headers(), body: null, blob: vi.fn() } as unknown as Response
    expect(await readCapped(r, 1000)).toBeNull()
    expect(r.blob).not.toHaveBeenCalled()
    const known = { headers: new Headers({ 'Content-Length': '5' }), body: null, blob: vi.fn(async () => new Blob(['12345'])) } as unknown as Response
    expect((await readCapped(known, 1000))?.size).toBe(5)
  })
})

describe('createFiles rawCapped', () => {
  it('reads the raw route with an abort signal and answers null past the cap', async () => {
    let signal: AbortSignal | undefined
    const fetch = vi.fn(async (_u: RequestInfo | URL, init?: RequestInit) => {
      signal = init?.signal ?? undefined
      return new Response('x'.repeat(50), { headers: { 'Content-Length': '50' } })
    })
    const files = createFiles({ base: s => `/api/sessions/${s}`, fetch: fetch as unknown as typeof globalThis.fetch })
    expect(await files.rawCapped!('s1', 'd1', 10)).toBeNull()
    expect(String(fetch.mock.calls[0][0])).toBe('/api/sessions/s1/artifacts/d1/raw')
    expect(signal?.aborted).toBe(true)
    expect((await files.rawCapped!('s1', 'd1', 100))?.size).toBe(50)
  })
})
