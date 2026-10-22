// Card #96: the runner's live board card dialog laid out like the Board's card sheet, in real
// Chromium at 390 and 3840 px: two columns on a wide screen (the description and the trail in the
// main one, status, type, priority, tags, repos and the worker beside it), folded under the title on
// a phone; status is a column picker whose one click moves the card (no native select, no separate
// Move button); one "Open in Board" link, in the header. With VITE_TICKET_SHOT_DIR set it saves a
// screenshot of each width.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { page, userEvent } from 'vitest/browser'
import { PinnedTicketModal } from './BoardCardView'
import { resetLiveBoardCardsForTest } from '../lib/liveBoardCard'
import '../index.css'
import '@blerglab/chat/blerg.css'

const apiFetch = vi.fn()
vi.mock('../apiFetch', () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))
afterEach(() => { apiFetch.mockReset(); resetLiveBoardCardsForTest() })

const shotDir = (import.meta.env as Record<string, string | undefined>).VITE_TICKET_SHOT_DIR

const card = {
  id: 'card-155', board_id: 'board-a', number: 155, title: 'Needs you: dismiss a review request (and other asks) without doing it',
  body: 'Owner 2026-10-22: a session requested a review of a document; the person replied to the session in chat instead of reviewing in the modal, and the review request stayed in Needs you with no way to clear it.\n\n- Add **Dismiss** on review requests in Needs you\n- Same Dismiss for questions\n\n' + 'Tests at 390/3840; CLI review status reports dismissed. '.repeat(6),
  type: 'task', priority: 'high', column_id: 'col-doing', column_name: 'in progress', tags: ['0.4.5', 'ux'], repos: ['example-org/blerg', 'example-org/docs'],
  origin: { repo: 'example-org/blerg', commit: 'abcdef0123456789', engine: 'claude' },
  columns: [{ id: 'col-inbox', name: 'inbox' }, { id: 'col-ready', name: 'ready' }, { id: 'col-doing', name: 'in progress' }, { id: 'col-review', name: 'review' }, { id: 'col-done', name: 'done' }],
  trail: [
    { type: 'comment', actor: 'Ann', text: 'Looks right; mind the bell too.', created_at: '2026-10-22T10:00:00Z' },
    { type: 'moved', actor: 'agent', to_column: 'inbox', created_at: '2026-10-22T10:01:00Z' },
    { type: 'moved', actor: 'agent', to_column: 'in progress', created_at: '2026-10-22T10:02:00Z' },
  ],
}
const pinned = { source: 'blerg-board', key: '#155', board_id: 'board-a', title: card.title, status: 'in progress' }
const URL_ = 'https://board.example.test/boards/board-a/cards/155'

function serve() {
  apiFetch.mockImplementation((url: string, init?: RequestInit) => {
    if (init?.method === 'POST' && url.includes('/move')) return Promise.resolve({ ok: true, status: 200, json: async () => ({}) })
    return Promise.resolve({ ok: true, status: 200, json: async () => ({ card, url: URL_ }) })
  })
}

describe.each([
  { width: 390, height: 844 },
  { width: 3840, height: 2160 },
])('the live card dialog at $width px', ({ width, height }) => {
  it('is laid out like the Board sheet: main and side columns, one Open in Board, no native select', async () => {
    await page.viewport(width, height)
    serve()
    render(<MemoryRouter><PinnedTicketModal ticket={pinned} sessionId="sess-a" worker="Port the parser" onClose={() => {}} /></MemoryRouter>)
    const live = await screen.findByTestId('board-card-live')
    const modal = screen.getByTestId('ticket-modal')
    if (shotDir) {
      document.documentElement.dataset.theme = 'dark'
      await page.screenshot({ path: `${shotDir}/ticket-modal-${width}.png`, element: modal })
      delete document.documentElement.dataset.theme
    }
    // One "Open in Board", in the header.
    const opens = within(modal).getAllByRole('link', { name: /Open in Board/ })
    expect(opens).toHaveLength(1)
    expect(modal.querySelector('header')!.contains(opens[0])).toBe(true)
    // No native select and no separate Move button.
    expect(modal.querySelector('select')).toBeNull()
    expect(within(modal).queryByRole('button', { name: /^Move$/ })).toBeNull()
    const main = within(live).getByTestId('board-card-main')
    const side = within(live).getByTestId('board-card-side')
    const desc = within(main).getByTestId('board-card-description')
    const m = main.getBoundingClientRect()
    const s = side.getBoundingClientRect()
    const d = desc.getBoundingClientRect()
    // The side holds the fields, each a quiet label beside its value, not a big section heading.
    for (const name of ['Status', 'Type', 'Priority', 'Tags', 'Repos', 'Worker']) expect(within(side).getByText(name)).toBeTruthy()
    expect(within(side).getByText('task')).toBeTruthy()
    expect(side.textContent).toContain('primary')
    expect(side.textContent).toContain('↳')
    expect(side.textContent).toContain('Port the parser')
    expect(within(main).getByTestId('board-card-trail').textContent).toContain('mind the bell')
    if (width > 640) {
      // Two columns: the side to the right of the main one, side by side.
      expect(s.left).toBeGreaterThanOrEqual(m.right)
      expect(Math.abs(s.top - m.top)).toBeLessThan(4)
      expect(s.width).toBeGreaterThan(200)
      expect(s.width).toBeLessThan(m.width)
      // The description uses the main column's whole width (no narrow column with space beside it).
      expect(d.width).toBeGreaterThan(m.width - 4)
    } else {
      // A phone: the side folds under the title, above the description.
      expect(s.bottom).toBeLessThanOrEqual(d.top)
      expect(Math.round(s.width)).toBeGreaterThanOrEqual(Math.round(m.width) - 1)
    }
    const dialog = modal.getBoundingClientRect()
    expect(dialog.right).toBeLessThanOrEqual(width)
    expect(live.scrollWidth).toBeLessThanOrEqual(live.clientWidth + 1)
  })

  it('moves the card with one click on a column in the status picker', async () => {
    await page.viewport(width, height)
    serve()
    render(<MemoryRouter><PinnedTicketModal ticket={pinned} sessionId="sess-a" onClose={() => {}} /></MemoryRouter>)
    const live = await screen.findByTestId('board-card-live')
    const picker = within(live).getByRole('group', { name: 'Status' })
    const current = within(picker).getByRole('button', { name: 'in progress' })
    expect(current.getAttribute('aria-pressed')).toBe('true')
    // The on state is the selected blue (#128), never the accent.
    const probe = document.createElement('span')
    probe.style.color = getComputedStyle(document.documentElement).getPropertyValue('--selected-border').trim()
    document.body.appendChild(probe)
    const blue = getComputedStyle(probe).color
    probe.remove()
    await waitFor(() => expect(getComputedStyle(current).borderTopColor).toBe(blue))
    for (const b of within(picker).getAllByRole('button')) {
      const r = b.getBoundingClientRect()
      expect(r.right).toBeLessThanOrEqual(width)
      if (width <= 640) expect(r.height).toBeGreaterThanOrEqual(44)
    }
    await userEvent.click(within(picker).getByRole('button', { name: 'review' }))
    await waitFor(() => expect(apiFetch.mock.calls.some(c => String(c[0]) === '/api/board-cards/board-a/155/move?session=sess-a')).toBe(true))
    const move = apiFetch.mock.calls.find(c => String(c[0]).includes('/move'))!
    expect(JSON.parse(String((move[1] as RequestInit).body))).toEqual({ column_id: 'col-review' })
    await waitFor(() => expect(within(picker).getByRole('button', { name: 'review' }).getAttribute('aria-pressed')).toBe('true'))
  })
})
