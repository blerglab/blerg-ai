// BLG-444: the card-time glyph and its tooltip, in real Chromium, at 390 and 3840 px. Owner
// screenshot capture only (VITE_CARDTIME_SHOTS_DIR set): `VITE_CARDTIME_SHOTS_DIR=/tmp/cardtime-shots
// npx vitest run src/components/SessionRail.cardtime.browser.test.tsx`.
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { render, waitFor, within } from '@testing-library/react'
import { page } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import type { TicketSummaryLive } from '@blerglab/chat'
import SessionRail from './SessionRail'
import { makeSession } from '../test/fixtures'
import { makeLive } from '../test/planTabFixture'
import { resetLiveBoardCardsForTest } from '../lib/liveBoardCard'
import { resetCardTimeDataForTest } from '../lib/cardTimeData'
import { resetSessionBoardsForTest } from '../lib/sessionBoard'
import { setTheme } from '../../../../packages/settings/theme'
import '@blerglab/chat/blerg.css'
import '../index.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({
  ...(await original<typeof import('../authClient')>()), getAccessToken: () => 'fixture-token', redirectToRefresh: vi.fn(),
}))
vi.mock('../claims', async original => ({ ...(await original<typeof import('../claims')>()), tokenExpired: () => false }))

const cardTimeResponses = new Map<string, unknown>()
vi.mock('../apiFetch', () => ({
  apiFetch: vi.fn(async (path: string) => {
    const m = /\/api\/board-cards\/([^/]+)\/(\d+)\/card-time$/.exec(path)
    if (m) return { ok: true, json: async () => cardTimeResponses.get(`${m[1]}#${m[2]}`) ?? {} }
    if (/\/api\/board-cards\//.test(path)) return { ok: false, json: async () => ({}) }
    if (/\/api\/sessions\/[^/]+\/permissions$/.test(path)) {
      return { ok: true, json: async () => ({ can_grant: false, reason: '', grants: [], mcp_tools: [], boards: [{ board_id: 'board-a', level: 'full' }] }) }
    }
    return { ok: true, json: async () => ({}) }
  }),
}))

const boardPin = (key: string, title: string): TicketSummaryLive =>
  ({ source: 'blerg-board', key, boardId: 'board-a', title, status: 'In progress', url: '', eventId: `e-${key}` }) as TicketSummaryLive

beforeEach(() => {
  localStorage.clear()
  resetLiveBoardCardsForTest()
  resetCardTimeDataForTest()
  resetSessionBoardsForTest()
  cardTimeResponses.clear()
})
afterEach(() => { vi.restoreAllMocks() })

const shots = import.meta.env.VITE_CARDTIME_SHOTS_DIR
async function capture(name: string, width: number) {
  if (!shots) return
  await page.screenshot({ path: `${shots}/${name}-${width}.png` })
}

it.each([390, 3840])('shows the over-budget glyph on the pinned card and the checklist item, at %i px', async width => {
  await page.viewport(width, width === 390 ? 844 : 2160)
  setTheme('dark')
  cardTimeResponses.set('board-a#12', { seconds: 24120, rounds: 3, verdict: 'changes', open_high: 1, open_medium: 2 })
  const checklist = { eventId: 'c1', updatedAt: null, items: [{ id: 'i1', text: 'Ship the budget nudge · #12', done: false, in_progress: true, assignees: [] }] }
  render(<MemoryRouter><div style={{ display: 'flex', height: width === 390 ? 844 : 2160, width }}>
    <SessionRail id="rail" session={makeSession({ status: 'running' })} toolChanges={0} mode={width < 700 ? 'drawer' : 'panel'} open onClose={() => {}} onOpenArtifact={() => {}}
      live={makeLive({ tickets: [boardPin('#12', 'Card-time budget nudge')], checklist })} />
  </div></MemoryRouter>)

  // Drawer mode (< 700 px) may render into a portal outside the render() container, so these
  // queries go through document.body rather than the container RTL returned.
  await waitFor(() => expect(document.body.querySelectorAll('[data-testid="rail-pin-cardtime"]').length).toBeGreaterThan(0), { timeout: 5000 })
  const live = within(document.getElementById('rail-panel-live') as HTMLElement)
  const pinGlyph = live.getByTestId('rail-pin-cardtime')
  expect(pinGlyph).toHaveAttribute('title', '6h42m worker time · 3 review rounds · changes requested · 1 high, 2 medium open')
  await waitFor(() => expect(document.body.querySelectorAll('[data-testid="rail-plan-cardtime"]').length).toBeGreaterThan(0), { timeout: 5000 })
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  await capture('pin-row', width)

  // Open the Plan tab so the checklist item's glyph is on screen too for the same capture set
  // (the Live and Plan panels are both mounted; only the active one is visible for a screenshot).
  const planTab = document.getElementById('rail-tab-plan')
  if (planTab) {
    planTab.click()
    await waitFor(() => expect(document.querySelector('#rail-panel-plan [data-testid="rail-plan-cardtime"]')).toBeVisible())
    await capture('checklist-item', width)
  }
})
