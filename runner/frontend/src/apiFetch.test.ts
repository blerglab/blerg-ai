import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'

// vi.mock factories are hoisted above top-level const declarations, so the
// spy itself must be created inside vi.hoisted to be visible in the factory.
const { redirectToRefresh } = vi.hoisted(() => ({ redirectToRefresh: vi.fn() }))

// Real getAccessToken/consumeAccessTokenFromFragment (so the bearer-header
// assertion exercises the actual in-memory token), but a spy in place of
// redirectToRefresh — a real one would try to navigate jsdom's location.
vi.mock('./authClient', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./authClient')>()
  return { ...actual, redirectToRefresh }
})

import { consumeAccessTokenFromFragment } from './authClient'
import { apiFetch } from './apiFetch'
import { clearRefreshAttempted } from './refreshGuard'

describe('apiFetch', () => {
  beforeEach(() => {
    redirectToRefresh.mockClear()
    window.history.replaceState(null, '', '/')
    window.history.replaceState(null, '', '/#access_token=tok-abc')
    consumeAccessTokenFromFragment()
    clearRefreshAttempted()
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('attaches the bearer token to every request', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ status: 200, ok: true })
    vi.stubGlobal('fetch', fetchMock)

    await apiFetch('/api/sessions')

    expect(fetchMock).toHaveBeenCalledWith('/api/sessions', {
      headers: { Authorization: 'Bearer tok-abc' },
    })
  })

  it('merges the bearer header with caller-supplied headers/init', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ status: 200, ok: true })
    vi.stubGlobal('fetch', fetchMock)

    await apiFetch('/api/sessions', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: '{}',
    })

    expect(fetchMock).toHaveBeenCalledWith('/api/sessions', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: 'Bearer tok-abc' },
      body: '{}',
    })
  })

  it('redirects to core refresh on a 401 instead of resolving with it', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ status: 401, ok: false })
    vi.stubGlobal('fetch', fetchMock)

    let settled = false
    apiFetch('/api/sessions').then(() => { settled = true })

    // let the fetch promise's .then chain run
    await Promise.resolve()
    await Promise.resolve()

    expect(redirectToRefresh).toHaveBeenCalledWith(location.href)
    // the 401 response is never handed back to the caller
    expect(settled).toBe(false)
  })

  it('redirects only once across two separate 401s in the same page load', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ status: 401, ok: false })
    vi.stubGlobal('fetch', fetchMock)

    // First 401: guard not yet tripped -> redirects, response never resolves.
    let firstSettled = false
    apiFetch('/api/sessions').then(() => { firstSettled = true })
    await Promise.resolve()
    await Promise.resolve()
    expect(redirectToRefresh).toHaveBeenCalledTimes(1)
    expect(firstSettled).toBe(false)

    // Second 401 (e.g. a second in-flight call landing after the guard was
    // already tripped): must NOT navigate again — instead resolves with the
    // 401 so the caller/UI can show an error rather than hang forever.
    const resp = await apiFetch('/api/sessions')
    expect(redirectToRefresh).toHaveBeenCalledTimes(1)
    expect(resp.status).toBe(401)
  })

  it('does not redirect on a non-401 response', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ status: 200, ok: true })
    vi.stubGlobal('fetch', fetchMock)

    const resp = await apiFetch('/api/sessions')

    expect(redirectToRefresh).not.toHaveBeenCalled()
    expect(resp.status).toBe(200)
  })
})
