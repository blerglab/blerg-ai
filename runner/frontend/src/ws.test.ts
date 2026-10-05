import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'

// vi.mock factories are hoisted above top-level const declarations, so the
// spies themselves must be created inside vi.hoisted to be visible in the
// factory below.
const { redirectToRefresh, ensureFreshToken, getAccessToken, consumeAccessTokenFromFragment } = vi.hoisted(() => ({
  redirectToRefresh: vi.fn(),
  ensureFreshToken: vi.fn<() => Promise<boolean>>(),
  getAccessToken: vi.fn<() => string | null>(),
  consumeAccessTokenFromFragment: vi.fn(),
}))

vi.mock('./authClient', () => ({
  redirectToRefresh,
  ensureFreshToken,
  getAccessToken,
  consumeAccessTokenFromFragment,
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
    FakeWebSocket.instances = []
    vi.stubGlobal('WebSocket', FakeWebSocket as unknown as typeof WebSocket)
    sessionStorage.clear()
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
    expect(redirectToRefresh).toHaveBeenCalledWith(location.href)
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
})
