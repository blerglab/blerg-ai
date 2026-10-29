import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { type ReactNode } from 'react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useTranscriptStore } from '@blerglab/chat'
import CommandPaletteProvider from './CommandPalette'
import RunnerCommands from './RunnerCommands'
import SessionDetail from './SessionDetail'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import * as wsMock from '../test/wsMock'
import { apiFetch } from '../apiFetch'
import { recentSessionIds, rememberSessionVisit } from '../lib/recentSessions'

// A token whose subject is the signed-in account, so visits are recorded for it.
const token = vi.hoisted(() => `x.${btoa(JSON.stringify({ sub: 'person-1' }))}.y`)
vi.mock('../lib/boardLink', async original => ({ ...await original<typeof import('../lib/boardLink')>(), loadBoardOrigin: async () => 'https://board.example.com' }))
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => token, ensureFreshToken: async () => true }))
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn() }))
vi.mock('@blerglab/chat', async original => {
  const chat = await original<typeof import('@blerglab/chat')>()
  return { ...chat, ChatView: ({ slots }: { slots: { header: ReactNode; transcript?: (content: ReactNode) => ReactNode } }) => <div>{slots.header}{slots.transcript?.(null)}</div> }
})
vi.mock('../lib/engineModels', async original => {
  const mod = await original<typeof import('../lib/engineModels')>()
  const models = [
    { id: 'model-next', name: 'Next', description: '', section: 'main', efforts: ['low', 'high'], default_effort: 'low' },
    { id: 'model-big', name: 'Big', description: '', section: 'main', efforts: ['low', 'high'], default_effort: 'low' },
  ]
  return { ...mod, useEngineModels: () => ({ models, loading: false, fallback: false, source: 'live', engineEfforts: ['low', 'high'] }) }
})

const ACCOUNT = 'person-1'
function show(path = '/sessions/sess-a') {
  useSessionStore.setState({
    sessions: [
      makeSession({ kind: 'agent', status: 'idle', title: 'Current', model: 'model-next', effort: 'high' }),
      makeSession({ id: 'alpha', title: 'Alpha work', status: 'running', started_at: '2026-10-01T00:00:00Z' }),
      makeSession({ id: 'beta', title: 'Beta work', status: 'idle', started_at: '2026-10-02T00:00:00Z' }),
      makeSession({ id: 'gamma', title: 'Gamma work', status: 'waiting', started_at: '2026-10-03T00:00:00Z' }),
      makeSession({ id: 'delta', title: 'Delta work', status: 'running', started_at: '2026-10-04T00:00:00Z' }),
      makeSession({ id: 'fresh', title: 'Never opened', status: 'running', started_at: '2026-10-26T00:00:00Z' }),
      makeSession({ id: 'cron-run', title: 'Literary agent', status: 'running', cron_id: 'c1', started_at: '2026-10-27T00:00:00Z' }),
      makeSession({ id: 'stopped', title: 'Education stopped', status: 'stopped', started_at: '2026-10-27T00:00:00Z' }),
      makeSession({ id: 'ended', title: 'Old finished', status: 'ended', ended_at: '2026-10-27T00:00:00Z' }),
    ],
    daemons: [makeDaemon()],
  })
  render(<MemoryRouter initialEntries={[path]}><CommandPaletteProvider>
    <RunnerCommands onNewSession={vi.fn()} />
    <Routes><Route path="/sessions/:id" element={<SessionDetail />} /><Route path="/sessions" element={<div>Session list</div>} /></Routes>
  </CommandPaletteProvider></MemoryRouter>)
}
const dialog = () => within(screen.getByRole('dialog', { name: 'Command palette' }))
function openPalette() {
  // The header button lives in the session view; elsewhere use Ctrl+K.
  const button = screen.queryByRole('button', { name: 'Search commands and files' })
  if (button) fireEvent.click(button)
  else fireEvent.keyDown(window, { key: 'k', ctrlKey: true })
  return screen.getByRole('combobox', { name: /Search commands/ })
}
const groupNames = () => dialog().getAllByRole('group').map(g => g.getAttribute('aria-labelledby') && document.getElementById(g.getAttribute('aria-labelledby')!)?.textContent)
const titles = (name: string) => within(dialog().getByRole('group', { name })).getAllByRole('option').map(o => o.querySelector('.palette-label > span')?.textContent?.trim())

beforeEach(() => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response('{}')))
  wsMock.resetWsMock()
  useTranscriptStore.setState({ sessions: {} })
  localStorage.clear()
  vi.mocked(apiFetch).mockReset().mockImplementation(async () => new Response('{}'))
})

describe('the empty command palette', () => {
  it('leads with this session, then three live sessions the person opened, then commands', () => {
    // Oldest visit first: delta is the most recent.
    for (const id of ['alpha', 'cron-run', 'ended', 'stopped', 'beta', 'gamma', 'delta']) rememberSessionVisit(ACCOUNT, id)
    show()
    openPalette()
    expect(groupNames()).toEqual(['This session', 'Recent sessions', 'Commands'])
    expect(titles('This session')).toEqual(['Quick open session files', 'Switch model…', 'Set effort…', 'Open board', 'Session insights'])
    // The first safe action is the default highlight.
    expect(dialog().getByRole('option', { selected: true })).toHaveTextContent('Quick open session files')
    // Crons, ended and stopped sessions, never-opened ones and the current session are not offered.
    expect(titles('Recent sessions')).toEqual(['Delta work', 'Gamma work', 'Beta work'])
    expect(titles('Commands')).not.toContain('Open board')
    expect(titles('Commands')).toContain('New session')
  })
  it('still finds crons, stopped and ended sessions by search', () => {
    rememberSessionVisit(ACCOUNT, 'alpha')
    show()
    const input = openPalette()
    for (const [query, title] of [['s literary', 'Literary agent'], ['s education', 'Education stopped'], ['s old finished', 'Old finished'], ['s never', 'Never opened']]) {
      fireEvent.change(input, { target: { value: query } })
      expect(within(dialog().getByRole('group', { name: 'Sessions' })).getByRole('option')).toHaveTextContent(title)
      fireEvent.click(dialog().getByRole('button', { name: 'Remove sessions scope' }))
      fireEvent.change(input, { target: { value: '' } })
    }
  })
  it('falls back to open sessions by latest activity when nothing was opened', () => {
    show()
    openPalette()
    expect(titles('Recent sessions')).toEqual(['Never opened', 'Delta work', 'Gamma work'])
  })
  it('records visits so the session just left leads the list next time', async () => {
    rememberSessionVisit(ACCOUNT, 'alpha')
    show()
    expect(recentSessionIds(ACCOUNT)[0]).toBe('sess-a')
    const input = openPalette()
    fireEvent.change(input, { target: { value: 's Beta' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    await waitFor(() => expect(recentSessionIds(ACCOUNT)[0]).toBe('beta'))
    openPalette()
    expect(titles('Recent sessions')).toEqual(['Current', 'Alpha work'])
  })
  it('turns Switch model… into a search for the choices', () => {
    show()
    const input = openPalette()
    fireEvent.click(dialog().getByRole('option', { name: 'Switch model…' }))
    expect(input).toHaveValue('Switch model: ')
    expect(dialog().getAllByRole('option').map(o => o.textContent)).toContain(' Switch model: Big')
    expect(dialog().queryByRole('option', { name: 'Switch model…' })).toBeNull()
    expect(screen.getByRole('dialog', { name: 'Command palette' })).toBeInTheDocument()
  })
  it('omits the This session group outside a session', () => {
    rememberSessionVisit(ACCOUNT, 'alpha')
    show('/sessions')
    openPalette()
    expect(groupNames()).toEqual(['Recent sessions', 'Commands'])
    expect(titles('Recent sessions')).toEqual(['Alpha work'])
    expect(titles('Commands')).toContain('Open board')
    expect(dialog().getByRole('option', { selected: true })).toHaveTextContent('Alpha work')
  })
})
