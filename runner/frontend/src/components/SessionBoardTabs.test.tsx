import { clearBoardPageCache } from '../lib/boardPageCache'
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import SessionBoardTabs from './SessionBoardTabs'
import type { SessionTabs } from '../hooks/useSessionTabs'
import type { SessionLive } from '../hooks/useSessionLive'
import { openSessionBoard } from '../lib/openSessionBoard'
import { resetSessionBoardLinksForTest } from '../lib/sessionBoardLinks'
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('./BoardCardView', () => ({ PinnedTicketModal: ({ ticket, onClose }: { ticket: { title: string }; onClose: () => void }) => <aside aria-label="Card details">Open card: {ticket.title}<button onClick={onClose}>Close card</button></aside> }))
vi.mock('../apiFetch', () => ({ apiFetch }))
// Keep the shared ticket dialog's message store offline in these board fixtures.
vi.mock('../ws', () => import('../test/wsMock'))
// Supply the auth lifecycle reached by the shared dialog's imports.
vi.mock('../authClient', () => ({
 redirectToRefresh: vi.fn(), consumeAccessTokenFromFragment: vi.fn(), startTokenRenewal: vi.fn(),
 renewSilently: vi.fn(async () => 'no-answer' as const), ensureFreshToken: vi.fn(async () => true),
 getAccessToken: () => ''
}))
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status })
const permissions = { can_grant: true, reason: '', grants: [], mcp_tools: [], boards: [{ board_id: 'b', board_name: 'Board', level: 'full' }] }
const card = (id: string, rank: string) => ({ id, board_id: 'b', number: Number(id), title: `Card ${id}`, column_id: 'todo', rank, version: 0 })
let target: HTMLDivElement
let pages = 0
let fail = false
beforeEach(() => {
 clearBoardPageCache()
 localStorage.clear(); vi.useFakeTimers(); resetSessionBoardLinksForTest(); pages = 0; fail = false
 target = document.createElement('div'); document.body.appendChild(target)
 apiFetch.mockReset().mockImplementation(async (url: string) => {
  if (url.endsWith('/permissions')) return json(permissions)
  if (url.includes('/view?')) {
   pages++
   if (fail) return json({}, 503)
   return json({ columns: [{ id: 'todo', name: 'Todo', rank: 'a' }], releases: [], cards: [card('2', 'z'), card('1', 'a')], next_cursor: '' })
  }
  return json({})
 })
})
afterEach(() => { target.remove(); vi.useRealTimers(); vi.restoreAllMocks() })
async function mount(sessionId = 's') {
 const tabs = { open: vi.fn(), setBoardLabel: vi.fn(), tabs: [{ id: 'board:b', view: { kind: 'board', boardId: 'b', label: 'Board' } }], visible: ['board:b'], targetFor: () => target } as unknown as SessionTabs
 render(<MemoryRouter><SessionBoardTabs tabs={tabs} sessionId={sessionId} live={{ live: { subagents: [], tickets: [] }, children: [] } as unknown as SessionLive} /></MemoryRouter>)
 await act(async () => { await vi.advanceTimersByTimeAsync(0) })
 return tabs
}
it('keeps last good data, backs off, pauses hidden polls and resumes on visibility', async () => {
 await mount(); expect(pages).toBe(1); fail = true
 await act(async () => { await vi.advanceTimersByTimeAsync(15_000) }); expect(pages).toBe(2)
 expect(screen.getByText('Card 1')).toBeTruthy(); expect(screen.getByRole('status')).toHaveTextContent("Couldn't refresh the board. Showing what was loaded at")
 await act(async () => { await vi.advanceTimersByTimeAsync(15_000) }); expect(pages).toBe(2)
 await act(async () => { await vi.advanceTimersByTimeAsync(15_000) }); expect(pages).toBe(3)
 vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden'); fireEvent(document, new Event('visibilitychange'))
 await act(async () => { await vi.advanceTimersByTimeAsync(120_000) }); expect(pages).toBe(3)
 vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible'); fail = false
 await act(async () => { fireEvent(document, new Event('visibilitychange')) }); expect(pages).toBe(4)
})
it('debounces filters, caches metadata and orders cards by rank', async () => {
 await mount()
 expect(screen.getAllByRole('button', { name: /Card [12]/ }).map(b => b.textContent)).toEqual(['#1Card 1', '#2Card 2'])
 fireEvent.change(screen.getByLabelText('Filter board text'), { target: { value: 'a' } })
 fireEvent.change(screen.getByLabelText('Filter board text'), { target: { value: 'ab' } })
 await act(async () => { await vi.advanceTimersByTimeAsync(299) }); expect(pages).toBe(1)
 await act(async () => { await vi.advanceTimersByTimeAsync(1) }); expect(pages).toBe(2)
 expect(apiFetch.mock.calls.filter(([u]) => String(u).includes('/view?')).map(([u]) => new URL(String(u), 'https://example.com').searchParams.get('metadata'))).toEqual(['1', '0'])
})
it('renders proxy metadata through the real tab and uses shared facet filters', async () => {
 const original = apiFetch.getMockImplementation()!
 apiFetch.mockImplementation(async (url: string) => url.includes('/view?') ? json({ columns: [{ id: 'todo', name: 'Todo' }, { id: 'empty', name: 'Empty' }], cards: [{ ...card('1', 'a'), type: 'bug', priority: 'urgent', size: 'M', risk: 'high', complexity: 'low', stale_at: '2026-01-01T00:00:00Z' }, card('2', 'z')] }) : original(url))
 await mount()
 const button = screen.getByRole('button', { name: /Card 1/ })
 expect(button).toHaveClass('shared-board-card')
 expect(button).toHaveTextContent('bugurgentMRisk: highComplexity: low')
 expect(button).toHaveTextContent('💤 stale')
 expect(screen.getByText('0')).toBeTruthy()
 fireEvent.click(screen.getByRole('button', { name: 'Filter' }))
 fireEvent.click(screen.getByRole('button', { name: 'Type bug, 1 card' }))
 expect(screen.queryByRole('button', { name: /Card 2/ })).toBeNull()
})
it('bounds unique cursors and removes duplicate cards across pages', async () => {
 const original = apiFetch.getMockImplementation()!
 apiFetch.mockImplementation(async (url: string) => {
  if (!url.includes('/view?')) return original(url)
  pages++
  return json({ columns: [{ id: 'todo', name: 'Todo' }], cards: [card('1', 'a')], next_cursor: `cursor-${pages}` })
 })
 await mount(); expect(pages).toBe(10)
 expect(screen.getAllByRole('button', { name: /Card 1/ })).toHaveLength(1)
 expect(screen.getByRole('status')).toHaveTextContent('1,000')
})

it('keeps an open card on a failed poll but closes it when access is denied', async () => {
 await mount()
 fireEvent.click(screen.getByRole('button', { name: /Card 1/ }))
 expect(screen.getByRole('complementary', { name: 'Card details' })).toBeTruthy()
 fail = true
 await act(async () => { await vi.advanceTimersByTimeAsync(15_000) })
 expect(screen.getByRole('complementary', { name: 'Card details' })).toBeTruthy()
 const original = apiFetch.getMockImplementation()!
 apiFetch.mockImplementation(async (url: string) => url.endsWith('/permissions') ? json({}, 403) : original(url))
 await act(async () => { await vi.advanceTimersByTimeAsync(30_000) })
 expect(screen.queryByRole('complementary', { name: 'Card details' })).toBeNull()
})
it('opens a docked card on click, swaps it, and restores focus when Escape dismisses it', async () => {
 await mount()
 const first = screen.getByRole('button', { name: /Card 1/ })
 fireEvent.click(first)
 expect(screen.queryByRole('dialog')).toBeNull()
 expect(screen.getByRole('region', { name: 'Board columns' })).toBeTruthy()
 expect(screen.getByRole('complementary', { name: 'Card details' })).toHaveTextContent('Card 1')
 const second = screen.getByRole('button', { name: /Card 2/ })
 fireEvent.click(second)
 expect(screen.getAllByRole('complementary')).toHaveLength(1)
 expect(screen.getByRole('complementary')).toHaveTextContent('Card 2')
 fireEvent.keyDown(document, { key: 'Escape' })
 expect(screen.queryByRole('complementary')).toBeNull()
 expect(document.activeElement).toBe(second)
 fireEvent.click(first)
 fireEvent.click(screen.getByRole('button', { name: 'Close card' }))
 expect(screen.queryByRole('complementary')).toBeNull()
})
it('moves a version zero card and keeps the conflict alert after refreshing', async () => {
 const original = apiFetch.getMockImplementation()!
 apiFetch.mockImplementation(async (url: string) => url.includes('/move?') ? json({ error: 'stale version' }, 409) : original(url))
 await mount()
 const transfer = { types: ['application/x-blerg-board-card'], getData: () => JSON.stringify({ board: 'b', number: 1, version: 0, column: 'old' }) }
 await act(async () => { fireEvent.drop(screen.getByRole('region', { name: 'Board columns' }).querySelector('section')!, { dataTransfer: transfer }) })
 expect(apiFetch.mock.calls.some(([u]) => String(u).includes('/move?'))).toBe(true)
 expect(pages).toBe(2)
 expect(screen.getByRole('status')).toHaveTextContent('The card changed.')
})
it('reloads metadata when a card names an unknown column', async () => {
 await mount()
 const original = apiFetch.getMockImplementation()!
 apiFetch.mockImplementation(async (url: string) => {
  if (!url.includes('/view?')) return original(url)
  pages++
  const fresh = new URL(url, 'https://example.com').searchParams.get('metadata') === '1'
  return json({ cards: [{ ...card('1', 'a'), column_id: 'new' }], columns: fresh ? [{ id: 'new', name: 'New' }] : [] })
 })
 await act(async () => { await vi.advanceTimersByTimeAsync(15_000) })
 expect(screen.getByRole('button', { name: /Card 1/ })).toBeTruthy()
 expect(screen.getByRole('heading', { name: 'New' })).toBeTruthy()
 expect(pages).toBe(3)
})
it('keeps each board tab’s column filter independent, never leaking into another tab (BLG-465)', async () => {
 const original = apiFetch.getMockImplementation()!
 apiFetch.mockImplementation(async (url: string) => url.includes('/view?')
  ? json({ columns: [{ id: 'inbox', name: 'Inbox' }, { id: 'prog', name: 'In progress' }], cards: [card('1', 'a')], next_cursor: '' })
  : original(url))
 const targetA = document.createElement('div'); document.body.appendChild(targetA)
 const targetB = document.createElement('div'); document.body.appendChild(targetB)
 const tabs = {
  open: vi.fn(), setBoardLabel: vi.fn(), scope: 'scope:',
  tabs: [{ id: 'board:b:a', view: { kind: 'board', boardId: 'b', label: 'In progress' } }, { id: 'board:b:b', view: { kind: 'board', boardId: 'b', label: 'Inbox' } }],
  visible: ['board:b:a', 'board:b:b'], targetFor: (id: string) => id === 'board:b:a' ? targetA : targetB,
 } as unknown as SessionTabs
 render(<MemoryRouter><SessionBoardTabs tabs={tabs} sessionId="s" live={{ live: { subagents: [], tickets: [] }, children: [] } as unknown as SessionLive} /></MemoryRouter>)
 await act(async () => { await vi.advanceTimersByTimeAsync(0) })
 const within = (root: HTMLElement) => ({
  filterButton: () => root.querySelector<HTMLButtonElement>('.filter-btn')!,
  columnButton: (name: string) => [...root.querySelectorAll<HTMLButtonElement>('.filter-opt')].find(b => b.textContent?.includes(name))!,
 })
 const a = within(targetA), b = within(targetB)
 fireEvent.click(a.filterButton())
 expect(a.columnButton('Inbox').getAttribute('aria-pressed')).toBe('true')
 expect(a.columnButton('In progress').getAttribute('aria-pressed')).toBe('true')
 fireEvent.click(a.columnButton('Inbox'))
 expect(a.columnButton('Inbox').getAttribute('aria-pressed')).toBe('false')
 fireEvent.keyDown(document, { key: 'Escape' })
 fireEvent.click(b.filterButton())
 expect(b.columnButton('Inbox').getAttribute('aria-pressed')).toBe('true')
 expect(b.columnButton('In progress').getAttribute('aria-pressed')).toBe('true')
})
it.each(['s', 'worker', 'channel'])('opens a granted board with requesting chat scope %s', async sessionId => {
 const tabs = await mount(sessionId)
 act(() => { expect(openSessionBoard(sessionId, 'b', 'Board')).toBe(true) })
 expect(tabs.open).toHaveBeenCalledWith({ kind: 'board', boardId: 'b', label: 'Board', sessionId })
})

it.each([429, 500, 503])('keeps loaded cards and server wording on a %s refresh and clears the notice on success', async status => {
 await mount()
 const original = apiFetch.getMockImplementation()!
 apiFetch.mockImplementation(async (url: string) => url.includes('/view?') ? json({ error: 'Please try again shortly.' }, status) : original(url))
 await act(async () => { await vi.advanceTimersByTimeAsync(15_000) })
 expect(screen.getByRole('status')).toHaveTextContent('Please try again shortly.')
 expect(screen.getByRole('status')).not.toHaveTextContent('access')
 expect(screen.getByText('Card 1')).toBeTruthy()
 fireEvent.click(screen.getByRole('button', { name: 'Dismiss board notice' }))
 expect(screen.queryByText(/Showing what was loaded/)).toBeNull()
 apiFetch.mockImplementation(original)
 await act(async () => { await vi.advanceTimersByTimeAsync(30_000) })
 expect(screen.queryByText(/Showing what was loaded/)).toBeNull()
})

it('draws the last loaded board immediately on workspace remount while refreshing quietly', async () => {
 await mount()
 cleanup()
 const original = apiFetch.getMockImplementation()!
 apiFetch.mockImplementation(async (url: string) => url.includes('/view?') ? new Promise<Response>(() => {}) : original(url))
 await mount()
 expect(screen.getByText('Card 1')).toBeTruthy()
 expect(screen.queryByText('Loading board…')).toBeNull()
 expect(document.querySelector('.shared-board-skeleton')).toBeNull()
})

it('closes this tab’s card before exiting its focused column', async () => {
 await mount()
 fireEvent.click(screen.getByRole('button', { name: 'Focus Todo' }))
 expect(document.querySelector('.shared-board-focused')).toBeTruthy()
 fireEvent.click(screen.getByRole('button', { name: /Card 1/ }))
 fireEvent.keyDown(document, { key: 'Escape' })
 expect(screen.queryByRole('complementary')).toBeNull()
 expect(document.querySelector('.shared-board-focused')).toBeTruthy()
 fireEvent.keyDown(document, { key: 'Escape' })
 expect(document.querySelector('.shared-board-focused')).toBeNull()
})
