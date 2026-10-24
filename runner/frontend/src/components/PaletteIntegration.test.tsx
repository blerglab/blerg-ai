import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { type ReactNode } from 'react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useTranscriptStore } from '@blerglab/chat'
import CommandPaletteProvider from './CommandPalette'
import RunnerCommands from './RunnerCommands'
import SessionDetail from './SessionDetail'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import * as wsMock from '../test/wsMock'
import { settingsIsOpen, setSettingsOpen } from '../lib/settingsDialog'
import { RAIL_PREF_KEY } from '../lib/sessionRail'
import { apiFetch } from '../apiFetch'
vi.mock('../lib/boardLink', async original => ({ ...await original<typeof import('../lib/boardLink')>(), loadBoardOrigin: async () => 'https://board.example.com' }))
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => 'fixture-token', ensureFreshToken: async () => true }))
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn() }))
vi.mock('@blerglab/chat', async original => ({
  ...await original<typeof import('@blerglab/chat')>(),
  ChatView: ({ slots }: { slots: { header: ReactNode } }) => <div>{slots.header}</div>,
}))
vi.mock('../lib/engineModels', async original => {
  const mod = await original<typeof import('../lib/engineModels')>()
  const models = [
    { id: 'model-next', name: 'Next', description: '', section: 'main', efforts: ['low', 'high'], default_effort: 'low' },
    { id: 'model-big', name: 'Big', description: '', section: 'main', efforts: ['low', 'high'], default_effort: 'low' },
  ]
  return { ...mod, useEngineModels: () => ({ models, loading: false, fallback: false, source: 'live', engineEfforts: ['low', 'high'] }) }
})
const viewerFetch = vi.fn()
function show(over = {}) {
  useSessionStore.setState({ sessions: [makeSession({ kind: 'agent', status: 'idle', title: 'Current', model: 'model-next', effort: 'high', ...over }), makeSession({ id: 'other', title: 'Other work' })], daemons: [makeDaemon()] })
  const onNew = vi.fn()
  render(<MemoryRouter initialEntries={['/sessions/sess-a']}><CommandPaletteProvider>
    <RunnerCommands onNewSession={onNew} />
    <Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes>
  </CommandPaletteProvider></MemoryRouter>)
  return onNew
}
function paletteResults() { return within(screen.getByRole('dialog', { name: /Command palette|Quick open/ })) }
function command(query: string) {
  fireEvent.click(screen.getByRole('button', { name: 'Search commands and files' }))
  const input = screen.getByRole('combobox', { name: /Search commands|Search session files/ })
  fireEvent.change(input, { target: { value: `>${query}` } })
  return input
}
function choose(query: string) { fireEvent.keyDown(command(query), { key: 'Enter' }) }
beforeEach(() => {
  viewerFetch.mockReset().mockImplementation(async () => new Response('{}'))
  vi.stubGlobal('fetch', viewerFetch)
  wsMock.resetWsMock()
  useTranscriptStore.setState({ sessions: {} })
  localStorage.clear()
  setSettingsOpen(false)
  vi.mocked(apiFetch).mockReset().mockImplementation(async () => new Response('{}'))
})
afterEach(() => vi.unstubAllGlobals())
describe('runner palette feature registrations', () => {
  it('opens the existing End confirmation and requires its affirmative action before DELETE', async () => {
    show()
    choose('End this session')
    const confirm = screen.getByRole('alertdialog', { name: 'End this session?' })
    expect(vi.mocked(apiFetch).mock.calls.some(([, init]) => init?.method === 'DELETE')).toBe(false)
    fireEvent.click(within(confirm).getByRole('button', { name: 'End session' }))
    await waitFor(() => expect(apiFetch).toHaveBeenCalledWith('/api/sessions/sess-a', { method: 'DELETE' }))
  })
  it('keeps the existing End confirmation in charge of shortcuts, focus and Esc', async () => {
    show()
    const end = screen.getByRole('button', { name: 'End session' })
    fireEvent.click(end)
    const confirm = screen.getByRole('alertdialog', { name: 'End this session?' })
    const cancel = within(confirm).getByRole('button', { name: 'Cancel' })
    await waitFor(() => expect(cancel).toHaveFocus())
    for (const key of ['k', 'p']) for (const meta of [false, true]) {
      fireEvent.keyDown(cancel, { key, ctrlKey: !meta, metaKey: meta })
      expect(screen.queryByRole('dialog', { name: /Command palette|Quick open/ })).toBeNull()
      expect(cancel).toHaveFocus()
    }
    // Entry through the shared context (including the phone button) has the same guard.
    fireEvent.click(screen.getByRole('button', { name: 'Search commands and files', hidden: true }))
    expect(screen.queryByRole('dialog', { name: /Command palette|Quick open/ })).toBeNull()
    fireEvent.keyDown(cancel, { key: 'Tab' })
    expect(within(confirm).getByRole('button', { name: 'End session' })).toHaveFocus()
    fireEvent.keyDown(document.activeElement!, { key: 'Escape' })
    expect(screen.queryByRole('alertdialog')).toBeNull()
    expect(end).toHaveFocus()
    expect(vi.mocked(apiFetch).mock.calls.some(([, init]) => init?.method === 'DELETE')).toBe(false)
  })
  it('offers other sessions but omits the current session, updating after navigation', () => {
    show()
    let input = command('Switch to session:')
    expect(paletteResults().getAllByRole('option')).toHaveLength(1)
    expect(paletteResults().getByRole('option')).toHaveTextContent('Switch to session: Other work')
    fireEvent.keyDown(input, { key: 'Enter' })
    input = command('Switch to session:')
    const rows = paletteResults().getAllByRole('option')
    expect(rows).toHaveLength(1)
    expect(rows[0]).toHaveTextContent('Switch to session: Current')
    expect(rows[0]).not.toHaveTextContent('Other work')
    fireEvent.change(input, { target: { value: '>Quick open session files' } })
    expect(paletteResults().getByRole('option').querySelector('kbd')).toHaveTextContent('Ctrl/⌘+P')
  })
  it('reveals a hidden panel, selects its real Files tab, and does not advertise an absent shell', async () => {
    localStorage.setItem(RAIL_PREF_KEY, '0')
    show()
    const input = command('Show Files panel')
    expect(paletteResults().getByRole('option')).toHaveTextContent('Show Files panel')
    fireEvent.keyDown(input, { key: 'Enter' })
    await waitFor(() => expect(screen.getByRole('tab', { name: /Files/ })).toHaveAttribute('aria-selected', 'true'))
    expect(screen.getByRole('tab', { name: /Files/ })).toHaveFocus()
    fireEvent.change(command('shell'), { target: { value: '>shell' } })
    expect(paletteResults().queryAllByRole('option')).toHaveLength(0)
  })
  it('registers Shell only after the owner capability reply and opens the dedicated shell path', async () => {
    vi.mocked(apiFetch).mockImplementation(async (url, init) => init?.method === 'POST' ? new Response('{}', { status: 503 }) : new Response(JSON.stringify(String(url).endsWith('/shell-capability') ? { available: true } : [])))
    show()
    command('shell')
    await waitFor(() => expect(paletteResults().getByRole('option', { name: /Shell/ })).toBeInTheDocument())
    fireEvent.keyDown(screen.getByRole('combobox', { name: /Search commands/ }), { key: 'Enter' })
    await waitFor(() => expect(apiFetch).toHaveBeenCalledWith('/api/sessions/sess-a/shells', expect.objectContaining({ method: 'POST' })))
    expect(wsMock.send.mock.calls.some(([frame]) => frame.type === 'send_input')).toBe(false)
  })
  it('uses engine catalog models and efforts with the existing websocket actions', () => {
    show()
    choose('Change effort: low')
    expect(wsMock.send).toHaveBeenCalledWith({ type: 'set_session_model', session_id: 'sess-a', effort: 'low' })
    choose('Change model: Big')
    expect(wsMock.send).toHaveBeenCalledWith({ type: 'set_session_model', session_id: 'sess-a', model: 'model-big', effort: 'high' })
  })
  it('does not offer the model or effort the session already uses', () => {
    show()
    const input = command('Change ')
    const titles = paletteResults().queryAllByRole('option').map(o => o.textContent ?? '')
    expect(titles.some(t => t.includes('Change model: Big'))).toBe(true)
    expect(titles.some(t => t.includes('Change effort: low'))).toBe(true)
    expect(titles.some(t => t.includes('Change model: Next'))).toBe(false)
    expect(titles.some(t => t.includes('Change effort: high'))).toBe(false)
    fireEvent.change(input, { target: { value: '' } })
  })
  it('does not register mutable model or End commands for an archived session', () => {
    show({ archived_at: '2026-10-23T00:00:00Z' })
    const input = command('Change model')
    expect(paletteResults().queryAllByRole('option')).toHaveLength(0)
    fireEvent.change(input, { target: { value: '>End this session' } })
    expect(paletteResults().queryAllByRole('option')).toHaveLength(0)
  })
  it('opens Settings and New session and switches to another session through registered actions', () => {
    const onNew = show()
    choose('Open Settings')
    expect(settingsIsOpen()).toBe(true)
    choose('Start a new session')
    expect(onNew).toHaveBeenCalledTimes(1)
    choose('Switch to session: Other work')
    expect(screen.getByText('Other work')).toBeInTheDocument()
  })
  it('discovers real accessible boards and resolves card-number commands without the legacy board API', async () => {
    vi.mocked(apiFetch).mockImplementation(async path => new Response(JSON.stringify(String(path) === '/api/session-boards' ? { configured: true, boards: [{ id: 'b-1', name: 'Design' }] } : {})))
    show()
    const input = command('card #172')
    await waitFor(() => expect(paletteResults().getByRole('option')).toHaveTextContent('Open card #172 on board Design'))
    expect(apiFetch).toHaveBeenCalledWith('/api/session-boards', expect.objectContaining({ signal: expect.any(AbortSignal) }))
    expect(vi.mocked(apiFetch).mock.calls.some(([url]) => String(url) === '/api/boards')).toBe(false)
    fireEvent.keyDown(input, { key: 'Escape' })
  })
  it('opens a P4 search result through the existing authorized workspace file viewer', async () => {
    vi.mocked(apiFetch).mockImplementation(async path => new Response(JSON.stringify(String(path).includes('/workspace?') ? {
      result: { ok: true, generation: 1, launch_generation: 'launch', entries: [{ path: 'app/main.ts', name: 'main.ts', directory: false }] }, members: [],
    } : {})))
    // The viewer uses the regular transport (fetch), independently of P4's candidate search.
    const fetch = viewerFetch.mockResolvedValue(new Response(JSON.stringify({ path: 'app/main.ts', view: 'text', content_type: 'text/plain', content_b64: btoa('fixture bytes') })))
    vi.stubGlobal('fetch', fetch)
    show()
    act(() => { window.dispatchEvent(new KeyboardEvent('keydown', { key: 'p', ctrlKey: true, bubbles: true, cancelable: true })) })
    fireEvent.change(screen.getByRole('combobox', { name: /Search commands|Search session files/ }), { target: { value: 'mn' } })
    await waitFor(() => expect(paletteResults().getByRole('option')).toHaveTextContent('app/main.ts'))
    fireEvent.keyDown(screen.getByRole('combobox', { name: /Search commands|Search session files/ }), { key: 'Enter' })
    await waitFor(() => expect(fetch.mock.calls.some(([url]) => String(url).includes('/workspace-file?path=app%2Fmain.ts'))).toBe(true))
    expect(screen.getByRole('dialog', { name: /main.ts/ })).toBeInTheDocument()
    await waitFor(() => expect(screen.getByText('fixture bytes')).toBeInTheDocument())
  })
})
