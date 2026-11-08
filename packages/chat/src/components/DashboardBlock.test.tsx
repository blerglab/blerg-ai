import {render, screen, fireEvent} from '@testing-library/react'
import {expect, it, vi} from 'vitest'
import DashboardBlock from './DashboardBlock'
vi.mock('./VisualBlock', () => ({default: () => <div data-testid="chart" />}))
const data = {kind: 'inline', rows: [{n: 0, text: 'Observed locally', delta: '+2 since yesterday'}]}
const view = {value: 'n', selection: 'single', unit: '%'}
const source = JSON.stringify({format: 'blerg-dashboard', version: 1, title: 'Telemetry', cards: [
  {id: 'stat', kind: 'stat', title: 'Workers', span: 1, data, view: {...view, delta: 'delta', sparkline: 'n'}},
  {id: 'gauge', kind: 'gauge', title: 'Usage', span: 1, data, view: {...view, min: 0, max: 100, thresholds: [80, 95]}},
  {id: 'text', kind: 'text', title: 'Coverage', span: 2, data, view: {value: 'text', selection: 'single'}},
  {id: 'chart', kind: 'chart', title: 'Merges', span: 4, data, spec: {mark: 'bar'}},
]})
it('renders all four card kinds, zero, thresholds, comparison and source', () => {
  const {container} = render(<DashboardBlock source={source} />)
  expect(container.querySelectorAll('.dashboard-card')).toHaveLength(4)
  expect(screen.getByRole('meter')).toHaveAttribute('aria-valuenow', '0')
  expect(screen.getByRole('img', {name: /Workers trend/})).toBeTruthy()
  expect(screen.getByText('+2 since yesterday')).toBeTruthy()
  expect(screen.getByText('Observed locally')).toBeTruthy()
  expect(screen.getByTestId('chart')).toBeTruthy()
  fireEvent.click(screen.getByRole('button', {name: 'Show source'}))
  expect(container.querySelector('pre')).toHaveTextContent('blerg-dashboard')
})
it('logs a safe cause and keeps invalid source available without exposing it', () => {
  const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
  render(<DashboardBlock source='{"secret":"canary-private"}' />)
  expect(screen.getByRole('status')).not.toHaveTextContent('canary-private')
  expect(warn).toHaveBeenCalledWith('dashboard_refused', {cause: 'invalid_document'})
  expect(JSON.stringify(warn.mock.calls)).not.toContain('canary-private')
  warn.mockRestore()
})
it('keeps the last valid drawing when a replacement is refused', () => {
  const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
  const mounted = render(<DashboardBlock source={source} />)
  const card = mounted.container.querySelector('.dashboard-card')
  mounted.rerender(<DashboardBlock source='{"version":0}' />)
  expect(screen.getByRole('status')).toHaveTextContent('Dashboard refused')
  expect(mounted.container.querySelector('.dashboard-card')).toBe(card)
  warn.mockRestore()
})
