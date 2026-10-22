// Card #128: an active board filter chip (tag, priority or size) is the selected blue, never the
// accent, the danger red or the lichen green. A priority keeps its own color as a small dot.
import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import { page } from 'vitest/browser'
import '../index.css'
import BoardFilterBar from './BoardFilterBar'

afterEach(() => {
  cleanup()
  delete document.documentElement.dataset.theme
})

function resolve(value: string): string {
  const probe = document.createElement('div')
  document.body.append(probe)
  probe.style.color = value
  const out = getComputedStyle(probe).color
  probe.remove()
  return out
}

describe('the board filter chips', () => {
  it.each([
    [390, 'light'], [390, 'dark'], [3840, 'light'], [3840, 'dark'],
  ] as const)('draws active tag, priority and size chips in the selected blue at %i px (%s)', async (width, theme) => {
    document.documentElement.dataset.theme = theme
    await page.viewport(width, width === 3840 ? 2160 : 844)
    render(
      <BoardFilterBar
        availableTags={['ops']} availablePriorities={['high', 'urgent', 'low']} availableSizes={['m']}
        filter={{ tags: ['ops'], priorities: ['high', 'urgent'], sizes: ['m'] }}
        onChange={() => {}}
      />,
    )
    const selected = resolve('var(--selected-border)')
    const selectedText = resolve('var(--selected-text)')
    expect(selected).toBe(theme === 'dark' ? 'rgb(76, 155, 255)' : 'rgb(47, 111, 214)')
    const notSelected = [resolve('var(--blaze)'), resolve('var(--danger)'), resolve('var(--lichen)')]
    for (const id of ['filter-tag-ops', 'filter-priority-high', 'filter-priority-urgent', 'filter-size-m']) {
      const chip = screen.getByTestId(id)
      expect(chip).toHaveAttribute('data-active', 'true')
      const cs = getComputedStyle(chip)
      expect(cs.borderTopColor, id).toBe(selected)
      expect(cs.color, id).toBe(selectedText)
      expect(notSelected).not.toContain(cs.borderTopColor)
    }
    // the priority's color survives as a dot, on and off
    expect(getComputedStyle(screen.getByTestId('filter-priority-dot-high')).backgroundColor).toBe(resolve('var(--blaze)'))
    expect(getComputedStyle(screen.getByTestId('filter-priority-dot-urgent')).backgroundColor).toBe(resolve('var(--danger)'))
    expect(getComputedStyle(screen.getByTestId('filter-priority-dot-low')).backgroundColor).toBe(resolve('var(--lichen)'))
    const off = getComputedStyle(screen.getByTestId('filter-priority-low'))
    expect(off.borderTopColor).not.toBe(selected)
    expect(off.color).toBe(resolve('var(--fog)'))
  })
})
