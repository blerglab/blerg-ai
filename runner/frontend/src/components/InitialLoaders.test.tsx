import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import SessionRail from './SessionRail'
import FilesTab from './FilesTab'
import CapabilitiesTab from './CapabilitiesTab'
import WorkspaceTree from './WorkspaceTree'
import HistoryTab, { TopicList } from './HistoryTab'
import ShellView from './ShellView'
import ShellWorkspace from '../test/ShellWorkspaceHarness'
import { PinnedTicketModal } from './BoardCardView'
import { resetLiveBoardCardsForTest } from '../lib/liveBoardCard'
import { sessionView } from '../lib/sessionView'
import { syncViewCacheSessions } from '../lib/sessionViewCache'
import { makeSession } from '../test/fixtures'
import { makeLive, filesOf } from '../test/planTabFixture'
import { FakeShell } from '../test/shellMock'
import { RAIL_TAB_KEY } from '../lib/sessionRail'

const read = vi.hoisted(() => vi.fn())
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: 'person', exp: 9999999999 }))}.x`, ensureFreshToken: async () => true }))
vi.mock('../apiFetch', () => ({ apiFetch: read }))
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../lib/chatTransport', async original => ({ ...await original<typeof import('../lib/chatTransport')>(), blergTransport: () => ({ files: null }) }))
// Transport callbacks and timing here; xterm painting is covered in Chromium.
vi.mock('@xterm/xterm', () => ({ Terminal: class {
  options = {}; cols = 80; rows = 24
  parser = { registerOscHandler: () => ({ dispose() {} }) }
  loadAddon() {} open() {} attachCustomKeyEventHandler() {} dispose() {} focus() {}
  onData() { return { dispose() {} } }
  write(_bytes: Uint8Array, done: () => void) { done() }
} }))
vi.mock('@xterm/addon-fit', () => ({ FitAddon: class { fit() {} } }))
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
}
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
const workspace = { result: { ok: true, launch_generation: 'launch', generation: 1, entries: [{ name: 'README.md', path: 'README.md', directory: false }], truncated: false }, members: [], runtime: 'host' }
const topics = { session_id: 'session', through_seq: 1, generated_at: '', tokens_hidden: false, topics: [], unmarked: null, strokes: [] }
const caps = { capabilities: { engine: 'claude', groups: [{ id: 'skills', label: 'Skills', items: [{ name: 'example-skill' }] }] }, seq: 1, ts: '' }
const pinned = { source: 'blerg-board', board_id: 'board', key: '#12', title: 'Example card' }
const card = { card: { id: 'card', board_id: 'board', number: 12, title: 'Example card', body: 'Card description' }, url: '' }
const artifact = { id: 'file', name: 'notes.txt', view: 'text' as const, size: 2, content_type: 'text/plain', created_at: '2026-10-20T10:00:00Z' }
beforeEach(() => { vi.useFakeTimers(); localStorage.clear(); resetLiveBoardCardsForTest(); syncViewCacheSessions([makeSession(), makeSession({ id: 'session' })]); read.mockReset().mockImplementation(() => Promise.resolve(json({}))) })
afterEach(() => { vi.useRealTimers() })
async function advance(ms: number) { await act(() => vi.advanceTimersByTimeAsync(ms)) }
async function delayed(label: string) {
  expect(screen.queryByRole('region', { name: label })).toBeNull()
  expect(screen.queryByText(`Loading ${label}`)).toBeNull()
  await advance(149)
  expect(screen.queryByText(`Loading ${label}`)).toBeNull()
  await advance(1)
  expect(screen.getByText(`Loading ${label}`)).toHaveClass('initial-loader-label')
  expect(screen.getByRole('region', { name: label })).toHaveAttribute('aria-busy', 'true')
}
function noSkeleton() { expect(document.querySelector('.initial-skeleton')).toBeNull() }
function rail(settled: boolean, planned = false) {
  return <MemoryRouter><SessionRail id="rail" session={makeSession()} live={makeLive({ settled, ...(planned ? { checklist: { items: [{ text: 'Cached step', done: false }], eventId: 'check', updatedAt: null } } : {}) })} mode="panel" open toolChanges={0} onClose={() => {}} onOpenArtifact={() => {}} /></MemoryRouter>
}
it.each(['live', 'plan'])('%s delays the first live read and preserves the selected tab', async tab => {
  localStorage.setItem(RAIL_TAB_KEY, tab)
  const view = render(rail(false))
  expect(screen.getByRole('tab', { name: new RegExp(`^${tab}`, 'i') })).toHaveAttribute('aria-selected', 'true')
  expect(screen.getByTestId('rail-session')).toBeInTheDocument()
  await delayed(tab === 'live' ? 'live session' : 'plan')
  view.rerender(rail(true, true))
  noSkeleton()
  expect(screen.getByTestId(tab === 'live' ? 'rail-session' : 'rail-checklist')).toBeInTheDocument()
})
it.each(['live', 'plan'])('%s never flashes for fast or cached reads', async tab => {
  localStorage.setItem(RAIL_TAB_KEY, tab)
  const view = render(rail(false))
  await advance(100)
  view.rerender(rail(true, true))
  await advance(300); noSkeleton()
  view.unmount()
  render(rail(true, true))
  await advance(300); noSkeleton()
})
it.each(['slow', 'fast', 'cached', 'error'])('History: %s initial read', async mode => {
  const pending = deferred<Response>(); read.mockReturnValue(pending.promise)
  if (mode === 'cached') sessionView('session', 'topics').keep({ etag: '"one"', reply: topics })
  render(<HistoryTab sessionId="session" active filesKey="" missing={null} onJump={() => {}} onOpenArtifact={() => {}} />)
  if (mode === 'slow' || mode === 'error') await delayed('history')
  else { await advance(mode === 'fast' ? 100 : 300); noSkeleton() }
  if (mode === 'cached') { noSkeleton(); expect(screen.getByTestId('reel-empty')).toBeInTheDocument() }
  await act(async () => pending.resolve(mode === 'error' ? json({}, 500) : json(topics)))
  await advance(300); noSkeleton()
  expect(screen.getByTestId(mode === 'error' ? 'reel-error' : 'reel-empty')).toBeInTheDocument()
})
it.each(['slow', 'fast', 'cached', 'empty', 'error'])('Files: %s initial read', async mode => {
  const files = filesOf(mode === 'cached' ? [artifact] : [])!
  files.status = mode === 'cached' || mode === 'empty' ? 'ready' : 'loading'
  const ui = () => <FilesTab sessionId="session" active files={files} onOpenArtifact={() => {}} />
  const view = render(ui())
  if (mode === 'slow' || mode === 'error') await delayed('files')
  else { await advance(mode === 'fast' ? 100 : 300); noSkeleton() }
  if (mode === 'cached' || mode === 'empty') noSkeleton()
  files.status = mode === 'error' ? 'error' : 'ready'; view.rerender(ui())
  await advance(300); noSkeleton()
  if (mode === 'error') expect(screen.getByText(/load the files/)).toBeInTheDocument()
})
it.each(['slow', 'fast', 'cached', 'error'])('Explorer: %s initial read', async mode => {
  const pending = deferred<Response>()
  if (mode === 'cached') {
    read.mockResolvedValue(json(workspace))
    const first = render(<WorkspaceTree sessionId="session" active />)
    await advance(0)
    expect(screen.getByRole('treeitem', { name: 'README.md' })).toBeInTheDocument(); first.unmount()
  }
  read.mockReturnValue(pending.promise)
  render(<WorkspaceTree sessionId="session" active />)
  if (mode === 'slow' || mode === 'error') await delayed('workspace')
  else { await advance(mode === 'fast' ? 100 : 300); noSkeleton() }
  if (mode === 'cached') { noSkeleton(); expect(screen.getByRole('treeitem', { name: 'README.md' })).toBeInTheDocument() }
  await act(async () => pending.resolve(mode === 'error' ? json({ error: 'Workspace unavailable.' }, 500) : json(workspace)))
  await advance(300); noSkeleton()
  if (mode === 'error') expect(screen.getByText('Workspace unavailable.')).toBeInTheDocument()
})
it.each(['slow', 'fast', 'cached', 'error'])('Capabilities: %s initial read', async mode => {
  const pending = deferred<Response>()
  read.mockImplementation((url: string) => url.endsWith('/capabilities') ? pending.promise : Promise.resolve(json({})))
  if (mode === 'cached') sessionView('sess-a', 'capabilities').keep({ payload: caps.capabilities, seq: 1, ts: '' })
  localStorage.setItem(RAIL_TAB_KEY, 'capabilities'); render(rail(true))
  if (mode === 'slow' || mode === 'error') await delayed('capabilities')
  else { await advance(mode === 'fast' ? 100 : 300); noSkeleton() }
  if (mode === 'cached') { noSkeleton(); expect(screen.getByText('example-skill')).toBeInTheDocument() }
  await act(async () => pending.resolve(mode === 'error' ? json({}, 500) : json(caps)))
  await advance(300); noSkeleton()
  expect(screen.getByTestId(mode === 'error' ? 'caps-none' : 'caps-header')).toBeInTheDocument()
})
it.each(['slow', 'fast', 'cached', 'error'])('Board card: %s initial read', async mode => {
  const pending = deferred<Response>()
  const ui = <MemoryRouter><PinnedTicketModal ticket={pinned} sessionId="session" onClose={() => {}} /></MemoryRouter>
  if (mode === 'cached') { read.mockResolvedValue(json(card)); const first = render(ui); await advance(0); first.unmount() }
  read.mockReturnValue(pending.promise); render(ui)
  if (mode === 'slow' || mode === 'error') await delayed('board card')
  else { await advance(mode === 'fast' ? 100 : 300); noSkeleton() }
  if (mode === 'cached') noSkeleton()
  await act(async () => pending.resolve(mode === 'error' ? json({ error: 'Unavailable' }, 500) : json(card)))
  await advance(300); noSkeleton()
  expect(screen.getByTestId(mode === 'error' ? 'board-card-failed' : 'board-card-live')).toBeInTheDocument()
})
it.each(['slow', 'fast', 'replay', 'disconnected', 'closed'])('Shell: %s initial output', async mode => {
  const shell = new FakeShell('shell'); if (mode === 'replay') shell.replay = ['Cached output']; else shell.connectionState = 'connecting'
  render(<ShellView transport={shell} />)
  if (mode === 'slow' || mode === 'disconnected' || mode === 'closed') await delayed('shell')
  else { await advance(mode === 'fast' ? 100 : 300); noSkeleton() }
  if (mode === 'replay') noSkeleton()
  act(() => { if (mode === 'disconnected' || mode === 'closed') shell.state?.(mode); else shell.emit('First output') })
  await advance(300); noSkeleton()
  expect(document.querySelector('.shell-terminal')).not.toHaveAttribute('aria-busy')
})
it('shows a terminal skeleton while a saved shell waits for inventory, never over restored output', async () => {
  const shell = new FakeShell('saved'); shell.replay = ['Cached shell output']
  localStorage.setItem('blerg-runner.tabs.owner.session', JSON.stringify({ tabs: [{ id: 'shell:saved', preview: false, view: { kind: 'shell', shellId: 'saved', runtime: 'sandbox', folder: 'project', label: 'Shell' } }], active: 'shell:saved' }))
  const pending = deferred<FakeShell[]>()
  render(<ShellWorkspace accountId="owner" sessionId="session" restoreShells={() => pending.promise}>Chat</ShellWorkspace>)
  await delayed('shell')
  await act(async () => pending.resolve([shell]))
  await advance(300); noSkeleton()
})

it('keeps known live content while replay is incomplete', async () => {
  render(rail(false, true))
  await advance(300)
  noSkeleton()
  expect(screen.getByTestId('rail-plan-summary')).toBeInTheDocument()
})
it('keeps cached files on screen while a refresh is pending', async () => {
  const files = filesOf([artifact])!
  files.status = 'loading'
  render(<FilesTab sessionId="session" active files={files} onOpenArtifact={() => {}} />)
  await advance(300)
  noSkeleton()
  expect(screen.getByText('notes.txt')).toBeInTheDocument()
})

it('keeps live MCP data visible while the capabilities report is pending', async () => {
  render(<CapabilitiesTab loading active report={null} pendingToolChanges={0} liveMcp={{ engine: 'claude', settled: true, since: null, servers: [{ name: 'example-server', source: 'session', status: 'connected', tools: 3 }] }} />)
  await advance(300)
  noSkeleton()
  expect(screen.getByText('example-server')).toBeInTheDocument()
})

const unavailableFiles = 'The files show once the chat has loaded.'
it('Files: an absent source stops loading after three seconds and a late list replaces the fallback', async () => {
  const view = render(<FilesTab sessionId="session" active onOpenArtifact={() => {}} />)
  await delayed('files')
  await advance(2849)
  expect(screen.getByText('Loading files')).toBeInTheDocument()
  await advance(1)
  noSkeleton()
  expect(document.querySelector('[aria-busy]')).toBeNull()
  expect(screen.getByText(unavailableFiles)).toBeInTheDocument()
  view.rerender(<FilesTab sessionId="session" active files={filesOf([artifact])!} onOpenArtifact={() => {}} />)
  expect(screen.getByText('notes.txt')).toBeInTheDocument()
  expect(screen.queryByText(unavailableFiles)).toBeNull()
})
it('Files: a source that arrives before the timeout ends the skeleton immediately', async () => {
  const view = render(<FilesTab sessionId="session" active onOpenArtifact={() => {}} />)
  await delayed('files')
  view.rerender(<FilesTab sessionId="session" active files={filesOf([artifact])!} onOpenArtifact={() => {}} />)
  noSkeleton()
  await advance(3000)
  expect(screen.getByText('notes.txt')).toBeInTheDocument()
  expect(screen.queryByText(unavailableFiles)).toBeNull()
})
it('Files: a new session gets its own bounded wait and unmount cancels the deadline', async () => {
  const view = render(<FilesTab sessionId="session" active onOpenArtifact={() => {}} />)
  await advance(3000)
  expect(screen.getByText(unavailableFiles)).toBeInTheDocument()
  view.rerender(<FilesTab sessionId="sess-a" active onOpenArtifact={() => {}} />)
  expect(screen.queryByText(unavailableFiles)).toBeNull()
  await delayed('files')
  view.unmount()
  expect(vi.getTimerCount()).toBe(0)
})
it.each(['none', 'empty replay'] as const)('Shell: running with %s ends the initial skeleton without output', async replay => {
  const shell = new FakeShell('silent'); shell.connectionState = 'connecting'
  const view = render(<ShellView transport={shell} />)
  await advance(150)
  expect(screen.getByText('Loading shell')).toBeInTheDocument()
  if (replay === 'empty replay') act(() => shell.output?.(new Uint8Array(), true))
  act(() => shell.state?.('running'))
  noSkeleton()
  expect(view.container.querySelector('[aria-busy]')).toBeNull()
  await advance(3000)
  noSkeleton()
})
it('Shell: an already running silent transport never shows a skeleton, including while reconnecting', async () => {
  const shell = new FakeShell('running')
  const view = render(<ShellView transport={shell} />)
  await advance(300)
  noSkeleton()
  expect(view.container.querySelector('[aria-busy]')).toBeNull()
  act(() => shell.state?.('syncing'))
  await advance(300)
  noSkeleton()
  expect(screen.getByText("Syncing the agent's commits…")).toBeInTheDocument()
})
it.each(['blerg-desktop-inventory-changed', 'blerg-desktop-scope-changed'])('Capabilities: %s refresh after an empty result never restarts the skeleton', async event => {
  const initial = deferred<Response>(), refresh = deferred<Response>()
  read.mockImplementation((url: string) => url.endsWith('/capabilities') ? initial.promise : Promise.resolve(json({})))
  localStorage.setItem(RAIL_TAB_KEY, 'capabilities')
  render(rail(true))
  await delayed('capabilities')
  await act(async () => initial.resolve(json({})))
  expect(screen.getByTestId('caps-none')).toBeInTheDocument()
  read.mockImplementation((url: string) => url.endsWith('/capabilities') ? refresh.promise : Promise.resolve(json({})))
  act(() => window.dispatchEvent(new Event(event)))
  await advance(300)
  noSkeleton()
  expect(screen.getByTestId('caps-none')).toBeInTheDocument()
  await act(async () => refresh.resolve(json(caps)))
  expect(screen.getByText('example-skill')).toBeInTheDocument()
})
it('History: the topic list leaves loading to its containing boundary', async () => {
  const view = render(<TopicList sessionId="session" view={{ status: 'loading' }} now={new Date()} missing={null} onJump={() => {}} onOpenArtifact={() => {}} />)
  await advance(300)
  expect(view.container).toBeEmptyDOMElement()
})

it('Shell: the delayed busy region never hides the connection notice', async () => {
  const shell = new FakeShell('connecting'); shell.connectionState = 'connecting'
  const view = render(<ShellView transport={shell} />)
  expect(screen.getByText('Connecting…')).toBeInTheDocument()
  await advance(149)
  expect(view.container.querySelector('[aria-busy]')).toBeNull()
  await advance(1)
  expect(screen.getByText('Connecting…')).toBeInTheDocument()
  expect(screen.getByText('Loading shell')).toBeInTheDocument()
})

it('Capabilities: a scope refresh of a cached report is not another initial load', async () => {
  sessionView('sess-a', 'capabilities').keep({ payload: caps.capabilities, seq: 1, ts: '' })
  const pending = deferred<Response>()
  read.mockImplementation((url: string) => url.endsWith('/capabilities') ? pending.promise : Promise.resolve(json({})))
  localStorage.setItem(RAIL_TAB_KEY, 'capabilities')
  render(rail(true))
  expect(screen.getByText('example-skill')).toBeInTheDocument()
  act(() => window.dispatchEvent(new Event('blerg-desktop-scope-changed')))
  await advance(300)
  noSkeleton()
  expect(document.querySelector('[aria-busy]')).toBeNull()
})
