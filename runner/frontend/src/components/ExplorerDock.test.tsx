import { beforeEach, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import ExplorerDock, { ExplorerToggle } from './ExplorerDock'
import { explorerPrefKey, useExplorerLayout } from '../hooks/useExplorerLayout'
import { CommandRegistry } from '../commands/registry'
import { PaletteContext } from '../commands/context'
import FilesTab from './FilesTab'

const account = vi.hoisted(() => ({ current: 'person-a' }))
const read = vi.hoisted(() => vi.fn())
vi.mock('../lib/liveBoardCard', () => ({ signedInAccount: () => account.current }))
vi.mock('../apiFetch', () => ({ apiFetch: read }))
vi.mock('../lib/chatTransport', () => ({ blergTransport: () => ({ files: null }) }))
function Harness({ enabled = true }: { enabled?: boolean }) {
  const layout = useExplorerLayout(enabled)
  return <div data-session-root><ExplorerToggle layout={layout} /><ExplorerDock sessionId="session" layout={layout} /></div>
}
const toggle = () => screen.getByRole('button', { name: 'Toggle explorer' })
beforeEach(() => {
  localStorage.clear()
  account.current = 'person-a'
  read.mockReset().mockImplementation(async (url: string) => {
    const path = new URL(url, 'https://example.com').searchParams.get('path')
    return new Response(JSON.stringify({ result: { ok: true, launch_generation: 'launch', generation: 1, entries: path ? [{ name: 'nested.txt', path: 'src/nested.txt', directory: false }] : [{ name: 'src', path: 'src', directory: true }, { name: 'README.md', path: 'README.md', directory: false }], truncated: false }, members: [], runtime: 'host' }))
  })
})
it('registers a palette command without a global shortcut and leaves native keys alone', async () => {
  const registry = new CommandRegistry()
  const view = render(<PaletteContext.Provider value={{ registry, open: () => {} }}><Harness /></PaletteContext.Provider>)
  const command = registry.getSnapshot().items.find(item => item.title === 'Toggle explorer')!
  expect(command).toBeDefined(); expect(command.shortcut).toBeUndefined()
  fireEvent.keyDown(window, { key: 'E', ctrlKey: true, shiftKey: true })
  expect(toggle()).toHaveAttribute('aria-expanded', 'false')
  fireEvent.click(toggle()); await screen.findByRole('tree')
  const input = screen.getByRole('combobox'); input.focus()
  fireEvent.keyDown(input, { key: 'E', metaKey: true, shiftKey: true })
  expect(document.activeElement).toBe(input)
  expect(toggle()).toHaveAttribute('aria-expanded', 'true')
  view.unmount(); expect(registry.getSnapshot().items).toHaveLength(0)
})
it('remembers open state and keyboard width per person, clamps hostile storage, and does not inherit on identity change', async () => {
  const view = render(<Harness />)
  fireEvent.click(toggle())
  const separator = screen.getByRole('separator', { name: 'Explorer width' })
  fireEvent.keyDown(separator, { key: 'End' })
  const max = Number(separator.getAttribute('aria-valuemax'))
  expect(Number(separator.getAttribute('aria-valuenow'))).toBe(max)
  expect(JSON.parse(localStorage.getItem(explorerPrefKey('person-a'))!)).toEqual({ open: true, width: max })
  view.unmount()
  const again = render(<Harness />)
  expect(screen.getByRole('separator')).toHaveAttribute('aria-valuenow', String(max))
  expect(toggle()).toHaveAttribute('aria-expanded', 'true')
  account.current = 'person-b'; again.rerender(<Harness />)
  expect(toggle()).toHaveAttribute('aria-expanded', 'false')
  fireEvent.click(toggle()); fireEvent.keyDown(screen.getByRole('separator'), { key: 'Home' })
  const other = JSON.parse(localStorage.getItem(explorerPrefKey('person-b'))!)
  expect(other.width).toBe(Number(screen.getByRole('separator').getAttribute('aria-valuemin')))
  account.current = 'person-a'; again.rerender(<Harness />)
  expect(screen.getByRole('separator')).toHaveAttribute('aria-valuenow', String(max))
  again.unmount()
  localStorage.setItem(explorerPrefKey('person-a'), '{"open":true,"width":100000000}')
  render(<Harness />)
  expect(screen.getByRole('separator')).toHaveAttribute('aria-valuenow', String(max))
  await screen.findByRole('treeitem', { name: /README/ })
})
it('navigates and expands the tree with arrows and restores header focus', async () => {
  render(<Harness />)
  fireEvent.click(toggle())
  const src = await screen.findByRole('treeitem', { name: /src/ })
  src.focus()
  fireEvent.keyDown(src, { key: 'ArrowRight' })
  const nested = await screen.findByRole('treeitem', { name: /nested/ })
  fireEvent.keyDown(src, { key: 'ArrowRight' })
  expect(document.activeElement).toBe(nested)
  expect(nested).toHaveAttribute('aria-level', '2')
  expect(screen.getAllByRole('treeitem').filter(row => row.tabIndex === 0)).toEqual([nested])
  fireEvent.keyDown(nested, { key: 'ArrowDown' }); expect(document.activeElement).toBe(screen.getByRole('treeitem', { name: /README/ }))
  fireEvent.keyDown(document.activeElement!, { key: 'Home' }); expect(document.activeElement).toBe(src)
  nested.focus()
  fireEvent.keyDown(nested, { key: 'ArrowLeft' }); expect(document.activeElement).toBe(src)
  fireEvent.keyDown(src, { key: 'ArrowLeft' }); expect(src).toHaveAttribute('aria-expanded', 'false')
  fireEvent.keyDown(src, { key: 'End' }); expect(document.activeElement).toBe(screen.getByRole('treeitem', { name: /README/ }))
  fireEvent.keyDown(document.activeElement!, { key: 'Escape' })
  expect(document.activeElement).toBe(toggle())
  expect(screen.queryByRole('region', { name: 'Explorer' })).toBeNull()
})
it('keeps published artifacts in Files without a workspace tree or extra source tab', async () => {
  render(<FilesTab sessionId="session" active onOpenArtifact={() => {}} />)
  expect(screen.queryByRole('button', { name: 'Workspace' })).toBeNull()
  expect(screen.queryByLabelText('File sources')).toBeNull()
  expect(screen.getByTestId('rail-files-none')).toBeTruthy()
  await waitFor(() => expect(read).not.toHaveBeenCalled())
})
it('does not register explorer for a missing session', () => {
  const registry = new CommandRegistry()
  render(<PaletteContext.Provider value={{ registry, open: () => {} }}><Harness enabled={false} /></PaletteContext.Provider>)
  expect(registry.getSnapshot().items).toHaveLength(0)
  fireEvent.click(toggle())
  expect(within(document.body).queryByRole('region', { name: 'Explorer' })).toBeNull()
})

it('works when browser storage is unavailable or malformed', async () => {
  localStorage.setItem(explorerPrefKey('person-a'), '{invalid')
  const view = render(<Harness />)
  fireEvent.click(toggle())
  await screen.findByRole('tree')
  view.unmount()
  const get = vi.spyOn(localStorage, 'getItem').mockImplementation(() => { throw new Error('storage disabled') })
  const set = vi.spyOn(localStorage, 'setItem').mockImplementation(() => { throw new Error('storage disabled') })
  try {
    render(<Harness />)
    fireEvent.click(toggle())
    await screen.findByRole('tree')
    fireEvent.click(screen.getByRole('button', { name: 'Collapse explorer' }))
    expect(toggle()).toHaveAttribute('aria-expanded', 'false')
  } finally { get.mockRestore(); set.mockRestore() }
})

it('cancels the active read when the explorer closes', () => {
  read.mockImplementation(() => new Promise(() => {}))
  render(<Harness />)
  fireEvent.click(toggle())
  expect(read).toHaveBeenCalledTimes(1)
  const signal = read.mock.calls[0][1].signal as AbortSignal
  expect(signal.aborted).toBe(false)
  fireEvent.click(screen.getByRole('button', { name: 'Collapse explorer' }))
  expect(signal.aborted).toBe(true)
})
