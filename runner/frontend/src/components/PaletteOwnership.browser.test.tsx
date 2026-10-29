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
    await userEvent.keyboard('{Meta>}k{/Meta}{Meta>}p{/Meta}')
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
  it('opens from the real composer and ordinary fields, cancels browser defaults, and restores drafts and focus', async () => {
    mountSession()
    const composer = document.querySelector<HTMLTextAreaElement>('.agent-composer textarea')!
    expect(composer).toBeTruthy()
    fireEvent.change(composer, { target: { value: '' } })
    const fields = document.createElement('div')
    fields.innerHTML = '<textarea aria-label="Notes"></textarea><input aria-label="Setting"><input type="search" aria-label="Search"><div contenteditable="true" role="textbox" aria-label="Rich text"></div>'
    document.body.append(fields)
    const controls = [composer, ...fields.querySelectorAll<HTMLElement>('textarea, input, [contenteditable]')]
    const shortcuts: KeyboardEvent[] = []
    const record = (event: KeyboardEvent) => { if ((event.ctrlKey || event.metaKey) && /^[kp]$/i.test(event.key)) shortcuts.push(event) }
    window.addEventListener('keydown', record, true)
    try {
      for (const control of controls) {
        control.focus()
        await userEvent.keyboard('draft')
        for (const value of ['k', 'p']) for (const modifier of ['Control', 'Meta']) {
          shortcuts.length = 0
          await userEvent.keyboard(`{${modifier}>}${value}{/${modifier}}`)
          expect(shortcuts.at(-1)?.defaultPrevented).toBe(true)
          const dialog = screen.getByRole('dialog', { name: value === 'k' ? 'Command palette' : 'Quick open' })
          expect(document.activeElement).toBe(within(dialog).getByRole('combobox'))
          expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
          await userEvent.keyboard('{Escape}')
          expect(document.activeElement).toBe(control)
          expect(control instanceof HTMLInputElement || control instanceof HTMLTextAreaElement ? control.value : control.textContent).toBe('draft')
        }
      }
      composer.focus()
      fireEvent.compositionStart(composer)
      for (const key of ['k', 'p']) for (const meta of [false, true]) {
        const event = new KeyboardEvent('keydown', { key, ctrlKey: !meta, metaKey: meta, isComposing: true, bubbles: true, cancelable: true })
        composer.dispatchEvent(event)
        expect(event.defaultPrevented).toBe(false)
        expect(screen.queryByRole('dialog')).toBeNull()
        expect(document.activeElement).toBe(composer)
      }
      fireEvent.compositionEnd(composer)
    } finally { window.removeEventListener('keydown', record, true); fields.remove() }
  })
  it('reserves the composer palette shortcut before a local editing binding', async () => {
    mountSession()
    const composer = document.querySelector<HTMLTextAreaElement>('.agent-composer textarea')!
    await userEvent.fill(composer, 'Unsent draft')
    const localBinding = vi.fn((event: KeyboardEvent) => {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault()
        event.stopPropagation()
      }
    })
    composer.addEventListener('keydown', localBinding)
    try {
      for (const modifier of ['Control', 'Meta']) {
        composer.focus()
        localBinding.mockClear()
        const events: KeyboardEvent[] = []
        const record = (event: KeyboardEvent) => { if (event.key === 'k') events.push(event) }
        window.addEventListener('keydown', record, true)
        await userEvent.keyboard(`{${modifier}>}k{/${modifier}}`)
        window.removeEventListener('keydown', record, true)
        expect(screen.getByRole('dialog', { name: 'Command palette' })).toBeTruthy()
        expect(events[0].defaultPrevented).toBe(true)
        expect(localBinding.mock.calls.some(([event]) => event.key === 'k')).toBe(false)
        await userEvent.keyboard('{Escape}')
        expect(composer).toHaveFocus()
        expect(composer.value).toBe('Unsent draft')
      }
    } finally { composer.removeEventListener('keydown', localBinding) }
  })
  it('leaves terminal and code-editor shortcuts to their focused surfaces', async () => {
    render(<MemoryRouter initialEntries={['/sessions/sess-a']}><CommandPaletteProvider><PaletteButton /></CommandPaletteProvider></MemoryRouter>)
    const surface = document.createElement('div')
    const input = document.createElement('textarea')
    surface.append(input)
    document.body.append(surface)
    try {
      for (const kind of ['xterm', 'monaco-editor', 'cm-editor', 'CodeMirror', 'ace_editor', 'opt-out']) {
        surface.className = kind
        if (kind === 'opt-out') surface.dataset.paletteShortcuts = 'ignore'
        input.focus()
        for (const key of ['k', 'p']) for (const meta of [false, true]) {
          const event = new KeyboardEvent('keydown', { key, ctrlKey: !meta, metaKey: meta, bubbles: true, cancelable: true })
          input.dispatchEvent(event)
          expect(event.defaultPrevented).toBe(false)
          expect(document.activeElement).toBe(input)
          expect(screen.queryByRole('dialog')).toBeNull()
        }
      }
    } finally { surface.remove() }
  })
  it('keeps the real End confirmation focused through palette shortcuts and dismisses it with Esc', async () => {
    mountSession()
    const end = screen.getByRole('button', { name: width === 390 ? 'Search commands and files' : 'End session' })
    await userEvent.click(end)
    if (width === 390) { await userEvent.fill(screen.getByRole('combobox', { name: /^Search commands/ }), '>End session'); await userEvent.keyboard('{ArrowDown}{Enter}') }
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
    expect(document.activeElement).toBe(screen.getByRole('combobox', { name: /^Search commands/ }))
  })
  it('shows the actual session commands with real shortcuts and only other sessions', async () => {
    mountSession()
    const entry = screen.getByRole('button', { name: 'Search commands and files' })
    if (shotDir && width === 390) await page.screenshot({ path: `${shotDir}/phone-search-entry-390x844.png` })
    entry.focus()
    await userEvent.keyboard('{Control>}k{/Control}')
    const dialog = screen.getByRole('dialog', { name: 'Command palette' })
    // The empty palette leads with this session's actions; only quick open has a shortcut there.
    expect(within(within(dialog).getByRole('group', { name: 'This session' })).getByRole('option', { name: 'Open board' })).toBeTruthy()
    expect(within(dialog).queryByRole('option', { name: /Build the widget/ })).toBeNull()
    expect(within(dialog).getByRole('option', { name: /Review the interface/ })).toBeTruthy()
    expect([...dialog.querySelectorAll('kbd')].map(key => key.textContent)).toEqual(['Ctrl/⌘+P'])
    const box = dialog.getBoundingClientRect()
    expect(box.width).toBeGreaterThan(width === 390 ? 380 : 1900)
    expect(box.right).toBeLessThanOrEqual(width)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    if (shotDir) await page.screenshot({ path: `${shotDir}/palette-${width}x${height}.png` })
    fireEvent.change(within(dialog).getByRole('combobox'), { target: { value: '>' } })
    // The label carries a count that can grow while commands load, so click by pattern, not by
    // the element's name at one moment.
    await page.getByRole('dialog', { name: 'Command palette' }).getByRole('option', { name: /More commands/ }).click()
    expect([...dialog.querySelectorAll('kbd')].map(key => key.textContent)).toEqual(expect.arrayContaining(['Ctrl/⌘+P', 'Esc / Alt+1', 'Alt+]', 'Alt+[']))
    await userEvent.keyboard('{Escape}{Control>}p{/Control}')
    const input = screen.getByRole('combobox', { name: 'Search session files' })
    await userEvent.keyboard('>Open board: Design')
    expect(input.getAttribute('aria-label')).toBe('Search commands')
    await waitFor(() => expect(within(screen.getByRole('dialog', { name: 'Command palette' })).getByRole('option', { name: /Open board: Design/ })).toBeTruthy())
    if (shotDir && width === 390) await page.screenshot({ path: `${shotDir}/quick-open-commands-390x844.png` })
  })
  it('opens Board commands in a new tab and keeps the runner conversation in place', async () => {
    mountSession()
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    const before = window.location.href
    const composer = document.querySelector('.agent-composer textarea')
    try {
      for (const [query, title, url] of [
        ['Open board: Design', 'Open board: Design', 'https://board.example.com/boards/design'],
      ]) {
        screen.getByRole('button', { name: 'Search commands and files' }).focus()
        await userEvent.keyboard('{Control>}k{/Control}')
        fireEvent.change(screen.getByRole('combobox', { name: /^Search commands/ }), { target: { value: `>${query}` } })
        await userEvent.click(await screen.findByRole('option', { name: title }))
        expect(open).toHaveBeenLastCalledWith(url, '_blank', 'noopener')
        expect(window.location.href).toBe(before)
        expect(document.querySelector('.agent-composer textarea')).toBe(composer)
        expect(screen.queryByRole('dialog', { name: 'Command palette' })).toBeNull()
      }
    } finally { open.mockRestore() }
  })
})
