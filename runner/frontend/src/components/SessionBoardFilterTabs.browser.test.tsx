// BLG-465: the Filter popover opens at a natural width, clamped inside the viewport and
// scrolling inside itself when tall; each board tab keeps its own filter state (never leaking
// into another open tab, and Duplicate copies it); size/risk/complexity list in their natural
// order; the Filter button and section headings are sentence case and normal weight, like the
// rest of the app (Insights tab's Filter, top bar, settings panel), not the standalone Board
// app's all-caps, bold look.
import { afterEach, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, within } from '@testing-library/react'
import { page } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import SessionBoardTabs from './SessionBoardTabs'
import type { SessionTabs } from '../hooks/useSessionTabs'
import type { SessionLive } from '../hooks/useSessionLive'
import '../index.css'
import { resetLiveBoardCardsForTest } from '../lib/liveBoardCard'
import { resetSessionBoardLinksForTest } from '../lib/sessionBoardLinks'

// Keep the shared ticket dialog's message store offline in these board fixtures.
vi.mock('../ws', () => import('../test/wsMock'))
// Supply the auth lifecycle reached by the shared dialog's imports.
vi.mock('../authClient', () => ({
 redirectToRefresh: vi.fn(), consumeAccessTokenFromFragment: vi.fn(), startTokenRenewal: vi.fn(),
 renewSilently: vi.fn(async () => 'no-answer' as const), ensureFreshToken: vi.fn(async () => true),
 getAccessToken: () => ['fixture', btoa(JSON.stringify({ sub: 'filter-tabs-person' })), 'fixture'].join('.')
}))
const columns = [{ id: 'inbox', name: 'Inbox' }, { id: 'prog', name: 'In progress' }, { id: 'review', name: 'Review' }, { id: 'done', name: 'Done' }]
const card = (n: number, over: Record<string, unknown> = {}) => ({ id: `c${n}`, board_id: 'b', number: n, title: `Card ${n}`, column_id: 'inbox', rank: String(n), version: 0, size: 'S', risk: 'low', complexity: 'low', priority: 'medium', ...over })
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn(async (url: string) => new Response(JSON.stringify(
  url.endsWith('/permissions') ? { can_grant: true, reason: '', boards: [{ board_id: 'b', board_name: 'Board', level: 'full' }], grants: [], mcp_tools: [] }
    : url.includes('/view?') ? { columns, cards: [card(1), card(2, { size: 'L', risk: 'high', complexity: 'medium' }), card(3, { size: 'XL', risk: 'medium' }), card(4, { size: 'M' })], releases: [], next_cursor: '' }
    : {}
), { status: 200 })) }))
afterEach(cleanup)

for (const width of [390, 3840]) it(`opens an own-sized, scrollable Filter popover per tab at ${width}px, never leaking between tabs`, async () => {
  localStorage.clear(); resetLiveBoardCardsForTest(); resetSessionBoardLinksForTest()
  await page.viewport(width, width === 390 ? 844 : 1200)
  const targetA = document.createElement('div'); targetA.style.height = '50dvh'; document.body.append(targetA)
  const targetB = document.createElement('div'); targetB.style.height = '50dvh'; document.body.append(targetB)
  const tabs = {
    open: vi.fn(), setBoardLabel: vi.fn(), scope: 'filter-shots:',
    tabs: [{ id: 'board:b:a', view: { kind: 'board', boardId: 'b', label: 'In progress' } }, { id: 'board:b:b', view: { kind: 'board', boardId: 'b', label: 'Inbox' } }],
    visible: ['board:b:a', 'board:b:b'],
    targetFor: (id: string) => id === 'board:b:a' ? targetA : targetB,
  } as unknown as SessionTabs
  try {
    render(<MemoryRouter><SessionBoardTabs tabs={tabs} sessionId="filter-shots-session" live={{ live: { subagents: [], tickets: [] }, children: [] } as unknown as SessionLive} /></MemoryRouter>)
    await within(targetA).findByText('Card 1')
    await within(targetB).findByText('Card 1')

    // The Filter button is sentence case and normal weight, like the rest of the app.
    const filterBtnA = within(targetA).getByRole('button', { name: 'Filter' })
    expect(filterBtnA.textContent).toBe('Filter')
    const style = getComputedStyle(filterBtnA)
    expect(style.textTransform).toBe('none')
    expect(Number(style.fontWeight)).toBeLessThan(600)

    fireEvent.click(filterBtnA)
    const popA = within(targetA).getByRole('dialog')
    const headingsA = within(popA).getAllByRole('heading', { level: 4 })
    expect(headingsA.map(h => h.textContent)).toEqual(['Columns', 'Priority', 'Size', 'Risk', 'Complexity', 'Tag'])
    for (const heading of headingsA) expect(getComputedStyle(heading).textTransform).toBe('none')

    // Natural width, clamped inside the viewport, scrolling inside itself when tall.
    const scrollableA = popA.querySelector<HTMLElement>('.filter-sheet-body') ?? popA
    if (scrollableA === popA) {
      const r = popA.getBoundingClientRect()
      expect(r.width).toBeLessThanOrEqual(420)
      expect(r.left).toBeGreaterThanOrEqual(0)
      expect(r.right).toBeLessThanOrEqual(width)
    }
    expect(getComputedStyle(scrollableA).overflowY).toBe('auto')

    // Natural order: size S, M, L, XL; risk/complexity low, medium, high.
    const chipValue = (b: HTMLElement) => b.getAttribute('aria-label')?.split(' ')[1]?.replace(',', '')
    const sizeGroup = within(popA).getByRole('group', { name: 'Size' })
    expect(within(sizeGroup).getAllByRole('button').map(chipValue)).toEqual(['S', 'M', 'L', 'XL'])
    const riskGroup = within(popA).getByRole('group', { name: 'Risk' })
    expect(within(riskGroup).getAllByRole('button').map(chipValue)).toEqual(['low', 'medium', 'high'])

    const shots = import.meta.env.VITE_BOARD_SHOTS_DIR
    if (shots) await page.screenshot({ path: `${shots}/filter-popover-${width}.png` })

    // Tab A's column pick never leaks into tab B.
    fireEvent.click(within(within(popA).getByRole('group', { name: 'Visible columns' })).getByRole('button', { name: 'Review column' }))
    fireEvent.keyDown(document, { key: 'Escape' })
    fireEvent.click(within(targetB).getByRole('button', { name: 'Filter' }))
    const popB = within(targetB).getByRole('dialog')
    expect(within(within(popB).getByRole('group', { name: 'Visible columns' })).getByRole('button', { name: 'Review column' }).getAttribute('aria-pressed')).toBe('true')
    if (shots) await page.screenshot({ path: `${shots}/filter-popover-two-tabs-${width}.png` })
  } finally { cleanup(); targetA.remove(); targetB.remove() }
})
