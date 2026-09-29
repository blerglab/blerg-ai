// Owner report: "sometimes I click the new modal, it pops up for a fraction of
// a second and then goes away, I press the new button again and the modal
// shows and stays this time".
//
// These run in real Chromium so the click is a real input event (Playwright),
// not fireEvent.click, and layout (the fixed overlay over the button) is real.
//
// Root cause (see useLaunchSheetOpen.ts): opening the sheet is very often the
// first HTTP call after the tab idled past the 10-minute access-token life (the
// sessions page is otherwise fed by a WebSocket that stays authenticated), so
// the sheet's own fetch gets a 401, apiFetch sends the whole page through
// core's /auth/refresh, and the reloaded page came back with the sheet closed.
// The second press works because the token is fresh by then.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
import { userEvent } from 'vitest/browser'
import tlUserEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async (orig) => ({
  ...(await orig<typeof import('../authClient')>()),
  getAccessToken: () => 'tok',
  // Never let a test navigate the test page itself.
  redirectToRefresh: vi.fn(),
}))

import SessionList from './SessionList'
import LaunchSheet from './LaunchSheet'
import { useLaunchSheetOpen } from '../hooks/useLaunchSheetOpen'
import { redirectToRefresh } from '../authClient'

// Mirrors App.tsx's Layout wiring of the "+ New" button to the sheet.
function Harness() {
  const [launchOpen, setLaunchOpen] = useLaunchSheetOpen()
  return (
    <MemoryRouter initialEntries={['/sessions']}>
      <SessionList variant="sidebar" onNewSession={() => setLaunchOpen(true)} />
      <LaunchSheet key={launchOpen ? 'open' : 'closed'} open={launchOpen} onClose={() => setLaunchOpen(false)} />
    </MemoryRouter>
  )
}

const json = (body: unknown, status = 200) =>
  Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }))

function okFetch(url: string) {
  if (url === '/api/repos') return json({ repos: [{ name: 'myrepo', full_name: 'org/myrepo', checked_out: ['ws'] }] })
  if (url === '/api/daemons') return json([{ id: 'd1', name: 'ws', mode: 'local', repos_root: '/r', status: 'connected', sandbox_available: true, available_engines: ['claude'] }])
  if (url === '/api/cluster/status') return json({ configured: false })
  if (url === '/api/me/credentials') return json({ unavailable: true })
  return json({})
}

const overlay = () => document.querySelector('.launch-overlay') as HTMLElement | null
const isOpen = () => {
  const o = overlay()
  return !!o && getComputedStyle(o).display !== 'none'
}
const frames = (n: number) => new Promise<void>(res => {
  const step = (left: number) => (left === 0 ? res() : requestAnimationFrame(() => step(left - 1)))
  step(n)
})

beforeEach(() => {
  sessionStorage.clear()
  vi.mocked(redirectToRefresh).mockClear()
})
afterEach(() => {
  vi.unstubAllGlobals()
  sessionStorage.clear()
})

describe('+ New → LaunchSheet', () => {
  it('a real click opens the sheet and it is still open frames later', async () => {
    vi.stubGlobal('fetch', vi.fn((url: string) => okFetch(url)))
    render(<Harness />)
    await userEvent.click(screen.getByRole('button', { name: '+ New' }))
    await frames(10)
    await new Promise(r => setTimeout(r, 300))
    expect(isOpen()).toBe(true)
    await waitFor(() => expect(screen.getByText('myrepo')).toBeTruthy())
    expect(isOpen()).toBe(true)
  })

  it('an expired token on open reloads the page through /auth/refresh — and the sheet is open again after it', async () => {
    // Every gated call answers 401, as they do once the access token expired.
    vi.stubGlobal('fetch', vi.fn(() => json({ error: 'unauthorized' }, 401)))
    render(<Harness />)
    await userEvent.click(screen.getByRole('button', { name: '+ New' }))
    await waitFor(() => expect(redirectToRefresh).toHaveBeenCalled())

    // The browser now leaves the page and comes back with a fresh token: a
    // brand-new document, nothing in memory survives. Model that as an
    // unmount + fresh mount (same tab, so sessionStorage is still there).
    cleanup()
    sessionStorage.removeItem('blerg_refresh_attempted') // main.tsx clears it once a token lands
    vi.stubGlobal('fetch', vi.fn((url: string) => okFetch(url)))
    render(<Harness />)

    await frames(2)
    expect(isOpen()).toBe(true)
    await waitFor(() => expect(screen.getByText('myrepo')).toBeTruthy())
  })

  it('closing the sheet is remembered too — a later reload does not reopen it', async () => {
    vi.stubGlobal('fetch', vi.fn((url: string) => okFetch(url)))
    render(<Harness />)
    await userEvent.click(screen.getByRole('button', { name: '+ New' }))
    await waitFor(() => expect(screen.getByText('myrepo')).toBeTruthy())
    await userEvent.click(screen.getByRole('button', { name: '✕' }))
    expect(isOpen()).toBe(false)
    cleanup()
    render(<Harness />)
    await frames(2)
    expect(isOpen()).toBe(false)
  })
})

describe('backdrop close', () => {
  async function openSheet() {
    vi.stubGlobal('fetch', vi.fn((url: string) => okFetch(url)))
    render(<Harness />)
    await userEvent.click(screen.getByRole('button', { name: '+ New' }))
    await waitFor(() => expect(screen.getByText('myrepo')).toBeTruthy())
  }

  it('a press and release on the backdrop closes the sheet', async () => {
    await openSheet()
    const user = tlUserEvent.setup()
    await user.pointer([{ keys: '[MouseLeft>]', target: overlay()! }, { keys: '[/MouseLeft]', target: overlay()! }])
    expect(isOpen()).toBe(false)
  })

  it('a press that starts inside the panel and is released on the backdrop does not close it', async () => {
    await openSheet()
    const user = tlUserEvent.setup()
    const input = screen.getByPlaceholderText(/Search repos/)
    // e.g. drag-selecting text in a field and letting go past the panel edge:
    // the browser dispatches that click to the common ancestor — the overlay.
    await user.pointer([
      { keys: '[MouseLeft>]', target: input },
      { target: overlay()! },
      { keys: '[/MouseLeft]', target: overlay()! },
    ])
    expect(isOpen()).toBe(true)
  })
})
