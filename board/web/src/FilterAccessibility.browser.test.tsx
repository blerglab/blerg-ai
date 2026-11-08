// The old collapsed strip is gone; every choice in the scrollable menu must remain visible
// when reached with the keyboard, including choices below the initial viewport (#126 N2).
import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, render, screen, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { FilterMenu } from './FilterMenu'
import { chipsFor, emptyFilter, type Chip } from './boardFilter'
import type { Card } from './api'
import './styles.css'

afterEach(cleanup)
describe('filter button and heading style', () => {
  // The standalone Board app keeps its own all-caps, bold Filter button and section headings —
  // only the session board tab (runner/frontend) switches to the app's sentence-case style
  // (BLG-465).
  it('stays bold and all-caps, unchanged by the board tab\'s override', async () => {
    const cards = [{ id: 'c1', number: 1, title: 'Card 1', type: 'bug', priority: 'high', tags: [] } as unknown as Card]
    render(<FilterMenu chips={chipsFor(cards, emptyFilter)} filter={emptyFilter} onFilter={() => {}} />)
    const btn = screen.getByRole('button', { name: 'Filter' })
    expect(getComputedStyle(btn).textTransform).toBe('uppercase')
    await userEvent.click(btn)
    const heading = within(screen.getByRole('dialog')).getAllByRole('heading', { level: 4 })[0]
    expect(getComputedStyle(heading).textTransform).toBe('uppercase')
  })
})
describe('filter popover width', () => {
  // The popover opens at a natural width, clamped inside the viewport, never the ~1400px span
  // that used to cross both board tabs and the side panel (BLG-465).
  it.each([390, 3840])('stays a natural width at %i px, scrolling inside itself when tall', async width => {
    await page.viewport(width, 700)
    const cards = Array.from({ length: 8 }, (_, i) => ({ id: `c${i}`, number: i, title: `Card ${i}`, type: 'bug', priority: 'high', tags: [`tag-${i}`] } as unknown as Card))
    render(<FilterMenu chips={chipsFor(cards, emptyFilter)} filter={emptyFilter} onFilter={() => {}} visibility={{ columns: [{ id: 'a', name: 'Inbox' }, { id: 'b', name: 'In progress' }], hidden: [], toggle: () => {}, showAll: () => {} }} />)
    await userEvent.click(screen.getByRole('button', { name: 'Filter' }))
    const pop = screen.getByRole('dialog')
    const scrollable = pop.querySelector<HTMLElement>('.filter-sheet-body') ?? pop
    if (scrollable === pop) {
      const r = pop.getBoundingClientRect()
      expect(r.width).toBeLessThanOrEqual(420)
      expect(r.left).toBeGreaterThanOrEqual(0)
      expect(r.right).toBeLessThanOrEqual(width)
    }
    expect(getComputedStyle(scrollable).overflowY).toBe('auto')
  })
})
describe('filter choice focus', () => {
  it.each([390, 3840])('keeps every focused chip visible at %i px', async width => {
    await page.viewport(width, 900)
    const chips: Chip[] = Array.from({ length: 100 }, (_, i) => ({ facet: 'tag', value: `tag-${String(i).padStart(3, '0')}`, label: '', count: 1, on: false }))
    render(<FilterMenu chips={chips} filter={emptyFilter} onFilter={() => {}} sheet={width < 640} />)
    await userEvent.click(screen.getByRole('button', { name: 'Filter' }))
    const group = screen.getByRole('group', { name: 'Tag' })
    const choices = within(group).getAllByRole('button')
    choices[0].focus()
    for (const choice of choices) {
      expect(document.activeElement).toBe(choice)
      const r = choice.getBoundingClientRect(), bounds = group.getBoundingClientRect()
      expect(r.top).toBeGreaterThanOrEqual(bounds.top - 1)
      expect(r.bottom).toBeLessThanOrEqual(Math.min(bounds.bottom, 900) + 1)
      await userEvent.keyboard('{Tab}')
    }
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  })
})
