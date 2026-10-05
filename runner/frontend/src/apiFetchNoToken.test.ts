import { describe, it, expect, vi, afterEach } from 'vitest'

// A separate file from apiFetch.test.ts on purpose: authClient's access token
// is module-level state with no clear(), so "there is no token yet" can't be
// reconstructed once a sibling test has set one.
const { redirectToRefresh } = vi.hoisted(() => ({ redirectToRefresh: vi.fn() }))

vi.mock('./authClient', () => ({
  getAccessToken: () => null,
  redirectToRefresh,
  ensureFreshToken: vi.fn().mockResolvedValue(false),
  consumeAccessTokenFromFragment: () => {},
  coreOrigin: () => 'http://core.test',
}))

import { apiFetch } from './apiFetch'

describe('apiFetch with no access token', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('omits the Authorization header entirely rather than sending "Bearer null"', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ status: 200, ok: true })
    vi.stubGlobal('fetch', fetchMock)

    await apiFetch('/api/sessions')

    const [, init] = fetchMock.mock.calls[0]
    const headers = (init?.headers ?? {}) as Record<string, string>
    expect(headers.Authorization).toBeUndefined()
    expect(JSON.stringify(headers)).not.toContain('Bearer null')
  })

  it('preserves caller-supplied headers when there is no token to add', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ status: 200, ok: true })
    vi.stubGlobal('fetch', fetchMock)

    await apiFetch('/api/sessions', { headers: { 'Content-Type': 'application/json' } })

    const [, init] = fetchMock.mock.calls[0]
    const headers = (init?.headers ?? {}) as Record<string, string>
    expect(headers['Content-Type']).toBe('application/json')
  })
})
