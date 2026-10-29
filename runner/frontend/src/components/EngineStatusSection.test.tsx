import { afterEach, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import EngineStatusSection from './EngineStatusSection'
import { clearEngineNotes, parseEngineRows } from '../lib/engineStatus'
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
afterEach(() => { vi.resetAllMocks(); clearEngineNotes() })

const desktops = [
  { id: 'desk-a', name: 'Workstation', engine_statuses: [
    { engine: 'claude', status: 'available' },
    { engine: 'codex', status: 'not_logged_in', reason: 'Codex is installed on Workstation but not logged in.', fix: 'Run `codex login` on Workstation, then Check again.', action: 'check_again' },
  ] },
  { id: 'desk-b', name: 'Laptop' },
]

it('lists every engine per desktop with its reason and fix, and an older daemon says to update', async () => {
  apiFetch.mockResolvedValue(new Response(JSON.stringify(desktops)))
  render(<EngineStatusSection />)
  const a = await screen.findByTestId('engines-desk-a')
  expect(within(a).getByTestId('engine-status-claude')).toHaveTextContent('Ready')
  const codex = within(a).getByTestId('engine-status-codex')
  expect(codex).toHaveTextContent('Not logged in')
  expect(codex).toHaveTextContent('Codex is installed on Workstation but not logged in.')
  expect(within(codex).getByText('codex login').tagName).toBe('CODE')
  expect(within(screen.getByTestId('engines-desk-b')).getByText(/older and reports only the engines that work/)).toBeInTheDocument()
})

it('Check again and Re-detect PATH ask the desktop and show the fresh result', async () => {
  apiFetch.mockImplementation((path: string, init?: RequestInit) => {
    if (path === '/api/daemons') return Promise.resolve(new Response(JSON.stringify(desktops)))
    const body = JSON.parse(String(init?.body)) as { redetect_path: boolean }
    return Promise.resolve(new Response(JSON.stringify({ available_engines: ['claude', 'codex'], path_added: body.redetect_path ? ['/opt/homebrew/bin'] : [],
      engine_statuses: [{ engine: 'claude', status: 'available' }, { engine: 'codex', status: 'available' }] })))
  })
  render(<EngineStatusSection />)
  const a = await screen.findByTestId('engines-desk-a')
  fireEvent.click(within(a).getByRole('button', { name: 'Check again' }))
  await waitFor(() => expect(apiFetch).toHaveBeenCalledWith('/api/daemons/desk-a/engine-check', expect.objectContaining({ method: 'POST', body: JSON.stringify({ redetect_path: false }) })))
  expect(await within(a).findByText('Ready: Claude, Codex.')).toBeInTheDocument()
  expect(within(within(a).getByTestId('engine-status-codex')).getByText('Ready')).toBeInTheDocument()
  fireEvent.click(within(a).getByRole('button', { name: 'Re-detect PATH' }))
  expect(await within(a).findByText(/Added \/opt\/homebrew\/bin to the daemon’s PATH/)).toBeInTheDocument()
})

it('a desktop that cannot answer says why', async () => {
  apiFetch.mockImplementation((path: string) => Promise.resolve(path === '/api/daemons'
    ? new Response(JSON.stringify(desktops))
    : new Response(JSON.stringify({ error: 'the desktop did not answer; update its daemon' }), { status: 504 })))
  render(<EngineStatusSection />)
  const a = await screen.findByTestId('engines-desk-a')
  fireEvent.click(within(a).getByRole('button', { name: 'Check again' }))
  expect(await within(a).findByText('the desktop did not answer; update its daemon')).toBeInTheDocument()
})

it('bounds and validates server rows', () => {
  const rows = parseEngineRows([
    { id: 'codex', status: 'not_installed', reason: 'x'.repeat(5000), action: 'rm -rf', searched_path: Array(100).fill('/bin'), daemon_id: 'd' },
    { id: 'codex', status: 'available' },
    { id: 'Bad Id', status: 'available' },
    { engine: 'hermes', status: '' },
    null,
  ], { codex: 'Codex' })
  expect(rows).toHaveLength(1)
  expect(rows[0].name).toBe('Codex')
  expect(rows[0].reason).toHaveLength(600)
  expect(rows[0].action).toBeUndefined()
  expect(rows[0].searched_path).toHaveLength(32)
  expect(parseEngineRows('nope')).toEqual([])
})
