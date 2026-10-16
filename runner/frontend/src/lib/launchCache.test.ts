import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'

// The signed-in account is read off the access token: each test says which.
let token: string | null = null
vi.mock('../authClient', async (orig) => ({
  ...(await orig<typeof import('../authClient')>()),
  getAccessToken: () => token,
  redirectToRefresh: vi.fn(),
  ensureFreshToken: () => Promise.resolve(false),
}))
// A renewal was already tried this page load: apiFetch hands the 401 back
// (the first time it would send the page away instead).
vi.mock('../refreshGuard', () => ({
  refreshAlreadyAttempted: () => true,
  markRefreshAttempted: vi.fn(),
  clearRefreshAttempted: vi.fn(),
}))

import {
  clearLaunchCache, readLaunchCache, fetchRepos, fetchDaemons, fetchCreds, prefetchLaunchData,
  isFresh, launchClock, launchModelsCache, FRESH_MS, fetchMcpConnections, launchMcpConnections, storeRepos,
} from './launchCache'

function jwt(sub: string, expInSeconds = 600): string {
  const payload = btoa(JSON.stringify({ sub, exp: Math.floor(Date.now() / 1000) + expInSeconds }))
  return `h.${payload}.s`
}

function okJSON(body: unknown) {
  return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) })
}

function apiMock() {
  return vi.fn((url: string) => {
    if (url === '/api/repos') return okJSON({ repos: [{ name: 'secret-repo', full_name: 'me/secret-repo', checked_out: [] }], stale_at: null })
    if (url === '/api/daemons') return okJSON([{ id: 'd1', name: 'desk' }])
    if (url === '/api/cluster/status') return okJSON({ configured: false })
    if (url === '/api/me/credentials') return okJSON({ engines: ['claude'], git: false })
    return okJSON({})
  })
}

// jsdom's sessionStorage is not dependable in this setup; an in-memory one.
function memoryStorage(): Storage {
  const m = new Map<string, string>()
  return {
    getItem: k => (m.has(k) ? m.get(k)! : null),
    setItem: (k, v) => void m.set(k, v),
    removeItem: k => void m.delete(k),
    clear: () => m.clear(),
    key: i => Array.from(m.keys())[i] ?? null,
    get length() { return m.size },
  }
}

const realNow = launchClock.now

describe('launchCache', () => {
  let storage: Storage

  beforeEach(() => {
    token = jwt('acct-a')
    storage = memoryStorage()
    vi.stubGlobal('sessionStorage', storage)
    clearLaunchCache()
  })
  afterEach(() => {
    launchClock.now = realNow
    vi.unstubAllGlobals()
  })

  it('keeps each part with the time it arrived, in this tab only (sessionStorage), never localStorage', async () => {
    const local = memoryStorage()
    vi.stubGlobal('localStorage', local)
    vi.stubGlobal('fetch', apiMock())
    await fetchRepos()
    const c = readLaunchCache()
    expect(c.repos?.value.repos[0].name).toBe('secret-repo')
    expect(isFresh(c.repos)).toBe(true)
    expect(c.daemons).toBeUndefined()
    expect(storage.getItem('blerg.launch.cache')).toContain('secret-repo')
    expect(local.length).toBe(0)
    launchClock.now = () => realNow() + FRESH_MS + 1
    expect(isFresh(readLaunchCache().repos)).toBe(false)
  })

  it("never shows one account's lists to another in the same tab", async () => {
    vi.stubGlobal('fetch', apiMock())
    await fetchRepos()
    token = jwt('acct-b')
    expect(readLaunchCache().repos).toBeUndefined()
    // Gone from storage too, not merely hidden.
    expect(storage.getItem('blerg.launch.cache')).toBeNull()
  })

  it('reads back what a reload left in sessionStorage for the same account', async () => {
    vi.stubGlobal('fetch', apiMock())
    await fetchDaemons()
    const saved = storage.getItem('blerg.launch.cache')
    clearLaunchCache() // a reload: module memory gone…
    storage.setItem('blerg.launch.cache', saved!) // …the tab's storage kept
    expect(readLaunchCache().daemons?.value[0].name).toBe('desk')
  })

  it('forgets everything on a 401 (signed out)', async () => {
    vi.stubGlobal('fetch', apiMock())
    await fetchRepos()
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: false, status: 401, json: () => Promise.resolve({}) })))
    await expect(fetchDaemons()).rejects.toThrow()
    expect(readLaunchCache().repos).toBeUndefined()
    expect(storage.getItem('blerg.launch.cache')).toBeNull()
  })

  it('treats failed credentials as unknown (null), never as a failure', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new Error('boom'))))
    await expect(fetchCreds()).resolves.toBeNull()
    expect(readLaunchCache().creds?.value).toBeNull()
  })

  it('prefetches the four parts once — no MCP connections, no tools — and shares a request in flight', async () => {
    const fetchMock = apiMock()
    vi.stubGlobal('fetch', fetchMock)
    prefetchLaunchData()
    prefetchLaunchData() // in flight: shared
    await vi.waitFor(() => expect(readLaunchCache().creds).toBeDefined())
    await vi.waitFor(() => expect(readLaunchCache().repos).toBeDefined())
    prefetchLaunchData() // fresh: nothing asked
    const urls = fetchMock.mock.calls.map(([u]) => u).sort()
    expect(urls).toEqual(['/api/cluster/status', '/api/daemons', '/api/me/credentials', '/api/repos'])
  })

  it('prefetches nothing without a live token, so a hover never sends the page away to sign in', () => {
    const fetchMock = apiMock()
    vi.stubGlobal('fetch', fetchMock)
    token = null
    prefetchLaunchData()
    token = jwt('acct-a', -60)
    prefetchLaunchData()
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('asks for the repository list conditionally and keeps its list on a 304', async () => {
    const LIST = { repos: [{ name: 'secret-repo', full_name: 'me/secret-repo', checked_out: [] }], stale_at: null }
    const sent: (string | null)[] = []
    vi.stubGlobal('fetch', vi.fn((_url: string, init?: RequestInit) => {
      const inm = new Headers(init?.headers).get('If-None-Match')
      sent.push(inm)
      if (inm === '"e1"') return Promise.resolve(new Response(null, { status: 304, headers: { ETag: '"e1"' } }))
      return Promise.resolve(new Response(JSON.stringify(LIST), { status: 200, headers: { ETag: '"e1"' } }))
    }))
    const first = await fetchRepos()
    expect(first.etag).toBe('"e1"')
    launchClock.now = () => realNow() + 60_000
    expect(isFresh(readLaunchCache().repos)).toBe(false)
    const second = await fetchRepos()
    expect(sent).toEqual([null, '"e1"'])
    expect(second.repos.map(r => r.name)).toEqual(['secret-repo'])
    // The 304 counts as a fresh answer.
    expect(isFresh(readLaunchCache().repos)).toBe(true)
  })

  it('keeps a list stored after a repos-folder change over an older answer landing late', async () => {
    let answer!: () => void
    vi.stubGlobal('fetch', vi.fn(() => new Promise(r => {
      answer = () => r({ ok: true, status: 200, json: () => Promise.resolve({ repos: [{ name: 'old', full_name: 'o/old', checked_out: [] }] }) })
    })))
    const slow = fetchRepos()
    storeRepos({ repos: [{ name: 'new', full_name: 'o/new', checked_out: [] }], stale: false })
    answer()
    await slow
    expect(readLaunchCache().repos?.value.repos[0].name).toBe('new')
  })

  it('keeps MCP connections without their URL or default tools (no hashes in storage)', async () => {
    vi.stubGlobal('fetch', vi.fn(() => okJSON({ connections: [{
      id: 'c1', name: 'calendar', url: 'https://mcp.example.test/x', auth_kind: 'static', status: 'ok',
      default_tools: { read: { mode: 'allow', hash: 'h-secret-hash' } },
    }] })))
    const live = await fetchMcpConnections()
    expect(live[0].default_tools).toHaveProperty('read') // the live answer is whole
    const stored = storage.getItem('blerg.launch.cache')!
    expect(stored).not.toContain('h-secret-hash')
    expect(stored).not.toContain('mcp.example.test')
    expect(launchMcpConnections.cached()).toEqual([{ id: 'c1', name: 'calendar', auth_kind: 'static', status: 'ok', url: '', default_tools: {} }])
  })

  it('keeps nothing about credentials after a 401', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: false, status: 401, json: () => Promise.resolve({}) })))
    await expect(fetchCreds()).resolves.toBeNull()
    expect(readLaunchCache().creds).toBeUndefined()
    expect(storage.getItem('blerg.launch.cache')).toBeNull()
  })

  it('keeps cluster catalogs separate from unscoped and workstation catalogs', () => {
    const list = { models: [], source: 'live' as const, engineEfforts: [] }
    launchModelsCache.put('codex', undefined, list, 'cluster')
    expect(launchModelsCache.get('codex', undefined, 'cluster')).toEqual(list)
    expect(launchModelsCache.get('codex')).toBeUndefined()
    expect(launchModelsCache.get('codex', 'd1')).toBeUndefined()
  })

  it('keeps model lists per engine and daemon', () => {
    const list = { models: [], source: 'daemon' as const, engineEfforts: [] }
    launchModelsCache.put('codex', 'd1', list)
    expect(launchModelsCache.get('codex', 'd1')).toEqual(list)
    expect(launchModelsCache.get('codex', 'd2')).toBeUndefined()
    expect(launchModelsCache.get('codex')).toBeUndefined()
  })
})
