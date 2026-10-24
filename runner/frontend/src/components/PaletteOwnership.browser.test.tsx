import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { page, userEvent } from 'vitest/browser'
import { useTranscriptStore } from '@blerglab/chat'
import CommandPaletteProvider, { PaletteButton } from './CommandPalette'
import RunnerCommands from './RunnerCommands'
import SessionDetail from './SessionDetail'
import TerminalDOMView from './TerminalDOMView'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import * as wsMock from '../test/wsMock'
import { apiFetch } from '../apiFetch'
import '../index.css'
import '@blerglab/chat/blerg.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => 'fixture-token', ensureFreshToken: async () => true }))
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn() }))
vi.mock('../lib/boardLink', async original => ({ ...await original<typeof import('../lib/boardLink')>(), loadBoardOrigin: async () => 'https://board.example.com' }))
vi.mock('../lib/engineModels', async original => ({
  ...await original<typeof import('../lib/engineModels')>(),
  useEngineModels: () => ({ models: [{ id: 'model-next', name: 'Next', efforts: ['low', 'high'], default_effort: 'low' }], loading: false, fallback: false, source: 'live', engineEfforts: ['low', 'high'] }),
}))
const shotDir = (import.meta.env as Record<string, string | undefined>).VITE_PALETTE_SHOT_DIR
beforeEach(() => {
  document.documentElement.setAttribute('data-theme', 'dark')
  wsMock.resetWsMock()
  localStorage.clear()
  useTranscriptStore.setState({ sessions: {} })
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [
    makeSession({ kind: 'agent', status: 'idle', model: 'model-next', effort: 'high' }),
    makeSession({ id: 'other', title: 'Review the interface' }),
  ] })
  vi.mocked(apiFetch).mockReset().mockImplementation(async url => new Response(JSON.stringify(String(url) === '/api/session-boards' ? { boards: [{ id: 'design', name: 'Design' }] } : {})))
  vi.stubGlobal('fetch', vi.fn(async () => new Response('{}')))
})
afterEach(() => { vi.unstubAllGlobals(); document.documentElement.removeAttribute('data-theme') })
function mountSession() {
  render(<MemoryRouter initialEntries={['/sessions/sess-a']}><CommandPaletteProvider>
    <RunnerCommands onNewSession={() => {}} />
    <div style={{ height: '100vh' }}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></div>
  </CommandPaletteProvider></MemoryRouter>)
}
function sentInput() {
  return wsMock.send.mock.calls.flatMap(([message]) => message.type === 'send_input' ? [atob(message.data)] : []).join('')
}
for (const [width, height] of [[390, 844], [3840, 2160]]) describe(`palette shortcut ownership at ${width}px`, () => {
  beforeEach(async () => { await page.viewport(width, height) })
  it('delivers Ctrl+K and Ctrl+P to the real terminal and preserves typing and composition', async () => {
    const { container } = render(<MemoryRouter initialEntries={['/sessions/sess-a']}><CommandPaletteProvider>
      <PaletteButton /><div style={{ height: 400 }}><TerminalDOMView sessionId="sess-a" /></div>
    </CommandPaletteProvider></MemoryRouter>)
    const terminal = container.querySelector('textarea')!
    terminal.focus()
    await userEvent.keyboard('x{Control>}k{/Control}{Control>}p{/Control}y')
    expect(sentInput()).toBe('x\x0b\x10y')
    expect(document.activeElement).toBe(terminal)
    expect(screen.queryByRole('dialog')).toBeNull()
    // Composition events are synthetic: Chromium's keyboard API cannot drive an OS IME.
    fireEvent.compositionStart(terminal)
    fireEvent.keyDown(terminal, { key: 'k', ctrlKey: true, isComposing: true })
    fireEvent.input(terminal, { target: { value: 'ka' }, data: 'ka', inputType: 'insertCompositionText', isComposing: true })
    fireEvent.compositionEnd(terminal)
    expect(sentInput()).toBe('x\x0b\x10yka')
    expect(screen.queryByRole('dialog')).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: 'Search commands and files' }))
    expect(document.activeElement).toBe(screen.getByRole('combobox'))
  })
  it('keeps the real End confirmation focused through palette shortcuts and dismisses it with Esc', async () => {
    mountSession()
    const end = screen.getByRole('button', { name: 'End session' })
    await userEvent.click(end)
    const dialog = screen.getByRole('alertdialog', { name: 'End this session?' })
    const cancel = within(dialog).getByRole('button', { name: 'Cancel' })
    await waitFor(() => expect(document.activeElement).toBe(cancel))
    await userEvent.keyboard('{Control>}k{/Control}{Control>}p{/Control}{Meta>}k{/Meta}{Meta>}p{/Meta}')
    expect(screen.queryByRole('dialog', { name: /Command palette|Quick open/ })).toBeNull()
    expect(document.activeElement).toBe(cancel)
    await userEvent.keyboard('{Tab}')
    expect(document.activeElement).toBe(within(dialog).getByRole('button', { name: 'End session' }))
    await userEvent.keyboard('{Shift>}{Tab}{/Shift}{Escape}')
    expect(screen.queryByRole('alertdialog')).toBeNull()
    expect(screen.queryByRole('dialog', { name: /Command palette|Quick open/ })).toBeNull()
    expect(document.activeElement).toBe(end)
    expect(vi.mocked(apiFetch).mock.calls.some(([, init]) => init?.method === 'DELETE')).toBe(false)
    await userEvent.keyboard('{Control>}k{/Control}')
    expect(document.activeElement).toBe(screen.getByRole('combobox', { name: 'Search commands' }))
  })
  it('shows the actual session commands with real shortcuts and only other sessions', async () => {
    mountSession()
    const entry = screen.getByRole('button', { name: 'Search commands and files' })
    if (shotDir && width === 390) await page.screenshot({ path: `${shotDir}/phone-search-entry-390x844.png` })
    entry.focus()
    await userEvent.keyboard('{Control>}k{/Control}')
    const dialog = screen.getByRole('dialog', { name: 'Command palette' })
    await waitFor(() => expect(within(dialog).getByRole('option', { name: /Open board: Design/ })).toBeTruthy())
    expect(within(dialog).queryByRole('option', { name: /Switch to session: Build the widget/ })).toBeNull()
    expect(within(dialog).getByRole('option', { name: /Switch to session: Review the interface/ })).toBeTruthy()
    const shortcuts = dialog.querySelectorAll('kbd')
    expect(shortcuts).toHaveLength(1)
    expect(shortcuts[0].textContent).toBe('Ctrl/⌘+P')
    const box = dialog.getBoundingClientRect()
    expect(box.width).toBeGreaterThan(width === 390 ? 380 : 1900)
    expect(box.right).toBeLessThanOrEqual(width)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    if (shotDir) await page.screenshot({ path: `${shotDir}/palette-${width}x${height}.png` })
    await userEvent.keyboard('{Escape}{Control>}p{/Control}')
    const input = screen.getByRole('combobox', { name: 'Search session files' })
    await userEvent.keyboard('>')
    expect(input.getAttribute('aria-label')).toBe('Search commands')
    await waitFor(() => expect(within(screen.getByRole('dialog', { name: 'Command palette' })).getByRole('option', { name: /Open board: Design/ })).toBeTruthy())
    if (shotDir && width === 390) await page.screenshot({ path: `${shotDir}/quick-open-commands-390x844.png` })
  })
})
