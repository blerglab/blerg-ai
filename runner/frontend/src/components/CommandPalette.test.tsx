import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { Profiler, useMemo } from 'react'
import { MemoryRouter, useNavigate } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import CommandPaletteProvider, { PaletteButton, PaletteDialog } from './CommandPalette'
import { usePaletteCommands } from '../commands/context'
import { CommandRegistry, type PaletteCommand, type PaletteProvider } from '../commands/registry'
const identity = vi.hoisted(() => ({ account: 'owner' }))
vi.mock('../lib/liveBoardCard', () => ({ signedInAccount: () => identity.account }))
beforeEach(() => { identity.account = 'owner' })
function Feature({ run = () => {}, providers = [] }: { run?: () => void; providers?: PaletteProvider[] }) {
  const items = useMemo(() => [{ id: 'settings', destructive: false, title: 'Open Settings', empty: true, run }, { id: 'shell', destructive: false, title: 'Open shell', shortcut: 'Ctrl+`', run }], [run])
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
    let input = screen.getByRole('combobox', { name: /^Search commands/ })
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
  it.each(['input', 'textarea', 'select', 'contenteditable', 'textbox', 'searchbox', 'combobox'])('opens from a focused %s and prevents the browser shortcut', kind => {
    show()
    const control = document.createElement(['input', 'textarea', 'select'].includes(kind) ? kind : 'div')
    control.tabIndex = 0
    if (kind === 'contenteditable') control.setAttribute('contenteditable', 'true')
    else if (kind.endsWith('box')) control.setAttribute('role', kind)
    document.body.appendChild(control)
    try {
      for (const value of ['k', 'p']) for (const meta of [false, true]) {
        control.focus()
        const event = new KeyboardEvent('keydown', { key: value, ctrlKey: !meta, metaKey: meta, bubbles: true, cancelable: true })
        act(() => { control.dispatchEvent(event) })
        expect(event.defaultPrevented).toBe(true)
        expect(screen.getByRole('dialog', { name: value === 'k' ? 'Command palette' : 'Quick open' })).toBeInTheDocument()
        fireEvent.keyDown(screen.getByRole('button', { name: 'Close search' }), { key: 'Escape' })
        expect(control).toHaveFocus()
      }
    } finally { control.remove() }
  })
  it.each(['xterm', 'monaco-editor', 'cm-editor', 'CodeMirror', 'ace_editor', 'opt-out'])('leaves shortcuts with a focused %s surface', kind => {
    show()
    const surface = document.createElement('div')
    if (kind === 'opt-out') surface.dataset.paletteShortcuts = 'ignore'
    else surface.className = kind
    const control = document.createElement('textarea')
    surface.append(control)
    document.body.append(surface)
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
    } finally { surface.remove() }
  })
  it('respects editor ownership across a shadow event path', () => {
    show()
    const host = document.createElement('div')
    document.body.append(host)
    const shadow = host.attachShadow({ mode: 'open' })
    shadow.innerHTML = '<div class="cm-editor"><textarea></textarea></div>'
    const control = shadow.querySelector('textarea')!
    control.focus()
    try {
      const event = new KeyboardEvent('keydown', { key: 'k', ctrlKey: true, bubbles: true, composed: true, cancelable: true })
      control.dispatchEvent(event)
      expect(event.defaultPrevented).toBe(false)
      expect(screen.queryByRole('dialog')).toBeNull()
    } finally { host.remove() }
  })
  it('leaves IME composition in the composer untouched', () => {
    show()
    const composer = screen.getByRole('textbox', { name: 'Session composer' })
    composer.focus()
    for (const value of ['k', 'p']) for (const meta of [false, true]) {
      const event = new KeyboardEvent('keydown', { key: value, ctrlKey: !meta, metaKey: meta, isComposing: true, bubbles: true, cancelable: true })
      composer.dispatchEvent(event)
      expect(event.defaultPrevented).toBe(false)
      expect(composer).toHaveFocus()
      expect(screen.queryByRole('dialog')).toBeNull()
    }
  })
  it('claims the browser default even when an app surface stops propagation', () => {
    show()
    const button = screen.getByRole('button', { name: 'Navigate' })
    button.addEventListener('keydown', event => event.stopPropagation())
    button.focus()
    act(() => { expect(key('k').defaultPrevented).toBe(true) })
    expect(screen.queryByRole('dialog')).toBeNull()
  })
  it.each(['dialog', 'alertdialog', 'native'])('does not open over an existing %s through the shortcut or button', role => {
    show()
    const modal = document.createElement(role === 'native' ? 'dialog' : 'div')
    if (role === 'native') modal.setAttribute('open', '')
    else { modal.setAttribute('role', role); modal.setAttribute('aria-modal', 'true') }
    document.body.appendChild(modal)
    try {
      for (const value of ['k', 'p']) for (const meta of [false, true]) {
        const event = new KeyboardEvent('keydown', { key: value, ctrlKey: !meta, metaKey: meta, bubbles: true, cancelable: true })
        act(() => { window.dispatchEvent(event) })
        expect(event.defaultPrevented).toBe(true)
      }
      fireEvent.click(screen.getByRole('button', { name: 'Search commands and files' }))
      expect(screen.queryByRole('combobox')).toBeNull()
    } finally { modal.remove() }
  })
  it('ignores modals under hidden ancestors without checkVisibility', () => {
    for (const hiddenBy of ['display', 'visibility', 'opacity', 'contentVisibility', 'hidden', 'aria-hidden', 'inert']) {
      const view = show()
      const ancestor = document.createElement('div')
      const modal = document.createElement('section')
      modal.setAttribute('aria-modal', 'true')
      // Force the fallback even in DOM environments that acquire checkVisibility later.
      Object.defineProperty(modal, 'checkVisibility', { value: undefined })
      if (['hidden', 'inert', 'aria-hidden'].includes(hiddenBy)) ancestor.setAttribute(hiddenBy, hiddenBy === 'aria-hidden' ? 'true' : '')
      else Object.assign(ancestor.style, { [hiddenBy]: hiddenBy === 'display' ? 'none' : hiddenBy === 'opacity' ? '0' : 'hidden' })
      ancestor.append(modal)
      document.body.append(ancestor)
      try {
        act(() => { expect(key('k').defaultPrevented).toBe(true) })
        expect(screen.getByRole('combobox')).toHaveFocus()
      } finally { ancestor.remove(); view.unmount() }
    }
  })
  it('recognizes restored dialog visibility without checkVisibility', () => {
    show()
    const ancestor = document.createElement('div')
    ancestor.style.visibility = 'hidden'
    const modal = document.createElement('section')
    modal.style.visibility = 'visible'
    modal.setAttribute('aria-modal', 'true')
    Object.defineProperty(modal, 'checkVisibility', { value: undefined })
    ancestor.append(modal)
    document.body.append(ancestor)
    try {
      act(() => { expect(key('k').defaultPrevented).toBe(true) })
      expect(screen.queryByRole('combobox')).toBeNull()
    } finally { ancestor.remove() }
  })
  it('cancels repeated shortcuts without reopening or resetting the palette', () => {
    show()
    for (const value of ['k', 'p']) for (const meta of [false, true]) {
      const repeat = () => new KeyboardEvent('keydown', { key: value, ctrlKey: !meta, metaKey: meta, repeat: true, bubbles: true, cancelable: true })
      const beforeOpen = repeat()
      act(() => { window.dispatchEvent(beforeOpen) })
      expect.soft(beforeOpen.defaultPrevented).toBe(true)
      expect(screen.queryByRole('dialog')).toBeNull()
      act(() => { window.dispatchEvent(new KeyboardEvent('keydown', { key: value, ctrlKey: !meta, metaKey: meta, bubbles: true, cancelable: true })) })
      const input = screen.getByRole('combobox')
      fireEvent.change(input, { target: { value: 'keep query' } })
      const whileOpen = repeat()
      act(() => { input.dispatchEvent(whileOpen) })
      expect.soft(whileOpen.defaultPrevented).toBe(true)
      expect(screen.getByRole('combobox')).toBe(input)
      expect(input).toHaveValue('keep query')
      fireEvent.keyDown(input, { key: 'Escape' })
    }
  })
  it('uses native visibility checks when available', () => {
    show()
    const modal = document.createElement('div')
    modal.setAttribute('aria-modal', 'true')
    const checkVisibility = vi.fn(() => false)
    Object.defineProperty(modal, 'checkVisibility', { value: checkVisibility })
    document.body.append(modal)
    try {
      act(() => { key('k') })
      expect(checkVisibility).toHaveBeenCalledWith(expect.objectContaining({ checkOpacity: true, checkVisibilityCSS: true, contentVisibilityAuto: true }))
      expect(screen.getByRole('combobox')).toHaveFocus()
    } finally { modal.remove() }
  })
  it('shows a shortcut only when the command registers one', () => {
    show()
    fireEvent.click(screen.getByRole('button', { name: 'Search commands and files' }))
    fireEvent.change(screen.getByRole('combobox'), { target: { value: '>' } })
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
  it('keeps the dialog idle on background refreshes and executes the latest callback', async () => {
    const registry = new CommandRegistry(), rendered = vi.fn(), search = vi.fn().mockResolvedValue({ items: [] })
    const providers: PaletteProvider[] = [{ id: 'boards', mode: 'commands', search }]
    const close = vi.fn(), run = vi.fn(), latest = vi.fn()
    function Registered({ action, title = 'Alpha' }: { action: () => void; title?: string }) {
      usePaletteCommands([{ id: 'a', destructive: false, title, empty: true, run: action }, { id: 'z', destructive: false, title: 'Zulu', empty: true, run: action }], providers)
      return null
    }
    const { PaletteContext } = await import('../commands/context')
    const context = { registry, open() {} }
    const dialog = <Profiler id="palette" onRender={rendered}><PaletteDialog registry={registry} mode="commands" close={close} /></Profiler>
    const tree = (action: () => void, title?: string) => <PaletteContext.Provider value={context}>
      <Registered action={action} title={title} />{dialog}
    </PaletteContext.Provider>
    const view = render(tree(run))
    fireEvent.change(screen.getByRole('combobox'), { target: { value: '>' } })
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('2 results'))
    fireEvent.keyDown(screen.getByRole('combobox'), { key: 'ArrowDown' })
    const active = screen.getByRole('option', { selected: true })
    rendered.mockClear()
    view.rerender(tree(latest))
    expect(rendered).not.toHaveBeenCalled()
    expect(search).toHaveBeenCalledTimes(1)
    expect(screen.getByRole('option', { selected: true })).toBe(active)
    view.rerender(tree(latest, 'Zzz'))
    expect(screen.getByRole('option', { selected: true })).toHaveTextContent('Zulu')
    fireEvent.keyDown(screen.getByRole('combobox'), { key: 'Enter' })
    expect(latest).toHaveBeenCalledOnce()
  })
  it('keeps active option IDs valid for file paths containing spaces', () => {
    const registry = new CommandRegistry()
    registry.register([{ id: 'file:a path.md', title: 'a path.md', empty: true, run() {} }])
    render(<PaletteDialog registry={registry} mode="commands" close={() => {}} />)
    const option = screen.getByRole('option'), input = screen.getByRole('combobox')
    expect(option.id).not.toMatch(/\s/)
    expect(input.getAttribute('aria-activedescendant')).toBe(option.id)
    expect(document.getElementById(input.getAttribute('aria-activedescendant')!)).toBe(option)
  })
  it('orders recent commands first, navigates options and removes feature commands on disposal', () => {
    const registry = new CommandRegistry(), run = vi.fn(), close = vi.fn()
    const off = registry.register([{ id: 'a', title: 'Alpha', empty: true, run }, { id: 'z', destructive: false, title: 'Zulu', empty: true, run }])
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

it('rejects a newly registered action without an explicit safety marker', () => {
  function Unclassified() {
    const command = { id: 'new-grant', title: 'Grant access', run: vi.fn() }
    // Simulate a JavaScript caller bypassing TypeScript's required field.
    usePaletteCommands([command as unknown as PaletteCommand])
    return null
  }
  expect(() => render(<MemoryRouter><CommandPaletteProvider><Unclassified /></CommandPaletteProvider></MemoryRouter>)).toThrow('must explicitly set destructive')
})
