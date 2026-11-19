// Audit through the shipped app's routes and controls, with deterministic service responses.
import { afterEach, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import App from './App'
import { parityBoard, parityCards, parityColumns } from '../../../packages/board/parityFixture'
import './styles.css'

vi.mock('./api', async original => ({ ...await original<typeof import('./api')>(), boardSocket: () => () => {}, api: vi.fn(async (path: string) => {
  if (path === '/api/me') return { kind: 'human', is_admin: true, is_human: true, account_id: 'audit-person', capabilities: ['card.write'] }
  if (path === '/api/me/theme') return { theme: 'dark' }
  if (path === '/api/version') return { release: '0.5.0', commit: 'fixture' }
  if (path === '/api/boards') return [parityBoard]
  if (path === '/api/boards/parity') return { ...parityBoard, key: 'DEMO', automation_token_set: false }
  if (path.endsWith('/columns')) return parityColumns
  if (path.includes('/cards/view')) return { cards: parityCards, older: {} }
  if (path.endsWith('/releases')) return [{ id: 'r1', name: 'Autumn release', status: 'planned' }]
  if (path.endsWith('/run')) return { state: 'stopped', ready: 0, blocked: 0, in_flight: 0 }
  if (path.endsWith('/run-targets')) return { can_choose: false, targets: [] }
  if (path.endsWith('/launch-options')) return { state: 'known', engines: [] }
  return []
}) }))
afterEach(() => { cleanup(); history.replaceState({}, '', '/'); localStorage.clear() })
for (const width of [390, 3840]) it(`audits board navigation and settings at ${width}px`, async () => {
  await page.viewport(width, 1000)
  localStorage.setItem('blerg-theme', 'dark')
  document.documentElement.dataset.theme = 'dark'
  history.replaceState({}, '', '/')
  const host = document.createElement('div'); host.id = 'root'; document.body.append(host)
  render(<App />, { container: host })
  await userEvent.click(await screen.findByRole('link', { name: /Release work/ }))
  await screen.findByText(parityCards[0].title)
  await screen.findByRole('option', { name: 'Autumn release' })
  const shots = import.meta.env.VITE_AUDIT_SHOTS_DIR
  if (shots) await page.screenshot({ element: document.getElementById('root')!, path: `${shots}/board-${width}.png` })
  if (!import.meta.env.VITE_AUDIT_BEFORE) {
    expect(screen.getByLabelText('Filter by release').getBoundingClientRect().width).toBeLessThan(240)
    expect(screen.getByLabelText('Filter by release').closest('label')!.getBoundingClientRect().height).toBeLessThan(50)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  }
  await userEvent.click(screen.getByRole('button', { name: /^Board settings/ }))
  await screen.findByRole('dialog', { name: 'Board settings' })
  if (shots) await page.screenshot({ element: document.getElementById('root')!, path: `${shots}/board-settings-${width}.png` })
  if (!import.meta.env.VITE_AUDIT_BEFORE) {
    expect(screen.getByRole('button', { name: 'Save key' }).getBoundingClientRect().width).toBeLessThan(200)
    expect(screen.getByRole('textbox', { name: 'Board name' }).getBoundingClientRect().width).toBeLessThanOrEqual(480)
  }
})
