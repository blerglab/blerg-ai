import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { page } from 'vitest/browser'
import { PinnedTicketModal } from './BoardCardView'
import { resetLiveBoardCardsForTest } from '../lib/liveBoardCard'
import '@blerglab/chat/blerg.css'

const apiFetch = vi.fn()
vi.mock('../apiFetch', () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))
afterEach(() => { apiFetch.mockReset(); resetLiveBoardCardsForTest() })

const card = {
  id: 'card-12', board_id: 'board-a', number: 12, title: 'Fix the flaky integration test that fails on the slow runners every other night', body: '## Steps\n\n' + 'A long paragraph of what happens. '.repeat(40),
  type: 'bug', priority: 'high', column_id: 'col-inbox', column_name: 'inbox', tags: ['ci', 'flaky', 'needs-triage', 'runner'], repos: ['example-org/a-repository-with-a-long-name', 'example-org/app'],
  origin: { repo: 'example-org/app', commit: 'abcdef0123456789', engine: 'claude' },
  columns: [{ id: 'col-inbox', name: 'inbox' }, { id: 'col-ready', name: 'ready' }, { id: 'col-doing', name: 'in progress' }, { id: 'col-done', name: 'done' }],
  trail: Array.from({ length: 12 }, (_, i) => i % 3 === 0
    ? { type: 'comment', actor: 'Ann', text: 'A comment with `code` and a very-long-unbroken-token-' + 'x'.repeat(80), created_at: '2026-10-20T10:00:00Z' }
    : { type: 'moved', actor: 'agent', to_column: 'ready', created_at: '2026-10-20T11:00:00Z' }),
}
const pinned = { source: 'blerg-board', key: '#12', board_id: 'board-a', title: 'Fix the flaky test', status: 'in progress' }

describe('the live board card dialog in Chromium', () => {
  it.each([390, 3840])('uses the screen and stays inside it at %i px', async width => {
    const height = width === 3840 ? 2160 : 844
    await page.viewport(width, height)
    apiFetch.mockResolvedValue({ ok: true, status: 200, json: async () => ({ card, url: 'https://board.example.test/boards/board-a/cards/12' }) })
    render(<MemoryRouter><PinnedTicketModal ticket={pinned} sessionId="sess-a" onClose={() => {}} /></MemoryRouter>)
    const live = await screen.findByTestId('board-card-live')
    const dialog = screen.getByTestId('ticket-modal').getBoundingClientRect()
    expect(dialog.left).toBeGreaterThanOrEqual(0)
    expect(dialog.right).toBeLessThanOrEqual(width)
    // A wide screen gets the ticket dialog's width (min(1400px, 94vw)); a phone the whole width.
    if (width === 3840) expect(dialog.width).toBeCloseTo(1400, 0)
    else expect(dialog.width).toBeGreaterThan(width - 40)
    expect(live.scrollWidth).toBeLessThanOrEqual(live.clientWidth + 1)
    for (const el of live.querySelectorAll<HTMLElement>('select, button, a')) {
      const box = el.getBoundingClientRect()
      expect(box.right).toBeLessThanOrEqual(width)
      if (width === 390 && el.tagName !== 'A') expect(box.height).toBeGreaterThanOrEqual(44)
    }
    expect(screen.getByLabelText('Move to column')).toBeTruthy()
  })
})
