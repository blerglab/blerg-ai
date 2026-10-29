// Card #159, in real Chromium with a real mouse (CDP) and the hovercards' manual clock, at 390 px and
// 3840 px: a pinned ticket in the chat is one compact system row at bubble width ("📌 Pinned #157 ◐
// title · time"), consecutive pins fold into one row, an unpin and a refresh say so on one line,
// another tracker's ticket shows its key, title and source, the #N is the card chip with its
// hovercard, and a click opens the same live dialog as the panel (#96: the card's live description,
// the two-column layout), not the pinned copy.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import { cdp, page } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { HOVERCARD, clearHovercardCache, resetRefStoresForTest, useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import '../index.css'
import { seedRichRail } from '../test/railFixture'
import { RAIL_PREF_KEY } from '../lib/sessionRail'
import { installHoverClock, type ManualHoverClock } from '../test/hoverClock'
import { resetSessionBoardsForTest } from '../lib/sessionBoard'
import { resetLiveBoardCardsForTest } from '../lib/liveBoardCard'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async (orig) => ({ ...(await orig<typeof import('../authClient')>()), getAccessToken: () => 'tok', redirectToRefresh: vi.fn() }))
vi.mock('../claims', async (orig) => ({ ...(await orig<typeof import('../claims')>()), tokenExpired: () => false }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))

import SessionDetail from './SessionDetail'

const shotsDir = (import.meta as unknown as { env: Record<string, string | undefined> }).env.VITE_PIN_SHOTS_DIR
const LIVE_DESCRIPTION = 'The live description, as the board has it now.'
const json = (body: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }))

function serve() {
  const { live } = seedRichRail()
  apiFetch.mockImplementation((url: string) => {
    if (url.endsWith('/live')) return json(live)
    if (url.endsWith('/permissions')) return json({ can_grant: true, reason: '', grants: [], mcp_tools: [], boards: [{ board_id: 'b1', board_name: 'Blerg', level: 'full' }] })
    const m = url.match(/^\/api\/board-cards\/b1\/(\d+)(?:\?|$)/)
    if (m) {
      const n = Number(m[1])
      return json({ card: { id: `c${n}`, board_id: 'b1', number: n, title: `Card ${n} as the board has it`, body: LIVE_DESCRIPTION, type: 'feature', priority: 'medium', column_id: 'k', column_name: 'In progress', columns: [{ id: 'k', name: 'In progress' }, { id: 'd', name: 'Done' }], trail: [] }, url: `/boards/b1/cards/${n}` })
    }
    if (url.startsWith('/api/tool-changes')) return json({ tool_changes: [] })
    return json({})
  })
  vi.stubGlobal('fetch', vi.fn(() => json([])))
}

const board = (n: number, title: string, status = 'in progress') => ({ source: 'blerg-board', key: `#${n}`, board_id: 'b1', title, status })

function seed() {
  const s = useTranscriptStore.getState()
  let seq = 0
  const ev = (kind: string, payload: unknown) => ({ session_id: 'sess-a', client_event_id: `pin${++seq}`, seq: 900 + seq, ts: new Date(Date.now() - 60_000).toISOString(), kind, payload } as AgentEvent)
  const say = (text: string) => s.ingest(ev('assistant_text', { text, done: true }))
  s.ingest(ev('ticket', { action: 'pin', ticket: board(160, 'Plan: card numbers in checklist items become chips with hovercards, everywhere they are drawn in the app') }))
  say('One pin.')
  s.ingest(ev('ticket', { action: 'pin', ticket: board(157, 'Card chips in checklist items') }))
  s.ingest(ev('ticket', { action: 'pin', ticket: board(158, 'Keep the plan honest') }))
  say('Two pins.')
  s.ingest(ev('ticket', { action: 'pin', ticket: { source: 'jira', key: 'ENG-12', title: 'Fix the login redirect', status: 'To Do', url: 'https://jira.example.test/browse/ENG-12' } }))
  say('A Jira ticket.')
  s.ingest(ev('ticket', { action: 'update', ticket: board(157, 'Card chips in checklist items', 'Done'), previous: { status: 'in progress', title: 'Card chips in checklist items' } }))
  say('A refresh.')
  s.ingest(ev('ticket', { action: 'unpin', ticket: board(127, 'Hovercards', 'Done') }))
  say('An unpin.')
}

type Mouse = { send(method: string, params?: object): Promise<unknown> }
let mouse: Mouse
let clock: ManualHoverClock
const hovercard = () => document.querySelector<HTMLElement>('[data-testid="hovercard"]')
async function moveTo(x: number, y: number) {
  const f = window.frameElement?.getBoundingClientRect()
  const k = f ? f.width / window.innerWidth : 1
  await mouse.send('Input.dispatchMouseEvent', { type: 'mouseMoved', x: (f?.left ?? 0) + x * k, y: (f?.top ?? 0) + y * k })
}
async function hover(el: Element) {
  const r = el.getBoundingClientRect()
  const x = Math.round(r.left + r.width / 2), y = Math.round(r.top + r.height / 2)
  await moveTo(x, y - 1)
  await moveTo(x, y)
  await opened()
}
async function opened() {
  await expect.poll(() => clock.pending()).toBeGreaterThan(0)
  clock.advance(HOVERCARD.openDelayMs)
  await expect.poll(hovercard).not.toBeNull()
}

beforeEach(async () => {
  localStorage.setItem(RAIL_PREF_KEY, 'false')
  resetRefStoresForTest()
  resetSessionBoardsForTest()
  resetLiveBoardCardsForTest()
  clearHovercardCache()
  clock = installHoverClock()
  mouse = cdp() as unknown as Mouse
  await moveTo(1, 1)
  serve()
})
afterEach(async () => {
  await moveTo(1, 1)
  clock.uninstall()
  vi.unstubAllGlobals()
  localStorage.removeItem(RAIL_PREF_KEY)
})

function renderPage() {
  render(<div style={{ height: '100vh' }}><MemoryRouter initialEntries={['/sessions/sess-a']}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></MemoryRouter></div>)
  seed()
}

async function rows(): Promise<HTMLElement[]> {
  return waitFor(() => {
    const found = [...document.querySelectorAll<HTMLElement>('.ticket-row')]
    if (found.length < 5) throw new Error(`only ${found.length} pin rows`)
    return found
  })
}

for (const [width, height] of [[390, 800], [3840, 2000]] as const) {
  describe(`pin rows at ${width} px`, () => {
    beforeEach(async () => { await page.viewport(width, height) })

    it('are one line each, no wider than a bubble, and say what happened', async () => {
      renderPage()
      const [single, folded, jira, update, unpin] = await rows()
      const timeline = document.querySelector('.agent-timeline')!.getBoundingClientRect()
      for (const row of [single, folded, jira, update, unpin]) {
        const box = row.getBoundingClientRect()
        const line = parseFloat(getComputedStyle(row).lineHeight) || 16
        expect(box.height, row.textContent!).toBeLessThan(line * 2) // one line
        expect(box.height, row.textContent!).toBeGreaterThanOrEqual(line) // and that line shows
        expect(box.width).toBeLessThanOrEqual(timeline.width * 0.92 + 1) // bubble width at most
        expect(row.querySelector('button.ticket-open, .ticket-actions')).toBeNull() // no "Open ticket"
      }
      // 📌 Pinned #160 ◐ <title, cut to one line> · <time>
      expect(single.textContent).toMatch(/^📌Pinned#160◐Plan: card numbers/)
      // The card number is never what gives way: the whole chip shows, the title is cut instead.
      const chip = within(single).getByTestId('ticket-chip')
      expect(chip.getBoundingClientRect().right).toBeLessThanOrEqual(single.getBoundingClientRect().right)
      expect(chip.scrollWidth).toBeLessThanOrEqual(chip.clientWidth + 1)
      expect(chip.getBoundingClientRect().width).toBeGreaterThan(18)
      const title = single.querySelector<HTMLElement>('.ticket-row-title')!
      if (width === 390) expect(title.scrollWidth).toBeGreaterThan(title.clientWidth) // cut to fit the line
      expect(single.querySelector('.ticket-row-time')!.textContent).toMatch(/min|now|ago/)
      // Consecutive pins: one row.
      expect(within(folded).getAllByTestId('ticket-chip').map(c => c.textContent)).toEqual(['#157', '#158'])
      expect(folded.textContent).toMatch(/^📌Pinned#157, #158/)
      // Another tracker: its key, its title and its source.
      expect(jira.textContent).toContain('ENG-12')
      expect(jira.textContent).toContain('○')
      expect(jira.textContent).toContain('Fix the login redirect')
      expect(jira.textContent).toContain('JIRA')
      // A refresh says what changed.
      expect(update.textContent).toMatch(/Updated#157✓/)
      expect(update.textContent).toContain('status in progress → Done')
      // An unpin.
      expect(unpin.textContent).toMatch(/^📌Unpinned#127\(done\)/)
      expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    })

    it('waits for a pinned chip hover delivered on a later frame before advancing time', async () => {
      renderPage()
      const [single] = await rows()
      single.scrollIntoView({ block: 'center' })
      const chip = within(single).getByTestId('ticket-chip')
      const r = chip.getBoundingClientRect()
      const moved = new Promise<void>((resolve, reject) => {
        requestAnimationFrame(() => { void moveTo(r.left + r.width / 2, r.top + r.height / 2).then(resolve, reject) })
      })
      await opened()
      await moved
      expect(hovercard()!.textContent).toContain('#160')
    })

    it('#N is the card chip: its hovercard, and a click opens the live ticket dialog (#96), not the pinned copy', async () => {
      renderPage()
      const [single] = await rows()
      single.scrollIntoView({ block: 'center' })
      const chip = within(single).getByTestId('ticket-chip')
      if (shotsDir) await page.screenshot({ path: `${shotsDir}/pins-${width}.png` })
      await hover(chip)
      await expect.poll(() => hovercard()!.textContent).toContain('#160')
      await moveTo(1, 1)
      chip.click()
      const live = await screen.findByTestId('board-card-live')
      await waitFor(() => expect(live.textContent).toContain(LIVE_DESCRIPTION))
      expect(screen.getByTestId('ticket-modal')).toBeTruthy()
      expect(screen.queryByText('No description')).toBeNull()
    })
  })
}
