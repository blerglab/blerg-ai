import { useState } from 'react'
import { beforeEach, afterEach, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { ViewerHostContext, type ViewerHostValue } from '@blerglab/chat'
import WorkspaceTree, { EXPLORER_SLOT, type ExplorerSnapshot } from './WorkspaceTree'
import { clearViews, readView, syncViewCacheSessions, writeView } from '../lib/sessionViewCache'
import { saveCompactFolders } from '../lib/compactFolders'
import { revealSessionFile, sessionFileRevealVersion } from '../lib/openSessionFile'
import type { TreeEntry } from '../lib/workspaceTree'
import '../index.css'
import '@blerglab/chat/blerg.css'
const read = vi.hoisted(() => vi.fn())
const computations = vi.hoisted(() => ({ decorated: vi.fn(), chain: vi.fn() }))
vi.mock('../apiFetch', () => ({ apiFetch: read }))
vi.mock('../lib/liveBoardCard', () => ({ signedInAccount: () => 'person-a' }))
vi.mock('../lib/chatTransport', () => ({ blergTransport: () => ({ files: null }) }))
vi.mock('../lib/workspaceTree', async original => {
  const tree = await original<typeof import('../lib/workspaceTree')>()
  return { ...tree, withDeletedEntries: (...args: Parameters<typeof tree.withDeletedEntries>) => { computations.decorated(); return tree.withDeletedEntries(...args) }, compactFolderChain: (...args: Parameters<typeof tree.compactFolderChain>) => { computations.chain(); return tree.compactFolderChain(...args) } }
})
const dir = (path: string): TreeEntry => ({ path, name: path.split('/').at(-1)!, directory: true })
const file = (path: string): TreeEntry => ({ ...dir(path), directory: false })
let listings: Record<string, TreeEntry[]>
let members: ExplorerSnapshot['members']
let searchResults: TreeEntry[]
const deep = 'dev/ll/api/deep.txt'
function Harness({ initialPath = '' }: { initialPath?: string }) {
  const [path, setPath] = useState(initialPath), [shown, setShown] = useState(true)
  const request = (path: string) => revealSessionFile({ account: 'person-a', session: 'review', path })
  const host: ViewerHostValue = { views: path ? [{ id: 'file', view: { kind: 'workspace', path } }] : [], active: path ? 'file' : 'chat', target: null, open: view => { if (view.kind === 'workspace') { setPath(view.path); request(view.path) } }, close: vi.fn(), keep: vi.fn() }
  return <ViewerHostContext.Provider value={host}>
    <button className="btn" onClick={() => setPath(deep)}>Switch file tab</button>
    <button className="btn" onClick={() => request(deep)}>Request deep file</button>
    <button className="btn" onClick={() => request('outside.txt')}>Request outside file</button>
    <button className="btn" onClick={() => setShown(!shown)}>Toggle tree</button>
    <div style={{ width: '100%', maxWidth: 340 }}><WorkspaceTree sessionId="review" active={shown} /></div>
  </ViewerHostContext.Provider>
}
beforeEach(async () => {
  members = []; searchResults = []
  listings = { '': [dir('dev'), file('outside.txt')], dev: [dir('dev/ll')], 'dev/ll': [dir('dev/ll/api')], 'dev/ll/api': [file(deep)] }
  syncViewCacheSessions([{ id: 'review', status: 'idle' }])
  read.mockReset().mockImplementation(async (url: string) => {
    if (url === '/api/me/preferences') return new Response('{}')
    const q = new URL(url, location.origin).searchParams
    return new Response(JSON.stringify({ result: { ok: true, launch_generation: 'launch', generation: 1, entries: q.get('op') === 'search' ? searchResults : listings[q.get('path') ?? ''] ?? [], truncated: false, status: { branch: 'main', changed: 0, untracked: 0 } }, members, runtime: 'host' }))
  })
  await saveCompactFolders(true)
})
afterEach(async () => { await saveCompactFolders(true); await page.viewport(1280, 800) })
function seed(overrides: Partial<ExplorerSnapshot> = {}) {
  searchResults = overrides.hits?.entries ?? []
  writeView<ExplorerSnapshot>('person-a', 'review', EXPLORER_SLOT, {
    binding: 'launch:1', pages: Object.fromEntries(Object.entries(listings).map(([p, entries]) => [p, { entries, truncated: false }])), expanded: ['', 'dev', 'dev/ll', 'dev/ll/api'], statuses: {}, statusErrors: {}, statusTimes: {}, members, runtime: 'host', unavailable: [], filter: '', hits: null, focusRoot: '', focusedPath: '', scrollTop: 0, revealVersion: sessionFileRevealVersion('person-a', 'review'), ...overrides,
  })
}
const snapshot = () => readView<ExplorerSnapshot>('person-a', 'review', EXPLORER_SLOT)!
async function settled() { await act(async () => { await new Promise(r => setTimeout(r, 30)) }) }
it('does not reveal on file-tab changes or override a restored filter, results and drill-in', async () => {
  seed({ filter: 'deep', hits: { entries: [file(deep)], truncated: false }, focusRoot: 'dev/ll' })
  render(<Harness initialPath="outside.txt" />)
  await settled()
  expect(screen.getByRole('combobox').getAttribute('value')).toBe('deep')
  expect(snapshot().focusRoot).toBe('dev/ll')
  expect(snapshot().hits).not.toBeNull()
  fireEvent.click(screen.getByText('Switch file tab'))
  await settled()
  expect(snapshot().filter).toBe('deep')
  expect(snapshot().focusRoot).toBe('dev/ll')
  expect(snapshot().hits).not.toBeNull()
})
it('keeps a matching filter and server results when opening their visible file', async () => {
  seed({ filter: 'deep', hits: { entries: [file(deep)], truncated: false }, focusRoot: 'dev/ll' })
  render(<Harness />)
  fireEvent.click(screen.getByRole('treeitem', { name: 'deep.txt' }))
  await settled()
  expect(snapshot().filter).toBe('deep')
  expect(snapshot().hits?.entries).toEqual([file(deep)])
  expect(snapshot().focusRoot).toBe('dev/ll')
})
it('keeps a drill-in and matching local filter for a reveal within it', async () => {
  seed({ filter: 'deep', focusRoot: 'dev/ll' })
  render(<Harness initialPath={deep} />)
  fireEvent.click(screen.getByText('Request deep file'))
  await waitFor(() => expect(snapshot().selectedPath).toBe(deep))
  expect(snapshot().filter).toBe('deep')
  expect(snapshot().focusRoot).toBe('dev/ll')
})
it('clears only the result list hiding a reveal and keeps the matching filter', async () => {
  seed({ filter: 'deep', hits: { entries: [], truncated: false }, focusRoot: 'dev/ll' })
  render(<Harness initialPath={deep} />)
  fireEvent.click(screen.getByText('Request deep file'))
  await waitFor(() => expect(snapshot().selectedPath).toBe(deep))
  expect(snapshot().hits).toBeNull()
  expect(snapshot().filter).toBe('deep')
  expect(snapshot().focusRoot).toBe('dev/ll')
})
it('uses the requested path rather than the active tab and persists completion before remount', async () => {
  seed({ filter: 'outside', focusRoot: 'dev/ll', focusedPath: 'outside.txt', selectedPath: 'outside.txt' })
  render(<Harness initialPath={deep} />)
  fireEvent.click(screen.getByText('Request outside file'))
  await waitFor(() => expect(snapshot().revealedPath).toBe('outside.txt'))
  expect(snapshot().filter).toBe('outside')
  expect(snapshot().focusRoot).toBe('')
  fireEvent.click(screen.getByText('Toggle tree')); fireEvent.click(screen.getByText('Toggle tree'))
  await settled()
  expect(snapshot().revealedPath).toBe('outside.txt')
  expect(snapshot().filter).toBe('outside')
})
it('does not re-reveal a completed request after a collapse and cached remount', async () => {
  seed()
  render(<Harness />)
  fireEvent.click(screen.getByText('Request deep file'))
  await waitFor(() => expect(snapshot().revealedPath).toBe(deep))
  fireEvent.click(screen.getByRole('treeitem', { name: 'dev/ll/api' }))
  fireEvent.click(screen.getByText('Toggle tree')); fireEvent.click(screen.getByText('Toggle tree'))
  await settled()
  expect(screen.queryByRole('treeitem', { name: 'deep.txt' })).toBeNull()
  expect(snapshot().revealedPath).toBe(deep)
})
it('keeps a compact row closed if a middle ancestor was collapsed before compaction was enabled', async () => {
  seed({ expanded: ['', 'dev', 'dev/ll/api'] })
  render(<Harness />)
  expect(screen.getByRole('treeitem', { name: 'dev/ll/api' }).getAttribute('aria-expanded')).toBe('false')
  expect(screen.queryByRole('treeitem', { name: 'deep.txt' })).toBeNull()
})
it('keeps repository and member rows, badges and member display names', async () => {
  members = [{ id: 'member', path: 'dev', name: 'Project display name' }]
  listings[''][0] = { ...dir('dev'), repository: true, member_id: 'member' }
  seed({ statuses: { dev: { branch: 'main', changed: 0, untracked: 0, partial: false } } })
  render(<Harness />)
  expect(screen.getByRole('treeitem', { name: 'Project display name' })).not.toBeNull()
  expect(screen.getByTitle('Branch main')).not.toBeNull()
  expect(screen.getByRole('treeitem', { name: 'Project display name/ll/api' })).not.toBeNull()
})
it.each(['merge', 'split'])('retains DOM focus and its single tab stop through a refresh-driven chain %s', async change => {
  if (change === 'merge') listings.dev = [dir('dev/ll'), file('dev/sibling.txt')]
  seed()
  render(<Harness />); await settled()
  const row = screen.getByRole('treeitem', { name: 'dev/ll/api' })
  fireEvent.click(screen.getByText('ll', { selector: '.workspace-segment' })); row.focus()
  await settled()
  listings.dev = change === 'merge' ? [dir('dev/ll')] : [dir('dev/ll'), file('dev/sibling.txt')]
  window.dispatchEvent(new Event('focus'))
  await waitFor(() => expect(screen.queryByRole('treeitem', { name: 'dev' }) === null).toBe(change === 'merge'), { timeout: 4000 })
  const restored = screen.getByRole('treeitem', { name: 'dev/ll/api' })
  expect(document.activeElement).toBe(restored)
  expect(restored.tabIndex).toBe(0)
  expect(screen.getAllByRole('treeitem').filter(row => row.tabIndex === 0)).toEqual([restored])
  expect(snapshot().focusedPath).toBe('dev/ll')
})
it('reuses decorated listings and row chains when only selection changes', async () => {
  seed(); render(<Harness />); await settled()
  computations.decorated.mockClear(); computations.chain.mockClear()
  fireEvent.click(screen.getByText('ll', { selector: '.workspace-segment' }))
  expect(computations.decorated).not.toHaveBeenCalled()
  expect(computations.chain).not.toHaveBeenCalled()
})
for (const width of [390, 3840]) it.each([true, false])(`supports a 25-level single-child path at ${width}px (compact=%s)`, async compact => {
  await page.viewport(width, 844); await saveCompactFolders(compact); clearViews()
  const parts = Array.from({ length: 25 }, (_, i) => `long-folder-${i}`)
  const paths = parts.map((_, i) => parts.slice(0, i + 1).join('/'))
  listings = { '': [dir(paths[0])] }
  paths.forEach((path, i) => { listings[path] = i < 24 ? [dir(paths[i + 1])] : [file(`${path}/leaf.txt`)] })
  render(<Harness />)
  fireEvent.click(await screen.findByRole('treeitem', { name: parts[0] }))
  if (!compact) for (let i = 1; i < 25; i++) fireEvent.click(await screen.findByRole('treeitem', { name: parts[i] }))
  const leaf = await screen.findByRole('treeitem', { name: 'leaf.txt' })
  const row = screen.getByRole('treeitem', { name: compact ? paths[24] : parts[24] })
  expect(screen.getAllByRole('treeitem')).toHaveLength(compact ? 2 : 26)
  expect(leaf.getAttribute('aria-level')).toBe(compact ? '2' : '26')
  if (compact) {
    const label = row.querySelector<HTMLElement>('.workspace-compact-name')!
    expect(label.title).toBe(paths[24]); expect(getComputedStyle(label).direction).toBe('rtl')
    expect(label.scrollWidth).toBeGreaterThan(label.clientWidth)
    expect(row.getAttribute('aria-level')).toBe('1')
    expect(row.getAttribute('aria-label')).toBe(paths[24])
  }
  row.focus(); await userEvent.keyboard('{ArrowRight}'); expect(document.activeElement).toBe(leaf)
  await userEvent.keyboard('{ArrowLeft}'); expect(document.activeElement).toBe(row)
  await userEvent.keyboard('{ArrowLeft}'); expect(row.getAttribute('aria-expanded')).toBe('false')
  await userEvent.keyboard('{ArrowRight}'); expect(row.getAttribute('aria-expanded')).toBe('true')
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
})

it('retains later cached listing pages when reopening a compact row', async () => {
  seed({ expanded: ['', 'dev', 'dev/ll'] })
  const cached = snapshot()
  writeView<ExplorerSnapshot>('person-a', 'review', EXPLORER_SLOT, { ...cached, pages: { ...cached.pages, 'dev/ll/api': { entries: [file(deep), file('dev/ll/api/later.txt')], truncated: false, loaded: 2, firstSize: 1 } } })
  render(<Harness />); await settled()
  fireEvent.click(screen.getByRole('treeitem', { name: 'dev/ll/api' }))
  await waitFor(() => expect(read.mock.calls.some(([url]) => String(url).includes('path=dev%2Fll%2Fapi'))).toBe(true))
  await settled()
  expect(screen.getByRole('treeitem', { name: 'later.txt' })).not.toBeNull()
  expect(snapshot().pages['dev/ll/api'].loaded).toBe(2)
})

const listPaths = () => read.mock.calls.filter(([url]) => String(url).includes('/workspace?') && new URL(String(url), location.origin).searchParams.get('op') === 'list').map(([url]) => new URL(String(url), location.origin).searchParams.get('path'))

it.each([true, false])('reopening a stale folder rereads every expanded descendant (compact=%s)', async compact => {
  await saveCompactFolders(compact)
  listings.dev = [dir('dev/ll'), dir('dev/unopened'), file('dev/sibling.txt')]
  seed({ expanded: ['', 'dev/ll', 'dev/ll/api'] })
  listings['dev/ll/api'] = [file(deep), file('dev/ll/api/fresh.txt')]
  render(<Harness />); await settled(); read.mockClear()
  fireEvent.click(screen.getByRole('treeitem', { name: 'dev' }))
  await screen.findByRole('treeitem', { name: 'fresh.txt' })
  expect(listPaths()).toEqual(['dev', 'dev/ll', 'dev/ll/api'])
  expect(snapshot().expanded).toEqual(expect.arrayContaining(['dev', 'dev/ll', 'dev/ll/api']))
})

it('rereads cached middle folders so a new file splits a reopened compact row', async () => {
  seed({ expanded: ['', 'dev/ll', 'dev/ll/api'] })
  listings['dev/ll'] = [dir('dev/ll/api'), file('dev/ll/split.txt')]
  render(<Harness />); await settled(); read.mockClear()
  fireEvent.click(screen.getByRole('treeitem', { name: 'dev/ll/api' }))
  await screen.findByRole('treeitem', { name: 'split.txt' })
  expect(screen.getByRole('treeitem', { name: 'dev/ll' }).getAttribute('aria-expanded')).toBe('true')
  expect(screen.getByRole('treeitem', { name: 'api' }).getAttribute('aria-expanded')).toBe('true')
  expect(listPaths()).toEqual(['dev', 'dev/ll', 'dev/ll/api'])
})

it('continues a newly extended single-child chain after revalidating its cached folders', async () => {
  listings['dev/ll'] = [file('dev/ll/old.txt')]
  seed({ expanded: [''], pages: Object.fromEntries(Object.entries(listings).filter(([path]) => path !== 'dev/ll/api').map(([path, entries]) => [path, { entries, truncated: false }])) })
  listings['dev/ll'] = [dir('dev/ll/api')]
  render(<Harness />); await settled(); read.mockClear()
  fireEvent.click(screen.getByRole('treeitem', { name: 'dev/ll' }))
  await screen.findByRole('treeitem', { name: 'deep.txt' })
  expect(screen.getByRole('treeitem', { name: 'dev/ll/api' }).getAttribute('aria-expanded')).toBe('true')
  expect(listPaths()).toEqual(['dev', 'dev/ll', 'dev/ll/api'])
})

it.each([true, false])('stops expansion at deleted entries exactly where the drawn row stops (cached=%s)', async cached => {
  seed({
    expanded: [''],
    pages: Object.fromEntries(Object.entries(listings).filter(([path]) => path === '' || cached && path === 'dev').map(([path, entries]) => [path, { entries, truncated: false }])),
    statuses: { '': { branch: 'main', changed: 1, untracked: 0, partial: false, files: [{ path: 'dev/removed.txt', code: 'D' }] } },
  })
  render(<Harness />); await settled(); read.mockClear()
  fireEvent.click(screen.getByRole('treeitem', { name: 'dev, deleted in this folder' }))
  await screen.findByRole('treeitem', { name: 'removed.txt, deleted' })
  await waitFor(() => expect(screen.queryByText('Loading…')).toBeNull())
  await settled()
  expect(listPaths()).toEqual(['dev'])
  expect(screen.getByRole('treeitem', { name: 'll' }).getAttribute('aria-expanded')).toBe('false')
  expect(screen.queryByRole('treeitem', { name: 'deep.txt' })).toBeNull()
})
