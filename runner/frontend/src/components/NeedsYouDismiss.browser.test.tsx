// Card #155 in real Chromium, on a phone and at 4K: Dismiss in Needs you closes a review request
// (the person may have answered in the chat), with a toast that offers Undo for 5 s; Undo puts it back
// and sends nothing; otherwise the dismissal is sent once the 5 s are up.
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import '../index.css'
import { seedRichRail } from '../test/railFixture'
import { RAIL_PREF_KEY, RAIL_TAB_KEY } from '../lib/sessionRail'
import { DISMISS_UNDO_MS, useDismissStore } from '../lib/needsYouDismiss'
import { useToastStore } from '../hooks/useToastStore'

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
import ToastContainer from './ToastContainer'

const REQUEST = {
  request_id: 'r1', artifact_id: 'sec2', artifact_name: 'SECURITY.md', artifact_version: 2, artifact_view: 'markdown',
  ask: 'OK to publish this policy?', state: 'pending', created_at: new Date().toISOString(),
}
const json = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }))

beforeEach(() => {
  useDismissStore.setState({ hidden: new Set() })
  useToastStore.setState({ toasts: [] })
  localStorage.removeItem(RAIL_PREF_KEY)
  localStorage.setItem(RAIL_TAB_KEY, 'live')
  const { live } = seedRichRail()
  const withReview = { ...live, review_requests: [{ ...REQUEST }] }
  apiFetch.mockReset()
  apiFetch.mockImplementation((url: string) => json(url.endsWith('/live') ? withReview : url.startsWith('/api/tool-changes') ? { tool_changes: [] } : {}))
  vi.stubGlobal('fetch', vi.fn(() => json([])))
})
afterEach(() => {
  vi.unstubAllGlobals()
  localStorage.removeItem(RAIL_PREF_KEY)
  localStorage.removeItem(RAIL_TAB_KEY)
})

const dismissCalls = () => apiFetch.mock.calls.filter(([url, init]) => String(url).endsWith('/r1/decide') && (init as RequestInit | undefined)?.method === 'POST')

it.each([[390, 844], [3840, 2160]])('Dismiss closes a review request, with Undo for 5 s, at %ipx', async (width, height) => {
  await page.viewport(width, height)
  render(
    <div style={{ height: '100vh' }}>
      <MemoryRouter initialEntries={['/sessions/sess-a']}>
        <Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes>
        <ToastContainer />
      </MemoryRouter>
    </div>,
  )
  if (width < 900) await userEvent.click(screen.getByTestId('rail-toggle'))
  const rail = await screen.findByTestId('session-rail')
  const row = await within(rail).findByTestId('rail-review-request')
  const dismiss = within(row).getByRole('button', { name: 'Dismiss review request: SECURITY.md' })
  const box = dismiss.getBoundingClientRect()
  expect(box.height).toBeGreaterThanOrEqual(24)
  expect(box.right).toBeLessThanOrEqual(width)

  // Dismiss: gone at once, a toast offers Undo; Undo puts it back and nothing is sent.
  await userEvent.click(dismiss)
  await waitFor(() => expect(within(rail).queryByTestId('rail-review-request')).toBeNull())
  const undo = await screen.findByTestId('toast-action')
  expect(undo.textContent).toBe('Undo')
  const toastBox = undo.closest('[role="status"]')!.getBoundingClientRect()
  expect(toastBox.right).toBeLessThanOrEqual(width)
  expect(toastBox.left).toBeGreaterThanOrEqual(0)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  await userEvent.click(undo)
  expect(await within(rail).findByTestId('rail-review-request')).toBeTruthy()
  await new Promise(done => setTimeout(done, DISMISS_UNDO_MS + 300))
  expect(dismissCalls()).toHaveLength(0)

  // Dismiss again and let the 5 s pass: the dismissal is sent, as a decision "dismiss".
  await userEvent.click(within(await within(rail).findByTestId('rail-review-request')).getByRole('button', { name: 'Dismiss review request: SECURITY.md' }))
  expect(dismissCalls()).toHaveLength(0)
  await waitFor(() => expect(dismissCalls()).toHaveLength(1), { timeout: DISMISS_UNDO_MS + 2000 })
  expect(JSON.parse(String((dismissCalls()[0][1] as RequestInit).body))).toEqual({ decision: 'dismiss' })
  expect(within(rail).queryByTestId('rail-review-request')).toBeNull()
}, 30_000)
