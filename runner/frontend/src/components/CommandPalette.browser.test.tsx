import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { createRef, useLayoutEffect, useMemo } from 'react'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import { page, userEvent } from 'vitest/browser'
import CommandPaletteProvider, { PaletteButton } from './CommandPalette'
import { usePalette, usePaletteCommands } from '../commands/context'
import type { CommandRegistry, PaletteProvider } from '../commands/registry'
import ShellWorkspace from '../test/ShellWorkspaceHarness'
import { ParentDiscardAction } from './ChildDiscard'
import { HostShellConsentRequired } from '../shellTransport'

import '../index.css'
import './IconButton.css'

vi.mock('../ws', () => import('../test/wsMock'))
function Feature({ run, providers }: { run: () => void; providers: PaletteProvider[] }) {
  const commands = useMemo(() => Array.from({ length: 80 }, (_, i) => ({
    id: `command-${i}`, destructive: false, title: `Command ${String(i).padStart(2, '0')}: open a long repository path with useful details`, shortcut: i === 0 ? 'Ctrl/⌘+P' : undefined, run,
  })), [run])
  usePaletteCommands(commands, providers)
  return <><PaletteButton /><textarea aria-label="Session composer" /></>
}

for (const width of [390, 3840]) describe(`command palette at ${width}px`, () => {
  it('uses the viewport, scrolls the active option into view, traps focus, and restores it on Esc', async () => {
    await page.viewport(width, 900)
    const run = vi.fn()
    const tree = (action: () => void) => <MemoryRouter initialEntries={['/sessions/s']}><CommandPaletteProvider><Feature run={action} providers={[]} /></CommandPaletteProvider></MemoryRouter>
    const view = render(tree(run))
    const trigger = screen.getByRole('button', { name: 'Search commands and files' })
    // Native button activation on Enter needs the iframe itself focused in Firefox.
    window.focus()
    trigger.focus()
    await waitFor(() => expect(document.hasFocus()).toBe(true))
    await userEvent.keyboard('{Control>}k{/Control}')
    const input = screen.getByRole('combobox'), dialog = screen.getByRole('dialog')
    expect(document.activeElement).toBe(input)
    const box = dialog.getBoundingClientRect()
    expect(box.width).toBeGreaterThan(width === 390 ? 380 : 1900)
    expect(box.right).toBeLessThanOrEqual(width)
    expect(box.bottom).toBeLessThanOrEqual(900)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    expect(screen.queryAllByRole('option')).toHaveLength(0)
    fireEvent.change(input, { target: { value: '>' } })
    expect(screen.getAllByRole('option')).toHaveLength(6)
    expect(input.getBoundingClientRect().right).toBeLessThanOrEqual(dialog.getBoundingClientRect().right)
    expect(input.getBoundingClientRect().width).toBeGreaterThan(width === 390 ? 200 : 1500)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    expect(screen.getByRole('group', { name: 'Commands' })).toBeTruthy()
    await userEvent.keyboard('{ArrowUp}{Enter}')
    expect(screen.getAllByRole('option')).toHaveLength(81)
    expect(screen.getByRole('option', { name: /^Focus mode/ })).toBeTruthy()
    await userEvent.keyboard('{ArrowUp}{ArrowUp}{ArrowUp}{ArrowUp}{ArrowUp}{ArrowUp}')
    await waitFor(() => expect(screen.getByRole('listbox').scrollTop).toBeGreaterThan(0))
    const option = document.getElementById(input.getAttribute('aria-activedescendant')!)!
    const listBox = screen.getByRole('listbox').getBoundingClientRect()
    expect(option.getBoundingClientRect().bottom).toBeLessThanOrEqual(listBox.bottom + 1)
    await waitFor(() => expect(screen.getByRole('status').textContent).toContain('81 results'))
    const list = screen.getByRole('listbox'), scroll = list.scrollTop
    const mutations: MutationRecord[] = []
    const observer = new MutationObserver(records => mutations.push(...records))
    observer.observe(list, { subtree: true, childList: true, attributes: true, characterData: true })
    view.rerender(tree(vi.fn()))
    await act(async () => { await Promise.resolve() })
    observer.disconnect()
    expect(mutations).toHaveLength(0)
    expect(document.getElementById(input.getAttribute('aria-activedescendant')!)).toBe(option)
    expect(list.scrollTop).toBe(scroll)
    await userEvent.keyboard('{Tab}')
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Close search' }))
    await userEvent.keyboard('{Shift>}{Tab}{/Shift}')
    expect(document.activeElement).toBe(input)
    await userEvent.keyboard('{Escape}')
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(document.activeElement).toBe(trigger)
    await userEvent.click(screen.getByRole('button', { name: 'Search commands and files' }))
    await userEvent.keyboard('{Tab}{Enter}')
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(run).not.toHaveBeenCalled()
  })
  it('offers the phone search button and quick open with a fake multirepository provider', async () => {
    await page.viewport(width, 900)
    const opened = vi.fn()
    const providers: PaletteProvider[] = [{ id: 'files', mode: 'files', search: async () => ({ items: [
      { id: 'app', title: 'app/src/HelloWorld.tsx', detail: 'App', run: () => opened('app/src/HelloWorld.tsx') },
      { id: 'lib', title: 'lib/src/HelloWorld.tsx', detail: 'Library', run: () => opened('lib/src/HelloWorld.tsx') },
    ], note: 'Partial results: the workspace search limit was reached.' }) }]
    render(<MemoryRouter initialEntries={['/sessions/s']}><CommandPaletteProvider><Feature run={vi.fn()} providers={providers} /></CommandPaletteProvider></MemoryRouter>)
    await userEvent.click(screen.getByRole('button', { name: 'Search commands and files' }))
    expect(screen.getByRole('dialog').textContent).toContain('Command palette')
    await userEvent.keyboard('{Escape}')
    await userEvent.keyboard('{Meta>}p{/Meta}')
    let input = screen.getByRole('combobox', { name: 'Search session files' })
    fireEvent.change(input, { target: { value: 'hlw' } })
    await waitFor(() => expect(screen.getAllByRole('option')).toHaveLength(2))
    expect(screen.getByRole('status').textContent).toContain('Partial results')
    await userEvent.keyboard('{ArrowDown}{Enter}')
    expect(opened).toHaveBeenCalledWith('lib/src/HelloWorld.tsx')
    await userEvent.keyboard('{Control>}p{/Control}')
    input = screen.getByRole('combobox')
    fireEvent.change(input, { target: { value: '>Command 07' } })
    expect(screen.getAllByRole('option')).toHaveLength(1)
    await act(async () => { fireEvent.keyDown(input, { key: 'Escape' }) })
  })
})

for (const width of [390, 3840]) it(`labels groups and crosses them by keyboard with a safe default at ${width}px`, async () => {
  await page.viewport(width, 900)
  const run = vi.fn()
  function Grouped() {
    const rows = useMemo(() => [
      { id: 'session', destructive: false, title: 'Widget redesign', group: 'sessions' as const, repo: 'example-org/widget', run },
      { id: 'card', destructive: false, title: '#218 Widget split view', group: 'cards' as const, detail: 'Design · In progress', run },
      { id: 'file', destructive: false, title: 'widget.ts', group: 'files' as const, detail: 'app/src/widget.ts', run },
      { id: 'command', destructive: false, title: 'Widget settings', run },
      { id: 'danger', title: 'Discard widget…', destructive: true, run },
    ], [])
    usePaletteCommands(rows)
    return <PaletteButton />
  }
  render(<MemoryRouter><CommandPaletteProvider><Grouped /></CommandPaletteProvider></MemoryRouter>)
  await userEvent.click(screen.getByRole('button', { name: 'Search commands and files' }))
  const input = screen.getByRole('combobox')
  fireEvent.change(input, { target: { value: 'widget' } })
  expect(screen.getAllByRole('group').map(group => group.getAttribute('aria-labelledby'))).toHaveLength(4)
  for (const name of ['Sessions', 'Cards', 'Files', 'Commands']) expect(screen.getByRole('group', { name })).toBeTruthy()
  for (const name of ['Cards', 'Files', 'Commands']) {
    await userEvent.keyboard('{ArrowDown}')
    const active = document.getElementById(input.getAttribute('aria-activedescendant')!)!
    expect(active.getAttribute('aria-selected')).toBe('true')
    expect(active.closest('[role=group]')).toBe(screen.getByRole('group', { name }))
    expect(document.activeElement).toBe(input)
  }
  fireEvent.change(input, { target: { value: '>Discard widget' } })
  expect(input.hasAttribute('aria-activedescendant')).toBe(false)
  await userEvent.keyboard('{Enter}')
  expect(run).not.toHaveBeenCalled()
  expect(screen.getByRole('option').classList.contains('palette-destructive')).toBe(true)
  await userEvent.keyboard('{ArrowDown}{Enter}')
  expect(run).toHaveBeenCalledOnce()
  expect(screen.queryByRole('dialog')).toBeNull()
})

for (const width of [390, 3840]) it(`opens empty on This session, then recent sessions, then commands at ${width}px`, async () => {
  await page.viewport(width, 900)
  const run = vi.fn()
  function Empty() {
    const rows = useMemo(() => [
      { id: 'quick', destructive: false, title: 'Quick open session files', here: 1, run },
      { id: 'shell', destructive: true, title: 'Toggle shell', here: 2, run },
      { id: 'model', destructive: false, title: 'Switch model…', here: 3, refine: 'Switch model: ', run },
      { id: 'model-big', destructive: false, title: 'Switch model: Big', run },
      ...['Widget redesign', 'Gadget fix', 'Sprocket docs', 'Fourth session'].map((title, i) => ({ id: `s${i}`, destructive: false, title, group: 'sessions' as const, recentRank: i, repo: 'example-org/a-rather-long-repository-name', run })),
      { id: 'cron', destructive: false, title: 'Nightly cron', group: 'sessions' as const, run },
      { id: 'new', destructive: false, title: 'New session', empty: true, run },
    ], [])
    usePaletteCommands(rows)
    return <PaletteButton />
  }
  render(<MemoryRouter initialEntries={['/sessions/s']}><CommandPaletteProvider><Empty /></CommandPaletteProvider></MemoryRouter>)
  await userEvent.click(screen.getByRole('button', { name: 'Search commands and files' }))
  const input = screen.getByRole('combobox'), dialog = screen.getByRole('dialog')
  expect(screen.getAllByRole('group').map(group => document.getElementById(group.getAttribute('aria-labelledby')!)!.textContent)).toEqual(['This session', 'Recent sessions', 'Commands'])
  expect(within(screen.getByRole('group', { name: 'Recent sessions' })).getAllByRole('option')).toHaveLength(3)
  expect(screen.queryByRole('option', { name: /Nightly cron/ })).toBeNull()
  expect(screen.getByRole('option', { selected: true }).textContent).toContain('Quick open session files')
  expect(dialog.getBoundingClientRect().right).toBeLessThanOrEqual(width)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  // Keyboard crosses from This session into Recent sessions.
  await userEvent.keyboard('{ArrowDown}{ArrowDown}{ArrowDown}')
  expect(document.getElementById(input.getAttribute('aria-activedescendant')!)!.closest('[role=group]')).toBe(screen.getByRole('group', { name: 'Recent sessions' }))
  // A menu entry refines the search instead of running or closing.
  await userEvent.keyboard('{ArrowUp}{Enter}')
  expect(input).toHaveProperty('value', 'Switch model: ')
  expect(document.activeElement).toBe(input)
  expect(screen.getByRole('option', { name: /Switch model: Big/ })).toBeTruthy()
  expect(run).not.toHaveBeenCalled()
})

for (const width of [390, 3840]) for (const kind of ['shell', 'discard'] as const) it(`protects real ${kind} consent and cancellation at ${width}px`, async () => {
  await page.viewport(width, 900)
  const title = kind === 'shell' ? 'New' : 'Discard'
  const query = kind === 'shell' ? 'New shell' : 'Discard session'
  const anchor = createRef<HTMLButtonElement>()
  let registry: CommandRegistry | undefined
  function Controls() {
    const palette = usePalette()
    useLayoutEffect(() => { registry = palette!.registry }, [palette])
    const commands = useMemo(() => [{ id: 'safe-help', title: `${title} help`, destructive: false, run: vi.fn() }], [])
    usePaletteCommands(commands)
    return <><PaletteButton /><button ref={anchor}>Session actions</button></>
  }
  const createShell = vi.fn(async () => { throw new HostShellConsentRequired({ challenge_id: 'challenge', machine_id: 'machine', account: 'user', uid: 1000, shell: '/bin/bash', cwd: '/project' }) })
  render(<MemoryRouter><CommandPaletteProvider><Controls />{kind === 'shell'
    ? <ShellWorkspace sessionId="session" accountId="owner" createShell={createShell} restoreShells={async () => []}><textarea aria-label="Composer" /></ShellWorkspace>
    : <ParentDiscardAction sessionId="session" title="Work" palette focusTarget={anchor} />
  }</CommandPaletteProvider></MemoryRouter>)
  const remember = vi.spyOn(registry!, 'remember')
  const search = async () => {
    await userEvent.click(screen.getByRole('button', { name: 'Search commands and files' }))
    const input = screen.getByRole('combobox')
    fireEvent.change(input, { target: { value: `>${title}` } })
    expect(screen.getByRole('option', { selected: true }).textContent).toContain(`${title} help`)
    fireEvent.change(input, { target: { value: query } })
    expect(screen.getByRole('option').classList.contains('palette-destructive')).toBe(true)
    expect(input.hasAttribute('aria-activedescendant')).toBe(false)
    await userEvent.keyboard('{Enter}')
    expect(screen.queryByRole('alertdialog')).toBeNull()
    expect(screen.getByRole('dialog', { name: 'Command palette' })).toBeTruthy()
    expect(createShell).not.toHaveBeenCalled()
  }
  await search()
  await userEvent.keyboard('{ArrowDown}{Enter}')
  const confirm = kind === 'shell'
    ? await screen.findByRole('dialog', { name: 'Allow a shell on this machine?' })
    : await screen.findByRole('alertdialog', { name: 'Discard session and all children?' })
  await userEvent.click(within(confirm).getByRole('button', { name: 'Cancel' }))
  expect(remember).not.toHaveBeenCalled()
  createShell.mockClear()
  await search()
})
