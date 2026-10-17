import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { refreshReturnUrl } from './refreshGuard'

// vi.mock factories are hoisted above top-level const declarations, so the
// spies themselves must be created inside vi.hoisted to be visible in the
// factory below.
const { redirectToRefresh, ensureFreshToken, getAccessToken, consumeAccessTokenFromFragment, renewSilently, startTokenRenewal } = vi.hoisted(() => ({
  redirectToRefresh: vi.fn(),
  ensureFreshToken: vi.fn<() => Promise<boolean>>(),
  getAccessToken: vi.fn<() => string | null>(),
  consumeAccessTokenFromFragment: vi.fn(),
  renewSilently: vi.fn<() => Promise<'token' | 'signed-out' | 'no-answer'>>(),
  startTokenRenewal: vi.fn(),
}))

vi.mock('./authClient', () => ({
  redirectToRefresh,
  ensureFreshToken,
  getAccessToken,
  consumeAccessTokenFromFragment,
  renewSilently,
  startTokenRenewal,
}))

// A minimal fake WebSocket: ws.ts only touches onopen/onclose/onmessage/onerror,
// readyState, and the constructor args (url, protocols). Nothing here ever
// actually opens — tests drive state by invoking the handlers directly.
class FakeWebSocket {
  static instances: FakeWebSocket[] = []
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSING = 2
  static readonly CLOSED = 3
  readyState = FakeWebSocket.CONNECTING
  onopen: (() => void) | null = null
  onclose: ((ev: { code: number }) => void) | null = null
  onmessage: ((ev: MessageEvent) => void) | null = null
  onerror: (() => void) | null = null
  url: string
  protocols?: string[]
  constructor(url: string, protocols?: string[]) {
    this.url = url
    this.protocols = protocols
    FakeWebSocket.instances.push(this)
  }
  close(): void {}
  send(): void {}
}

// A base64url-encoded JWT-shaped string carrying only the `exp` claim claims.ts
// cares about. Header/signature content is irrelevant — never verified here.
function fakeToken(expEpochSeconds: number): string {
  const b64url = (obj: unknown) =>
    btoa(JSON.stringify(obj)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
  return `${b64url({ alg: 'none' })}.${b64url({ exp: expEpochSeconds })}.sig`
}

const UNEXPIRED_TOKEN = () => fakeToken(Math.floor(Date.now() / 1000) + 3600)
const EXPIRED_TOKEN = () => fakeToken(Math.floor(Date.now() / 1000) - 3600)

describe('ws.ts', () => {
  beforeEach(() => {
    vi.resetModules()
    redirectToRefresh.mockClear()
    ensureFreshToken.mockReset()
    ensureFreshToken.mockResolvedValue(false)
    getAccessToken.mockReset()
    consumeAccessTokenFromFragment.mockReset()
    renewSilently.mockReset()
    renewSilently.mockResolvedValue('no-answer')
    FakeWebSocket.instances = []
    vi.stubGlobal('WebSocket', FakeWebSocket as unknown as typeof WebSocket)
    sessionStorage.clear()
    window.history.replaceState(null, '', '/')
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('a never-opened 1006 close with an unexpired token schedules a reconnect, not a navigation', async () => {
    getAccessToken.mockReturnValue(UNEXPIRED_TOKEN())
    vi.useFakeTimers()

    await import('./ws')
    expect(FakeWebSocket.instances).toHaveLength(1)
    const socket = FakeWebSocket.instances[0]

    const timersBefore = vi.getTimerCount()
    socket.onclose?.({ code: 1006 })

    expect(redirectToRefresh).not.toHaveBeenCalled()
    // scheduleReconnect() queued exactly one new setTimeout.
    expect(vi.getTimerCount()).toBe(timersBefore + 1)

    vi.useRealTimers()
  })

  it('a never-opened 1006 close with an expired token redirects exactly once, no reconnect timer', async () => {
    getAccessToken.mockReturnValue(EXPIRED_TOKEN())
    vi.useFakeTimers()

    await import('./ws')
    const socket = FakeWebSocket.instances[0]

    const timersBefore = vi.getTimerCount()
    socket.onclose?.({ code: 1006 })
    await vi.advanceTimersByTimeAsync(0) // the quiet renewal is tried first and (here) comes back empty

    expect(redirectToRefresh).toHaveBeenCalledTimes(1)
    expect(redirectToRefresh).toHaveBeenCalledWith(refreshReturnUrl())
    // no reconnect scheduled — we're navigating away instead
    expect(vi.getTimerCount()).toBe(timersBefore)

    vi.useRealTimers()
  })

  it('an expired token is renewed quietly and the socket reconnected, with no page redirect', async () => {
    getAccessToken.mockReturnValue(EXPIRED_TOKEN())
    ensureFreshToken.mockResolvedValue(true)
    vi.useFakeTimers()

    await import('./ws')
    const before = FakeWebSocket.instances.length
    FakeWebSocket.instances[before - 1].onclose?.({ code: 1006 })
    await vi.advanceTimersByTimeAsync(0)

    expect(redirectToRefresh).not.toHaveBeenCalled()
    expect(FakeWebSocket.instances.length).toBe(before + 1) // reconnected at once with the renewed token

    vi.useRealTimers()
  })

  it('onopen clears the refresh-attempted guard so a later dead close can redirect again', async () => {
    getAccessToken.mockReturnValue(EXPIRED_TOKEN())
    vi.useFakeTimers()

    await import('./ws')
    const { refreshAlreadyAttempted } = await import('./refreshGuard')

    const socket = FakeWebSocket.instances[0]
    socket.onclose?.({ code: 1006 })
    await vi.advanceTimersByTimeAsync(0)
    expect(redirectToRefresh).toHaveBeenCalledTimes(1)
    expect(refreshAlreadyAttempted()).toBe(true)

    socket.onopen?.()
    expect(refreshAlreadyAttempted()).toBe(false)

    vi.useRealTimers()
  })

  // The live bug: a bounce that failed (a runner restart) left the flag in this tab's
  // sessionStorage, and every later load — reloads included — silently skipped the socket.
  it('a stale attempted flag from an earlier load is ignored: a fresh load with no token bounces', async () => {
    sessionStorage.setItem('blerg_refresh_attempted', '1')
    getAccessToken.mockReturnValue(null)

    await import('./ws')

    expect(redirectToRefresh).toHaveBeenCalledTimes(1)
    expect(redirectToRefresh).toHaveBeenCalledWith(refreshReturnUrl())
  })

  it('the return leg from /auth/refresh without a token does not bounce again; it raises the banner', async () => {
    sessionStorage.setItem('blerg_refresh_attempted', '1')
    window.history.replaceState(null, '', '/sessions#blerg_refresh_return')
    getAccessToken.mockReturnValue(null)

    await import('./ws')
    const { signInStuck } = await import('./signIn')

    expect(redirectToRefresh).not.toHaveBeenCalled()
    expect(FakeWebSocket.instances).toHaveLength(0)
    expect(signInStuck()).toBe(true)
    expect(location.hash).toBe('') // a reload from here is a fresh load
  })

  it('the rolling counter still caps fresh-load bounces at MAX_ATTEMPTS', async () => {
    getAccessToken.mockReturnValue(null)
    const { MAX_ATTEMPTS } = await import('./refreshGuard')
    for (let i = 0; i < MAX_ATTEMPTS; i++) {
      vi.resetModules()
      await import('./ws')
    }
    expect(redirectToRefresh).toHaveBeenCalledTimes(MAX_ATTEMPTS)

    vi.resetModules()
    await import('./ws')
    const { signInStuck } = await import('./signIn')
    expect(redirectToRefresh).toHaveBeenCalledTimes(MAX_ATTEMPTS)
    expect(signInStuck()).toBe(true)
  })

  it('the banner from a spent bounce heals itself: a quiet retry gets a token and the socket connects', async () => {
    vi.useFakeTimers()
    sessionStorage.setItem('blerg_refresh_attempted', '1')
    window.history.replaceState(null, '', '/sessions#blerg_refresh_return')
    getAccessToken.mockReturnValue(null)

    await import('./ws')
    const { signInStuck } = await import('./signIn')
    expect(signInStuck()).toBe(true)
    expect(FakeWebSocket.instances).toHaveLength(0)

    // core still unreachable at the first timed attempt
    await vi.advanceTimersByTimeAsync(5_000)
    expect(signInStuck()).toBe(true)
    expect(FakeWebSocket.instances).toHaveLength(0)

    // core answers at the next timed attempt (no online event, no visibility change: only the
    // retry itself can bring the socket up)
    const tok = UNEXPIRED_TOKEN()
    renewSilently.mockImplementation(async () => {
      getAccessToken.mockReturnValue(tok)
      return 'token'
    })
    await vi.advanceTimersByTimeAsync(15_000)
    expect(renewSilently).toHaveBeenCalledTimes(2)
    expect(signInStuck()).toBe(false)
    const ws = FakeWebSocket.instances[FakeWebSocket.instances.length - 1]
    expect(ws.protocols).toEqual(['bearer', tok])
    expect(redirectToRefresh).not.toHaveBeenCalled()
    vi.useRealTimers()
  })
})
