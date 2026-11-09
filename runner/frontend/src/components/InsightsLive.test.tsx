import { act, fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, expect, it, vi } from 'vitest'
import { InsightsDashboard } from './Insights'
import { richInsights } from '../test/insightsFixture'
import { emptyActivity } from '../test/activityFixture'
import InsightsTable from './InsightsTable'

const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks() })
it('refreshes Overview while visible and suspends reads for a hidden panel or browser tab', async () => {
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'setTimeout', 'clearTimeout'] })
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  apiFetch.mockImplementation(async (url:string) => ({ ok: true, json: async () => url.includes('/activity') ? emptyActivity : richInsights }))
  const view = render(<MemoryRouter><InsightsDashboard /></MemoryRouter>)
  await act(async () => {})
  const reads = () => apiFetch.mock.calls.filter(([url]) => url.startsWith('/api/insights?')).length
  expect(reads()).toBe(1)
  await act(async () => { vi.advanceTimersByTime(30_000) })
  expect(reads()).toBe(2)
  view.rerender(<MemoryRouter><InsightsDashboard active={false} /></MemoryRouter>)
  await act(async () => { vi.advanceTimersByTime(60_000) })
  expect(reads()).toBe(2)
  view.rerender(<MemoryRouter><InsightsDashboard /></MemoryRouter>)
  await act(async () => {})
  expect(reads()).toBe(3)
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden')
  fireEvent(document, new Event('visibilitychange'))
  await act(async () => { vi.advanceTimersByTime(60_000) })
  expect(reads()).toBe(3)
})
it('sorts numeric columns by their raw values and announces the direction', () => {
  render(<InsightsTable><thead><tr><th>Model</th><th>Output</th></tr></thead><tbody>
    <tr data-sort={['A', 2000]}><td>A</td><td>2k</td></tr>
    <tr data-sort={['B', 10000]}><td>B</td><td>10k</td></tr>
  </tbody></InsightsTable>)
  fireEvent.click(screen.getByRole('button', { name: 'Output' }))
  expect(screen.getAllByRole('row')[1]).toHaveTextContent('A')
  expect(screen.getByRole('columnheader', { name: 'Output ↑' })).toHaveAttribute('aria-sort', 'ascending')
  fireEvent.click(screen.getByRole('button', { name: 'Output ↑' }))
  expect(screen.getAllByRole('row')[1]).toHaveTextContent('B')
})

it('keeps the report, geometry and scroll while a refresh is pending or fails, and redraws cached data on remount', async () => {
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
  apiFetch.mockImplementation(async (url:string) => ({ ok: true, json: async () => url.includes('/activity') ? emptyActivity : richInsights }))
  const mount = () => render(<MemoryRouter><InsightsDashboard workspaceId="quiet-refresh" /></MemoryRouter>)
  const view = mount()
  await act(async () => {})
  const tile = screen.getByTestId('question-health')
  const shell = tile.closest('.insights-tab')!
  shell.scrollTop = 120
  let reject!: (e: Error) => void
  apiFetch.mockImplementation(() => new Promise((_, fail) => { reject = fail }))
  await act(async () => { vi.advanceTimersByTime(30_000) })
  expect(screen.getByTestId('question-health')).toBe(tile)
  expect(screen.queryByText('Updating…')).toBeNull()
  expect(tile.closest('[style*="opacity"]')).toBeNull()
  expect(shell.scrollTop).toBe(120)
  await act(async () => { reject(new Error('offline')) })
  expect(screen.getByTestId('question-health')).toBe(tile)
  view.unmount()
  mount()
  expect(screen.getByTestId('question-health')).toHaveTextContent('Are my agents working well?')
  // The chart keeps an empty live region for focused/tapped values. Refresh
  // must not replace cached content with an announced loading message.
  expect(screen.getByTestId('question-health').querySelector('[role="status"]')).toBeEmptyDOMElement()
})
