import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { FEW_TRIES, imageQueueState, MAX_DELAY_MS, MAX_IN_FLIGHT, MAX_TRIES, queuedRead, resetImageQueueBackoff } from './imageQueue'

// A refusal as the transport raises it: the status, the sentence and the server's code.
class Refused extends Error {
  status: number
  serverText: string
  code?: string
  constructor(status: number, code?: string, serverText = 'refused') {
    super(serverText)
    this.status = status
    this.code = code
    this.serverText = serverText
  }
}
const busy = () => new Refused(409, 'busy', 'The workspace file reader is busy.')

beforeEach(() => resetImageQueueBackoff())
afterEach(() => {
  vi.useRealTimers()
  resetImageQueueBackoff()
})

describe('the folder-read queue', () => {
  it('keeps at most two reads in flight and retries a busy answer until it loads', async () => {
    let inFlight = 0
    let peak = 0
    let calls = 0
    const read = async (name: string) => {
      calls++
      inFlight++
      peak = Math.max(peak, inFlight)
      await new Promise(r => setTimeout(r, 5))
      inFlight--
      if (calls <= 4) throw busy() // the server is busy for the first tries
      return name
    }
    const got = await Promise.all(['a', 'b', 'c', 'd', 'e'].map(n => queuedRead(() => read(n))))
    expect(got).toEqual(['a', 'b', 'c', 'd', 'e'])
    expect(peak).toBeLessThanOrEqual(MAX_IN_FLIGHT)
    expect(imageQueueState()).toEqual({ active: 0, waiting: 0 })
  }, 20_000)

  it.each([
    ['a 404', new Refused(404, 'not_found')],
    ['a session that is not running', new Refused(409, 'not_running', 'The session has no live workspace daemon.')],
    ['a 409 without a code (an older server, a proxy)', new Refused(409, undefined, 'The workspace file reader is busy.')],
    ['an error that is no refusal', new Error('boom')],
  ])('does not retry %s, and frees its place', async (_name, error) => {
    let calls = 0
    await expect(queuedRead(async () => { calls++; throw error }, undefined, { untilAborted: true })).rejects.toBe(error)
    expect(calls).toBe(1)
    expect(imageQueueState()).toEqual({ active: 0, waiting: 0 })
  })

  it('drops a waiting read when it is cancelled', async () => {
    const hold: Array<() => void> = []
    const slow = () => queuedRead(() => new Promise<string>(r => hold.push(() => r('x'))))
    const a = slow()
    const b = slow()
    await new Promise(r => setTimeout(r, 0))
    const ac = new AbortController()
    const c = queuedRead(async () => 'never', ac.signal)
    await new Promise(r => setTimeout(r, 0))
    expect(imageQueueState().waiting).toBe(1)
    ac.abort()
    await expect(c).rejects.toThrow()
    expect(imageQueueState().waiting).toBe(0)
    hold.forEach(f => f())
    await Promise.all([a, b])
    expect(imageQueueState()).toEqual({ active: 0, waiting: 0 })
  })

  it('a document image keeps trying a busy server past MAX_TRIES, the wait capped, until it loads', async () => {
    vi.useFakeTimers()
    let calls = 0
    const read = async () => {
      if (++calls <= MAX_TRIES + 6) throw busy()
      return 'loaded'
    }
    const p = queuedRead(read, new AbortController().signal, { untilAborted: true })
    await vi.advanceTimersByTimeAsync((MAX_TRIES + 6) * (MAX_DELAY_MS + 250))
    await expect(p).resolves.toBe('loaded')
    expect(calls).toBe(MAX_TRIES + 7)
  })

  it('stops when the document closes', async () => {
    vi.useFakeTimers()
    const ac = new AbortController()
    const p = queuedRead(async () => { throw busy() }, ac.signal, { untilAborted: true })
    const settled = expect(p).rejects.toThrow()
    await vi.advanceTimersByTimeAsync(20_000)
    ac.abort()
    await settled
    expect(imageQueueState()).toEqual({ active: 0, waiting: 0 })
  })

  // The README's MORE POWER thumbnail: the daemon dropped its reply, and the server answered 409
  // daemon_timeout after its timeout. Reading again helps, but each try can hold a server slot for
  // 10 s, so it is tried a few times, not for as long as the document is open.
  it.each(['daemon_timeout', 'changed'])('reads again after %s, at most FEW_TRIES times even while open', async code => {
    vi.useFakeTimers()
    let calls = 0
    const p = queuedRead(async () => { calls++; throw new Refused(409, code) }, new AbortController().signal, { untilAborted: true })
    const settled = expect(p).rejects.toMatchObject({ code })
    await vi.advanceTimersByTimeAsync(60_000)
    await settled
    expect(calls).toBe(FEW_TRIES)
  })

  it('a reply that did not arrive once, then does, loads', async () => {
    let calls = 0
    const read = async () => {
      if (++calls === 1) throw new Refused(409, 'daemon_timeout', 'The session daemon did not provide a workspace file.')
      return 'loaded'
    }
    await expect(queuedRead(read, undefined, { untilAborted: true })).resolves.toBe('loaded')
    expect(calls).toBe(2)
  })

  it('holds every read of the page through one backoff after a refusal', async () => {
    vi.useFakeTimers()
    const starts: Array<[string, number]> = []
    let first = true
    const t0 = Date.now()
    const a = queuedRead(async () => {
      starts.push(['a', Date.now() - t0])
      if (first) { first = false; throw busy() }
      return 'a'
    })
    await vi.advanceTimersByTimeAsync(0)
    // A second image asks after the refusal: it waits for the same backoff instead of probing.
    const b = queuedRead(async () => { starts.push(['b', Date.now() - t0]); return 'b' })
    await vi.advanceTimersByTimeAsync(0)
    expect(starts.map(s => s[0])).toEqual(['a'])
    await vi.advanceTimersByTimeAsync(MAX_DELAY_MS)
    await expect(Promise.all([a, b])).resolves.toEqual(['a', 'b'])
    expect(starts.find(s => s[0] === 'b')![1]).toBeGreaterThanOrEqual(250)
  })

  it('pauses retries while the tab is hidden and resumes when it is shown', async () => {
    vi.useFakeTimers()
    let state: DocumentVisibilityState = 'hidden'
    const spy = vi.spyOn(document, 'visibilityState', 'get').mockImplementation(() => state)
    try {
      let calls = 0
      const p = queuedRead(async () => { if (++calls === 1) throw busy(); return 'shown' }, new AbortController().signal, { untilAborted: true })
      await vi.advanceTimersByTimeAsync(30_000)
      expect(calls).toBe(1) // hidden: no retry, however long
      state = 'visible'
      document.dispatchEvent(new Event('visibilitychange'))
      await vi.advanceTimersByTimeAsync(MAX_DELAY_MS)
      await expect(p).resolves.toBe('shown')
      expect(calls).toBe(2)
    } finally {
      spy.mockRestore()
    }
  })

  it('tells the caller about each refusal it tries again', async () => {
    const seen: unknown[] = []
    let calls = 0
    await queuedRead(async () => { if (++calls < 3) throw busy(); return 'ok' }, undefined, { onRefused: e => seen.push(e) })
    expect(seen).toHaveLength(2)
  })
})
