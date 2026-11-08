import { it, expect } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { BarChart } from './InsightsCharts'

const rows = [{ key: 'alpha', count: 10 }, { key: 'beta', count: 5 }]
const many = (n: number) => Array.from({ length: n }, (_, i) => `record ${i}`)

it('names the clear-selection control differently from the expand-a-long-list control (BLG-466)', () => {
 render(<BarChart rows={rows} records={key => many(key === 'alpha' ? 60 : 3)} />)
 fireEvent.click(screen.getByRole('button', { name: 'Show alpha: 10' }))
 // The bar list collapses to one row once a bar is selected, so only the records list can show
 // the "See all" expand control alongside the unrelated "Show all" that clears the selection.
 expect(screen.getByRole('button', { name: 'Show all' })).toBeInTheDocument()
 expect(screen.getByRole('button', { name: 'See all 60' })).toBeInTheDocument()
})

it('resets the records list to collapsed when the selected bar changes', () => {
 render(<BarChart rows={rows} records={key => many(key === 'alpha' ? 60 : 3)} />)
 fireEvent.click(screen.getByRole('button', { name: 'Show alpha: 10' }))
 fireEvent.click(screen.getByRole('button', { name: 'See all 60' }))
 expect(screen.getByRole('list', { name: 'Records for alpha' }).querySelectorAll('li')).toHaveLength(60)
 fireEvent.click(screen.getByRole('button', { name: 'Show all' })) // back to every bar
 fireEvent.click(screen.getByRole('button', { name: 'Show beta: 5' }))
 // beta has only 3 records (under the 50 cap): no stale "expanded" from alpha carries over, and
 // there is nothing left to expand.
 expect(screen.getByRole('list', { name: 'Records for beta' }).querySelectorAll('li')).toHaveLength(3)
 expect(screen.queryByRole('button', { name: /See all|Show top 50/ })).toBeNull()
})
