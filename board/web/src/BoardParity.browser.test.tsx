import { afterEach, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, within, waitFor } from '@testing-library/react'
import { page } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import { BoardView } from './BoardView'
import { MeProvider } from './me'
import { parityBoard, parityCards, parityColumns } from '../../../packages/board/parityFixture'
import './styles.css'

let destination = 'lane-0'
vi.mock('./api', async original => ({ ...await original<typeof import('./api')>(), boardSocket: () => () => {}, api: vi.fn(async (path: string) => {
  if (path === '/api/me') return { kind: 'human', is_admin: true, is_human: true, account_id: 'parity-person' }
  if (path === '/api/boards/parity') return parityBoard
  if (path.endsWith('/columns')) return parityColumns
  if (path.includes('/cards/view')) return { cards: parityCards.map(c => c.number === 1 ? { ...c, column_id: destination } : c), older: {} }
  if (path === '/api/cards/card-1') return { ...parityCards[0], column_id: destination }
  if (path === '/api/cards/card-1/move') { destination = 'lane-3'; return {} }
  if (path.endsWith('/run')) return { state: 'stopped', ready: 0, blocked: 0, in_flight: 0 }
  return []
}) }))
afterEach(cleanup)
for (const width of [390, 3840]) it(`uses the shared compact board at ${width}px`, async () => {
  localStorage.clear(); sessionStorage.clear()
  destination = 'lane-0'
  await page.viewport(width, width === 390 ? 844 : 1200)
  render(<MemoryRouter><MeProvider><BoardView boardId="parity" /></MeProvider></MemoryRouter>)
  await screen.findByText(parityCards[0].title)
  for (const el of document.querySelectorAll('.shared-board-column, .shared-board-card')) expect(el.getBoundingClientRect().width).toBeLessThanOrEqual(290)
  expect(getComputedStyle(screen.getByText(parityCards[0].title)).fontWeight).toBe('400')
  expect(parseFloat(getComputedStyle(screen.getByText(parityCards[0].title)).lineHeight)).toBeCloseTo(20.3)
  const shots = import.meta.env.VITE_BOARD_SHOTS_DIR
  if (shots) await page.screenshot({ path: `${shots}/board-${width}.png` })
  fireEvent.click(screen.getByRole('button', { name: 'Filter' }))
  fireEvent.click(within(screen.getByRole('group', { name: 'Visible columns' })).getByRole('button', { name: 'Inbox column' }))
  fireEvent.keyDown(document, { key: 'Escape' })
  expect(document.querySelectorAll('.shared-board-column')).toHaveLength(3)
  cleanup()
  render(<MemoryRouter><MeProvider><BoardView boardId="parity" /></MeProvider></MemoryRouter>)
  await waitFor(() => expect(document.querySelectorAll('.shared-board-column')).toHaveLength(3))
  expect(screen.queryByRole('button', { name: /DEMO-1/ })).toBeNull()
  fireEvent.click(screen.getByRole('button', { name: 'Show all columns' }))
  expect(document.querySelectorAll('.shared-board-column')).toHaveLength(4)
  fireEvent.click(screen.getByRole('button', { name: 'Filter' }))
  fireEvent.click(within(screen.getByRole('group', { name: 'Visible columns' })).getByRole('button', { name: 'Done column' }))
  fireEvent.keyDown(document, { key: 'Escape' })
  fireEvent.click(screen.getByRole('button', { name: /DEMO-1/ }))
  fireEvent.click(await screen.findByRole('button', { name: '→ Done' }))
  await screen.findByText('DEMO-1 moved to hidden column Done.')
})
