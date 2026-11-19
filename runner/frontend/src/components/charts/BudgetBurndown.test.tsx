import { expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import BudgetBurndown from './BudgetBurndown'
import { buildBurndown, WEEK } from '../../lib/budgetBurndown'
import type { UsageBudgetSample } from '../../lib/insights'

const START = Date.parse('2026-11-01T00:00:00Z')
const RESET = START + WEEK

function sample(usedPercent: number, atMs: number): UsageBudgetSample {
  return { provider: 'claude', window_id: '7d', used_percent: usedPercent, window_start: new Date(START).toISOString(), resets_at: new Date(RESET).toISOString(), sampled_at: new Date(atMs).toISOString() }
}

it('reads 0% used plainly, not as missing data', () => {
 const series = buildBurndown([sample(0, START + 10 * 60_000)], 'claude')!
 render(<BudgetBurndown series={series} now={START + 10 * 60_000} />)
 expect(screen.getByTestId('budget-burndown-claude')).toBeInTheDocument()
 expect(screen.getByText(/0% used as of/)).toBeInTheDocument()
})

it('flags a pace that crosses 100% before reset, with the time it would', () => {
 const series = buildBurndown([sample(40, START + 86400_000)], 'claude')!
 render(<BudgetBurndown series={series} now={START + 86400_000} />)
 expect(screen.getByText(/the budget runs out around/)).toBeInTheDocument()
 expect(document.querySelector('.budget-burndown-over')).toBeTruthy()
 expect(document.querySelector('.budget-burndown-over-dot')).toBeTruthy()
})

it('stays on the even-pace reading without the "over" styling when under budget', () => {
 const series = buildBurndown([sample(5, START + 86400_000)], 'claude')!
 render(<BudgetBurndown series={series} now={START + 86400_000} />)
 expect(screen.getByText(/On pace to stay within the weekly budget/)).toBeInTheDocument()
 expect(document.querySelector('.budget-burndown-over')).toBeNull()
 expect(document.querySelector('.budget-burndown-projection')).toBeTruthy()
})

it('shows last week faintly when a previous window was recorded', () => {
 const previousStart = START - WEEK
 const rows: UsageBudgetSample[] = [
  { provider: 'claude', window_id: '7d', used_percent: 70, window_start: new Date(previousStart).toISOString(), resets_at: new Date(START).toISOString(), sampled_at: new Date(previousStart + 86400_000).toISOString() },
  sample(10, START + 3600_000),
 ]
 const series = buildBurndown(rows, 'claude')!
 render(<BudgetBurndown series={series} now={START + 3600_000} />)
 expect(screen.getByText(/Last week is shown faintly for comparison/)).toBeInTheDocument()
 // A single recorded reading is a dot, not a path (an SVG path with one point draws nothing).
 expect(document.querySelector('.budget-burndown-previous-dot')).toBeTruthy()
})

it('draws a broken trace across a sampling gap instead of joining straight through it', () => {
 const rows: UsageBudgetSample[] = [sample(2, START + 600_000), sample(4, START + 1_200_000), sample(30, START + 3 * 3600_000)]
 const series = buildBurndown(rows, 'claude')!
 const { container } = render(<BudgetBurndown series={series} now={START + 3 * 3600_000} />)
 const actualPaths = [...container.querySelectorAll('path')].filter(p => p.getAttribute('stroke') === 'var(--chart-git)')
 expect(actualPaths).toHaveLength(1)
 // The isolated reading after the gap is a single point: an SVG path with only one command draws
 // nothing, so it needs its own dot to stay visible rather than silently vanishing.
 expect(container.querySelectorAll('circle[fill="var(--chart-git)"]')).toHaveLength(1)
})
