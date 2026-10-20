import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { ChecklistItems } from './WorkCards'

describe('checklist work indicators', () => {
  it('distinguishes pending, Doing and complete, and animates only current live work', () => {
    const items = [{ id: 'a', text: 'Read', done: false }, { id: 'b', text: 'Build', done: false, in_progress: true },
      { id: 'c', text: 'Test', done: false, in_progress: true }, { id: 'd', text: 'Ship', done: true }]
    const { rerender } = render(<ChecklistItems items={items} live />)
    const rows = screen.getAllByRole('listitem')
    expect(rows[0]).toHaveTextContent('(to do)')
    expect(rows[1]).toHaveTextContent('(doing)')
    expect(rows[1].querySelector('.checklist-progress')).toHaveClass('animated')
    expect(rows[2]).toHaveTextContent('(doing)')
    expect(rows[3]).toHaveTextContent('(done)')
    rerender(<ChecklistItems items={items} />)
    expect(screen.getAllByRole('listitem')[1].querySelector('.checklist-progress')).not.toHaveClass('animated')
  })

  it('highlights the stable target and exposes scoped element refs after reorder', () => {
    const ref = vi.fn()
    const a = { id: 'item-a', text: 'Build', done: false, in_progress: true }
    const b = { id: 'item-b', text: 'Build', done: false }
    const { rerender } = render(<ChecklistItems items={[a, b]} highlightedId="item-a" onItemElement={ref} />)
    expect(screen.getAllByRole('listitem')[0]).toHaveClass('checklist-highlight')
    expect(ref).toHaveBeenCalledWith('item-a', screen.getAllByRole('listitem')[0])
    rerender(<ChecklistItems items={[b, a]} highlightedId="item-a" onItemElement={ref} />)
    expect(screen.getAllByRole('listitem')[0]).not.toHaveClass('checklist-highlight')
    expect(screen.getAllByRole('listitem')[1]).toHaveClass('checklist-highlight')
    expect(screen.getAllByRole('listitem')[1]).toHaveAttribute('tabindex', '-1')
  })
})
