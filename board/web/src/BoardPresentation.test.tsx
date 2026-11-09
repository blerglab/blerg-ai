import { fireEvent, render, screen } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { BoardCard, BoardColumn } from '../../../packages/board/BoardPresentation'
import { FilterMenu } from '../../../packages/board/FilterMenu'
import { chipsFor, emptyFilter } from '../../../packages/board/boardFilter'

it('renders empty columns, zero counts, long titles and every metadata chip with real host callbacks', () => {
  const open = vi.fn()
  const card = { number: 0, title: 'LongUnbrokenTitle'.repeat(30), type: 'bug', priority: 'urgent', size: 'XL', risk: 'high', complexity: 'low', tags: ['one', 'two', 'three', 'four'], stale_at: '2026-01-01T00:00:00Z', stuck_at: '2026-01-01T00:00:00Z' }
  render(<><BoardColumn name="Empty" count={0}><div className="cards" /></BoardColumn><BoardColumn name="Doing" count={1}><div className="cards"><BoardCard card={card} onClick={open} /></div></BoardColumn></>)
  expect(screen.getByText('0')).toBeTruthy()
  expect(screen.getByText('#0')).toBeTruthy()
  expect(screen.getByText(card.title).tagName).toBe('SPAN')
  for (const text of ['bug', 'urgent', 'XL', 'Risk: high', 'Complexity: low', 'one', 'two', 'three']) expect(screen.getByText(text)).toBeTruthy()
  expect(screen.queryByText('four')).toBeNull()
  expect(screen.getByText(/💤 stale/)).toBeTruthy()
  expect(screen.getByText(/⏸ stuck/)).toBeTruthy()
  fireEvent.click(screen.getByRole('button')); expect(open).toHaveBeenCalledOnce()
})

it('keeps selected zero-count filters removable with the same fake card shape', () => {
  const change = vi.fn()
  const filter = { ...emptyFilter, type: ['bug'] }
  render(<FilterMenu chips={chipsFor([], filter)} filter={filter} onFilter={change} sheet={false} />)
  fireEvent.click(screen.getByRole('button', { name: 'Filter, 1 on' }))
  fireEvent.click(screen.getByRole('button', { name: 'Type bug, 0 cards' }))
  expect(change).toHaveBeenCalledWith(emptyFilter)
})

it('offers the same focus action on double click and touch with a labeled return', () => {
  const focus = vi.fn()
  const { rerender } = render(<BoardColumn name="Doing" count={80} onFocusColumn={focus} />)
  fireEvent.doubleClick(screen.getByRole('heading', { name: 'Doing' }))
  expect(focus).toHaveBeenCalledTimes(1)
  fireEvent.click(screen.getByRole('button', { name: 'Focus Doing' }))
  expect(focus).toHaveBeenCalledTimes(2)
  rerender(<BoardColumn name="Doing" count={80} focused onFocusColumn={focus} />)
  fireEvent.click(screen.getByRole('button', { name: 'Back to all columns' }))
  expect(focus).toHaveBeenCalledTimes(3)
})
