// The old collapsed strip is gone; every choice in the scrollable menu must remain visible
// when reached with the keyboard, including choices below the initial viewport (#126 N2).
import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, render, screen, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { FilterMenu } from './FilterMenu'
import { emptyFilter, type Chip } from './boardFilter'
import './styles.css'

afterEach(cleanup)
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
