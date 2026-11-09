import { browserWidths } from '../test/browserWidths'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { page } from 'vitest/browser'
import { PinnedTicketModal, PinnedTicketSideView } from './BoardCardView'
import { OPEN_SESSION_BOARD } from '../lib/openSessionBoard'
import { resetSessionBoardLinksForTest } from '../lib/sessionBoardLinks'
import { resetLiveBoardCardsForTest } from '../lib/liveBoardCard'
import '../index.css'
import './SessionBoardTabs.css'
import { setTheme } from '../../../../packages/settings/theme'
import '@blerglab/chat/blerg.css'

const apiFetch = vi.fn()
vi.mock('../apiFetch', () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))
afterEach(() => { apiFetch.mockReset(); resetLiveBoardCardsForTest(); resetSessionBoardLinksForTest() })

const card = {
  version: 0, id: 'card-12', board_id: 'board-a', number: 12, title: 'Fix the flaky integration test that fails on the slow runners every other night', body: '## Steps\n\n' + 'A long paragraph of what happens. '.repeat(40),
  type: 'bug', priority: 'high', column_id: 'col-inbox', column_name: 'inbox', tags: ['ci', 'flaky', 'needs-triage', 'runner'], repos: ['example-org/a-repository-with-a-long-name', 'example-org/app'],
  origin: { repo: 'example-org/app', commit: 'abcdef0123456789', engine: 'claude' },
  size: 'M', risk: 'medium', complexity: 'low', created_at: '2026-10-19T09:00:00Z', updated_at: '2026-10-20T11:00:00Z',
  columns: [{ id: 'col-inbox', name: 'inbox' }, { id: 'col-ready', name: 'ready' }, { id: 'col-doing', name: 'in progress' }, { id: 'col-done', name: 'done' }],
  trail: Array.from({ length: 12 }, (_, i) => i % 3 === 0
    ? { type: 'comment', actor: 'Ann', text: 'A comment with `code` and a very-long-unbroken-token-' + 'x'.repeat(80), created_at: '2026-10-20T10:00:00Z' }
    : { type: 'moved', actor: 'agent', to_column: 'ready', created_at: '2026-10-20T11:00:00Z' }),
}
const pinned = { source: 'blerg-board', key: '#12', board_id: 'board-a', title: 'Fix the flaky test', status: 'in progress' }

describe('the live board card dialog in Chromium', () => {
  it.each(browserWidths([390, 3840]))('uses the screen and stays inside it at %i px', async width => {
    const height = width === 3840 ? 2160 : 844
    await page.viewport(width, height)
    apiFetch.mockResolvedValue({ ok: true, status: 200, json: async () => ({ card, url: 'https://board.example.test/boards/board-a/cards/12' }) })
    render(<MemoryRouter><PinnedTicketModal ticket={pinned} sessionId="sess-a" onClose={() => {}} /></MemoryRouter>)
    const live = await screen.findByTestId('board-card-live')
    // When it was filed and last changed, and its estimate, are part of the details (owner 2026-11-05).
    expect(screen.getByTestId('board-card-created').textContent).not.toBe('')
    expect(screen.getByTestId('board-card-updated').textContent).not.toBe('')
    expect(screen.getByTestId('board-card-estimate').textContent).toContain('M')
    const dialog = screen.getByTestId('ticket-modal').getBoundingClientRect()
    expect(dialog.left).toBeGreaterThanOrEqual(0)
    expect(dialog.right).toBeLessThanOrEqual(width)
    // A bounded sheet on a wide screen (maximize goes edge to edge); it is not capped inside its pane.
    if (width === 3840) expect(dialog.width).toBeCloseTo(Math.min(1120, width * 0.9), 0)
    else expect(dialog.width).toBeGreaterThan(width - 40)
    expect(live.scrollWidth).toBeLessThanOrEqual(live.clientWidth + 1)
    for (const el of live.querySelectorAll<HTMLElement>('button, a')) {
      const box = el.getBoundingClientRect()
      expect(box.right).toBeLessThanOrEqual(width)
      if (width === 390 && el.tagName !== 'A') expect(box.height).toBeGreaterThanOrEqual(44)
    }
    expect(screen.getByRole('group', { name: 'Status' })).toBeTruthy()
  })
})

it('moves a newly created version zero card from Status', async () => {
 apiFetch.mockImplementation(async (_url, init) => ({ ok: true, status: 200, json: async () => init?.method === 'POST' ? {} : { card, url: '' } }))
 render(<MemoryRouter><PinnedTicketModal ticket={pinned} sessionId="sess-a" onClose={() => {}} /></MemoryRouter>)
 fireEvent.click(await screen.findByRole('button', { name: 'ready' }))
 await waitFor(() => expect(apiFetch.mock.calls.some(([, init]) => init?.method === 'POST')).toBe(true))
 expect(JSON.parse(apiFetch.mock.calls.find(([, init]) => init?.method === 'POST')![1].body).if_match).toBe(0)
})

it('refreshes Status on a real conflict and sends the fresh version only on the next click', async () => {
 let version = 0
 const moves: number[] = []
 apiFetch.mockImplementation(async (_url, init) => {
  if (init?.method === 'POST') {
   moves.push(JSON.parse(init.body).if_match)
   if (moves.length === 1) { version = 1; return { ok: false, status: 409, json: async () => ({ error: 'the board refused: {"error": "stale version"}' }) } }
   return { ok: true, status: 200, json: async () => ({}) }
  }
  return { ok: true, status: 200, json: async () => ({ card: { ...card, version }, url: '' }) }
 })
 const view = render(<MemoryRouter><PinnedTicketModal ticket={pinned} sessionId="sess-a" onClose={() => {}} /></MemoryRouter>)
 fireEvent.click(await screen.findByRole('button', { name: 'ready' }))
 await screen.findByText('The card changed. Review its current status and try again.')
 expect(moves).toEqual([0])
 view.unmount()
 render(<MemoryRouter><PinnedTicketModal ticket={pinned} sessionId="sess-a" onClose={() => {}} /></MemoryRouter>)
 fireEvent.click(await screen.findByRole('button', { name: 'ready' }))
 await waitFor(() => expect(moves).toEqual([0, 1]))
})

it('hides Open board tab only in a board tab sheet', async () => {
 const host = (event: Event) => event.preventDefault()
 window.addEventListener(OPEN_SESSION_BOARD, host)
 try {
  apiFetch.mockImplementation(async url => ({ ok: true, status: 200, json: async () => String(url).endsWith('/permissions')
   ? { can_grant: true, reason: '', grants: [], mcp_tools: [], boards: [{ board_id: 'board-a', board_name: 'Board', level: 'read' }] }
   : { card, url: '' } }))
  const view = render(<MemoryRouter><PinnedTicketModal ticket={pinned} sessionId="sess-a" onClose={() => {}} /></MemoryRouter>)
  await screen.findByRole('button', { name: 'Open board tab' })
  view.rerender(<MemoryRouter><PinnedTicketModal ticket={pinned} sessionId="sess-a" tabScoped onClose={() => {}} /></MemoryRouter>)
  expect(screen.queryByRole('button', { name: 'Open board tab' })).toBeNull()
 } finally { window.removeEventListener(OPEN_SESSION_BOARD, host) }
})

it('turns a shared pinned dialog into a ticket tab through its workspace host', async () => {
 const opened: unknown[] = []
 const close = vi.fn()
 const host = (event: Event) => { const e = event as CustomEvent; if (e.detail.ticket) opened.push(e.detail.ticket); e.preventDefault() }
 window.addEventListener(OPEN_SESSION_BOARD, host)
 try {
  apiFetch.mockImplementation(async url => ({ ok: true, status: 200, json: async () => String(url).endsWith('/permissions')
   ? { can_grant: true, reason: '', grants: [], mcp_tools: [], boards: [{ board_id: 'board-a', board_name: 'Board', level: 'read' }] }
   : { card, url: '' } }))
  render(<MemoryRouter><PinnedTicketModal ticket={pinned} sessionId="sess-a" onClose={close} /></MemoryRouter>)
  fireEvent.click(await screen.findByRole('button', { name: 'Open as tab' }))
  expect(opened).toEqual([pinned])
  expect(close).toHaveBeenCalledOnce()
 } finally { window.removeEventListener(OPEN_SESSION_BOARD, host) }
})

for (const width of [412, 390, 430, 3840]) for (const theme of ['light', 'dark', 'colorblind-dark'] as const) {
 it(`keeps the fully populated ticket header readable at ${width} in ${theme}`, async () => {
  await page.viewport(width, width === 3840 ? 2160 : 915)
  setTheme(theme)
  const host = (event: Event) => event.preventDefault()
  window.addEventListener(OPEN_SESSION_BOARD, host)
  try {
   const ownerCard = { ...card, reference: 'BLG-145', title: 'Keep ticket headers readable on phones' }
   apiFetch.mockImplementation(async url => ({ ok: true, status: 200, json: async () => String(url).endsWith('/permissions')
    ? { can_grant: true, reason: '', grants: [], mcp_tools: [], boards: [{ board_id: 'board-a', board_name: 'Board', level: 'read' }] }
    : { card: ownerCard, url: 'https://board.example.test/boards/board-a/cards/12' } }))
   render(<MemoryRouter><PinnedTicketModal ticket={pinned} sessionId="sess-a" onClose={() => {}} /></MemoryRouter>)
   await screen.findByRole('button', { name: 'Open board tab' })
   await screen.findByTestId('board-card-live')
   const modal = screen.getByTestId('ticket-modal')
   const header = modal.querySelector<HTMLElement>('.ticket-modal-header')!
   const key = header.querySelector<HTMLElement>('.ticket-key')!
   const title = header.querySelector<HTMLElement>('h2')!
   const shots = import.meta.env.VITE_CHROME_SHOTS_DIR
   if (shots) {
    const frame = document.createElement('div')
    frame.style.cssText = 'position:fixed;inset:0;width:100vw;height:100vh;pointer-events:none'
    document.body.append(frame)
    expect(frame.getBoundingClientRect().width).toBe(width)
    try { await page.screenshot({ path: `${shots}/ticket-${width}-${theme}.png`, element: frame }) } finally { frame.remove() }
   }
   expect(key.getBoundingClientRect().height).toBeLessThan(30)
   expect(key.title).toBe('BLG-145')
   expect(header.scrollWidth).toBeLessThanOrEqual(header.clientWidth)
   if (width < 640) {
    expect(header.getBoundingClientRect().height).toBeLessThan(190)
    expect(title.getBoundingClientRect().width).toBeGreaterThan(width - 80)
    const columns = screen.getByTestId('board-card-columns')
    const tops = [...columns.querySelectorAll('button')].map(button => button.getBoundingClientRect().top)
    expect(new Set(tops).size).toBe(1)
    const top = header.getBoundingClientRect().top
    modal.scrollTop = 200
    expect(header.getBoundingClientRect().top).toBeLessThan(top - 100)
    modal.scrollTop = 0
   }
   if (width === 3840) {
    // Density follows a narrow sheet even inside an ultrawide workspace.
    modal.style.width = '390px'
    expect(header.getBoundingClientRect().height).toBeLessThan(190)
    expect(key.getBoundingClientRect().height).toBeLessThan(30)
    expect(title.getBoundingClientRect().width).toBeGreaterThan(300)
   }
   for (const control of header.querySelectorAll<HTMLElement>('button, a')) {
    expect(control.getAttribute('title')).toBeTruthy()
    if (width < 640 && !control.classList.contains('ticket-key')) expect(control.getBoundingClientRect().height).toBeGreaterThanOrEqual(44)
   }
  } finally { window.removeEventListener(OPEN_SESSION_BOARD, host) }
 })
}

it('uses the full width for a ticket tab title in a narrow pane on a wide screen', async () => {
 await page.viewport(3840, 2160)
 apiFetch.mockResolvedValue({ ok: true, status: 200, json: async () => ({ card: { ...card, reference: 'BLG-145' }, url: '' }) })
 render(<MemoryRouter><div style={{ width: 390, display: 'flex' }}><PinnedTicketSideView ticket={pinned} sessionId="sess-a" onClose={() => {}} /></div></MemoryRouter>)
 await screen.findByTestId('board-card-live')
 const title = screen.getByRole('heading', { level: 2, name: /^BLG-145 ·/ })
 expect(title.textContent).toContain('BLG-145')
 expect(title.getBoundingClientRect().width).toBeGreaterThan(340)
 expect(title.scrollWidth).toBeLessThanOrEqual(title.clientWidth)
 expect(screen.queryByRole('button', { name: 'Open board tab' })).toBeNull()
})
