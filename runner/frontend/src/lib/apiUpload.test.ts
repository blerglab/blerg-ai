import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { apiUpload } from '../apiFetch'

// A stand-in XMLHttpRequest that lets a test play the network's part.
class FakeXHR {
  static last: FakeXHR
  method = ''
  url = ''
  headers: Record<string, string> = {}
  body: unknown
  status = 0
  responseText = ''
  upload: { onprogress: ((e: { lengthComputable: boolean; loaded: number; total: number }) => void) | null } = { onprogress: null }
  onload: (() => void) | null = null
  onerror: (() => void) | null = null
  onabort: (() => void) | null = null
  aborted = false
  constructor() { FakeXHR.last = this }
  open(method: string, url: string) { this.method = method; this.url = url }
  setRequestHeader(k: string, v: string) { this.headers[k] = v }
  getResponseHeader() { return 'application/json' }
  send(body: unknown) { this.body = body }
  abort() { this.aborted = true; this.onabort?.() }
}

beforeEach(() => { vi.stubGlobal('XMLHttpRequest', FakeXHR); localStorage.clear() })
afterEach(() => { vi.unstubAllGlobals() })

describe('apiUpload', () => {
  it('sends the body with the given headers and reports progress', async () => {
    const seen: Array<[number, number]> = []
    const blob = new Blob(['abc'])
    const p = apiUpload('/api/x', { method: 'POST', body: blob, headers: { 'X-Artifact-Name': 'a.pdf' } }, (l, t) => seen.push([l, t]))
    const x = FakeXHR.last
    expect(x.method).toBe('POST')
    expect(x.url).toBe('/api/x')
    expect(x.headers['X-Artifact-Name']).toBe('a.pdf')
    expect(x.body).toBe(blob)
    x.upload.onprogress?.({ lengthComputable: true, loaded: 1, total: 3 })
    x.upload.onprogress?.({ lengthComputable: false, loaded: 2, total: 0 }) // not reported
    x.upload.onprogress?.({ lengthComputable: true, loaded: 3, total: 3 })
    x.status = 201
    x.responseText = '{"id":"z"}'
    x.onload?.()
    const r = await p
    expect(seen).toEqual([[1, 3], [3, 3]])
    expect(r.status).toBe(201)
    expect(await r.json()).toEqual({ id: 'z' })
  })

  it('turns a network failure into a TypeError and a cancel into an AbortError', async () => {
    const p1 = apiUpload('/api/x', { body: new Blob(['a']) })
    FakeXHR.last.onerror?.()
    await expect(p1).rejects.toBeInstanceOf(TypeError)

    const ctl = new AbortController()
    const p2 = apiUpload('/api/x', { body: new Blob(['a']), signal: ctl.signal })
    const x = FakeXHR.last
    ctl.abort()
    expect(x.aborted).toBe(true)
    await expect(p2).rejects.toMatchObject({ name: 'AbortError' })
  })

  it('does not start when the signal is already aborted', async () => {
    const ctl = new AbortController()
    ctl.abort()
    const p = apiUpload('/api/x', { body: new Blob(['a']), signal: ctl.signal })
    expect(FakeXHR.last.aborted).toBe(true)
    await expect(p).rejects.toMatchObject({ name: 'AbortError' })
  })
})
