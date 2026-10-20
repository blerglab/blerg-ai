// An explicit review request in real Chromium, on a phone and a laptop: Live → Needs you lists
// "Review: SECURITY.md" with the ask under it, the row opens the file in the viewer, and the viewer's
// bar (the ask, Approve, Request changes) fits the screen and decides it.
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import '../index.css'
import { seedRichRail } from '../test/railFixture'
import { RAIL_PREF_KEY, RAIL_TAB_KEY } from '../lib/sessionRail'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async (orig) => ({
  ...(await orig<typeof import('../authClient')>()),
  getAccessToken: () => 'tok',
  redirectToRefresh: vi.fn(),
}))
vi.mock('../claims', async (orig) => ({ ...(await orig<typeof import('../claims')>()), tokenExpired: () => false }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))

import SessionDetail from './SessionDetail'

const ASK = 'This changes how people report vulnerabilities and how long we take to answer them. OK to publish this policy as written, or should the disclosure window change first?'
const FILE = { id: 'sec2', name: 'SECURITY.md', size: 64, content_type: 'text/markdown', view: 'markdown', origin: 'agent', version: 2, latest_version: 2 }
const REQUEST = {
  request_id: 'r1', artifact_id: 'sec2', artifact_name: 'SECURITY.md', artifact_version: 2, artifact_view: 'markdown',
  ask: ASK, state: 'pending', created_at: new Date().toISOString(),
}

const json = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }))
let decided: unknown = null

beforeEach(() => {
  localStorage.removeItem(RAIL_PREF_KEY)
  localStorage.setItem(RAIL_TAB_KEY, 'live')
  decided = null
  const { live } = seedRichRail()
  const withReview = { ...live, review_requests: [{ ...REQUEST }], counts: { ...live.counts, needs_you: live.counts.needs_you + 1 } }
  apiFetch.mockImplementation((url: string) => json(url.endsWith('/live') ? withReview : url.startsWith('/api/tool-changes') ? { tool_changes: [] } : {}))
  vi.stubGlobal('fetch', vi.fn((url: string, init?: RequestInit) => {
    const u = String(url)
    if (u.endsWith('/artifacts')) return json({ artifacts: [FILE] })
    if (u.endsWith('/review-requests')) return json({ review_requests: [decided ?? REQUEST], can_decide: true })
    if (u.endsWith('/r1/decide') && init?.method === 'POST') {
      decided = { ...REQUEST, state: 'approved', decided_at: new Date().toISOString() }
      return json(decided)
    }
    if (u.includes('/artifacts/sec2/raw')) return Promise.resolve(new Response('# Security\n\nReport issues privately.\n', { status: 200 }))
    return json([])
  }))
})
afterEach(() => {
  vi.unstubAllGlobals()
  localStorage.removeItem(RAIL_PREF_KEY)
  localStorage.removeItem(RAIL_TAB_KEY)
})

function renderPage() {
  render(
    <div style={{ height: '100vh' }}>
      <MemoryRouter initialEntries={['/sessions/sess-a']}>
        <Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes>
      </MemoryRouter>
    </div>,
  )
}

it.each([390, 1280])('Needs you lists the review, and the viewer bar approves it at %i px', async width => {
  await page.viewport(width, 800)
  renderPage()
  if (width < 900) await userEvent.click(screen.getByTestId('rail-toggle'))
  const rail = await screen.findByTestId('session-rail')
  const row = await within(rail).findByTestId('rail-review-request')
  expect(row.textContent).toContain('Review: SECURITY.md')
  expect(row.textContent).toContain('v2')
  expect(row.textContent).toContain('OK to publish this policy')
  await waitFor(() => expect(rail.getBoundingClientRect().width).toBeGreaterThan(0))
  const rr = row.getBoundingClientRect()
  const box = rail.getBoundingClientRect()
  expect(rr.left).toBeGreaterThanOrEqual(box.left - 0.5)
  expect(rr.right).toBeLessThanOrEqual(box.right + 0.5)
  expect(row.scrollWidth).toBeLessThanOrEqual(row.clientWidth + 1)
  // The Needs you count includes it.
  expect(within(screen.getByTestId('rail-needs')).getByText('3')).toBeTruthy()

  await userEvent.click(within(row).getByRole('button'))
  const bar = await screen.findByTestId('review-request-bar')
  expect(within(bar).getByTestId('review-request-bar-ask').textContent).toBe(ASK)
  const approve = await within(bar).findByRole('button', { name: 'Approve' })
  const change = within(bar).getByRole('button', { name: 'Request changes' })
  for (const b of [approve, change]) {
    const r = b.getBoundingClientRect()
    expect(r.height).toBeGreaterThanOrEqual(20)
    expect(r.left).toBeGreaterThanOrEqual(0)
    expect(r.right).toBeLessThanOrEqual(width)
  }
  expect(bar.scrollWidth).toBeLessThanOrEqual(bar.clientWidth + 1)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)

  await userEvent.click(approve)
  await waitFor(() => expect(screen.getByTestId('review-request-bar').textContent).toContain('Approved'))
  expect(within(screen.getByTestId('review-request-bar')).queryByRole('button', { name: 'Approve' })).toBeNull()
})
