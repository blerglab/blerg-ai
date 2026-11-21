// An explicit review request in real Chromium, on a phone and a laptop: Live → Needs you lists
// "Review: SECURITY.md" as the same compact row as a question, opening the viewer. Its
// bar (the ask, Approve, Request changes) fits the screen and decides it.
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import '../index.css'
import { seedRichRail } from '../test/railFixture'
import { useMessageStore } from '../hooks/useMessageStore'
import { setTheme } from '../../../../packages/settings/theme'
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
  useMessageStore.setState({ messages: [{ id: 'm-ask', session_id: 'sess-a', kind: 'ask', status: 'open', answerable: true, body: live.asks[0].question, created_at: REQUEST.created_at }] })
  const withReview = { ...live, tool_changes: 0, review_requests: [{ ...REQUEST }], counts: { ...live.counts, needs_you: 2 } }
  apiFetch.mockImplementation((url: string) => json(
    url.endsWith('/live') ? { ...withReview, review_requests: decided ? [] : [REQUEST] }
      : url.endsWith('/permissions') ? { can_grant: true, reason: '', grants: [], mcp_tools: [], boards: [] }
        : url.startsWith('/api/models') ? { models: [{ id: 'review-model', name: 'Review model', section: 'main', efforts: ['high'], default_effort: 'high' }], source: 'live' }
          : url.startsWith('/api/tool-changes') ? { tool_changes: [] } : {}))
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

it.each([390, 3840])('Needs you lists the review, and the viewer bar approves it at %i px', async width => {
  await page.viewport(width, 800)
  renderPage()
  if (width < 900) await userEvent.click(screen.getByTestId('rail-toggle'))
  const rail = await screen.findByTestId('session-rail')
  const row = await within(rail).findByTestId('rail-review-request')
  expect(row.textContent).toContain('Review: SECURITY.md')
  expect(row.textContent).not.toContain(ASK)
  expect(within(row).getByRole('button', { name: /Review: SECURITY/ })).toHaveClass('workspace-row')
  expect(within(row).getByRole('button', { name: /Dismiss review request/ })).toHaveTextContent('⊘')
  await waitFor(() => expect(rail.getBoundingClientRect().width).toBeGreaterThan(0))
  const rr = row.getBoundingClientRect()
  const box = rail.getBoundingClientRect()
  expect(rr.left).toBeGreaterThanOrEqual(box.left - 0.5)
  expect(rr.right).toBeLessThanOrEqual(box.right + 0.5)
  expect(row.scrollWidth).toBeLessThanOrEqual(row.clientWidth + 1)
  // The Needs you count includes it.
  expect(within(screen.getByTestId('rail-needs')).getByText('2')).toBeTruthy()

  await userEvent.click(within(row).getByRole('button', { name: /Review: SECURITY\.md/ }))
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

function expectAlignedRows(review: HTMLElement, question: HTMLElement) {
  // The text starts at the same edge whether a row shows a one-line title or a full question.
  for (const selector of ['.question-card-glyph', '.question-card-title, .question-full-text']) {
    const reviewBox = review.querySelector(selector)!.getBoundingClientRect()
    const questionBox = question.querySelector(selector)!.getBoundingClientRect()
    expect(reviewBox.left, `${selector} left edge`).toBeCloseTo(questionBox.left, 1)
    if (selector === '.question-card-glyph') expect(reviewBox.width, 'leading glyph slot width').toBeCloseTo(questionBox.width, 1)
  }
}

// The counted review is reachable from both real shell entry points, including the phone sheet.
for (const width of [390, 3840]) it(`Questions and Needs you share flat rows and remove decided reviews at ${width}px`, async () => {
  await page.viewport(width, width === 390 ? 844 : 2160)
  setTheme(width === 3840 ? 'colorblind-dark' : 'dark')
  renderPage()
  const shots = import.meta.env.VITE_QUESTION_SHOT_DIR
  const shot = async (surface: string) => {
    if (!shots) return
    const frame = document.createElement('div')
    frame.style.cssText = 'position:fixed;inset:0;width:100vw;height:100vh;pointer-events:none'
    document.body.append(frame)
    try { await page.screenshot({ element: frame, path: `${shots}/review-${surface}-${width}.png` }) } finally { frame.remove() }
  }
  if (width === 390) await userEvent.click(screen.getByTestId('rail-toggle'))
  const needs = await screen.findByTestId('rail-needs')
  const review = await within(needs).findByRole('button', { name: /Review: SECURITY/ })
  const question = within(needs).getByRole('button', { name: /The staging migration/ })
  for (const row of [review, question]) {
    expect(row).toHaveClass('workspace-row')
    // BLG-497: a question the person must answer shows in full; other rows stay one compact line.
    const full = row.querySelector('.question-full-text')
    if (full) {
      expect(full).toBeVisible()
      expect(getComputedStyle(full).whiteSpace).toBe('pre-wrap')
      continue
    }
    expect(row.querySelector('.question-card-title')).toBeVisible()
    expect(getComputedStyle(row.querySelector('.question-card-title')!).whiteSpace).toBe('nowrap')
    expect(row.closest('.rail-item')!.getBoundingClientRect().height).toBeLessThan(45)
  }
  expectAlignedRows(review, question)
  await shot('needs-you')
  if (width === 390) await userEvent.keyboard('{Escape}')
  await userEvent.click(screen.getByRole('button', { name: 'Toggle explorer' }))
  await userEvent.click(await screen.findByRole('tab', { name: 'Questions' }))
  const tab = screen.getByRole('tab', { name: 'Questions' })
  await waitFor(() => expect(tab).toHaveAttribute('aria-description', '2 open questions'))
  const queue = screen.getByRole('region', { name: 'Question queue' })
  expect(within(queue).getByRole('button', { name: /The staging migration/ })).toBeVisible()
  const queuedReview = within(queue).getByRole('button', { name: /Review: SECURITY/ })
  expect(queuedReview).toBeVisible()
  expect(queue.querySelectorAll('.attention-row')).toHaveLength(2)
  expect(queue.scrollWidth).toBeLessThanOrEqual(queue.clientWidth + 1)
  expectAlignedRows(queuedReview, within(queue).getByRole('button', { name: /The staging migration/ }))
  await shot('questions')
  if (width === 3840) {
    await page.getByRole('region', { name: 'Question queue' }).getByRole('button', { name: 'Review: SECURITY.md', exact: true }).hover()
    const preview = await screen.findByRole('tooltip')
    expect(preview).toHaveTextContent(ASK)
    expect(preview).toHaveTextContent('v2')
  }
  await userEvent.click(queuedReview)
  const bar = await screen.findByTestId('review-request-bar')
  expect(within(bar).getByRole('button', { name: 'Request changes' })).toBeVisible()
  await shot('file')
  await userEvent.click(within(bar).getByRole('button', { name: 'Approve' }))
  await waitFor(() => expect(screen.getByTestId('review-request-bar')).toHaveTextContent('Approved'))
  if (width === 390) await userEvent.click(screen.getByRole('button', { name: 'Toggle explorer' }))
  await waitFor(() => expect(screen.getByRole('tab', { name: 'Questions' })).toHaveAttribute('aria-description', '1 open questions'))
  expect(within(screen.getByRole('region', { name: 'Question queue' })).queryByRole('button', { name: /Review: SECURITY/ })).toBeNull()
})
