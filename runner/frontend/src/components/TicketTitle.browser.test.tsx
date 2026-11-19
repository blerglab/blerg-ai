import { afterEach, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { page } from 'vitest/browser'
import TicketCard from './TicketCard'
import type { Ticket } from '../types'
import '../index.css'

vi.mock('../ws', () => import('../test/wsMock'))
afterEach(cleanup)
for (const width of [390, 1024, 3840]) it(`wraps the runner card title at ${width}px`, async () => {
  await page.viewport(width, 900)
  const title = 'LongUnbrokenTitle'.repeat(24)
  const ticket: Ticket = {
    id: 'card', board_id: 'board', column_id: 'ready', title, body: null,
    priority: 'medium', size: 'm', rank: 'a', archived_at: null, session_id: null,
    version: 1, created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z',
    tags: [], repos: [], blocked: false,
  }
  render(<MemoryRouter><div style={{ width: 280 }}><TicketCard ticket={ticket} columns={[]} /></div></MemoryRouter>)
  const card = screen.getByTestId('ticket-card')
  const text = screen.getByText(title)
  expect(card.scrollWidth).toBeLessThanOrEqual(card.clientWidth)
  expect(text.getBoundingClientRect().height).toBeGreaterThan(32)
  expect(getComputedStyle(text).overflowWrap).toBe('anywhere')
})
