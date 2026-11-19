import { expect, it } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import ContextFillChart from './ContextFillChart'
import type { ContextFillSeries } from '../../lib/contextFill'

const FROM = '2026-10-25T00:00:00.000Z', TO = '2026-10-26T00:00:00.000Z'

const main = (points: ContextFillSeries['points'], title = 'Improve ticket flow'): ContextFillSeries => ({ sessionId: 'main-1', title, role: 'main', points })
const other = (id: string, peakPercent: number, role: ContextFillSeries['role'] = 'worker'): ContextFillSeries =>
 ({ sessionId: id, title: `Conversation ${id}`, role, points: [{ at: '2026-10-25T10:00:00.000Z', percent: peakPercent, compacted: false }] })

function renderChart(series: ContextFillSeries[]) {
 return render(<MemoryRouter><ContextFillChart series={series} from={FROM} to={TO} hourly={false} /></MemoryRouter>)
}

it('names Main "Main", not its own title, and keeps the title for its tooltip only', () => {
 renderChart([main([{ at: '2026-10-25T10:00:00.000Z', percent: 81, compacted: false }]), other('worker-1', 30)])
 expect(screen.getByText('Main is 81% full as of Oct 25. No compactions were recorded in this period.')).toBeInTheDocument()
 expect(document.querySelector('.context-fill-series-main')).toBeTruthy()
 expect(document.querySelector('.context-fill-series-other')).toBeTruthy()
 const link = screen.getByRole('link', { name: 'Main' })
 expect(link).toHaveTextContent('Main')
 expect(link).not.toHaveTextContent('Improve ticket flow')
 expect(link).toHaveAttribute('title', 'Main — Improve ticket flow')
})

it('names a compaction count and attributes Main\'s share of it, as two sentences', () => {
 renderChart([
  main([{ at: '2026-10-25T09:00:00.000Z', percent: 90, compacted: false }, { at: '2026-10-25T10:00:00.000Z', percent: 12, compacted: true }]),
  { ...other('worker-1', 95), points: [{ at: '2026-10-25T08:00:00.000Z', percent: 95, compacted: false }, { at: '2026-10-25T09:00:00.000Z', percent: 18, compacted: true }] },
 ])
 expect(screen.getByText('Main is 12% full as of Oct 25. 2 compactions in this period, 1 in Main.')).toBeInTheDocument()
})

it('gives each legend entry its line\'s color swatch', () => {
 renderChart([main([{ at: '2026-10-25T10:00:00.000Z', percent: 81, compacted: false }]), other('worker-1', 30)])
 const mainSwatch = screen.getByRole('link', { name: 'Main' }).previousElementSibling!
 expect(mainSwatch).toHaveClass('context-fill-swatch-main')
 const otherSwatch = screen.getByRole('link', { name: 'Conversation worker-1' }).previousElementSibling!
 expect(otherSwatch).toHaveClass('context-fill-swatch-other')
})

it('shows at most the 5 fullest other conversations, the rest behind "See all"', () => {
 const others = Array.from({ length: 8 }, (_, i) => other(`worker-${i}`, 100 - i))
 renderChart([main([{ at: '2026-10-25T10:00:00.000Z', percent: 50, compacted: false }]), ...others])
 expect(screen.getAllByRole('link').length).toBe(1 /* main */ + 5)
 expect(screen.getByRole('link', { name: 'Conversation worker-0' })).toBeInTheDocument()
 expect(screen.queryByRole('link', { name: 'Conversation worker-7' })).toBeNull()
 const showAll = screen.getByRole('button', { name: 'See all 8' })
 fireEvent.click(showAll)
 expect(screen.getAllByRole('link').length).toBe(1 + 8)
 expect(screen.getByRole('link', { name: 'Conversation worker-7' })).toBeInTheDocument()
})

it('shows a conversation\'s own name, reading and time on hover or focus, and its name opens it', () => {
 renderChart([main([{ at: '2026-10-25T10:00:00.000Z', percent: 81, compacted: false }]), other('worker-1', 44)])
 expect(screen.queryByRole('status')).toBeNull()
 fireEvent.mouseEnter(document.querySelector('.context-fill-series-other')!)
 expect(screen.getByRole('status')).toHaveTextContent('Conversation worker-1: 44% as of Oct 25')
 fireEvent.mouseLeave(document.querySelector('.context-fill-series-other')!)
 expect(screen.queryByRole('status')).toBeNull()
 const link = screen.getByRole('link', { name: 'Conversation worker-1' })
 expect(link).toHaveAttribute('href', '/sessions/worker-1')
 fireEvent.focus(link)
 expect(screen.getByRole('status')).toHaveTextContent('Conversation worker-1: 44% as of Oct 25')
})

it('names the hovered Main line "Main" on screen even though the chart line\'s own tooltip carries its title', () => {
 renderChart([main([{ at: '2026-10-25T10:00:00.000Z', percent: 81, compacted: false }])])
 fireEvent.mouseEnter(document.querySelector('.context-fill-series-main')!)
 expect(screen.getByRole('status')).toHaveTextContent('Main: 81% as of Oct 25')
 expect(document.querySelector('.context-fill-series-main')).toHaveAttribute('aria-label', 'Main — Improve ticket flow: 81% as of Oct 25')
})

it('breaks compaction attribution cleanly when Main has no readings in range', () => {
 renderChart([other('worker-1', 70)])
 expect(screen.getByText('No readings for Main in this period. No compactions were recorded in this period.')).toBeInTheDocument()
 expect(document.querySelector('.context-fill-series-main')).toBeNull()
})

it('shows no reading yet when there is no data at all', () => {
 renderChart([])
 expect(screen.getByText('No reading yet this period.')).toBeInTheDocument()
 expect(document.querySelector('.context-fill-series')).toBeNull()
})
