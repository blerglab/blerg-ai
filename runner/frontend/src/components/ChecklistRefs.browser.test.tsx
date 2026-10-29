// Card #157, in real Chromium with a real mouse (CDP) and the hovercards' manual clock, at 390 px
// (the drawer) and 3840 px (the panel): card numbers in checklist items. "#155 Dismiss in Needs you"
// (card #155 on the session's one board, done) links in place on Plan and on Live's line, with the
// card's hovercard, opens the ticket dialog, and shows ✓ beside the unticked item; "#1 thing" (no
// card #1) stays text; an item whose card agrees with it shows no glyph; each card is read once.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import { cdp, page, userEvent } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import { HOVERCARD, clearHovercardCache, resetRefStoresForTest } from '@blerglab/chat'
import SessionRail from './SessionRail'
import { makeSession } from '../test/fixtures'
import { makeLive } from '../test/planTabFixture'
import { installHoverClock, type ManualHoverClock } from '../test/hoverClock'
import { resetSessionBoardsForTest } from '../lib/sessionBoard'
import { resetLiveBoardCardsForTest } from '../lib/liveBoardCard'
import '@blerglab/chat/blerg.css'
import '../index.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async (original) => ({
  ...(await original<typeof import('../authClient')>()), getAccessToken: () => 'fixture-token', redirectToRefresh: vi.fn(),
}))
vi.mock('../claims', async (original) => ({ ...(await original<typeof import('../claims')>()), tokenExpired: () => false }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))

const json = (body: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }))
const COLUMNS = [{ id: 'todo', name: 'To do' }, { id: 'doing', name: 'In progress' }, { id: 'done', name: 'Done', is_terminal: true }]
const CARDS: Record<string, { title: string; column: string }> = {
  155: { title: 'Dismiss in Needs you', column: 'done' },
  156: { title: 'Review the fold', column: 'doing' },
}
let reads: string[] = []

function serve() {
  apiFetch.mockImplementation((url: string) => {
    if (url.endsWith('/permissions')) return json({ can_grant: true, reason: '', grants: [], mcp_tools: [], boards: [{ board_id: 'b1', board_name: 'Blerg', level: 'full' }] })
    const m = url.match(/^\/api\/board-cards\/b1\/(\d+)/)
    if (m) {
      reads.push(m[1])
      const c = CARDS[m[1]]
      if (!c) return json({ error: 'not found' }, 404)
      return json({ card: { id: `c${m[1]}`, board_id: 'b1', number: Number(m[1]), title: c.title, column_id: c.column, column_name: COLUMNS.find(x => x.id === c.column)!.name, columns: COLUMNS }, url: `/boards/b1/cards/${m[1]}` })
    }
    return json({}, 404)
  })
}

const items = [
  { id: 'a', text: '#155 Dismiss in Needs you', done: false },
  { id: 'b', text: '#156 Review the fold', done: false, in_progress: true },
  { id: 'c', text: 'Make #1 thing work', done: false },
  { id: 'd', text: 'Ship it', done: true },
]

function show(width: number, list = items) {
  const drawer = width < 1024
  render(<MemoryRouter><div style={{ display: 'flex', height: '100vh', width }}>
    <div style={{ flex: 1 }} />
    <SessionRail id="rail" session={makeSession({ status: 'running' })} toolChanges={0} mode={drawer ? 'drawer' : 'panel'} open onClose={() => {}} onOpenArtifact={() => {}}
      live={makeLive({ checklist: { items: list, eventId: 'c1', updatedAt: null } as never })} />
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
  // Resolving the card renders its hover binding after the link. Wait for
  // that binding, then re-aim real input as the checklist finishes layout.
  await expect.poll(() => el.closest('.hc-anchor, [data-hc-anchor]')).not.toBeNull()
  await expect.poll(async () => {
    const r = el.getBoundingClientRect()
    const x = Math.round(r.left + r.width / 2), y = Math.round(r.top + r.height / 2)
    await moveTo(x, y - 1)
    await moveTo(x, y)
    return clock.pending() > 0 || card() !== null
  }).toBe(true)
  clock.advance(HOVERCARD.openDelayMs)
  await expect.poll(card).not.toBeNull()
}

beforeEach(async () => {
  reads = []
  resetRefStoresForTest()
  resetSessionBoardsForTest()
  resetLiveBoardCardsForTest()
  clearHovercardCache()
  localStorage.clear()
  clock = installHoverClock()
  mouse = cdp() as unknown as Mouse
  await moveTo(1, 1)
  serve()
})
afterEach(async () => { await moveTo(1, 1); clock.uninstall(); apiFetch.mockReset() })

const row = (id: string) => document.querySelector<HTMLElement>(`[data-checklist-id="${id}"]`)!

for (const [width, height] of [[390, 800], [3840, 2000]] as const) {
  describe(`card numbers in the checklist at ${width} px`, () => {
    beforeEach(async () => { await page.viewport(width, height) })

    it('on Plan: #155 links with the card\'s hovercard and its ✓, #1 stays text, and each card is read once', async () => {
      show(width)
      await userEvent.click(screen.getByRole('tab', { name: /Plan/ }))
      await waitFor(() => expect(within(row('a')).getByTestId('plan-ref')).toHaveTextContent('#155'))
      // The unticked item's card is done: ✓ beside it, words in its name.
      await waitFor(() => expect(within(row('a')).getByTestId('plan-card-state')).toHaveAttribute('data-card-state', 'done'))
      expect(within(row('a')).getByTestId('plan-card-state')).toHaveAccessibleName('Card #155 is done; this item is not started')
      // The doing item's card is in progress: they agree, no glyph.
      await waitFor(() => expect(within(row('b')).getByTestId('plan-ref')).toHaveTextContent('#156'))
      expect(within(row('b')).queryByTestId('plan-card-state')).toBeNull()
      // No card #1: the words as written, no link, no glyph.
      expect(within(row('c')).queryByTestId('plan-ref')).toBeNull()
      expect(row('c').textContent).toContain('Make #1 thing work')
      expect([...reads].sort()).toEqual(['1', '155', '156'])
      // The link sits inside the panel and the title keeps one line.
      const ref = within(row('a')).getByTestId('plan-ref')
      const rail = (document.querySelector('[role="dialog"]') ?? screen.getByTestId('session-rail')).getBoundingClientRect()
      expect(ref.getBoundingClientRect().right).toBeLessThanOrEqual(rail.right)
      expect(row('a').getBoundingClientRect().height).toBeLessThanOrEqual(30)
      await hover(ref)
      await expect.poll(() => card()!.textContent).toContain('Dismiss in Needs you')
      await moveTo(1, 1)
      ref.click()
      expect(await screen.findByRole('dialog', { name: /Dismiss in Needs you|#155/ })).toBeInTheDocument()
      expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    })

    it('keeps the keyboard expand control: Tab from the card link reaches the chevron, Enter shows the whole item', async () => {
      const long = [{ id: 'a', text: '#155 Dismiss in Needs you, and also move every follow-up the review raised into its own card on the board', done: false }, { id: 'c', text: 'Make #1 thing work, a second item whose words run past the edge of a phone', done: false }]
      show(width, long)
      await userEvent.click(screen.getByRole('tab', { name: /Plan/ }))
      const ref = await waitFor(() => within(row('a')).getByTestId('plan-ref'))
      const toggle = within(row('a')).getByTestId('rail-plan-expand')
      const title = within(row('a')).getByTestId('rail-plan-title')
      expect(toggle).toHaveAttribute('aria-expanded', 'false')
      expect(toggle.getAttribute('aria-controls')).toBe(title.id)
      ref.focus()
      await userEvent.tab()
      expect(document.activeElement).toBe(toggle)
      await userEvent.keyboard('{Enter}')
      expect(toggle).toHaveAttribute('aria-expanded', 'true')
      expect(title).toHaveAttribute('data-expanded', 'true')
      expect(getComputedStyle(title).whiteSpace).toBe('normal')
      if (width === 390) expect(row('a').getBoundingClientRect().height).toBeGreaterThan(30)
      await userEvent.keyboard('{Enter}')
      expect(toggle).toHaveAttribute('aria-expanded', 'false')
      // The same control on an item that names no card: Tab reaches it from the one before.
      await userEvent.tab()
      expect(document.activeElement).toBe(within(row('c')).getByTestId('rail-plan-expand'))
      await userEvent.keyboard(' ')
      expect(within(row('c')).getByTestId('rail-plan-expand')).toHaveAttribute('aria-expanded', 'true')
      expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    })

    it('on Live\'s checklist line: the doing item\'s #156 links, with no glyph as its card agrees', async () => {
      show(width)
      const line = await screen.findByTestId('rail-plan-summary')
      // The doing item comes first on the line: #156 agrees with it.
      await waitFor(() => expect(within(line).getAllByTestId('plan-ref').map(r => r.textContent)).toEqual(['#156']))
      expect(within(line).queryByTestId('plan-card-state')).toBeNull()
      expect(within(line).getByTestId('rail-plan-summary-text').textContent).toBe('Checklist · 1 of 4 · Doing: #156 Review the fold')
      await hover(within(line).getByTestId('plan-ref'))
      await expect.poll(() => card()!.textContent).toContain('Review the fold')
    })

    it('on Live\'s checklist line with nothing doing: the next item\'s #155 links, with the ✓ of its done card', async () => {
      show(width, items.filter(i => i.id !== 'b'))
      const line = await screen.findByTestId('rail-plan-summary')
      await waitFor(() => expect(within(line).getAllByTestId('plan-ref').map(r => r.textContent)).toEqual(['#155']))
      await waitFor(() => expect(within(line).getByTestId('plan-card-state')).toHaveAttribute('data-card-state', 'done'))
      expect(within(line).getByTestId('rail-plan-summary-text').textContent).toBe('Checklist · 1 of 3 · Next: #155 Dismiss in Needs you✓')
      within(line).getByTestId('plan-ref').click()
      expect(await screen.findByRole('dialog', { name: /Dismiss in Needs you|#155/ })).toBeInTheDocument()
    })
  })
}
