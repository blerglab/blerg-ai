// Card #139, in real Chromium with a real mouse (CDP) and the hovercards' manual clock, at 1280 px
// (the panel) and 390 px (the drawer): a card chip that names no board (an imported mark, a
// `mark --card N` line) links to and previews the card on the session's one board; with several
// boards, or none, it stays the number, and its hovercard says the board is unknown. A pinned
// ticket of the board with no board of its own reads the card from the session's one board too.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import { cdp, page, userEvent } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import { HOVERCARD, clearHovercardCache, type TicketSummaryLive } from '@blerglab/chat'
import SessionRail from './SessionRail'
import { makeSession } from '../test/fixtures'
import { makeLive } from '../test/planTabFixture'
import { reelReply } from '../test/reelFixture'
import type { Stroke, TopicsReply } from '../lib/topics'
import { installHoverClock, type ManualHoverClock } from '../test/hoverClock'
import { resetSessionBoardsForTest } from '../lib/sessionBoard'
import { resetLiveBoardCardsForTest } from '../lib/liveBoardCard'
import { railReady } from '../test/railReady'
import '@blerglab/chat/blerg.css'
import '../index.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async (original) => ({
  ...(await original<typeof import('../authClient')>()), getAccessToken: () => 'fixture-token', redirectToRefresh: vi.fn(),
}))
vi.mock('../claims', async (original) => ({ ...(await original<typeof import('../claims')>()), tokenExpired: () => false }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))

const NOW = new Date()
function historyReply(): TopicsReply {
  const reply = reelReply(NOW)
  reply.strokes = [{
    id: 'st-imported', kind: 'decision', source: 'mark', by: 'person', text: 'History becomes a compact feed of strokes', seq: 300,
    at: new Date(NOW.getTime() - 60_000).toISOString(), jump: null, backdated: true, card: { number: 115 },
  } satisfies Stroke]
  return reply
}
vi.mock('../hooks/useTopics', () => ({ useTopics: () => ({ status: 'ready', reply: historyReply() }) }))

const json = (body: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }))
const board = (id: string, name: string) => ({ board_id: id, board_name: name, level: 'full', created_via: 'launch', created_at: NOW.toISOString(), launch: true })
function permissions(boards: ReturnType<typeof board>[]) {
  return { can_grant: true, reason: '', grants: [], mcp_tools: [], boards }
}
const liveCard = (boardId: string, number: number) => ({
  card: { id: 'c1', board_id: boardId, number, title: `Live title of #${number}`, column_id: 'col', column_name: 'Doing', columns: [{ id: 'col', name: 'Doing' }], priority: 'high' },
  url: `/boards/${boardId}/cards/${number}`,
})

function serve(boards: ReturnType<typeof board>[]) {
  apiFetch.mockImplementation((url: string) => {
    if (url.endsWith('/permissions')) return json(permissions(boards))
    const m = url.match(/^\/api\/board-cards\/([^/]+)\/(\d+)/)
    if (m) return json(liveCard(decodeURIComponent(m[1]), Number(m[2])))
    return json({}, 404)
  })
}

const boardPin: TicketSummaryLive = { source: 'blerg-board', key: '#121', title: 'History strokes: follow-ups', status: 'In progress', url: '', eventId: 'e-121', updatedAt: 1 }

function show(width: number) {
  const drawer = width < 1024
  render(<MemoryRouter><div style={{ display: 'flex', height: '100vh', width }}>
    <div style={{ flex: 1 }} />
    <SessionRail id="rail" session={makeSession({ status: 'running' })} toolChanges={0} mode={drawer ? 'drawer' : 'panel'} open onClose={() => {}} onOpenArtifact={() => {}}
      live={makeLive({ tickets: [boardPin] })} />
  </div></MemoryRouter>)
}

type Mouse = { send(method: string, params?: object): Promise<unknown> }
let mouse: Mouse
let clock: ManualHoverClock
const card = () => document.querySelector<HTMLElement>('[data-testid="hovercard"]')
async function moveTo(x: number, y: number) {
  const f = window.frameElement?.getBoundingClientRect()
  const k = f ? f.width / window.innerWidth : 1
  await mouse.send('Input.dispatchMouseEvent', { type: 'mouseMoved', x: (f?.left ?? 0) + x * k, y: (f?.top ?? 0) + y * k })
}
async function hover(el: Element) {
  await railReady()
  const r = el.getBoundingClientRect()
  const x = Math.round(r.left + r.width / 2), y = Math.round(r.top + r.height / 2)
  await moveTo(x, y - 1)
  await moveTo(x, y)
  await expect.poll(() => clock.pending()).toBeGreaterThan(0)
  clock.advance(HOVERCARD.openDelayMs)
  await expect.poll(() => card()?.style.visibility).toBe('visible')
}
async function history() {
  await userEvent.click(screen.getByRole('tab', { name: /History/ }))
  return within(screen.getByTestId('reel-list')).findByTestId('stroke-card')
}

beforeEach(async () => {
  clock = installHoverClock()
  localStorage.clear()
  clearHovercardCache()
  resetSessionBoardsForTest()
  resetLiveBoardCardsForTest()
  mouse = cdp() as unknown as Mouse
  await moveTo(1, 1)
})
afterEach(async () => { await moveTo(1, 1); clock.uninstall(); apiFetch.mockReset() })

describe('a card chip that names no board (#139)', () => {
  for (const width of [1280, 390]) {
    it(`at ${width} px: with one board, links to its card and previews it live`, async () => {
      await page.viewport(width, 900)
      serve([board('b1', 'Product')])
      show(width)
      let chip = await history()
      await expect.poll(() => (chip = screen.getByTestId('stroke-card')).tagName).toBe('BUTTON')
      expect(chip.getAttribute('href')).toBeNull()
      expect(chip).toHaveTextContent('#115')
      await hover(chip)
      await expect.poll(() => card()!.textContent).toContain('Live title of #115')
      expect(card()!.textContent).toContain('#115')
      expect(apiFetch.mock.calls.some(([u]) => String(u).startsWith('/api/board-cards/b1/115'))).toBe(true)
    })

    it(`at ${width} px: with two boards, stays the number and its hovercard says the board is unknown`, async () => {
      await page.viewport(width, 900)
      serve([board('b1', 'Product'), board('b2', 'Ops')])
      show(width)
      const chip = await history()
      // Settled: the permissions were read, and still no board.
      await expect.poll(() => apiFetch.mock.calls.some(([u]) => String(u).endsWith('/permissions'))).toBe(true)
      expect(chip.tagName).toBe('SPAN')
      expect(chip).toHaveAttribute('data-board', 'unknown')
      await hover(chip)
      expect(card()!.textContent).toContain('Card')
      expect(card()!.textContent).toContain('#115')
      expect(card()!.textContent).toContain('board unknown')
      expect(apiFetch.mock.calls.some(([u]) => String(u).startsWith('/api/board-cards/'))).toBe(false)
    })
  }

  it('at 1280 px: a pinned ticket of the board with no board of its own reads its card from the session\'s one board', async () => {
    await page.viewport(1280, 900)
    serve([board('b1', 'Product')])
    show(1280)
    await expect.poll(() => apiFetch.mock.calls.some(([u]) => String(u).endsWith('/permissions'))).toBe(true)
    const pin = await screen.findByTestId('rail-pin-open')
    await hover(pin)
    await expect.poll(() => apiFetch.mock.calls.some(([u]) => String(u).startsWith('/api/board-cards/b1/121'))).toBe(true)
    await expect.poll(() => card()!.textContent).toContain('Live title of #121')
  })
})

describe('History opens cards inside Blerg (#215)', () => {
  it.each(['click', 'Enter', 'Space'])('opens the ticket modal with %s without navigating', async activation => {
    await page.viewport(1280, 900)
    serve([board('b1', 'Product')])
    show(1280)
    await history()
    await expect.poll(() => screen.getByTestId('stroke-card').tagName).toBe('BUTTON')
    const chip = screen.getByTestId('stroke-card')
    expect(chip.getAttribute('href')).toBeNull()
    expect(chip.getAttribute('target')).toBeNull()
    expect(chip.getAttribute('aria-label')).toContain('Card')
    const location = window.location.href
    chip.focus()
    if (activation === 'click') await userEvent.click(chip)
    else await userEvent.keyboard(activation === 'Enter' ? '{Enter}' : ' ')
    const modal = await screen.findByTestId('ticket-modal')
    expect(modal.textContent).toContain('#115')
    await screen.findByTestId('board-card-live')
    expect(window.location.href).toBe(location)
  })
})
