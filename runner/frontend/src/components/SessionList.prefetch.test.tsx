// Hovering (or focusing) "+ New" warms what the New session sheet opens on, so
// the sheet opens at once — and only once per moment, however often the
// pointer passes over it.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

vi.mock('../ws', () => import('../test/wsMock'))
const liveToken = `h.${btoa(JSON.stringify({ sub: 'acct-1', exp: Math.floor(Date.now() / 1000) + 600 }))}.s`
vi.mock('../authClient', async (orig) => ({
  ...(await orig<typeof import('../authClient')>()),
  getAccessToken: () => liveToken,
  redirectToRefresh: vi.fn(),
}))

import SessionList from './SessionList'
import { useSessionStore } from '../hooks/useSessionStore'
import { clearLaunchCache, readLaunchCache } from '../lib/launchCache'

const SHEET_URLS = ['/api/repos', '/api/daemons', '/api/cluster/status', '/api/me/credentials']

describe('+ New prefetch', () => {
  beforeEach(() => {
    clearLaunchCache()
    useSessionStore.setState({ daemons: [], sessions: [], statusChangedAt: {}, serverVersion: '' })
  })
  afterEach(() => vi.unstubAllGlobals())

  it('fires the sheet requests once on hover, not again on a second hover or focus', async () => {
    const fetchMock = vi.fn(() => Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) }))
    vi.stubGlobal('fetch', fetchMock)
    render(
      <MemoryRouter>
        <SessionList variant="sidebar" onNewSession={vi.fn()} />
      </MemoryRouter>,
    )
    // The sidebar's own cluster pill asks for the status on mount; count from after it.
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalled())
    const before = fetchMock.mock.calls.length
    const button = screen.getByRole('button', { name: '+ New' })
    fireEvent.pointerEnter(button)
    await vi.waitFor(() => expect(readLaunchCache().creds).toBeDefined())
    await vi.waitFor(() => expect(readLaunchCache().repos).toBeDefined())
    fireEvent.pointerEnter(button)
    fireEvent.focus(button)
    const asked = (fetchMock.mock.calls.slice(before) as unknown as [string][]).map(([u]) => u)
    for (const url of SHEET_URLS) expect(asked.filter(u => u === url)).toHaveLength(1)
    expect(asked.some(u => u.startsWith('/api/mcp'))).toBe(false)
  })
})
