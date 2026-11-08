// BLG-444: the card-time glyph on the pinned-card row and the checklist item row, fetched from
// GET /api/board-cards/{board}/{number}/card-time and shown only once a card has crossed its
// first budget multiple (a card under budget renders no glyph at all, not a zero).
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import type { TicketSummaryLive } from '@blerglab/chat'
import SessionRail, { type SessionRailProps } from './SessionRail'
import { makeSession } from '../test/fixtures'
import { makeLive } from '../test/planTabFixture'
import { resetLiveBoardCardsForTest } from '../lib/liveBoardCard'
import { resetCardTimeDataForTest } from '../lib/cardTimeData'
import { resetSessionBoardsForTest } from '../lib/sessionBoard'

const cardTimeResponses = new Map<string, unknown>()

vi.mock('../apiFetch', () => ({
  apiFetch: vi.fn(async (path: string) => {
    const m = /\/api\/board-cards\/([^/]+)\/(\d+)\/card-time$/.exec(path)
    if (m) {
      const body = cardTimeResponses.get(`${m[1]}#${m[2]}`) ?? {}
      return { ok: true, json: async () => body }
    }
    if (/\/api\/board-cards\//.test(path)) return { ok: false, json: async () => ({}) }
    if (/\/api\/sessions\/[^/]+\/permissions$/.test(path)) {
      return {
        ok: true,
        json: async () => ({ can_grant: false, reason: '', grants: [], mcp_tools: [], boards: [{ board_id: 'board-a', level: 'full' }] }),
      }
    }
    return { ok: true, json: async () => ({}) }
  }),
}))

const boardPin = (key: string, number: number, title: string): TicketSummaryLive =>
  ({ source: 'blerg-board', key, boardId: 'board-a', title, status: 'In progress', url: '', eventId: `e-${number}` }) as TicketSummaryLive

function rail(live: ReturnType<typeof makeLive>, over: Partial<SessionRailProps> = {}) {
  const props: SessionRailProps = {
    id: 'rail', session: makeSession({ status: 'running' }), live, toolChanges: 0, mode: 'panel', open: true, onClose: () => {},
    onOpenArtifact: () => {}, ...over,
  }
  return render(<MemoryRouter><SessionRail {...props} /></MemoryRouter>)
}

beforeEach(() => {
  localStorage.clear()
  resetLiveBoardCardsForTest()
  resetCardTimeDataForTest()
  resetSessionBoardsForTest()
  cardTimeResponses.clear()
})

describe('the card-time glyph', () => {
  it('shows nothing on a pinned card under budget', async () => {
    cardTimeResponses.set('board-a#12', {})
    rail(makeLive({ tickets: [boardPin('#12', 12, 'Under budget')] }))
    await waitFor(() => expect(screen.getByTestId('rail-pin')).toBeInTheDocument())
    expect(screen.queryByTestId('rail-pin-cardtime')).not.toBeInTheDocument()
  })

  it('shows the total, rounds, verdict and open findings once a card is over budget', async () => {
    cardTimeResponses.set('board-a#12', { seconds: 24120, rounds: 2, verdict: 'changes', open_high: 1, open_medium: 2 })
    const { container } = rail(makeLive({ tickets: [boardPin('#12', 12, 'Over budget')] }))
    // The same pin renders once in Live, once in Plan (both panels are mounted, one hidden).
    await waitFor(() => expect(container.querySelectorAll('[data-testid="rail-pin-cardtime"]').length).toBeGreaterThan(0))
    const live = within(container.querySelector('#rail-panel-live') as HTMLElement)
    const glyph = live.getByTestId('rail-pin-cardtime')
    expect(glyph).toHaveTextContent('⏱')
    expect(glyph).toHaveAttribute('title', '6h42m worker time · 2 review rounds · changes requested · 1 high, 2 medium open')
    // The existing pin-state glyph must still be there too — a sibling, not a replacement.
    expect(within(live.getByTestId('rail-pin')).getByTestId('rail-pin-state')).toBeInTheDocument()
  })

  it('shows on a checklist item naming the same card, read from the session\'s one board', async () => {
    cardTimeResponses.set('board-a#12', { seconds: 14400, rounds: 1 })
    const checklist = { eventId: 'c1', updatedAt: null, items: [{ id: 'i1', text: 'Fix #12', done: false, assignees: [] }] }
    rail(makeLive({ tickets: [boardPin('#12', 12, 'Over budget')], checklist }))
    const glyph = await screen.findByTestId('rail-plan-cardtime')
    expect(glyph).toHaveAttribute('title', '4h worker time · 1 review round')
  })
})
