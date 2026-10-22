// Card #134 in real Chromium, at 390 and 3840 px: Live's Tickets list only the cards in progress;
// Plan's Tickets list every pin as the same compact line with one state glyph (✓ ◐ ○, one weight,
// no status chips, no live orb), "N of M done" in the header and the checklist right under it; the
// hovercard says a card was read live from the board. Hovercard timing runs on the test's clock.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import { HOVERCARD, clearHovercardCache, type TicketSummaryLive } from '@blerglab/chat'
import SessionRail, { PIN_READ_WAIT_MS } from './SessionRail'
import { makeSession } from '../test/fixtures'
import { makeLive } from '../test/planTabFixture'
import { installHoverClock, type ManualHoverClock } from '../test/hoverClock'
import { resetLiveBoardCardsForTest } from '../lib/liveBoardCard'
import '@blerglab/chat/blerg.css'
import '../index.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async (original) => ({
  ...(await original<typeof import('../authClient')>()), getAccessToken: () => 'fixture-token', redirectToRefresh: vi.fn(),
}))
vi.mock('../claims', async (original) => ({ ...(await original<typeof import('../claims')>()), tokenExpired: () => false }))
const columns = [{ id: 'c-in', name: 'Inbox' }, { id: 'c-doing', name: 'In progress' }, { id: 'c-done', name: 'Done', is_terminal: true }]
// The board: #12 is in progress, #13 done, #14 in the inbox, #15 archived. A test can hold the
// reads back (held) and let them land when it says.
const board = vi.hoisted(() => ({ held: null as null | Array<() => void> }))
vi.mock('../apiFetch', () => ({
  apiFetch: vi.fn(async (url: string) => {
    const m = /\/api\/board-cards\/b1\/(\d+)/.exec(url)
    if (!m) return { ok: false, status: 404, json: async () => ({}) }
    const n = Number(m[1])
    if (board.held) await new Promise<void>(res => board.held!.push(res))
    const column = n === 12 ? 'c-doing' : n === 13 ? 'c-done' : 'c-in'
    return { ok: true, status: 200, json: async () => ({ url: '', card: { id: `c${n}`, board_id: 'b1', number: n, title: `Board card ${n}`, column_id: column, column_name: columns.find(c => c.id === column)!.name, columns, ...(n === 15 ? { archived_at: '2026-10-20T10:00:00Z' } : {}) } }) }
  }),
}))

const pin = (key: string, title: string, at: number, status: string, board = false): TicketSummaryLive =>
  ({ source: board ? 'blerg-board' : 'tracker', key, title, status, url: '', eventId: `e-${key}`, updatedAt: at, ...(board ? { boardId: 'b1' } : {}) })
const tickets = [
  pin('#12', 'Agent copy 12', 50, 'To do', true),
  pin('#13', 'Agent copy 13', 40, 'In progress', true),
  pin('#14', 'Agent copy 14', 30, 'In progress', true),
  pin('WEB-7', 'A tracker ticket in review', 20, 'In Review'),
  pin('WEB-8', 'A closed tracker ticket', 10, 'Closed'),
]
const checklist = { eventId: 'c1', updatedAt: null, items: [{ id: 'a', text: 'First step', done: false }, { id: 'b', text: 'Second step', done: true }] }

function show(width: number) {
  render(<MemoryRouter><div style={{ display: 'flex', height: '100vh', width }}>
    <div style={{ flex: 1 }} />
    <SessionRail id="rail" session={makeSession({ status: 'running' })} toolChanges={0} mode={width < 700 ? 'drawer' : 'panel'} open onClose={() => {}} onOpenArtifact={() => {}}
      live={makeLive({ tickets, checklist })} />
  </div></MemoryRouter>)
}

let clock: ManualHoverClock
beforeEach(() => {
  board.held = null
  clock = installHoverClock()
  localStorage.clear()
  clearHovercardCache()
  resetLiveBoardCardsForTest()
})
afterEach(() => clock.uninstall())

const state = (row: HTMLElement) => row.querySelector<HTMLElement>('[data-testid="rail-pin-state"]')!

describe('Tickets on Live and Plan, in Chromium', () => {
  it.each([390, 3840])('at %i px: the header counts an archived pin as done', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 844)
    render(<MemoryRouter><div style={{ display: 'flex', height: '100vh', width }}><div style={{ flex: 1 }} />
      <SessionRail id="rail" session={makeSession({ status: 'running' })} toolChanges={0} mode={width < 700 ? 'drawer' : 'panel'} open onClose={() => {}} onOpenArtifact={() => {}}
        live={makeLive({ tickets: [pin('#15', 'Agent copy 15', 60, 'In progress', true), pin('#12', 'Agent copy 12', 50, 'To do', true)], checklist })} />
    </div></MemoryRouter>)
    await userEvent.click(screen.getByRole('tab', { name: /Plan/ }))
    const section = screen.getByTestId('rail-ticket')
    await expect.poll(() => within(section).getByTestId('rail-ticket-done').textContent).toBe('1 of 2 done')
    const archived = within(section).getAllByTestId('rail-ticket-row')[0]
    expect(state(archived).textContent).toBe('✓')
    expect(within(archived).getByTestId('rail-ticket-open').getAttribute('aria-label')).toContain('Done (Archived)')
  })

  it.each([390, 3840])('at %i px: Live waits for a board card\'s read, so a card already done never flashes; Plan shows the pinned state muted meanwhile', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 844)
    board.held = []
    render(<MemoryRouter><div style={{ display: 'flex', height: '100vh', width }}><div style={{ flex: 1 }} />
      <SessionRail id="rail" session={makeSession({ status: 'running' })} toolChanges={0} mode={width < 700 ? 'drawer' : 'panel'} open onClose={() => {}} onOpenArtifact={() => {}}
        live={makeLive({ tickets: [pin('#13', 'Pinned as in progress, done on the board', 50, 'In progress', true), pin('WEB-7', 'A tracker ticket in review', 20, 'In Review')], checklist })} />
    </div></MemoryRouter>)
    // The tracker pin is there at once; the board card waits for its read.
    const live = screen.getByTestId('rail-pins')
    expect(within(live).getAllByTestId('rail-pin').map(r => r.dataset.ticketKey)).toEqual(['WEB-7'])
    await userEvent.click(screen.getByRole('tab', { name: /Plan/ }))
    const row = within(screen.getByTestId('rail-ticket')).getAllByTestId('rail-ticket-row')[0]
    expect(state(row).textContent).toBe('◐')
    expect(state(row).dataset.pending).toBe('true')
    const muted = getComputedStyle(state(row)).opacity
    // The read lands: done. It was never on Live.
    await expect.poll(() => board.held!.length).toBeGreaterThan(0)
    for (const go of board.held!.splice(0)) go()
    await expect.poll(() => state(row).textContent).toBe('✓')
    expect(state(row).dataset.pending).toBeUndefined()
    expect(getComputedStyle(state(row)).opacity).not.toBe(muted)
    await userEvent.click(screen.getByRole('tab', { name: /Live/ }))
    expect(within(screen.getByTestId('rail-pins')).getAllByTestId('rail-pin').map(r => r.dataset.ticketKey)).toEqual(['WEB-7'])
  })

  it('a board read that does not land: after the wait, Live trusts the pinned state', async () => {
    await page.viewport(1280, 900)
    board.held = []
    render(<MemoryRouter><div style={{ display: 'flex', height: '100vh', width: 1280 }}><div style={{ flex: 1 }} />
      <SessionRail id="rail" session={makeSession({ status: 'running' })} toolChanges={0} mode="panel" open onClose={() => {}} onOpenArtifact={() => {}}
        live={makeLive({ tickets: [pin('#12', 'Slow board', 50, 'In progress', true)], checklist })} />
    </div></MemoryRouter>)
    clock.advance(PIN_READ_WAIT_MS - 1)
    await new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))
    expect(screen.queryByTestId('rail-pin')).toBeNull()
    clock.advance(1)
    await expect.poll(() => screen.queryByTestId('rail-pin')?.dataset.ticketKey).toBe('#12')
  })

  it.each([390, 3840])('at %i px: Live lists only the cards in progress, from the board\'s column', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 844)
    show(width)
    const live = screen.getByTestId('rail-pins')
    // #12 is in progress on the board (the agent pinned it as To do); #13 is done, #14 in the inbox.
    await expect.poll(() => within(live).getAllByTestId('rail-pin').map(r => r.dataset.ticketKey)).toEqual(['#12', 'WEB-7'])
    for (const r of within(live).getAllByTestId('rail-pin')) {
      expect(state(r).textContent).toBe('◐')
      expect(r.getBoundingClientRect().height).toBeLessThan(28)
    }
    expect(live.querySelector('[data-testid="ticket-status"], .board-live-mark')).toBeNull()
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  })

  it.each([390, 3840])('at %i px: Plan lists every pin in one line with one glyph of one weight; "N of M done"; the checklist right under it', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 844)
    show(width)
    await userEvent.click(screen.getByRole('tab', { name: /Plan/ }))
    const section = screen.getByTestId('rail-ticket')
    await expect.poll(() => within(section).getAllByTestId('rail-ticket-row').map(r => `${r.dataset.ticketKey}${state(r).textContent}`))
      .toEqual(['#12◐', '#13✓', '#14○', 'WEB-7◐', 'WEB-8✓'])
    expect(within(section).getByTestId('rail-ticket-done').textContent).toBe('2 of 5 done')
    const rows = within(section).getAllByTestId('rail-ticket-row')
    const glyphs = rows.map(state)
    const box = glyphs.map(g => g.getBoundingClientRect())
    const style = glyphs.map(g => getComputedStyle(g))
    for (let i = 1; i < glyphs.length; i++) {
      expect(Math.abs(box[i].width - box[0].width)).toBeLessThan(0.5)
      expect(style[i].fontSize).toBe(style[0].fontSize)
      expect(style[i].fontWeight).toBe(style[0].fontWeight)
    }
    // Each state its color: done green, in progress the selected blue, not started muted.
    expect(style[1].color).not.toBe(style[0].color)
    expect(style[2].color).not.toBe(style[0].color)
    expect(style[2].color).not.toBe(style[1].color)
    for (const r of rows) expect(r.getBoundingClientRect().height).toBeLessThan(28)
    expect(section.querySelector('[data-testid="ticket-status"], .board-live-mark, .ticket-source')).toBeNull()
    // The words are in the line's name and the glyph's tooltip.
    expect(within(rows[2]).getByTestId('rail-ticket-open').getAttribute('aria-label')).toBe('#14 Board card 14, Not started (Inbox)')
    expect(state(rows[1]).getAttribute('title')).toBe('Done')
    // The checklist right under the tickets.
    const checklistTop = screen.getByTestId('rail-checklist').getBoundingClientRect().top
    expect(checklistTop - section.getBoundingClientRect().bottom).toBeLessThan(2)
    expect(section.getBoundingClientRect().height).toBeLessThan(5 * 28 + 48)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  })

  it('at 3840 px: the hovercard says the card was read live from the board', async () => {
    await page.viewport(3840, 2160)
    show(3840)
    await userEvent.click(screen.getByRole('tab', { name: /Plan/ }))
    const row = await screen.findAllByTestId('rail-ticket-row').then(rs => rs[0])
    await expect.poll(() => row.dataset.live).toBe('true')
    await userEvent.hover(within(row).getByTestId('rail-ticket-open'))
    clock.advance(HOVERCARD.openDelayMs)
    await expect.poll(() => document.querySelector('[data-testid="hovercard-live"]')?.textContent).toBe('Live from the board')
    expect(document.querySelector('[data-testid="hovercard-status"]')?.textContent).toBe('In progress')
  })
})
