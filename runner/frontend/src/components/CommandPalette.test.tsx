import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { useMemo } from 'react'
import { MemoryRouter, useNavigate } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import CommandPaletteProvider, { PaletteButton, PaletteDialog } from './CommandPalette'
import { usePaletteCommands } from '../commands/context'
import { CommandRegistry, type PaletteProvider } from '../commands/registry'
const identity = vi.hoisted(() => ({ account: 'owner' }))
vi.mock('../lib/liveBoardCard', () => ({ signedInAccount: () => identity.account }))
beforeEach(() => { identity.account = 'owner' })
function Feature({ run = () => {}, providers = [] }: { run?: () => void; providers?: PaletteProvider[] }) {
  const items = useMemo(() => [{ id: 'settings', title: 'Open Settings', run }, { id: 'shell', title: 'Open shell', shortcut: 'Ctrl+`', run }], [run])
  usePaletteCommands(items, providers)
  const navigate = useNavigate()
  return <><PaletteButton /><button onClick={() => navigate('/sessions/other')}>Navigate</button><textarea aria-label="Session composer" /></>
}
function show(path = '/sessions/a', providers?: PaletteProvider[], run?: () => void) {
  return render(<MemoryRouter initialEntries={[path]}><CommandPaletteProvider><Feature providers={providers} run={run} /></CommandPaletteProvider></MemoryRouter>)
}
const key = (value: string, ctrlKey = true) => {
  const event = new KeyboardEvent('keydown', { key: value, ctrlKey, bubbles: true, cancelable: true })
  const target = document.activeElement ?? window
  target.dispatchEvent(event)
  return event
}
afterEach(() => vi.useRealTimers())
describe('command palette', () => {
  it('opens from an app control, executes a fuzzy command and restores focus on Esc', () => {
    const run = vi.fn()
    show('/sessions/a', [], run)
    const trigger = screen.getByRole('button', { name: 'Navigate' })
    trigger.focus()
    act(() => { expect(key('k').defaultPrevented).toBe(true) })
    let input = screen.getByRole('combobox', { name: 'Search commands' })
    expect(input).toHaveFocus()
    fireEvent.change(input, { target: { value: '>opst' } })
    expect(screen.getAllByRole('option')).toHaveLength(1)
    expect(input).toHaveAttribute('aria-controls', screen.getByRole('listbox').id)
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(run).toHaveBeenCalledTimes(1)
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(trigger).toHaveFocus()
    act(() => { key('k') })
    input = screen.getByRole('combobox')
    fireEvent.keyDown(input, { key: 'Escape' })
    expect(trigger).toHaveFocus()
  })
  it.each(['input', 'textarea', 'select', 'contenteditable', 'textbox'])('leaves shortcuts with a focused %s and its own handler', kind => {
    show()
    const control = document.createElement(kind === 'contenteditable' || kind === 'textbox' ? 'div' : kind)
    if (kind === 'contenteditable') control.setAttribute('contenteditable', 'true')
    if (kind === 'textbox') { control.setAttribute('role', 'textbox'); control.tabIndex = 0 }
    document.body.appendChild(control)
    const received = vi.fn()
    control.addEventListener('keydown', received)
    control.focus()
    try {
      for (const value of ['k', 'p']) for (const meta of [false, true]) {
        const event = new KeyboardEvent('keydown', { key: value, ctrlKey: !meta, metaKey: meta, bubbles: true, cancelable: true })
        control.dispatchEvent(event)
        expect(event.defaultPrevented).toBe(false)
        expect(screen.queryByRole('dialog')).toBeNull()
      }
      expect(received).toHaveBeenCalledTimes(4)
    } finally { control.remove() }
  })
  it('lets an app surface claim its own shortcut before the global handler', () => {
    show()
    const button = screen.getByRole('button', { name: 'Navigate' })
    button.addEventListener('keydown', event => event.preventDefault())
    fireEvent.keyDown(button, { key: 'k', ctrlKey: true })
    expect(screen.queryByRole('dialog')).toBeNull()
  })
  it.each(['dialog', 'alertdialog', 'native'])('does not open over an existing %s through the shortcut or button', role => {
    show()
    const modal = document.createElement(role === 'native' ? 'dialog' : 'div')
    if (role === 'native') modal.setAttribute('open', '')
    else { modal.setAttribute('role', role); modal.setAttribute('aria-modal', 'true') }
    document.body.appendChild(modal)
    try {
      for (const value of ['k', 'p']) act(() => { expect(key(value).defaultPrevented).toBe(false) })
      fireEvent.click(screen.getByRole('button', { name: 'Search commands and files' }))
      expect(screen.queryByRole('combobox')).toBeNull()
    } finally { modal.remove() }
  })
  it('shows a shortcut only when the command registers one', () => {
    show()
    fireEvent.click(screen.getByRole('button', { name: 'Search commands and files' }))
    expect(screen.getByRole('option', { name: /Open Settings/ }).querySelector('kbd')).toBeNull()
    expect(screen.getByRole('option', { name: /Open shell/ }).querySelector('kbd')?.textContent).toBe('Ctrl+`')
  })
  it('takes Print only inside a session, supports >, lists shortcuts, and traps forward/backward Tab', () => {
    show()
    act(() => { expect(key('p').defaultPrevented).toBe(true) })
    const input = screen.getByRole('combobox', { name: 'Search session files' })
    fireEvent.change(input, { target: { value: '>shell' } })
    expect(screen.getByRole('option')).toHaveTextContent('Ctrl+`')
    fireEvent.keyDown(input, { key: 'Tab' })
    expect(screen.getByRole('button', { name: 'Close search' })).toHaveFocus()
    fireEvent.keyDown(document.activeElement!, { key: 'Tab', shiftKey: true })
    expect(input).toHaveFocus()
  })
  it.each(['/boards', '/boards/b/ticket/c', '/insights'])('respects shortcut ownership on %s', path => {
    show(path)
    act(() => { expect(key('p').defaultPrevented).toBe(false) })
    if (path.startsWith('/boards')) {
      act(() => { expect(key('k').defaultPrevented).toBe(false) })
      expect(screen.queryByRole('dialog')).toBeNull()
    }
    act(() => { expect(key('/', false).defaultPrevented).toBe(false) })
  })
  it('cancels superseded searches and ignores stale replies even when a provider ignores abort', async () => {
    vi.useFakeTimers()
    const requests: { query: string; signal: AbortSignal; resolve: (r: { items: { id: string; title: string; run(): void }[] }) => void }[] = []
    const providers: PaletteProvider[] = [{ id: 'fake', mode: 'files', search: (query, signal) => new Promise(resolve => requests.push({ query, signal, resolve })) }]
    show('/sessions/a', providers)
    act(() => { key('p') })
    await act(async () => { vi.advanceTimersByTime(150) })
    const input = screen.getByRole('combobox')
    fireEvent.change(input, { target: { value: 'new' } })
    expect(requests[0].signal.aborted).toBe(true)
    await act(async () => { vi.advanceTimersByTime(150) })
    await act(async () => { requests[1].resolve({ items: [{ id: 'new', title: 'new.ts', run() {} }] }) })
    expect(screen.getByRole('option')).toHaveTextContent('new.ts')
    await act(async () => { requests[0].resolve({ items: [{ id: 'stale', title: 'new-stale.ts', run() {} }] }) })
    expect(screen.getAllByRole('option')).toHaveLength(1)
    expect(screen.getByRole('option')).toHaveTextContent(/^new.ts/)
    fireEvent.keyDown(input, { key: 'Escape' })
    expect(requests[1].signal.aborted).toBe(true)
  })
  it('reports provider errors and closes on route changes', async () => {
    const search = vi.fn().mockRejectedValue(new Error('Workspace busy'))
    show('/sessions/a', [{ id: 'fake', mode: 'files', search }])
    act(() => { key('p') })
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('Workspace busy'))
    fireEvent.click(screen.getByRole('button', { name: 'Navigate' }))
    expect(screen.queryByRole('dialog')).toBeNull()
    act(() => { key('k') })
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })
  it('refuses a selected command if the sign-in changed while the palette was open', () => {
    const run = vi.fn()
    show('/sessions/a', [], run)
    act(() => { key('k') })
    const input = screen.getByRole('combobox')
    fireEvent.change(input, { target: { value: '>Settings' } })
    identity.account = 'another'
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(run).not.toHaveBeenCalled()
    expect(screen.queryByRole('dialog')).toBeNull()
  })
  it('orders recent commands first, navigates options and removes feature commands on disposal', () => {
    const registry = new CommandRegistry(), run = vi.fn(), close = vi.fn()
    const off = registry.register([{ id: 'a', title: 'Alpha', run }, { id: 'z', title: 'Zulu', run }])
    registry.remember('z')
    render(<PaletteDialog registry={registry} mode="commands" close={close} />)
    const options = screen.getAllByRole('option'), input = screen.getByRole('combobox')
    expect(options[0]).toHaveTextContent('Zulu')
    fireEvent.keyDown(input, { key: 'ArrowDown' })
    expect(input).toHaveAttribute('aria-activedescendant', options[1].id)
    fireEvent.keyDown(input, { key: 'ArrowUp' })
    expect(input).toHaveAttribute('aria-activedescendant', options[0].id)
    act(off)
    expect(screen.queryAllByRole('option')).toHaveLength(0)
  })
})
