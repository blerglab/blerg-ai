import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { CommandRegistry, paletteGroups, type PaletteItem, type PaletteProvider } from '../commands/registry'
import { cardProvider } from '../commands/cardProvider'
import { apiFetch } from '../apiFetch'
import { PaletteDialog } from './CommandPalette'
const identity = vi.hoisted(() => ({ account: 'owner' }))
vi.mock('../lib/liveBoardCard', () => ({ signedInAccount: () => identity.account }))
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn() }))
const item = (id: string, extra: Partial<PaletteItem> = {}): PaletteItem => ({ id, title: id, run: vi.fn(), ...extra })
const type = (query: string) => fireEvent.change(screen.getByRole('combobox'), { target: { value: query } })
const tick = async () => { await act(async () => { vi.advanceTimersByTime(150) }) }
afterEach(() => { vi.useRealTimers(); vi.mocked(apiFetch).mockReset(); identity.account = 'owner' })
function show(items: PaletteItem[], providers: PaletteProvider[] = [], registry = new CommandRegistry()) {
  registry.register(items, providers)
  render(<PaletteDialog registry={registry} mode="commands" close={vi.fn()} />)
  return registry
}
describe('grouped palette', () => {
  it('caps each labeled listbox group at five, expands by keyboard, and navigates across groups', () => {
    show(paletteGroups.flatMap(group => Array.from({ length: 8 }, (_, i) => item(`${group}-${i}`, { title: `match ${i}`, group }))))
    type('match')
    expect(screen.getAllByRole('group').map(g => g.getAttribute('aria-labelledby'))).toHaveLength(4)
    for (const name of ['Sessions', 'Cards', 'Files', 'Commands']) {
      const group = screen.getByRole('group', { name })
      expect(within(group).getAllByRole('option')).toHaveLength(6)
      expect(within(group).getByRole('option', { name: `More ${name.toLowerCase()} (3)` })).toBeInTheDocument()
    }
    const input = screen.getByRole('combobox')
    for (let i = 0; i < 5; i++) fireEvent.keyDown(input, { key: 'ArrowDown' })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(within(screen.getByRole('group', { name: 'Sessions' })).getAllByRole('option')).toHaveLength(8)
    expect(screen.getByRole('option', { selected: true })).toHaveTextContent('match 5')
    for (let i = 0; i < 3; i++) fireEvent.keyDown(input, { key: 'ArrowDown' })
    expect(screen.getByRole('option', { selected: true }).closest('[role=group]')).toHaveAccessibleName('Cards')
    expect(document.getElementById(input.getAttribute('aria-activedescendant')!)).toBe(screen.getByRole('option', { selected: true }))
  })
  it.each([['s ', 'Sessions'], ['session:', 'Sessions'], ['#', 'Cards'], ['/', 'Files'], ['>', 'Commands']])('turns %s into a removable %s chip', (prefix, name) => {
    show(paletteGroups.map(group => item(group, { title: 'match', group })))
    type(`${prefix}match`)
    expect(screen.getByRole('combobox')).toHaveValue('match')
    expect(screen.getAllByRole('group')).toHaveLength(1)
    expect(screen.getByRole('group', { name })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: `Remove ${name.toLowerCase()} scope` }))
    expect(screen.getAllByRole('group')).toHaveLength(4)
    expect(screen.getByRole('combobox')).toHaveFocus()
  })
  it('removes an empty chip on Backspace and preserves ordinary spaces while typing', () => {
    show([])
    type('s ')
    expect(screen.getByRole('button', { name: 'Remove sessions scope' })).toBeInTheDocument()
    fireEvent.keyDown(screen.getByRole('combobox'), { key: 'Backspace' })
    expect(screen.queryByRole('button', { name: /Remove .* scope/ })).toBeNull()
    type('a normal query')
    expect(screen.getByRole('combobox')).toHaveValue('a normal query')
  })
  it('shows this session, at most three offered sessions in their order, and only safe common actions when empty', () => {
    const r = new CommandRegistry()
    r.remember('session-7'); r.remember('danger')
    show([
      ...Array.from({ length: 8 }, (_, i) => item(`session-${i}`, { group: 'sessions', recentRank: i < 5 ? 4 - i : undefined })),
      item('New session', { empty: true }), item('Open board', { empty: true }), item('Settings', { empty: true }),
      item('danger', { destructive: true, empty: true }), item('uncommon'), item('file', { group: 'files' }),
      item('insights', { here: 7 }), item('quick open', { here: 1 }), item('shell', { here: 2, destructive: true }),
      item('model menu', { here: 3, refine: 'Switch model: ' }),
    ], [], r)
    expect(screen.getAllByRole('group').map(g => g.getAttribute('aria-labelledby') && document.getElementById(g.getAttribute('aria-labelledby')!)?.textContent)).toEqual(['This session', 'Recent sessions', 'Commands'])
    expect(within(screen.getByRole('group', { name: 'This session' })).getAllByRole('option').map(o => o.textContent?.trim())).toEqual(['quick open', 'shell', 'model menu', 'insights'])
    expect(screen.getByRole('option', { selected: true })).toHaveTextContent('quick open')
    // Recency in the registry does not reorder or add sessions: only recentRank counts.
    expect(within(screen.getByRole('group', { name: 'Recent sessions' })).getAllByRole('option').map(o => o.textContent?.trim())).toEqual(['session-4', 'session-3', 'session-2'])
    expect(within(screen.getByRole('group', { name: 'Commands' })).getAllByRole('option')).toHaveLength(3)
    expect(screen.queryByRole('option', { name: 'danger' })).toBeNull()
    expect(screen.queryByRole('option', { name: 'uncommon' })).toBeNull()
    expect(screen.queryByRole('group', { name: 'Files' })).toBeNull()
    // A menu entry is never a search result; typing finds the real choices.
    type('model')
    expect(screen.queryByRole('option', { name: 'model menu' })).toBeNull()
  })
  it('orders session: blerg by name, repo and detail even after recency', () => {
    const r = new CommandRegistry()
    r.remember('detail')
    show([
      item('detail', { title: 'Other', group: 'sessions', detail: 'blerg' }),
      item('repo', { title: 'Build', group: 'sessions', repo: 'example-org/blerg' }),
      item('name', { title: 'Blerg redesign', group: 'sessions' }),
    ], [], r)
    type('session: blerg')
    expect(screen.getAllByRole('option').map(o => o.textContent)).toEqual([' Blerg redesign', ' Buildexample-org/blerg', ' Otherblerg'])
  })
  it('never defaults to a destructive best match, including after a safe command disappears', () => {
    const r = new CommandRegistry(), danger = vi.fn()
    r.remember('danger')
    r.register([item('danger', { title: 'Discard', destructive: true, run: danger })])
    const off = r.register([item('safe', { title: 'Discard help' })])
    render(<PaletteDialog registry={r} mode="commands" close={vi.fn()} />)
    type('>Discard')
    expect(screen.getAllByRole('option')[1]).toHaveClass('palette-destructive')
    expect(screen.getByRole('option', { selected: true })).toHaveTextContent('Discard help')
    act(off)
    expect(screen.queryByRole('option', { selected: true })).toBeNull()
    expect(screen.getByRole('combobox')).not.toHaveAttribute('aria-activedescendant')
    fireEvent.keyDown(screen.getByRole('combobox'), { key: 'Enter' })
    expect(danger).not.toHaveBeenCalled()
  })
  it('requires an explicit arrow or pointer selection when only destructive actions match', () => {
    const r = new CommandRegistry(), danger = vi.fn()
    r.remember('danger')
    show([item('danger', { title: 'Discard session…', destructive: true, run: danger })], [], r)
    type('>Discard')
    const input = screen.getByRole('combobox')
    expect(input).not.toHaveAttribute('aria-activedescendant')
    expect(screen.queryByRole('option', { selected: true })).toBeNull()
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(danger).not.toHaveBeenCalled()
    fireEvent.keyDown(input, { key: 'ArrowDown' })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(danger).toHaveBeenCalledOnce()
  })
  it('rejects new provider commands without a safety classification', async () => {
    vi.useFakeTimers()
    show([], [{ id: 'new-actions', mode: 'commands', search: async () => ({ items: [item('Grant access')] }) }])
    type('>Grant')
    await tick()
    expect(screen.queryByRole('option')).toBeNull()
    expect(screen.getByRole('status')).toHaveTextContent('Some search actions are unavailable.')
  })
  it('closes on an identity boundary so settled card summaries cannot follow another sign-in', () => {
    const r = new CommandRegistry(), close = vi.fn()
    r.register([item('card', { group: 'cards', title: '#218 Private search result' })])
    render(<PaletteDialog registry={r} mode="commands" close={close} />)
    type('#Private')
    expect(screen.getByRole('option')).toHaveTextContent('Private search result')
    identity.account = 'another'
    fireEvent(window, new Event('blerg-identity-changed'))
    expect(close).toHaveBeenCalledOnce()
  })
  it('debounces card requests, aborts superseded searches and hides stale replies', async () => {
    vi.useFakeTimers()
    const requests: { query: string; signal: AbortSignal; resolve: (value: { items: PaletteItem[] }) => void }[] = []
    const search = vi.fn((query: string, signal: AbortSignal) => new Promise<{ items: PaletteItem[] }>(resolve => requests.push({ query, signal, resolve })))
    show([], [{ id: 'cards', mode: 'cards', search }])
    type('ab'); await tick(); expect(search).not.toHaveBeenCalled()
    type('abc'); type('abcd')
    await act(async () => { vi.advanceTimersByTime(149) })
    expect(search).not.toHaveBeenCalled()
    await act(async () => { vi.advanceTimersByTime(1) })
    expect(requests[0].query).toBe('abcd')
    type('#12')
    expect(requests[0].signal.aborted).toBe(true)
    await tick()
    expect(requests[1].query).toBe('12')
    await act(async () => { requests[1].resolve({ items: [item('new', { title: '#12 New', searchMatched: true })] }) })
    expect(screen.getByRole('option')).toHaveTextContent('#12 New')
    await act(async () => { requests[0].resolve({ items: [item('old', { title: '#13 Old', searchMatched: true })] }) })
    expect(screen.queryByRole('option', { name: '#13 Old' })).toBeNull()
    type('>')
    expect(requests[1].signal.aborted).toBe(true)
  })
})
describe('card provider access', () => {
  it('searches only discovered boards, drops denied results, and binds modal references to the board', async () => {
    vi.mocked(apiFetch).mockImplementation(async path => new Response(JSON.stringify(
      path === '/api/session-boards' ? { boards: [{ id: 'design', name: 'Design' }, { id: 'denied', name: 'Denied' }] } :
      path.includes('/design/') ? { cards: [{ board_id: 'untrusted', number: 218, title: 'Split view', column_name: 'Doing' }] } : {}
    ), { status: path.includes('/denied/') ? 403 : 200 }))
    const open = vi.fn(), controller = new AbortController()
    const result = await cardProvider(open).search('split', controller.signal)
    expect(result.items).toHaveLength(1)
    expect(result.items[0]).toMatchObject({ title: '#218 Split view', detail: 'Design · Doing', group: 'cards' })
    result.items[0].run()
    expect(open).toHaveBeenCalledWith(expect.objectContaining({ board_id: 'design', key: '#218' }))
    identity.account = 'another'
    result.items[0].run()
    expect(open).toHaveBeenCalledOnce()
    expect(apiFetch).toHaveBeenCalledTimes(3)
    for (const [, init] of vi.mocked(apiFetch).mock.calls) expect(init?.signal).toBe(controller.signal)
  })
  it('refuses results after cancellation or a changed sign-in', async () => {
    vi.mocked(apiFetch).mockImplementation(async () => {
      identity.account = 'another'
      return new Response(JSON.stringify({ boards: [{ id: 'private', name: 'Private' }] }))
    })
    await expect(cardProvider(vi.fn()).search('text', new AbortController().signal)).rejects.toThrow('sign-in changed')
    expect(apiFetch).toHaveBeenCalledTimes(1)
    const controller = new AbortController(); controller.abort()
    await expect(cardProvider(vi.fn()).search('text', controller.signal)).rejects.toThrow()
  })
})
