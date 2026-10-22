// History's folds and compact topic summary in real Chromium, on a topic shaped like the owner's
// (27 card proposals, 3 subagents, 3 messages): at 390 px (the drawer) and 3840 px (the 340 px panel) the fold is one line whose card
// chips wrap inside the panel, it opens onto its 27 lines, the opened topic is one line per kind, a
// waiting item is pinned in view at the top, and nothing is wider than the screen.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import '../index.css'
import { seedRichRail } from '../test/railFixture'
import { ownerReply } from '../test/reelFixture'
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

const json = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }))

function setup(waiting: boolean) {
  localStorage.removeItem(RAIL_PREF_KEY)
  localStorage.setItem(RAIL_TAB_KEY, 'history')
  const { live } = seedRichRail()
  const reply = ownerReply(new Date(), { waiting })
  // The seeded conversation holds seq 1 on: every jump is reachable.
  for (const s of reply.strokes!) if (s.jump) s.jump = { ...s.jump, seq: 1 }
  apiFetch.mockImplementation((url: string) => json(url.endsWith('/topics') ? reply : url.endsWith('/live') ? live : url.startsWith('/api/tool-changes') ? { tool_changes: [] } : {}))
  vi.stubGlobal('fetch', vi.fn(() => json([])))
  render(
    <div style={{ height: '100vh' }}>
      <MemoryRouter initialEntries={['/sessions/sess-a']}>
        <Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes>
      </MemoryRouter>
    </div>,
  )
}

afterEach(() => {
  vi.unstubAllGlobals()
  localStorage.removeItem(RAIL_PREF_KEY)
  localStorage.removeItem(RAIL_TAB_KEY)
})

async function openPanel(width: number): Promise<HTMLElement> {
  if (width < 1024) {
    await userEvent.click(screen.getByTestId('rail-toggle'))
    const drawer = await screen.findByRole('dialog', { name: 'Session panel' })
    await waitFor(() => expect(drawer.getBoundingClientRect().right).toBe(width))
    return drawer
  }
  const panel = await screen.findByTestId('session-rail')
  // The panel is a fixed 340 px on every screen: the fold has to fit it.
  expect([340, 341]).toContain(Math.round(panel.getBoundingClientRect().width)) // 340 px and its 1 px border
  await userEvent.click(within(panel).getByRole('tab', { name: /History/ }))
  return panel
}

function inside(panel: HTMLElement, els: Iterable<Element>) {
  const p = panel.getBoundingClientRect()
  for (const el of els) {
    const r = el.getBoundingClientRect()
    if (r.width === 0) continue
    expect(r.left).toBeGreaterThanOrEqual(p.left - 0.5)
    expect(r.right).toBeLessThanOrEqual(p.right + 0.5)
  }
}

for (const [width, height] of [[390, 760], [3840, 2000]] as const) {
  describe(`the owner's topic at ${width} px`, () => {
    beforeEach(async () => { await page.viewport(width, height) })

    it('folds the 27 proposals into one line whose chips wrap inside the panel, and opens it onto its lines', async () => {
      setup(false)
      const panel = await openPanel(width)
      const feed = await within(panel).findByTestId('reel-list')
      const strokes = within(feed).getAllByTestId('stroke')
      expect(strokes).toHaveLength(2)
      const fold = strokes.find(s => s.textContent!.includes('Filed 26 cards'))!
      const text = within(fold).getByTestId('stroke-text')
      // The fold's words are one line; its 26 chips wrap below it, inside the panel.
      expect(text.getBoundingClientRect().height).toBeLessThanOrEqual(parseFloat(getComputedStyle(text).lineHeight) + 1)
      const chips = within(fold).getAllByTestId('stroke-card')
      expect(chips).toHaveLength(26)
      inside(panel, chips)
      expect(chips[0].getBoundingClientRect().top).toBeGreaterThan(text.getBoundingClientRect().top)
      await userEvent.click(text)
      const items = within(fold).getAllByTestId('stroke-item')
      expect(items).toHaveLength(27)
      inside(panel, items)
      // The opened topic: one line per kind, every chip inside the panel.
      await userEvent.click(within(panel).getByTestId('reel-head'))
      const summary = within(panel).getByTestId('reel-summary')
      expect(Array.from(summary.children).map(c => c.getAttribute('data-testid'))).toEqual(['reel-sum-messages', 'reel-sum-agents', 'reel-sum-files', 'reel-sum-commits'])
      inside(panel, summary.querySelectorAll('.reel-chip, .stroke-time'))
      expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    })

    it('pins the waiting proposal at the top, in view, inside the panel', async () => {
      setup(true)
      const panel = await openPanel(width)
      const pinned = await within(panel).findByTestId('stroke-pinned')
      const pin = within(pinned).getByTestId('stroke-pin')
      expect(pin).toHaveTextContent('waiting')
      // The waiting proposal itself, in its own words: not the fold, none of its chips.
      expect(within(pin).getByTestId('stroke-text')).toHaveTextContent(/^File a board card$/)
      expect(within(pinned).queryAllByTestId('stroke-card')).toHaveLength(0)
      expect(within(pinned).getAllByTestId('stroke-pin')).toHaveLength(1)
      const box = panel.getBoundingClientRect()
      const r = pinned.getBoundingClientRect()
      expect(r.top).toBeGreaterThanOrEqual(box.top)
      expect(r.bottom).toBeLessThanOrEqual(Math.min(box.bottom, height))
      inside(panel, [pinned, pin])
      expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    })
  })
}
